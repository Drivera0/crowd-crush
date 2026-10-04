# Flash the Pulse warning sign (Arduino Uno R4 WiFi) from Windows and set SIGN_URL.
#
#   pwsh scripts/flash-sign.ps1            # find the board, compile, upload, read its IP, update .env
#   pwsh scripts/flash-sign.ps1 -Port COM7 # pick the port yourself
#   pwsh scripts/flash-sign.ps1 -NoUpload  # just read the IP from a board that is already flashed
#   pwsh scripts/flash-sign.ps1 -NoEnv     # don't touch .env
#   pwsh scripts/flash-sign.ps1 -Beacon    # Bluetooth beacon "PULSE-S" instead of Wi-Fi: the sign is then driven over USB only
#                                          # (set SIGN_URL=serial:auto yourself; .env is not touched)
#
# Needs the Arduino IDE 2 (its bundled arduino-cli is used) or arduino-cli on PATH.
# arduino/sign/arduino_secrets.h holds the Wi-Fi name and password (optional: without it the sign
# works over USB only; copied from arduino_secrets.h.example on first run). The R4 only joins 2.4 GHz networks.
# The firmware gets a build id (pulse_build.h) so `pwsh scripts/boards.ps1 status` can say if it is current.
# Close the Arduino IDE serial monitor and stop a Pulse server that drives the sign over USB first.

param(
  [string]$Port = '',
  [switch]$NoUpload,
  [switch]$NoEnv,
  [switch]$Beacon,
  [int]$TimeoutSec = 60
)

$ErrorActionPreference = 'Stop'
. "$PSScriptRoot/boards-lib.ps1"
$repo = Split-Path -Parent $PSScriptRoot
$sketch = Join-Path $repo 'arduino/sign'
$secrets = Join-Path $sketch 'arduino_secrets.h'
$fqbn = 'arduino:renesas_uno:unor4wifi'
$cli = Get-ArduinoCli

# ---- Wi-Fi secrets (optional)
if (-not (Test-Path $secrets)) {
  Copy-Item (Join-Path $sketch 'arduino_secrets.h.example') $secrets
  Write-Host "Created $secrets"
  Write-Host 'Put your Wi-Fi name and password in it (2.4 GHz; a phone hotspot works), then run this again.'
  Write-Host 'Or skip Wi-Fi: the sign works over USB (SIGN_URL=serial:auto). Run this again to flash it as is.'
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
  $root = Join-Path $env:TEMP 'pulse-sign-build'
  Remove-Item -Recurse -Force $root -ErrorAction SilentlyContinue
  $b = New-SketchBuildDir $sketch 'sign' $root
  & $cli core install arduino:renesas_uno | Out-Null
  Write-Host "Compiling and uploading firmware $($b.Fw)…"
  if ($Beacon) {
    & $cli lib install ArduinoBLE | Out-Null
    & $cli compile --fqbn $fqbn --build-property 'compiler.cpp.extra_flags=-DSIGN_BEACON=1' --upload --port $Port $b.Dir
  } else {
    & $cli compile --fqbn $fqbn --upload --port $Port $b.Dir
  }
  if ($LASTEXITCODE -ne 0) { throw 'Upload failed. Close the Arduino IDE serial monitor (and stop Pulse if it drives the sign over USB), and try again. Still stuck: double-tap the RESET button and retry.' }
  Remove-Item -Recurse -Force $root -ErrorAction SilentlyContinue
  Start-Sleep -Seconds 2 # board resets after upload
}

if ($Beacon) {
  Write-Host 'Sign flashed as Bluetooth beacon PULSE-S (no Wi-Fi). Drive it over USB: SIGN_URL=serial:auto, with the server running natively on this machine.'
  exit 0
}

# ---- ask the sign over USB for its status until it has joined Wi-Fi
Write-Host "Waiting up to $TimeoutSec s for the sign to join Wi-Fi…"
$sp = Open-BoardPort $Port $true
$url = $null
$st = $null
$deadline = (Get-Date).AddSeconds($TimeoutSec)
try {
  while (-not $url -and (Get-Date) -lt $deadline) {
    $s = Get-BoardStatus $sp 3
    if ($s) {
      $st = $s
      if ($s.wifi -and $s.ip) { $url = "http://$($s.ip)" }
      elseif ($s.wifi) {
        # Older firmware has no "ip": fall back to its boot line.
        $line = Wait-BoardLine $sp 'Sign ready:' 2
        if ($line -match 'SIGN_URL=(http://[0-9.]+)') { $url = $Matches[1] }
      }
    }
    if (-not $url) { Start-Sleep -Seconds 1 }
  }
} finally {
  if ($sp.IsOpen) { $sp.Close() }
}
if ($st) { Write-Host "  sign: firmware $($st.fw), level $($st.level), Wi-Fi $(if ($st.wifi) { $st.ssid } else { "not joined (trying $($st.ssid))" })" }
if (-not $url) {
  Write-Host 'No Wi-Fi yet. The sign still works over USB (SIGN_URL=serial:auto with the server running natively on this PC).'
  Write-Host 'For Wi-Fi: check the name/password (2.4 GHz), or set a network over USB: pwsh scripts/boards.ps1 wifi'
  exit 1
}
Write-Host "Sign is at $url"
if ($NoEnv) { exit 0 }

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
