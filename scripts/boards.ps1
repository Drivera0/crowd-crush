# Pulse boards on Windows: the same commands as scripts/boards.sh (Mac / Linux), for this PC.
#
#   pwsh scripts/boards.ps1 status        every board on USB: port, name, zone, firmware (up to date?), Wi-Fi, peers;
#                                         then what a running Pulse server sees (http://localhost:8080)
#   pwsh scripts/boards.ps1 flash         flash the sign and every zone light (one at a time; .env untouched)
#   pwsh scripts/boards.ps1 wifi          save a Wi-Fi network on every board over USB (asks for name + password)
#   pwsh scripts/boards.ps1 env           print SIGN_URL for the boards found (USB form and Wi-Fi form)
#
# A program holding a board's port (Arduino IDE Serial Monitor, a Pulse server running natively with
# serial:auto) makes it "busy": close it first. A Pulse server in WSL uses Wi-Fi and never holds the ports.

param(
  [Parameter(Position = 0)][ValidateSet('status', 'flash', 'wifi', 'env')][string]$Command = 'status',
  [string]$Server = 'http://localhost:8080'
)

$ErrorActionPreference = 'Stop'
. "$PSScriptRoot/boards-lib.ps1"
$repo = Split-Path -Parent $PSScriptRoot

$want = @{
  'sign'       = (Get-FirmwareId (Join-Path $repo 'arduino/sign/sign.ino')).Split(' ')[0]
  'zone-light' = (Get-FirmwareId (Join-Path $repo 'arduino/zone-light/zone-light.ino')).Split(' ')[0]
}

# Asks every board port "S"; returns one object per port.
function Get-UsbBoards {
  foreach ($b in Get-BoardPorts) {
    $row = [pscustomobject]@{ Port = $b.Port; Arduino = $b.Arduino; Status = $null; Note = '' }
    try {
      $sp = Open-BoardPort $b.Port $b.Arduino
      try { $row.Status = Get-BoardStatus $sp 4 } finally { $sp.Close() }
      if (-not $row.Status) { $row.Note = 'no answer: old firmware (flash it)' }
    } catch {
      $row.Note = 'busy: another program has the port open'
    }
    $row
  }
}

function Show-Status {
  $rows = @(Get-UsbBoards)
  if (-not $rows) { Write-Host 'No boards on USB (data cable? CP210x driver for the ESP32s?).' }
  $rows | ForEach-Object {
    $s = $_.Status
    if (-not $s) { return [pscustomobject]@{ Port = $_.Port; Board = $(if ($_.Arduino) { 'sign?' } else { 'zone-light?' }); Zone = ''; Firmware = ''; WiFi = ''; Hears = $_.Note } }
    $fw = if (-not $s.fw) { 'old (no build id)' } elseif ($s.fw.Split(' ')[0] -eq $want[$s.kind]) { "$($s.fw) (up to date)" } else { "$($s.fw) (OUT OF DATE)" }
    $wifi = if ($s.wifi) { "$($s.ssid) $($s.ip)" } elseif ($s.ssid) { "no (trying $($s.ssid))" } else { 'no' }
    $hears = (@($s.peers) | Where-Object { $_ } | ForEach-Object { "$($_.name) $($_.dist) m" }) -join ', '
    [pscustomobject]@{ Port = $_.Port; Board = $(if ($s.kind -eq 'sign') { 'sign' } else { $s.name }); Zone = $s.zone; Firmware = $fw; WiFi = $wifi; Hears = $hears }
  } | Format-Table -AutoSize | Out-String -Width 200 | Write-Host
  try {
    $hw = Invoke-RestMethod "$Server/api/hardware" -TimeoutSec 3
    Write-Host "Pulse server at $Server sees:"
    $hw | Where-Object { $_.kind -ne 'laptop' } | ForEach-Object {
      [pscustomobject]@{ Board = $_.name; Online = $_.online; Link = $_.link; Where = $(if ($_.port) { $_.port } else { $_.url }); Shows = $_.level; Firmware = $_.fw; Old = $_.fwOld }
    } | Format-Table -AutoSize | Out-String -Width 200 | Write-Host
  } catch {
    Write-Host "No Pulse server answering at $Server."
  }
}

switch ($Command) {
  'status' { Show-Status }
  'flash' {
    & "$PSScriptRoot/flash-sign.ps1" -NoEnv
    & "$PSScriptRoot/flash-zone-lights.ps1" -NoEnv
    Show-Status
  }
  'wifi' {
    Write-Host 'Saves a Wi-Fi network on each board over USB (kept in its flash, tried before the built-in ones). 2.4 GHz only.'
    $ssid = Read-Host 'Wi-Fi name'
    if (-not $ssid -or $ssid.Length -gt 32) { throw 'want a name of up to 32 characters' }
    $sec = Read-Host 'Password (not shown)' -AsSecureString
    $pass = [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
    if ($pass.Length -gt 63) { throw 'password: up to 63 characters' }
    foreach ($b in Get-BoardPorts) {
      $sp = Open-BoardPort $b.Port $b.Arduino
      try {
        $st = Get-BoardStatus $sp 4
        if (-not $st) { Write-Host "  $($b.Port): no answer (old firmware: flash it first)"; continue }
        $sp.DiscardInBuffer()
        $sp.Write("W $ssid`t$pass`n")
        $reply = Wait-BoardLine $sp 'wifi:' 4
        if (-not $reply) { Write-Host "  $($b.Port): no confirmation (firmware too old for W: flash it)"; continue }
        Write-Host "  $($b.Port) ($(if ($st.kind -eq 'sign') { 'sign' } else { $st.name })): $($reply -replace '^wifi: ', '')"
        $deadline = (Get-Date).AddSeconds(30)
        $ok = $false
        while (-not $ok -and (Get-Date) -lt $deadline) {
          Start-Sleep -Seconds 2
          $s2 = Get-BoardStatus $sp 3
          if ($s2 -and $s2.wifi -and $s2.ssid -eq $ssid) { $ok = $true; Write-Host "    joined: http://$($s2.ip)" }
        }
        if (-not $ok) { Write-Host '    not joined within 30 s (wrong password, 5 GHz, out of range?). It keeps trying; USB works meanwhile.' }
      } finally { $sp.Close() }
    }
    $pass = $null
  }
  'env' {
    $rows = @(Get-UsbBoards | Where-Object { $_.Status })
    $usb = @(); $net = @()
    $sign = $rows | Where-Object { $_.Status.kind -eq 'sign' } | Select-Object -First 1
    if ($sign) { $usb += 'serial:auto'; $net += $(if ($sign.Status.ip) { "http://$($sign.Status.ip)" } else { 'serial:auto' }) }
    foreach ($r in ($rows | Where-Object { $_.Status.kind -eq 'zone-light' -and $_.Status.zone } | Sort-Object { $_.Status.zone })) {
      $usb += "$($r.Status.zone)=serial:auto"
      $net += "$($r.Status.zone)=$(if ($r.Status.ip) { "http://$($r.Status.ip)" } else { 'serial:auto' })"
    }
    if (-not $usb) { throw 'no Pulse boards answered on USB' }
    Write-Host '# server running natively on this PC (boards on USB):'
    Write-Host ('SIGN_URL=' + ($usb -join ','))
    Write-Host '# server in WSL (boards on Wi-Fi):'
    Write-Host ('SIGN_URL=' + ($net -join ','))
  }
}
