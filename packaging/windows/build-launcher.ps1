<#
  Builds the MaxZit Launcher (with its icon and version resources) and its
  per-user MSI with the setup wizard.

    .\packaging\windows\build-launcher.ps1 -Version 1.3.1

  Output: bin\launcher\maxzit-launcher.exe and
  dist\maxzit-launcher-<version>-x64.msi. Same tools as build-msi.ps1
  (Go, WiX 5.0.2 with the UI extension); signing with -SignThumbprint or
  WATCHZIT_SIGN_THUMBPRINT when a certificate is there.
#>
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$SignThumbprint = $env:WATCHZIT_SIGN_THUMBPRINT,
    [string]$TimestampUrl = "http://timestamp.digicert.com"
)
$ErrorActionPreference = "Stop"

function Invoke-Sign([string[]]$Files) {
    if (-not $SignThumbprint) { return }
    $signtool = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" -ErrorAction SilentlyContinue |
        Sort-Object FullName -Descending | Select-Object -First 1
    if (-not $signtool) { throw "signtool.exe not found; install the Windows SDK (Signing Tools)" }
    foreach ($f in $Files) {
        & $signtool.FullName sign /sha1 $SignThumbprint /fd sha256 /tr $TimestampUrl /td sha256 /d "MaxZit Launcher" $f
        if ($LASTEXITCODE -ne 0) { throw "signing $f failed" }
    }
}

$root = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$dist = Join-Path $root "dist"
$work = Join-Path $root "bin\launcher"
New-Item -ItemType Directory -Force $dist, $work | Out-Null

$msiVersion = ($Version -replace '[^0-9.].*$', '')
if ($msiVersion -notmatch '^\d+\.\d+\.\d+$') { throw "Version must start with major.minor.patch, got '$Version'" }

Push-Location $root
try {
    $env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
    if (-not $env:GOTMPDIR) { $env:GOTMPDIR = Join-Path $root ".gotmp"; New-Item -ItemType Directory -Force $env:GOTMPDIR | Out-Null }
    $exe = Join-Path $work "maxzit-launcher.exe"
    # Icon, version and publisher as Windows resources of the exe (Apps and
    # the watchzit: links show this icon).
    $cmdDir = Join-Path $root "cmd\maxzit-launcher"
    Push-Location $cmdDir
    try {
        go run github.com/tc-hib/go-winres@v0.3.3 simply --arch amd64 --manifest gui --icon (Join-Path $root "brand\maxzit-icon-256.png") `
            --product-name "MaxZit Launcher" --file-description "MaxZit Launcher" --copyright "MaxZit" `
            --original-filename maxzit-launcher.exe --product-version "$msiVersion.0" --file-version "$msiVersion.0"
        if ($LASTEXITCODE -ne 0) { throw "go-winres failed" }
    } finally { Pop-Location }
    try {
        go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$Version" -o $exe ./cmd/maxzit-launcher
        if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    } finally { Remove-Item (Join-Path $cmdDir "rsrc_windows_amd64.syso") -ErrorAction SilentlyContinue }
    Invoke-Sign $exe

    $wix = Join-Path $env:USERPROFILE ".dotnet\tools\wix.exe"
    if (-not (Test-Path $wix)) { $wix = "wix" }
    $msi = Join-Path $dist "maxzit-launcher-$Version-x64.msi"
    & $wix build -ext WixToolset.UI.wixext -arch x64 `
        -d "Version=$msiVersion" -d "LauncherExe=$exe" -d "ArtDir=$PSScriptRoot" -d "RootDir=$root" `
        -o $msi (Join-Path $PSScriptRoot "launcher.wxs")
    if ($LASTEXITCODE -ne 0) { throw "wix build failed" }
    Invoke-Sign $msi
    Remove-Item (Join-Path $dist "*.wixpdb") -ErrorAction SilentlyContinue
    Write-Host "built $msi"
}
finally {
    Pop-Location
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
}
