# Builds dist\release: runs the tests, then compiles panel.exe with the
# version from git (e.g. v1.4.0 or v1.4.0-3-gabc1234) and copies the
# documents next to it.
$ErrorActionPreference = 'Stop'

$go = Join-Path $PSScriptRoot 'tools\go\bin\go.exe'
if (!(Test-Path $go)) { $go = (Get-Command go -ErrorAction SilentlyContinue).Source }
if (!$go) { throw 'Go is not installed: put it on PATH or in tools\go' }
# go.mod pins the toolchain; with GOTOOLCHAIN=auto an older Go downloads it.
Write-Host (& $go version)

$version = 'dev'
if (Get-Command git -ErrorAction SilentlyContinue) {
    try {
        $described = & git -C $PSScriptRoot describe --tags --always --dirty 2>$null
        if ($LASTEXITCODE -eq 0 -and $described) { $version = "$described".Trim() }
    } catch {
        # Not a git checkout (e.g. a ZIP download): keep "dev".
    }
}
Write-Host "Building version $version"

$env:GOCACHE = Join-Path $PSScriptRoot '.gocache'
& $go test ./...
if ($LASTEXITCODE -ne 0) { throw "Go tests failed: $LASTEXITCODE" }

$stage = Join-Path $PSScriptRoot 'dist\release'
if (Test-Path $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
New-Item -ItemType Directory -Force $stage | Out-Null

$env:CGO_ENABLED = '0'
& $go build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$version" -o (Join-Path $stage 'panel.exe') .
if ($LASTEXITCODE -ne 0) { throw "Go build failed: $LASTEXITCODE" }

foreach ($file in 'README.md', 'README.txt', 'LICENSE', 'THIRD_PARTY_NOTICES.md', 'panel.json.example') {
    Copy-Item (Join-Path $PSScriptRoot $file) (Join-Path $stage $file) -Force
}
Write-Host "Release staged in $stage"
