<#
.SYNOPSIS
  Builds the Windows agent: native backend → remote-agent.exe → signed MSI.

.EXAMPLE
  ./packaging/windows/build.ps1 -Version 1.2.3                  # mingw native, unsigned
  ./packaging/windows/build.ps1 -Version 1.2.3 -Native msvc     # MSVC DLL with the WGC path
  ./packaging/windows/build.ps1 -Version 1.2.3 -Stage exe       # stop after the exe (CI signs it, then -Stage msi)

.NOTES
  Requirements on the build machine:
    - Go (version from agent/go.mod)
    - mingw-w64 gcc/g++ on PATH (MSYS2 UCRT64: pacman -S mingw-w64-ucrt-x86_64-gcc make).
      cgo always needs gcc on Windows, even for -Native msvc.
    - -Native msvc: Visual Studio 2022 Build Tools + Windows SDK (C++/WinRT), CMake ≥ 3.24
    - WiX v5: dotnet tool install --global wix --version 5.0.2
              wix extension add --global WixToolset.Util.wixext/5.0.2
              wix extension add --global WixToolset.UI.wixext/5.0.2
  Signing (optional, applied to the exe(s) and the MSI):
    - $env:SIGNTOOL_CERT_SHA1  thumbprint of an OV certificate in the store (hardware token / Key Vault CSP), or
    - CI uses azure/trusted-signing-action between -Stage exe and -Stage msi (see .github/workflows/agent-windows.yml).
    - $env:SIGNTOOL_TIMESTAMP_URL defaults to http://timestamp.digicert.com
  Update signing: the release key lives in proto/release_key.go; sign the MSI with
  `remotectl-release sign` (server/cmd/remotectl-release) after Authenticode signing.
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$Version,
  [ValidateSet('mingw', 'msvc')][string]$Native = 'mingw',
  [ValidateSet('all', 'native', 'exe', 'msi')][string]$Stage = 'all',
  [string]$ProductName = 'RemoteAgent',
  # Pre-fills the server address on the installer's "Add this PC" page.
  [string]$ServerUrl = $env:SERVER_URL
)

$ErrorActionPreference = 'Stop'
$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$NativeDir = Join-Path $Root 'native\windows'
$AgentDir = Join-Path $Root 'agent'
$BinDir = Join-Path $AgentDir 'dist\windows'
$OutDir = Join-Path $Root 'dist'
New-Item -ItemType Directory -Force -Path $BinDir, $OutDir | Out-Null

function Invoke-Checked([string]$File, [string[]]$Arguments) {
  & $File @Arguments
  if ($LASTEXITCODE -ne 0) { throw "$File $($Arguments -join ' ') failed with exit code $LASTEXITCODE" }
}

function Invoke-Sign([string[]]$Files) {
  if (-not $env:SIGNTOOL_CERT_SHA1) { Write-Host 'signing: SIGNTOOL_CERT_SHA1 not set, skipping'; return }
  $ts = if ($env:SIGNTOOL_TIMESTAMP_URL) { $env:SIGNTOOL_TIMESTAMP_URL } else { 'http://timestamp.digicert.com' }
  foreach ($f in $Files) {
    Invoke-Checked 'signtool.exe' @('sign', '/sha1', $env:SIGNTOOL_CERT_SHA1, '/fd', 'SHA256', '/tr', $ts, '/td', 'SHA256', $f)
  }
}

function Build-Native {
  if ($Native -eq 'mingw') {
    Invoke-Checked 'make' @('-C', $NativeDir, '-f', 'Makefile.mingw', 'CXX=g++', 'AR=ar')
  } else {
    $b = Join-Path $NativeDir 'build\cmake'
    Invoke-Checked 'cmake' @('-S', $NativeDir, '-B', $b, '-A', 'x64', '-DRC_WGC=ON')
    Invoke-Checked 'cmake' @('--build', $b, '--config', 'Release')
    Copy-Item (Join-Path $NativeDir 'build\msvc\rcnative.dll') $BinDir -Force
  }
}

function Build-Exe {
  $env:CGO_ENABLED = '1'
  $env:GOOS = 'windows'
  $env:GOARCH = 'amd64'
  if (-not $env:CC) { $env:CC = 'gcc' }
  if (-not $env:CXX) { $env:CXX = 'g++' }
  $ld = "-s -w -X main.version=$Version"
  $tags = if ($Native -eq 'msvc') { 'rcnative_msvc' } else { '' }
  Push-Location $AgentDir
  try {
    Invoke-Checked 'go' @('build', '-trimpath', "-tags=$tags", "-ldflags=$ld", '-o', (Join-Path $BinDir 'remote-agent.exe'), './cmd/remote-agent')
  } finally { Pop-Location }
  $toSign = @(Join-Path $BinDir 'remote-agent.exe')
  if ($Native -eq 'msvc') { $toSign += (Join-Path $BinDir 'rcnative.dll') }
  Invoke-Sign $toSign
}

function Build-Msi {
  $msi = Join-Path $OutDir "$ProductName-$Version-x64.msi"
  $dll = if ($Native -eq 'msvc') { 'yes' } else { 'no' }
  # MSI versions are numeric major.minor.build; strip any pre-release suffix.
  $msiVersion = ($Version -split '[-+]')[0]
  $wixArgs = @('build', (Join-Path $PSScriptRoot 'RemoteAgent.wxs'), '-arch', 'x64',
    '-ext', 'WixToolset.Util.wixext', '-ext', 'WixToolset.UI.wixext',
    '-d', "Version=$msiVersion", '-d', "BinDir=$BinDir", '-d', "ProductName=$ProductName", '-d', "IncludeNativeDll=$dll")
  if ($ServerUrl) { $wixArgs += @('-d', "ServerUrl=$ServerUrl") }
  Invoke-Checked 'wix' ($wixArgs + @('-o', $msi))
  Invoke-Sign @($msi)
  $hash = (Get-FileHash -Algorithm SHA256 $msi).Hash.ToLower()
  Set-Content -Path "$msi.sha256" -Value "$hash  $(Split-Path -Leaf $msi)" -NoNewline
  Write-Host "built $msi"
  Write-Host "sha256 $hash"
}

switch ($Stage) {
  'native' { Build-Native }
  'exe' { Build-Native; Build-Exe }
  'msi' { Build-Msi }
  'all' { Build-Native; Build-Exe; Build-Msi }
}
