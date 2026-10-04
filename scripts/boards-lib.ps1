# Shared helpers for the Windows board scripts (dot-source it: . "$PSScriptRoot/boards-lib.ps1").
# The Mac / Linux equivalent is scripts/boards.sh (which uses the Go tool server/cmd/boards).

# arduino-cli from PATH, else the one bundled with Arduino IDE 2.
function Get-ArduinoCli {
  $cli = (Get-Command arduino-cli -ErrorAction SilentlyContinue).Source
  if (-not $cli) {
    $bundled = Join-Path $env:LOCALAPPDATA 'Programs\Arduino IDE\resources\app\lib\backend\resources\arduino-cli.exe'
    if (Test-Path $bundled) { $cli = $bundled }
  }
  if (-not $cli) { throw 'arduino-cli not found. Install Arduino IDE 2 (https://www.arduino.cc/en/software) or arduino-cli.' }
  return $cli
}

# Firmware build id: git blob hash (7 hex) of the sketch with LF line endings + today's date.
# Same as `boards fwid` (server/internal/sign/firmware.go); the server compares the hash.
function Get-FirmwareId([string]$Ino) {
  $text = [Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes($Ino)) -replace "`r`n", "`n"
  $body = [Text.Encoding]::UTF8.GetBytes($text)
  $hdr = [Text.Encoding]::ASCII.GetBytes("blob $($body.Length)`0")
  $sha = [Security.Cryptography.SHA1]::Create()
  $hash = $sha.ComputeHash([byte[]]($hdr + $body))
  $hex = ($hash | ForEach-Object { $_.ToString('x2') }) -join ''
  return $hex.Substring(0, 7) + ' ' + (Get-Date -Format 'yyyy-MM-dd')
}

# Copies a sketch into a local build folder (arduino-cli can't build from a \\wsl$ path) with
# its secrets (or the example if there are none: Wi-Fi is optional) and a pulse_build.h.
function New-SketchBuildDir([string]$SketchDir, [string]$Name, [string]$Into) {
  $dir = Join-Path $Into $Name
  New-Item -ItemType Directory -Force $dir | Out-Null
  Copy-Item (Join-Path $SketchDir "$Name.ino") $dir -Force
  $secrets = Join-Path $SketchDir 'arduino_secrets.h'
  if (Test-Path $secrets) { Copy-Item $secrets $dir -Force }
  else { Copy-Item (Join-Path $SketchDir 'arduino_secrets.h.example') (Join-Path $dir 'arduino_secrets.h') -Force }
  $fw = Get-FirmwareId (Join-Path $SketchDir "$Name.ino")
  [IO.File]::WriteAllText((Join-Path $dir 'pulse_build.h'), "#define PULSE_FW `"$fw`"`n")
  return @{ Dir = $dir; Fw = $fw }
}

# USB serial ports that can be a Pulse board: Arduino (2341) and USB-UART bridges (CP210x, CH340).
function Get-BoardPorts {
  Get-CimInstance Win32_PnPEntity |
    Where-Object { $_.PNPDeviceID -match 'VID_(2341|10C4|1A86|0403|303A)' -and $_.Name -match '\((COM\d+)\)' } |
    ForEach-Object {
      $null = $_.Name -match '\((COM\d+)\)'
      [pscustomobject]@{ Port = $Matches[1]; Arduino = ($_.PNPDeviceID -match 'VID_2341') }
    } | Sort-Object { [int]($_.Port -replace 'COM', '') }
}

# Opens a board's port the way the server does: DTR on for the R4 (it needs it), DTR and RTS
# left off for an ESP32's CP210x (they drive its reset: asserting them reboots the board).
function Open-BoardPort([string]$Port, [bool]$Arduino) {
  $sp = New-Object System.IO.Ports.SerialPort $Port, 115200
  $sp.ReadTimeout = 300
  $sp.NewLine = "`n"
  $sp.DtrEnable = $Arduino
  $sp.RtsEnable = $Arduino
  $sp.Open()
  return $sp
}

# Sends "S" (again every second) and returns the board's status JSON as an object, or $null.
function Get-BoardStatus($sp, [int]$TimeoutSec = 4) {
  $sp.DiscardInBuffer()
  $deadline = (Get-Date).AddSeconds($TimeoutSec)
  $next = Get-Date
  while ((Get-Date) -lt $deadline) {
    if ((Get-Date) -ge $next) { $sp.Write("S`n"); $next = (Get-Date).AddSeconds(1) }
    try { $line = $sp.ReadLine().Trim() } catch [TimeoutException] { continue }
    if ($line.StartsWith('{')) {
      try { $o = $line | ConvertFrom-Json; if ($o.kind) { return $o } } catch {}
    }
  }
  return $null
}

# Waits for a line starting with $Prefix (e.g. "wifi:") and returns it, or $null.
function Wait-BoardLine($sp, [string]$Prefix, [int]$TimeoutSec = 3) {
  $deadline = (Get-Date).AddSeconds($TimeoutSec)
  while ((Get-Date) -lt $deadline) {
    try { $line = $sp.ReadLine().Trim() } catch [TimeoutException] { continue }
    if ($line.StartsWith($Prefix)) { return $line }
  }
  return $null
}
