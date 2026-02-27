# Camera to Immich

A cross-platform tool for processing camera RAW files with RawTherapee and uploading them to an Immich server.

## Features

- **Multi-format RAW support**: Process any RAW format supported by RawTherapee (ORF, CR2, NEF, ARW, RAF, DNG, etc.)
- **Automatic drive detection**: Finds your camera card automatically by volume label
- **State management**: Remembers which files have been processed to avoid duplicates
- **RawTherapee integration**: Process RAW files using your custom PP3 profiles
- **Immich upload**: Upload both processed and camera-generated JPGs to your Immich server
- **Profile tagging**: Automatically tag processed images with the profile name used
- **Parallel processing**: Utilizes multiple CPU cores for faster RAW processing
- **Cross-platform**: Works on Windows and macOS

## Supported RAW Formats

RawTherapee supports over 100 RAW formats including:

| Camera Brand | RAW Extensions |
|-------------|----------------|
| Olympus/OM System | `.ORF` |
| Canon | `.CR2`, `.CR3` |
| Nikon | `.NEF`, `.NRW` |
| Sony | `.ARW`, `.SRF`, `.SR2` |
| Fujifilm | `.RAF` |
| Panasonic | `.RW2` |
| Pentax | `.PEF`, `.DNG` |
| Leica | `.RWL`, `.DNG` |
| Adobe | `.DNG` |

Configure the `raw_extensions` option to specify which formats to process.

## Prerequisites

