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

2. **immich-go** CLI tool
   - Install: `go install github.com/simulot/immich-go@latest`
   - Or download from [GitHub releases](https://github.com/simulot/immich-go/releases)

3. **Go 1.21+** (for building from source)
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
  "rawtherapee_executable": "",
  "pp3_profile_path": "/path/to/your/profile.pp3",
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
| `rawtherapee_executable` | Path to rawtherapee-cli (auto-detected if empty) | Auto |
| `pp3_profile_path` | Path to your PP3 processing profile | Required (if processing RAW) |
| `jpeg_quality` | Output JPEG quality (1-100) | `92` |
| `output_directory` | Where to save processed JPEGs | `~/.camera-to-immich/output` |
| `immich_executable` | Path to immich-go (auto-detected if empty) | Auto |
| `immich_server_url` | Your Immich server URL | Required |
| `immich_api_key` | Your Immich API key | Required |
| `immich_album` | Album to upload to (optional) | None |
| `immich_tags` | Tags to add to all uploads | `[]` |
| `process_raw_files` | Process RAW files with RawTherapee (if false, only upload JPGs) | `true` |
| `upload_camera_jpgs` | Also upload camera-generated JPGs (when processing RAW) | `true` |
| `tag_with_profile_name` | Tag processed files with profile name | `true` |
| `cleanup_after_upload` | Delete processed files after successful upload to save disk space | `true` |
| `workers` | Number of parallel workers for RAW processing (0 = auto-detect based on CPU cores) | `0` (auto) |
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

- By default, the tool auto-detects the number of CPU cores and uses that many parallel workers
- Use `-workers N` to manually set the number of workers
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
```

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