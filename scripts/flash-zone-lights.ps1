# Flash the Pulse zone lights (ESP32 DevKit V1, one per zone) from Windows and add them to SIGN_URL.
#
#   pwsh scripts/flash-zone-lights.ps1                 # every ESP32 plugged in → zones A, B, … in port order
#   pwsh scripts/flash-zone-lights.ps1 -Zones B,A      # pick which zone each board (in port order) shows
#   pwsh scripts/flash-zone-lights.ps1 -NoUpload       # just read the IPs from boards already flashed
#
# Needs the CP210x USB driver (Windows Update → Optional updates, or silabs.com), the
# "esp32 by Espressif" core in arduino-cli, and arduino/zone-light/arduino_secrets.h with
# your Wi-Fi (copied from the sign's on first run). ESP32s only join 2.4 GHz networks.

param(
  [string[]]$Zones = @(),
  [string[]]$Ports = @(),
  [switch]$NoUpload,
  [int]$TimeoutSec = 40
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$sketch = Join-Path $repo 'arduino/zone-light'
$secrets = Join-Path $sketch 'arduino_secrets.h'
# Huge APP partition: Wi-Fi + Bluetooth + web server don't fit the default 1.2 MB app slot.
$fqbn = 'esp32:esp32:esp32:PartitionScheme=huge_app'

# ---- arduino-cli
$cli = (Get-Command arduino-cli -ErrorAction SilentlyContinue).Source
if (-not $cli) {
  $bundled = Join-Path $env:LOCALAPPDATA 'Programs\Arduino IDE\resources\app\lib\backend\resources\arduino-cli.exe'
  if (Test-Path $bundled) { $cli = $bundled }
}
if (-not $cli) { throw 'arduino-cli not found. Install Arduino IDE 2 or arduino-cli.' }

# ---- Wi-Fi secrets (reuse the sign's if this sketch has none)
if (-not (Test-Path $secrets)) {
  $signSecrets = Join-Path $repo 'arduino/sign/arduino_secrets.h'
  if (Test-Path $signSecrets) { Copy-Item $signSecrets $secrets }
  else {
    Copy-Item (Join-Path $sketch 'arduino_secrets.h.example') $secrets
    Write-Host "Put your Wi-Fi name and password in $secrets, then run this again."
    exit 1
  }
}

# ---- boards: CP210x / CH340 USB-serial ports
if (-not $Ports) {
  $Ports = Get-CimInstance Win32_PnPEntity |
    Where-Object { $_.PNPDeviceID -match 'VID_(10C4|1A86)' -and $_.Name -match '\((COM\d+)\)' } |
    ForEach-Object { if ($_.Name -match '\((COM\d+)\)') { $Matches[1] } } |
    Sort-Object { [int]($_ -replace 'COM', '') }
}
if (-not $Ports) {
  throw 'No ESP32 found. Install the CP210x driver (Windows Update → Optional updates, or silabs.com) and use a data USB cable.'
}
if (-not $Zones) { $Zones = for ($i = 0; $i -lt $Ports.Count; $i++) { [string][char](65 + $i) } }
if ($Zones.Count -lt $Ports.Count) { throw "Got $($Ports.Count) boards but only $($Zones.Count) zones." }
Write-Host ("Boards: " + (($Ports | ForEach-Object -Begin { $i = 0 } -Process { "$_ → zone $($Zones[$i++])" }) -join ', '))

# ---- compile once (from a local copy: arduino-cli can't build from a \\wsl$ path), upload to each
if (-not $NoUpload) {
  $tmp = Join-Path $env:TEMP 'pulse-zone-build\zone-light'
  New-Item -ItemType Directory -Force $tmp | Out-Null
  Copy-Item (Join-Path $sketch 'zone-light.ino'), $secrets $tmp -Force
  $build = Join-Path $env:TEMP 'pulse-zone-build\out'
  Write-Host 'Compiling…'
  & $cli compile --fqbn $fqbn --output-dir $build $tmp
  if ($LASTEXITCODE -ne 0) { throw 'Compile failed.' }
  foreach ($p in $Ports) {
    Write-Host "Uploading to $p… (if it sticks at 'Connecting…', hold the BOOT button)"
    & $cli upload --fqbn $fqbn --port $p --input-dir $build $tmp
    if ($LASTEXITCODE -ne 0) { throw "Upload to $p failed. Close any serial monitor on it and retry." }
  }
  Remove-Item -Recurse -Force (Split-Path $tmp) -ErrorAction SilentlyContinue
}

# ---- read "Zone light ready: http://x.x.x.x" from each board (reset it first so it prints again)
$found = @{}
for ($i = 0; $i -lt $Ports.Count; $i++) {
  $p = $Ports[$i]
  $sp = New-Object System.IO.Ports.SerialPort $p, 115200
  $sp.ReadTimeout = 1000
  $url = $null
  try {
    $sp.Open()
    # EN/RTS pulse resets the ESP32 so it prints its address again.
    $sp.DtrEnable = $false; $sp.RtsEnable = $true; Start-Sleep -Milliseconds 120; $sp.RtsEnable = $false
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while (-not $url -and (Get-Date) -lt $deadline) {
      try { $line = $sp.ReadLine().Trim() } catch [TimeoutException] { continue }
      if ($line -match 'ready: (http://[0-9.]+)') { $url = $Matches[1] }
    }
  } finally {
    if ($sp.IsOpen) { $sp.Close() }
  }
  if ($url) {
    Write-Host "  $p → zone $($Zones[$i]) at $url"
    $found[$Zones[$i]] = $url
  } else {
    Write-Host "  ${p}: no address within $TimeoutSec s (check Wi-Fi name/password, 2.4 GHz)."
  }
}
if (-not $found.Count) { exit 1 }

# ---- SIGN_URL in .env: keep the main sign and other zones, replace these zones
$envFile = Join-Path $repo '.env'
if (-not (Test-Path $envFile)) { Copy-Item (Join-Path $repo '.env.example') $envFile }
$text = Get-Content $envFile -Raw
$parts = @()
# @(...) everywhere: a one-item result would otherwise collapse to a string and += would glue text together.
if ($text -match '(?m)^SIGN_URL=(.*)$') { $parts = @($Matches[1].Trim() -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ }) }
$parts = @($parts | Where-Object { -not ($_ -match '^([A-Za-z][A-Za-z0-9_-]*)=' -and $found.ContainsKey($Matches[1].ToUpper())) })
foreach ($z in ($found.Keys | Sort-Object)) { $parts += "$z=$($found[$z])" }
$value = $parts -join ','
if ($text -match '(?m)^SIGN_URL=') { $text = $text -replace '(?m)^SIGN_URL=.*$', "SIGN_URL=$value" }
else { $text = $text.TrimEnd() + "`nSIGN_URL=$value`n" }
[System.IO.File]::WriteAllText($envFile, $text.Replace("`r`n", "`n"))
Write-Host "SIGN_URL=$value"
Write-Host 'Restart the server and press "Run test alert" on the dashboard.'