1. **RawTherapee** with CLI support
   - Windows: [Download from rawtherapee.com](https://rawtherapee.com/)
   - macOS: `brew install rawtherapee` or download from website

2. **ExifTool** (for reading EXIF metadata from RAW files)
   - Windows: [Download from exiftool.org](https://exiftool.org/) - extract and add to PATH
   - macOS: `brew install exiftool`
   - Linux: `sudo apt install libimage-exiftool-perl` or `sudo dnf install perl-Image-ExifTool`

3. **immich-go** CLI tool
   - Install: `go install github.com/simulot/immich-go@latest`
   - Or download from [GitHub releases](https://github.com/simulot/immich-go/releases)

4. **Go 1.21+** (for building from source)
   - [Download from go.dev](https://go.dev/dl/)

## Installation

### From Source

```bash
# Clone the repository
git clone https://github.com/ohavrylyuk/camera-to-immich.git
cd camera-to-immich

# Build for your platform
go build -o camera-to-immich.exe ./cmd/camera-to-immich  # Windows
go build -o camera-to-immich ./cmd/camera-to-immich       # macOS/Linux

# Or use the build script
.\build.ps1  # Windows PowerShell
```

### Cross-compilation

Build for all platforms from any OS:

```powershell
# Windows (from PowerShell)
$env:GOOS="windows"; $env:GOARCH="amd64"; go build -o camera-to-immich-windows.exe ./cmd/camera-to-immich
$env:GOOS="darwin"; $env:GOARCH="amd64"; go build -o camera-to-immich-macos-intel ./cmd/camera-to-immich
$env:GOOS="darwin"; $env:GOARCH="arm64"; go build -o camera-to-immich-macos-arm ./cmd/camera-to-immich
```

## Configuration

### Initialize Configuration

```bash
camera-to-immich -init
```

This creates a sample configuration file at:
- Windows: `%USERPROFILE%\.camera-to-immich\config.json`
- macOS: `~/.camera-to-immich/config.json`

### Configuration File

```json
{
  "drive_label": "OM SYSTEM",
  "raw_extensions": [".ORF"],
  "convert_to_dng": false,
  "dng_converter_path": "",
  "cleanup_dng_files": true,
  "rawtherapee_executable": "",
  "pp3_profile_path": "/path/to/your/profile.pp3",
  "pp3_bw_profile_path": "/path/to/your/bw-profile.pp3",
  "jpeg_quality": 92,
  "output_directory": "/path/to/output",
  "immich_executable": "",
  "immich_server_url": "https://your-immich-server.com",
  "immich_api_key": "your-api-key-here",
  "immich_album": "Camera Uploads",
  "immich_tags": ["camera", "photography"],
  "process_raw_files": true,
  "upload_camera_jpgs": true,
  "tag_with_profile_name": true,
  "cleanup_after_upload": true,
  "workers": 0,
  "dry_run": false
}
```

### Configuration Options

| Option | Description | Default |
|--------|-------------|---------|
| `drive_label` | Volume label of your camera card | `OM SYSTEM` |
| `raw_extensions` | Array of RAW file extensions to process | `[".ORF"]` |
| `convert_to_dng` | Convert RAW to DNG before RawTherapee (for unsupported cameras) | `false` |
| `dng_converter_path` | Path to Adobe DNG Converter (auto-detected if empty) | Auto |
| `dng_output_directory` | Directory for intermediate DNG files | Temp dir |
| `dng_compressed` | Use compressed DNG format (smaller files) | `false` |
| `dng_embed_original` | Embed original RAW in DNG (larger files) | `false` |
| `cleanup_dng_files` | Delete intermediate DNG files after processing | `true` |
| `rawtherapee_executable` | Path to rawtherapee-cli (auto-detected if empty) | Auto |
| `pp3_profile_path` | Path to your PP3 processing profile (for color images) | Required (if processing RAW) |
| `pp3_bw_profile_path` | Path to your B&W PP3 profile (optional, for B&W detection) | None |
| `jpeg_quality` | Output JPEG quality (1-100) | `92` |
| `output_directory` | Where to save processed JPEGs | `~/.camera-to-immich/output` |
| `tone_formula_path` | Path to tone calibration formula JSON (from tone-calibrator) | None |
| `apply_tone_formula` | Apply tone formula based on camera JPEG EXIF settings (uses `pp3_profile_path` as base) | `false` |
| `immich_executable` | Path to immich-go (auto-detected if empty) | Auto |
| `immich_server_url` | Your Immich server URL | Required |
| `immich_api_key` | Your Immich API key | Required |
| `immich_album` | Album to upload to (optional) | None |
| `immich_tags` | Tags to add to all uploads | `[]` |
| `process_raw_files` | Process RAW files with RawTherapee (if false, only upload JPGs) | `true` |
| `upload_camera_jpgs` | Also upload camera-generated JPGs (when processing RAW) | `true` |
| `tag_with_profile_name` | Tag processed files with profile name | `true` |
| `cleanup_after_upload` | Delete processed files after successful upload to save disk space | `true` |
| `workers` | Number of parallel workers for RAW processing (0 = auto, max 4 to prevent memory issues) | `0` (auto, max 4) |
| `dry_run` | Preview without processing/uploading | `false` |

### Camera-Specific Examples

**Olympus/OM System:**
```json
{
  "drive_label": "OM SYSTEM",
  "raw_extensions": [".ORF"]
}
```

**Canon:**
```json
{
  "drive_label": "EOS_DIGITAL",
  "raw_extensions": [".CR2", ".CR3"]
}
```

**Nikon:**
```json
{
  "drive_label": "NIKON",
  "raw_extensions": [".NEF", ".NRW"]
}
```

**Sony:**
```json
{
  "drive_label": "SONY",
  "raw_extensions": [".ARW"]
}
```

**Multiple Cameras:**
```json
{
  "drive_label": "",
  "raw_extensions": [".ORF", ".CR2", ".NEF", ".ARW", ".RAF", ".DNG"]
}
```

### DNG Conversion for Unsupported Cameras

Some newer cameras (like the OM System OM-3) may not be natively supported by RawTherapee yet. In this case, you can use Adobe DNG Converter to convert the RAW files to DNG format before processing with RawTherapee.

**Requirements:**
- [Adobe DNG Converter](https://www.adobe.com/support/downloads/dng/dng_converter.html) (free download from Adobe)
  - Windows: `C:\Program Files\Adobe\Adobe DNG Converter\Adobe DNG Converter.exe`
  - macOS: `/Applications/Adobe DNG Converter.app`

**Configuration for OM System OM-3 (or other unsupported cameras):**
```json
{
  "drive_label": "OM SYSTEM",
  "raw_extensions": [".ORF"],
  "convert_to_dng": true,
  "cleanup_dng_files": true,
  "pp3_profile_path": "C:/Users/YourName/AppData/Local/RawTherapee/profiles/OM3_Default_DNG.pp3"
}
```

**How it works:**
1. RAW files (e.g., `.ORF`) are scanned from the camera card
2. Each RAW file is converted to DNG using Adobe DNG Converter
3. The DNG file is processed with RawTherapee using your PP3 profile
4. The resulting JPEG is uploaded to Immich
5. Intermediate DNG files are cleaned up (if `cleanup_dng_files` is enabled)

**PP3 Profile Notes:**
- When using DNG conversion, you may need to create a separate PP3 profile for DNG files
- The DNG file may have slightly different characteristics than the original RAW
- Test with a few files first to ensure your profile produces the desired results

### B&W Detection for Camera B&W Shots

When shooting in B&W mode on your camera, the JPG sidecar file is grayscale but the RAW file still contains full color data. The tool can automatically detect B&W images from the sidecar JPG and handle them specially:

**How it works:**
1. When processing images, the tool analyzes the sidecar JPG (camera-generated JPG)
2. If ~95% of pixels are grayscale (R≈G≈B), the image is detected as B&W
3. If `pp3_bw_profile_path` is configured and exists, the B&W profile is used to process the RAW
4. If no B&W profile is configured, the sidecar JPG is uploaded instead (preserving the camera's B&W processing)
5. B&W images are tagged with `b&w` tag for easy filtering in Immich

**Configuration for B&W detection:**
```json
{
  "pp3_profile_path": "/path/to/color/profile.pp3",
  "pp3_bw_profile_path": "/path/to/bw/profile.pp3"
}
```

**Tagging behavior:**
- B&W processed images get: `b&w`, `processed`, `profile:YourBWProfileName`
- B&W sidecar-only images get: `b&w`, `camera-original`
- Color processed images get: `processed`, `profile:YourColorProfileName`

This feature is useful for photographers who:
- Shoot in B&W mode in-camera for creative preview
- Want to use a dedicated B&W processing profile for their RAW files
- Want to preserve the camera's B&W processing when no custom profile is available

### Tone Calibration for OM System Cameras

OM System cameras (OM-3, OM-1, etc.) have in-camera tone adjustments (Highlights, Shadows, Midtones, Contrast) that affect the camera-generated JPEG. When shooting RAW+JPEG, these settings are stored in the JPEG EXIF data. The tone calibration feature automatically applies matching adjustments to your RAW processing, so your processed JPEGs match the camera's rendering.

**How it works:**
1. A calibration tool analyzes RAW+JPEG pairs to learn how camera settings map to RawTherapee's ToneEqualizer
2. Linear regression generates a formula file (`tone_formula.json`) with coefficients for each ToneEqualizer band
3. During processing, the tool reads the sidecar JPEG's EXIF data and generates a custom PP3 profile
4. The custom profile applies the ToneEqualizer settings matching your in-camera tone choices

**Step 1: Generate the Calibration Formula**

Run the tone calibrator on a set of RAW+JPEG pairs (minimum 20+ recommended):

```bash
# Build the calibrator
go build -o tone-calibrator.exe ./cmd/tone-calibrator

# Run calibration on your camera card with a base PP3 profile
tone-calibrator -source "E:\DCIM\100OMSYS" -formula tone_formula.json -base-pp3 "C:\path\to\your\base-profile.pp3"

# Example with DNG conversion (for cameras not supported by RawTherapee)
tone-calibrator -source "E:\DCIM\100OMSYS" -formula tone_formula.json -base-pp3 "C:\path\to\base.pp3" -dng

# Apply formula to a single RAW file (test mode)
tone-calibrator -apply "E:\DCIM\100OMSYS\P1193660.ORF" -formula tone_formula.json -base-pp3 "C:\path\to\base.pp3"

# Command line options:
#   -source    Directory containing ORF+JPG pairs (required for calibration)
#   -formula   Output/input formula JSON file
#   -base-pp3  Base PP3 profile to overlay ToneEqualizer settings (required)
#   -dng       Use ORF → DNG → JPG workflow (for cameras not supported by RawTherapee)
#   -start     Start from file containing this substring (to resume calibration)
#   -limit     Limit number of samples to process
#   -apply     Apply formula to a single RAW file (test mode)
#   -out       Output JPG path for -apply mode (default: same directory as input)
```

The calibrator outputs accuracy metrics:
- **R²** (0-1): How well the formula fits the data (>0.8 is good)
- **RMSE**: Average error in ToneEqualizer units
- **Mean Error**: Average prediction error per band

**Step 2: Configure camera-to-immich**

Add tone calibration settings to your config.json:

```json
{
  "apply_tone_formula": true,
  "tone_formula_path": "C:/Users/YourName/.camera-to-immich/tone_formula.json"
}
```

| Option | Description |
|--------|-------------|
| `apply_tone_formula` | Enable automatic tone matching |
| `tone_formula_path` | Path to the calibration formula JSON |

The `pp3_profile_path` is used as the base profile for the ToneEqualizer overlay.

**Step 3: Process as usual**

When running camera-to-immich, the tool will automatically:
1. Find the sidecar JPEG for each RAW file
2. Extract EXIF tone settings (Highlights, Shadows, Midtones, Contrast)
3. Calculate ToneEqualizer band values using the formula
4. Generate a temporary PP3 with your base profile (`pp3_profile_path`) + ToneEqualizer settings
5. Process the RAW with the customized profile

**OM System EXIF Tags Used:**
- `ToneLevel` - Contains H (Highlights), S (Shadows), M (Midtones) values (-7 to +7)
- `PictureModeContrast` - Contrast adjustment (-2 to +2)

**Formula File Format:**
```json
{
  "bands": {
    "Band0": {"C0": -5.2, "Highlights": 1.8, "Shadows": -0.5, "Midtones": 0.3, "Contrast": 2.1},
    "Band1": {"C0": -3.1, "Highlights": 2.5, "Shadows": -0.8, "Midtones": 0.4, "Contrast": 1.5},
    ...
  },
  "accuracy": {
    "Band0": {"R2": 0.89, "RMSE": 4.2, "MeanError": -0.3},
    ...
  },
  "sample_count": 27,
  "created_at": "2026-02-08T12:00:00Z"
}
```

### Grain Analyzer (Optional Tool)

The grain analyzer is a standalone utility that analyzes film scans to derive RawTherapee film grain settings. This is useful when you want to add film-like grain to your digital photos that matches the characteristics of a specific film stock.

**Use Case:**
- You have scanned film photos and want to replicate that grain look on digital images
- You want to create custom film grain profiles for RawTherapee based on actual film characteristics

**Build the tool:**

```bash
go build -o grain-analyzer.exe ./cmd/grain-analyzer  # Windows
go build -o grain-analyzer ./cmd/grain-analyzer       # macOS/Linux
```

**Usage:**

```bash
# Analyze a directory of film scans
grain-analyzer -source "C:\path\to\film\scans"

# Analyze with a specific sample size
grain-analyzer -source "C:\path\to\film\scans" -samples 10
```

**Command-line Options:**

| Option | Description | Default |
|--------|-------------|---------|
| `-source` | Directory containing film scans (JPG/PNG) | Required |
| `-samples` | Number of images to analyze | `5` |

**Output:**

The tool analyzes the grain characteristics of your film scans and outputs recommended RawTherapee Film Grain settings:

```
RECOMMENDED RAWTHERAPEE FILM GRAIN SETTINGS
==================================================

[Film Grain]
Enabled=true
ISO=200
Strength=25
Scale=85
```

**Metrics Analyzed:**
- **Std Dev (grain indicator)**: Correlates with grain visibility
- **Local Variance**: Indicates grain texture intensity
- **High Freq Energy**: Indicates grain size/coarseness

**Applying the Settings:**

1. Open RawTherapee
2. Go to the **Detail** tab → **Film Grain**
3. Enable Film Grain
4. Enter the suggested ISO, Strength, and Scale values
5. Save as a PP3 profile for reuse

## Usage

### Basic Usage

1. Insert your camera card
2. Run the processor:

```bash
camera-to-immich
```

### Command-line Options

```bash
camera-to-immich [options]

Options:
  -config string     Path to configuration file
  -profile string    Path to PP3 profile (overrides config)
  -server string     Immich server URL (overrides config)
  -key string        Immich API key (overrides config)
  -output string     Output directory (overrides config)
  -drive string      Drive label to search for (overrides config)
  -dry-run           Show what would be done without doing it
  -jpg-only          Upload JPG files only, skip RAW processing
  -no-camera-jpgs    Skip uploading camera-generated JPGs (only upload processed files)
  -skip-upload       Process files but skip uploading to Immich
  -limit int         Limit the number of files to process (0 = no limit)
  -workers int       Number of parallel workers for processing (0 = auto based on CPU cores)
  -keep-files        Keep processed files in output directory (don't clean up)
  -list-drives       List all available drives and exit
  -init              Create a sample configuration file
  -verbose           Enable verbose output
  -version           Show version information
  -state-info        Show state file information and exit
  -clear-state       Clear the processed files state and exit
  -cache-info        Show cache information (size, file count) and exit
  -clear-cache       Clear the preview image cache and exit
```

### Examples

```bash
# List available drives to find your camera card
camera-to-immich -list-drives

# Preview what would be processed (dry run)
camera-to-immich -dry-run

# Use a specific profile
camera-to-immich -profile "C:\Profiles\vivid.pp3"

# Override Immich settings
camera-to-immich -server "https://photos.example.com" -key "your-api-key"

# Upload JPG files only (skip RAW processing)
camera-to-immich -jpg-only

# Process with verbose output
camera-to-immich -verbose

# Process and keep the output files (don't auto-cleanup)
camera-to-immich -keep-files

# Check state file information
camera-to-immich -state-info

# Clear processed files history (start fresh)
camera-to-immich -clear-state

# Process with 8 parallel workers (for multi-core CPUs)
camera-to-immich -workers 8

# Check cache size (preview images from web editor)
camera-to-immich -cache-info

# Clear the preview cache to free disk space
camera-to-immich -clear-cache
```

## Workflow

1. **Drive Detection**: The tool searches for a drive with the configured label
2. **File Scanning**: Scans the DCIM folder for RAW and JPG files
3. **State Check**: Compares found files against previously processed files
4. **Parallel Processing**: Uses RawTherapee CLI to convert RAW → JPEG with your PP3 profile (uses multiple CPU cores for faster processing)
5. **Upload**: Uploads processed JPEGs (tagged with profile name) and camera JPGs to Immich
6. **Cleanup**: Deletes processed files from output directory (unless `-keep-files` is used)
7. **State Update**: Records processed files to avoid re-processing

## Performance

The tool supports parallel RAW processing to take advantage of multi-core CPUs:

- By default, the tool uses up to **4 parallel workers** (capped to prevent memory exhaustion)
- RawTherapee uses ~1-2GB RAM per instance, so running too many in parallel can cause out-of-memory errors
- Use `-workers N` to manually set the number of workers based on your available RAM:
  - **4 workers** recommended for 16 GB RAM
  - **6-8 workers** recommended for 32 GB RAM
  - **12-16 workers** recommended for 64 GB RAM
- Example: With 4 workers processing 8 files, total time is ~3.4x faster than sequential processing

**Benchmark (8 files with 4 workers):**
- Sequential would take: ~188s
- Parallel time: ~56s
- Speedup: ~3.4x

## File Structure

```
~/.camera-to-immich/
├── config.json      # Configuration file
├── state.json       # Processing state (tracked files)
└── output/          # Default output directory for processed JPEGs
    ├── *.jpg        # Processed JPEG files (cleaned up after upload)
    └── .cache/      # Preview image cache (from web editor)
```

## Cache Management

When using the web editor (`-editor` mode), preview images are generated and cached to improve performance. Over time, this cache can grow significantly.

### Automatic Cleanup
- Cache files for processed images are automatically cleaned up after successful processing
- This prevents the cache from growing indefinitely during normal usage

### Manual Cleanup
- **Check cache size**: `camera-to-immich -cache-info`
- **Clear all cache**: `camera-to-immich -clear-cache`

### Web Editor API
When the web editor is running, you can also manage the cache via the API:
- **GET `/api/cache`** - Get cache statistics (file count, total size)
- **DELETE `/api/cache`** - Clear all cached preview files

### Cache Location
The cache is stored in `.cache` subdirectory of your output directory:
- Default: `~/.camera-to-immich/output/.cache/`
- Custom: `{output_directory}/.cache/`

## Troubleshooting

### Drive not found

- Make sure your camera card is inserted
- Check the volume label matches (default: "OM SYSTEM")
- Run `camera-to-immich -list-drives` to see available drives

### RawTherapee not found

- Install RawTherapee with CLI support
- Set the path in config: `"rawtherapee_executable": "C:\\Program Files\\RawTherapee\\rawtherapee-cli.exe"`

### immich-go not found

- Install: `go install github.com/simulot/immich-go@latest`
- Or download from GitHub and set path in config

### ExifTool not found

ExifTool is required for reading EXIF metadata from RAW files (ORF, CR2, NEF, etc.):

- **Windows**: Download from [exiftool.org](https://exiftool.org/), extract `exiftool(-k).exe`, rename to `exiftool.exe`, and add to your PATH
- **macOS**: `brew install exiftool`
- **Linux**: `sudo apt install libimage-exiftool-perl` or `sudo dnf install perl-Image-ExifTool`

Verify installation: `exiftool -ver` should show version number

### Upload fails

- Verify your Immich server URL and API key
- Test connection: `immich-go upload -server YOUR_URL -key YOUR_KEY -dry-run .`

## Building

### Requirements

- Go 1.21 or later

### Build Commands

```bash
# Development build
go build ./cmd/camera-to-immich

# Production build (smaller binary)
go build -ldflags="-s -w" ./cmd/camera-to-immich

# Run tests
go test ./...
```

## License

MIT License - See LICENSE file for details.

## Contributing

1. Fork the repository
2. Create your feature branch
3. Commit your changes
4. Push to the branch
5. Create a Pull Request