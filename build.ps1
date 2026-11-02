# Rebuilds duetto.exe from source on Windows. Requires Go >= 1.24 and Node >= 20.
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
Push-Location web
npm ci
npm run check
npm test
npm run build
Pop-Location
go test ./...
go build -trimpath -ldflags "-s -w -X main.version=$(git describe --always --dirty 2>$null)" -o duetto.exe ./cmd/duetto
Write-Host "Built duetto.exe"
