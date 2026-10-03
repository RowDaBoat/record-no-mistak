# Builds rec.exe (no cgo, no console window). Needs Go; ffmpeg 8+ at runtime.
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
go run github.com/akavel/rsrc@v0.10.2 -manifest rec.manifest -arch amd64 -o rsrc_windows_amd64.syso
go build -trimpath -ldflags "-s -w -H=windowsgui" -o rec.exe .
Write-Host "Built $PSScriptRoot\rec.exe"
