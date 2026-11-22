# interlinked installer for Windows (PowerShell 5.1+).
#   irm https://raw.githubusercontent.com/romanpert/ainterlinked/main/install.ps1 | iex
# Downloads the release binary to %LOCALAPPDATA%\Programs\interlinked, verifies
# its checksum, adds it to your user PATH, then wires every detected AI harness.
# Set $env:INTERLINKED_NO_WIRE = '1' to only install the binary.
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = 'romanpert/ainterlinked'
$version = if ($env:INTERLINKED_VERSION) { $env:INTERLINKED_VERSION } else { 'latest' }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$asset = "interlinked-windows-$arch.exe"
$base = if ($version -eq 'latest') { "https://github.com/$repo/releases/latest/download" } else { "https://github.com/$repo/releases/download/$version" }

$dir = Join-Path $env:LOCALAPPDATA 'Programs\interlinked'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir 'interlinked.exe'
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("interlinked-" + [guid]::NewGuid() + '.exe')

Write-Host "Downloading $asset ($version)..."
Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $tmp
$sums = (Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS").Content
if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
$line = ($sums -split "`n") | Where-Object { $_ -match (" " + [regex]::Escape($asset) + '\s*$') } | Select-Object -First 1
$want = if ($line) { ($line -split '\s+')[0].ToLower() } else { '' }
$got = (Get-FileHash -Algorithm SHA256 $tmp).Hash.ToLower()
if (-not $want -or $want -ne $got) { Remove-Item $tmp -Force; throw "checksum mismatch for $asset - aborting" }
Move-Item -Force $tmp $exe
Write-Host "Installed $exe"

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not (($userPath -split ';') -contains $dir)) {
    [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ';' + $dir), 'User')
    $env:Path = $env:Path + ';' + $dir
    Write-Host "Added $dir to your user PATH (new terminals pick it up)."
}

if ($env:INTERLINKED_NO_WIRE -ne '1') { & $exe install }
