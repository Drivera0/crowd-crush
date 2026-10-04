# Flash the Pulse warning sign (Arduino Uno R4 WiFi) from Windows and set SIGN_URL.
#
#   pwsh scripts/flash-sign.ps1            # find the board, compile, upload, read its IP, update .env
#   pwsh scripts/flash-sign.ps1 -Port COM7 # pick the port yourself
#   pwsh scripts/flash-sign.ps1 -NoUpload  # just read the IP from a board that is already flashed
#
# Needs the Arduino IDE 2 (its bundled arduino-cli is used) or arduino-cli on PATH,
# and arduino/sign/arduino_secrets.h with your Wi-Fi name and password
# (copied from arduino_secrets.h.example on first run). The R4 only joins 2.4 GHz networks.

param(
  [string]$Port = '',
  [switch]$NoUpload,
  [int]$TimeoutSec = 60
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$sketch = Join-Path $repo 'arduino/sign'
$secrets = Join-Path $sketch 'arduino_secrets.h'
$fqbn = 'arduino:renesas_uno:unor4wifi'

# ---- arduino-cli
$cli = (Get-Command arduino-cli -ErrorAction SilentlyContinue).Source
if (-not $cli) {
  $bundled = Join-Path $env:LOCALAPPDATA 'Programs\Arduino IDE\resources\app\lib\backend\resources\arduino-cli.exe'
  if (Test-Path $bundled) { $cli = $bundled }
}
if (-not $cli) { throw 'arduino-cli not found. Install Arduino IDE 2 (https://www.arduino.cc/en/software) or arduino-cli.' }

# ---- Wi-Fi secrets
if (-not (Test-Path $secrets)) {
  Copy-Item (Join-Path $sketch 'arduino_secrets.h.example') $secrets
  Write-Host "Created $secrets"
  Write-Host 'Put your Wi-Fi name and password in it (2.4 GHz; a phone hotspot works), then run this again.'
  exit 1
}
if ((Get-Content $secrets -Raw) -match '"your-wifi"|"your-password"') {
  Write-Host "Fill in your Wi-Fi name and password in $secrets first, then run this again."
  exit 1
}

# ---- board
if (-not $Port) {
  $boards = & $cli board list --format json | ConvertFrom-Json
  $list = if ($boards.detected_ports) { $boards.detected_ports } else { $boards }
  $hit = $list | Where-Object { $_.matching_boards.fqbn -contains $fqbn } | Select-Object -First 1
  if (-not $hit) { throw 'No Arduino Uno R4 WiFi found. Plug it in with a data USB cable, or pass -Port COMx.' }
  $Port = $hit.port.address
}
Write-Host "Board on $Port"

# ---- compile and upload (from a local copy: arduino-cli can't build from a \\wsl$ path)
if (-not $NoUpload) {
  $tmp = Join-Path $env:TEMP 'pulse-sign-build\sign'
  New-Item -ItemType Directory -Force $tmp | Out-Null
  Copy-Item (Join-Path $sketch 'sign.ino'), $secrets $tmp -Force
  & $cli core install arduino:renesas_uno | Out-Null
  Write-Host 'Compiling and uploading…'
  & $cli compile --fqbn $fqbn --upload --port $Port $tmp
  if ($LASTEXITCODE -ne 0) { throw 'Upload failed. Close the Arduino IDE serial monitor if it is open, and try again.' }
  Remove-Item -Recurse -Force (Split-Path $tmp) -ErrorAction SilentlyContinue
  Start-Sleep -Seconds 2 # board resets after upload
}

# ---- read "Sign ready: SIGN_URL=http://x.x.x.x" from the serial port
Write-Host "Waiting up to $TimeoutSec s for the sign to join Wi-Fi…"
$sp = New-Object System.IO.Ports.SerialPort $Port, 115200
$sp.ReadTimeout = 1000
$sp.DtrEnable = $true
$url = $null
$deadline = (Get-Date).AddSeconds($TimeoutSec)
try {
  $sp.Open()
  while (-not $url -and (Get-Date) -lt $deadline) {
    try { $line = $sp.ReadLine() } catch [TimeoutException] { continue }
    $line = $line.Trim()
    if ($line) { Write-Host "  sign: $line" }
    if ($line -match 'SIGN_URL=(http://[0-9.]+)') { $url = $Matches[1] }
  }
} finally {
  if ($sp.IsOpen) { $sp.Close() }
}
if (-not $url) {
  Write-Host 'No IP yet. Check the Wi-Fi name/password, that the network is 2.4 GHz, then press the board''s RESET button and run:'
  Write-Host "  pwsh scripts/flash-sign.ps1 -NoUpload -Port $Port"
  exit 1
}
Write-Host "Sign is at $url"

# ---- SIGN_URL in .env (keeps any per-zone boards listed after the first entry)
$envFile = Join-Path $repo '.env'
if (-not (Test-Path $envFile)) { Copy-Item (Join-Path $repo '.env.example') $envFile }
$text = Get-Content $envFile -Raw
if ($text -match '(?m)^SIGN_URL=(.*)$') {
  $rest = ($Matches[1] -split ',' | Where-Object { $_ -match '^\s*[A-Za-z]+=' }) -join ','
  $value = if ($rest) { "$url,$rest" } else { $url }
  $text = $text -replace '(?m)^SIGN_URL=.*$', "SIGN_URL=$value"
} else {
  $text = $text.TrimEnd() + "`nSIGN_URL=$url`n"
}
[System.IO.File]::WriteAllText($envFile, $text.Replace("`r`n", "`n"))
Write-Host 'Updated SIGN_URL in .env. Restart the server (./bin/pulse) and press "Run test alert" on the dashboard.'
