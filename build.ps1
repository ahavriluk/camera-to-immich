# Build script for camera-to-immich
# Run from the project root directory

$ErrorActionPreference = "Stop"

$projectName = "camera-to-immich"
$outputDir = ".\build"

# Create output directory
New-Item -ItemType Directory -Force -Path $outputDir | Out-Null

Write-Host "Building $projectName..." -ForegroundColor Cyan

# Build for Windows (amd64)
Write-Host "`nBuilding for Windows (amd64)..." -ForegroundColor Yellow
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -ldflags="-s -w" -o "$outputDir\$projectName-windows-amd64.exe" .\cmd\camera-to-immich
if ($LASTEXITCODE -eq 0) {
    Write-Host "  ✓ $outputDir\$projectName-windows-amd64.exe" -ForegroundColor Green
} else {
    Write-Host "  ✗ Build failed" -ForegroundColor Red
    exit 1
}

# Build for macOS (Intel)
Write-Host "`nBuilding for macOS (Intel)..." -ForegroundColor Yellow
$env:GOOS = "darwin"
$env:GOARCH = "amd64"
go build -ldflags="-s -w" -o "$outputDir\$projectName-darwin-amd64" .\cmd\camera-to-immich
if ($LASTEXITCODE -eq 0) {
    Write-Host "  ✓ $outputDir\$projectName-darwin-amd64" -ForegroundColor Green
} else {
    Write-Host "  ✗ Build failed" -ForegroundColor Red
    exit 1
}

# Build for macOS (Apple Silicon)
Write-Host "`nBuilding for macOS (Apple Silicon)..." -ForegroundColor Yellow
$env:GOOS = "darwin"
$env:GOARCH = "arm64"
go build -ldflags="-s -w" -o "$outputDir\$projectName-darwin-arm64" .\cmd\camera-to-immich
if ($LASTEXITCODE -eq 0) {
    Write-Host "  ✓ $outputDir\$projectName-darwin-arm64" -ForegroundColor Green
} else {
    Write-Host "  ✗ Build failed" -ForegroundColor Red
    exit 1
}

# Reset to host environment
$env:GOOS = ""
$env:GOARCH = ""

Write-Host "`n✓ All builds completed successfully!" -ForegroundColor Green
Write-Host "`nBuild artifacts in: $outputDir" -ForegroundColor Cyan

# List built files
Write-Host "`nBuilt files:" -ForegroundColor Cyan
Get-ChildItem $outputDir | ForEach-Object {
    $size = [math]::Round($_.Length / 1MB, 2)
    Write-Host "  $($_.Name) ($size MB)"
}