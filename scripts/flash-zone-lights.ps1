# Flash the Pulse zone lights (ESP32 DevKit V1, one per zone) from Windows and add them to SIGN_URL.
# Each board keeps the zone it already knows (stored in its flash); one that has none gets its letter
# from arduino/zone-light/zones.map (by MAC) or the next free one, sent over USB ("L calm X"), so its
# Bluetooth beacon reads PULSE-X. No Wi-Fi is needed for any of this.
#
#   pwsh scripts/flash-zone-lights.ps1                 # every ESP32 plugged in, one at a time
#   pwsh scripts/flash-zone-lights.ps1 -Ports COM9     # just this one
#   pwsh scripts/flash-zone-lights.ps1 -Zones B,A      # force the zone of each board (in port order)
#   pwsh scripts/flash-zone-lights.ps1 -NoUpload       # just read zones and addresses from boards already flashed
#   pwsh scripts/flash-zone-lights.ps1 -NoEnv          # don't touch .env
#
# Needs the CP210x USB driver (Windows Update → Optional updates, or silabs.com) and the
# "esp32 by Espressif" core in arduino-cli (installed if missing). arduino/zone-light/arduino_secrets.h
# is optional (Wi-Fi; copied from the sign's if missing). ESP32s only join 2.4 GHz networks.
# Close any serial monitor and stop a Pulse server that drives the boards over USB first.

param(
  [string[]]$Zones = @(),
  [string[]]$Ports = @(),
  [switch]$NoUpload,
  [switch]$NoEnv,
  [int]$TimeoutSec = 30
)

$ErrorActionPreference = 'Stop'
. "$PSScriptRoot/boards-lib.ps1"
$repo = Split-Path -Parent $PSScriptRoot
$sketch = Join-Path $repo 'arduino/zone-light'
$secrets = Join-Path $sketch 'arduino_secrets.h'
# Huge APP partition: Wi-Fi + Bluetooth + web server don't fit the default 1.2 MB app slot.
$fqbn = 'esp32:esp32:esp32:PartitionScheme=huge_app'
$cli = Get-ArduinoCli

# ---- Wi-Fi secrets: optional (reuse the sign's if this sketch has none)
if (-not (Test-Path $secrets)) {
  $signSecrets = Join-Path $repo 'arduino/sign/arduino_secrets.h'
  if (Test-Path $signSecrets) { Copy-Item $signSecrets $secrets }
  else { Write-Host 'No arduino_secrets.h: building without Wi-Fi (USB only). Set Wi-Fi later with: pwsh scripts/boards.ps1 wifi' }
}

# ---- boards: CP210x / CH340 USB-serial ports
if (-not $Ports) { $Ports = @(Get-BoardPorts | Where-Object { -not $_.Arduino } | ForEach-Object { $_.Port }) }
if (-not $Ports) {
  throw 'No ESP32 found. Install the CP210x driver (Windows Update → Optional updates, or silabs.com) and use a data USB cable.'
}
Write-Host ("Boards: " + ($Ports -join ', '))

# ---- compile once, upload to each, one at a time
if (-not $NoUpload) {
  $tmp = Join-Path $env:TEMP 'pulse-zone-build'
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  $b = New-SketchBuildDir $sketch 'zone-light' $tmp
  $build = Join-Path $tmp 'out'
  & $cli core install esp32:esp32 --additional-urls https://espressif.github.io/arduino-esp32/package_esp32_index.json | Out-Null
  Write-Host "Compiling firmware $($b.Fw)…"
  & $cli compile --fqbn $fqbn --output-dir $build $b.Dir
  if ($LASTEXITCODE -ne 0) { throw 'Compile failed.' }
  foreach ($p in $Ports) {
    Write-Host "Uploading to $p… (if it sticks at 'Connecting…', hold the BOOT button)"
    & $cli upload --fqbn $fqbn --port $p --input-dir $build $b.Dir
    if ($LASTEXITCODE -ne 0) { throw "Upload to $p failed. Close any serial monitor on it (and stop Pulse if it drives it over USB) and retry." }
  }
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

# ---- zone map (MAC → letter)
$map = @{}
$mapFile = Join-Path $sketch 'zones.map'
if (Test-Path $mapFile) {
  Get-Content $mapFile | Where-Object { $_ -match '^\s*([0-9A-Fa-f]{4})\s*=\s*([A-Za-z0-9_-]+)' } | ForEach-Object {
    $null = $_ -match '^\s*([0-9A-Fa-f]{4})\s*=\s*([A-Za-z0-9_-]+)'
    $map[$Matches[1].ToUpper()] = $Matches[2].ToUpper()
  }
}

# ---- ask each board over USB who it is; give a zone to one that has none
$found = @{}
$used = @{}
$want = @{}
for ($i = 0; $i -lt $Ports.Count; $i++) { if ($Zones.Count -gt $i) { $want[$Ports[$i]] = $Zones[$i].ToUpper() } }
foreach ($p in $Ports) {
  $sp = Open-BoardPort $p $false
  try {
    $st = Get-BoardStatus $sp $TimeoutSec
    if (-not $st) { Write-Host "  ${p}: no answer over USB (old firmware? run without -NoUpload)"; continue }
    $z = $st.zone
    if ($want[$p]) { $z = $want[$p] }
    elseif (-not $z -and $map[$st.mac]) { $z = $map[$st.mac] }
    elseif (-not $z) { foreach ($c in [char[]]'ABCDEFGHIJKLMNOPQRSTUVWXYZ') { if (-not $used["$c"] -and -not ($map.Values -contains "$c")) { $z = "$c"; break } } }
    if ($z -ne $st.zone) {
      $sp.Write("L $($st.level) $z`n")
      Start-Sleep -Milliseconds 300
      $st = Get-BoardStatus $sp 3
    }
    $used[$z] = $true
    # Just flashed: give it a moment to join Wi-Fi (USB works either way).
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while (-not $st.wifi -and $st.ssid -and (Get-Date) -lt $deadline) {
      Start-Sleep -Seconds 2
      $s2 = Get-BoardStatus $sp 3
      if ($s2) { $st = $s2 }
    }
    $wifi = if ($st.wifi) { "on $($st.ssid) at http://$($st.ip)" } else { "no Wi-Fi yet ($(if ($st.ssid) { "trying $($st.ssid)" } else { 'none set' }))" }
    Write-Host "  $p → $($st.name) (MAC $($st.mac)), firmware $($st.fw), $wifi"
    $found[$z] = if ($st.wifi -and $st.ip) { "http://$($st.ip)" } else { 'serial:auto' }
  } finally {
    if ($sp.IsOpen) { $sp.Close() }
  }
}
if (-not $found.Count) { exit 1 }

if ($NoEnv) {
  Write-Host ('Boards: ' + (($found.Keys | Sort-Object | ForEach-Object { "$_=$($found[$_])" }) -join ','))
  exit 0
}

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
Write-Host 'Restart the server and press "Run test alert" on the dashboard. (serial:auto entries need the server running natively on this PC, not in WSL.)'
