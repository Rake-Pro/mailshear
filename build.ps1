<#
.SYNOPSIS
  Build or install mailshear on Windows.
.DESCRIPTION
  .\build.ps1            builds bin\mailshear.exe
  .\build.ps1 -Install   runs go install so `mailshear` works from any shell;
                         adds $(go env GOPATH)\bin to the user PATH if missing
  .\build.ps1 -Install -NoAddToPath   skip the PATH edit
  .\build.ps1 -Test      runs go vet and go test
  mailshear is one interactive program: run it in a terminal and it walks you
  through the account setup on first start.
#>
param(
  [switch]$Install,
  [switch]$NoAddToPath,
  [switch]$Test
)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
$env:CGO_ENABLED = '0'

if ($Test) {
  go vet ./...
  go test ./...
  exit $LASTEXITCODE
}

if ($Install) {
  # go run ./tools/install: go install, user PATH (registry + broadcast), config bootstrap.
  if ($NoAddToPath) { go run ./tools/install -no-add-to-path } else { go run ./tools/install }
  exit $LASTEXITCODE
}

New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -ldflags="-s -w" -o bin\mailshear.exe ./cmd/mailshear
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Host "built: bin\mailshear.exe  (run: .\bin\mailshear.exe)"
exit 0
