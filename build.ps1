# Build script for camera-to-immich
# Run from the project root directory

$ErrorActionPreference = "Stop"

$projectName = "camera-to-immich"
$outputDir = ".\build"

# All cmd/ binaries to build. Add new entries here as new tools are added.
$binaries = @(
    @{ Name = "camera-to-immich"; Path = ".\cmd\camera-to-immich" },
    @{ Name = "chroma-denoise";   Path = ".\cmd\chroma-denoise"   }
)

# Target platforms (GOOS, GOARCH, output suffix, extension).
$targets = @(
    @{ GOOS = "windows"; GOARCH = "amd64"; Suffix = "windows-amd64"; Ext = ".exe" },
    @{ GOOS = "darwin";  GOARCH = "amd64"; Suffix = "darwin-amd64";  Ext = ""     },
    @{ GOOS = "darwin";  GOARCH = "arm64"; Suffix = "darwin-arm64";  Ext = ""     },
    @{ GOOS = "linux";   GOARCH = "amd64"; Suffix = "linux-amd64";   Ext = ""     }
)

# Create output directory
New-Item -ItemType Directory -Force -Path $outputDir | Out-Null

Write-Host "Building $projectName..." -ForegroundColor Cyan

foreach ($target in $targets) {
    Write-Host "`nBuilding for $($target.GOOS)/$($target.GOARCH)..." -ForegroundColor Yellow
    $env:GOOS = $target.GOOS
    $env:GOARCH = $target.GOARCH

    foreach ($bin in $binaries) {
        $outFile = "$outputDir\$($bin.Name)-$($target.Suffix)$($target.Ext)"
        go build -ldflags="-s -w" -o $outFile $bin.Path
        if ($LASTEXITCODE -eq 0) {
            Write-Host "  ✓ $outFile" -ForegroundColor Green
        } else {
            Write-Host "  ✗ Build failed for $($bin.Name)" -ForegroundColor Red
            exit 1
        }
    }
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