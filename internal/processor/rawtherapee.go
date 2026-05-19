package processor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	customexif "github.com/ohavrylyuk/camera-to-immich/internal/exif"
)

// RawTherapeeConfig contains configuration for RawTherapee processing
type RawTherapeeConfig struct {
	ExecutablePath string        // Path to rawtherapee-cli executable
	ProfilePath    string        // Path to the PP3 profile file
	OutputDir      string        // Directory for processed JPEGs
	Quality        int           // JPEG quality (1-100)
	Timeout        time.Duration // Timeout for processing a single file (0 = no timeout)
}

// RawTherapee handles processing ORF files with RawTherapee CLI
type RawTherapee struct {
	config RawTherapeeConfig
}

// NewRawTherapee creates a new RawTherapee processor
func NewRawTherapee(config RawTherapeeConfig) (*RawTherapee, error) {
	// Set defaults
	if config.ExecutablePath == "" {
		config.ExecutablePath = findRawTherapeeExecutable()
	}

	if config.Quality == 0 {
		config.Quality = 92
	}

	// Validate executable exists
	if _, err := exec.LookPath(config.ExecutablePath); err != nil {
		return nil, fmt.Errorf("rawtherapee-cli not found at '%s': %v", config.ExecutablePath, err)
	}

	// Validate profile exists
	if config.ProfilePath != "" {
		if _, err := os.Stat(config.ProfilePath); os.IsNotExist(err) {
			return nil, fmt.Errorf("PP3 profile not found at '%s'", config.ProfilePath)
		}
	}

	// Ensure output directory exists
	if config.OutputDir != "" {
		if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create output directory: %v", err)
		}
	}

	return &RawTherapee{config: config}, nil
}

// ProcessFile processes a single ORF file and returns the path to the output JPEG
// Uses the default profile configured in RawTherapeeConfig
func (rt *RawTherapee) ProcessFile(inputPath string) (string, error) {
	return rt.ProcessFileWithProfile(inputPath, rt.config.ProfilePath)
}

// ProcessFileWithProfile processes a single RAW file with a specific PP3 profile
// If profilePath is empty, processes without a profile (RawTherapee defaults)
func (rt *RawTherapee) ProcessFileWithProfile(inputPath string, profilePath string) (string, error) {
	// Determine output path
	baseName := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	outputPath := filepath.Join(rt.config.OutputDir, baseName+".jpg")

	// Build command arguments
	args := []string{
		"-o", outputPath,
		"-j" + fmt.Sprintf("%d", rt.config.Quality), // JPEG quality
		"-Y", // Overwrite output if exists
	}

	// Add profile if specified
	if profilePath != "" {
		args = append(args, "-p", profilePath)
	}

	// Add input file
	args = append(args, "-c", inputPath)

	// Execute rawtherapee-cli with optional timeout
	var cmd *exec.Cmd
	var ctx context.Context
	var cancel context.CancelFunc
	
	timeout := rt.config.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute // Default 5 minute timeout per file
	}
	
	ctx, cancel = context.WithTimeout(context.Background(), timeout)
	defer cancel()
	
	cmd = exec.CommandContext(ctx, rt.config.ExecutablePath, args...)
	output, err := cmd.CombinedOutput()
	
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("rawtherapee-cli timed out after %v", timeout)
	}
	if err != nil {
		return "", fmt.Errorf("rawtherapee-cli failed: %v\nOutput: %s", err, string(output))
	}

	// Verify output file was created
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		return "", fmt.Errorf("output file was not created: %s", outputPath)
	}

	return outputPath, nil
}

// ProcessFileStacked processes a single RAW file with multiple stacked PP3 profiles.
// Later profiles in the slice override settings from earlier ones (RawTherapee
// semantics for repeated -p flags).
func (rt *RawTherapee) ProcessFileStacked(inputPath string, profilePaths []string) (string, error) {
	baseName := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	outputPath := filepath.Join(rt.config.OutputDir, baseName+".jpg")

	args := []string{
		"-o", outputPath,
		"-j" + fmt.Sprintf("%d", rt.config.Quality),
		"-Y",
	}
	for _, p := range profilePaths {
		if p == "" {
			continue
		}
		args = append(args, "-p", p)
	}
	args = append(args, "-c", inputPath)

	timeout := rt.config.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, rt.config.ExecutablePath, args...)
	output, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("rawtherapee-cli timed out after %v", timeout)
	}
	if err != nil {
		return "", fmt.Errorf("rawtherapee-cli failed: %v\nOutput: %s", err, string(output))
	}

	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		return "", fmt.Errorf("output file was not created: %s", outputPath)
	}

	return outputPath, nil
}

// GetPerImageProfilePath returns the path to a per-image PP3 profile if it exists
// Returns empty string if no per-image profile exists
func (rt *RawTherapee) GetPerImageProfilePath(imagePath string) string {
	baseName := strings.TrimSuffix(filepath.Base(imagePath), filepath.Ext(imagePath))
	pp3Path := filepath.Join(rt.config.OutputDir, baseName+".pp3")
	
	if _, err := os.Stat(pp3Path); err == nil {
		return pp3Path
	}
	return ""
}

// GetProfileName returns the name of the PP3 profile being used
func (rt *RawTherapee) GetProfileName() string {
	if rt.config.ProfilePath == "" {
		return "default"
	}
	return strings.TrimSuffix(filepath.Base(rt.config.ProfilePath), ".pp3")
}

// GetOutputDir returns the output directory
func (rt *RawTherapee) GetOutputDir() string {
	return rt.config.OutputDir
}

// findRawTherapeeExecutable tries to find the rawtherapee-cli executable
func findRawTherapeeExecutable() string {
	// Try common names
	names := []string{"rawtherapee-cli"}

	switch runtime.GOOS {
	case "windows":
		names = append(names,
			"rawtherapee-cli.exe",
			`C:\Program Files\RawTherapee\5.12\rawtherapee-cli.exe`,
			`C:\Program Files\RawTherapee\rawtherapee-cli.exe`,
			`C:\Program Files (x86)\RawTherapee\rawtherapee-cli.exe`,
		)
	case "darwin":
		names = append(names,
			"/Applications/RawTherapee.app/Contents/MacOS/rawtherapee-cli",
			"/usr/local/bin/rawtherapee-cli",
			"/opt/homebrew/bin/rawtherapee-cli",
		)
	}

	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
		// Also check if it's a direct path that exists
		if _, err := os.Stat(name); err == nil {
			return name
		}
	}

	return "rawtherapee-cli" // Fall back to PATH lookup
}

// ValidateProfile checks if a PP3 profile file is valid
func ValidateProfile(profilePath string) error {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return fmt.Errorf("failed to read profile: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "[Version]") {
		return fmt.Errorf("invalid PP3 profile: missing [Version] section")
	}

	return nil
}

// GeneratePP3WithAspectRatio creates a PP3 profile with camera aspect ratio crop applied
// Returns the path to the generated PP3 file, or empty string if no aspect ratio crop is needed
func GeneratePP3WithAspectRatio(rawPath string, basePP3Content string, outputDir string) (string, error) {
	// Get aspect ratio info from EXIF
	aspectInfo, err := customexif.GetAspectRatio(rawPath)
	if err != nil {
		return "", fmt.Errorf("failed to get aspect ratio: %v", err)
	}

	// Check if aspectInfo is nil
	if aspectInfo == nil {
		return "", nil
	}

	// Check if we need to apply a crop (only for non-4:3 aspect ratios)
	if aspectInfo.Ratio == "4:3" || aspectInfo.CropFrame == nil {
		return "", nil // No crop needed, use default profile
	}

	// Generate PP3 with crop settings
	pp3Content := ApplyAspectRatioCropToPP3(basePP3Content, aspectInfo.CropFrame)

	// Write PP3 file
	baseName := strings.TrimSuffix(filepath.Base(rawPath), filepath.Ext(rawPath))
	pp3Path := filepath.Join(outputDir, baseName+".pp3")

	if err := os.WriteFile(pp3Path, []byte(pp3Content), 0644); err != nil {
		return "", fmt.Errorf("failed to write PP3: %v", err)
	}

	return pp3Path, nil
}

// ApplyAspectRatioCropToPP3 applies crop settings to a PP3 profile
func ApplyAspectRatioCropToPP3(pp3Content string, cropFrame *customexif.CropFrame) string {
	if cropFrame == nil {
		return pp3Content
	}

	lines := strings.Split(pp3Content, "\n")
	result := make([]string, 0, len(lines))

	inSection := ""
	cropEnabledSet := false
	cropXSet := false
	cropYSet := false
	cropWSet := false
	cropHSet := false

	for _, line := range lines {
		// Track current section
		if strings.HasPrefix(line, "[") && strings.HasSuffix(strings.TrimSpace(line), "]") {
			inSection = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(line), "]"), "[")
		}

		// Modify Crop section
		if inSection == "Crop" {
			if strings.HasPrefix(line, "Enabled=") {
				line = "Enabled=true"
				cropEnabledSet = true
			}
			if strings.HasPrefix(line, "X=") {
				line = fmt.Sprintf("X=%d", cropFrame.X)
				cropXSet = true
			}
			if strings.HasPrefix(line, "Y=") {
				line = fmt.Sprintf("Y=%d", cropFrame.Y)
				cropYSet = true
			}
			if strings.HasPrefix(line, "W=") {
				line = fmt.Sprintf("W=%d", cropFrame.Width)
				cropWSet = true
			}
			if strings.HasPrefix(line, "H=") {
				line = fmt.Sprintf("H=%d", cropFrame.Height)
				cropHSet = true
			}
		}

		result = append(result, line)
	}

	// Add crop settings if not found in base profile
	if !cropEnabledSet || !cropXSet || !cropYSet || !cropWSet || !cropHSet {
		result = ensureCropSection(result, cropFrame, cropEnabledSet, cropXSet, cropYSet, cropWSet, cropHSet)
	}

	return strings.Join(result, "\n")
}

// ensureCropSection adds or updates crop settings in PP3 content
func ensureCropSection(lines []string, cropFrame *customexif.CropFrame, enabledSet, xSet, ySet, wSet, hSet bool) []string {
	result := make([]string, 0, len(lines)+10)
	cropSectionFound := false
	inCropSection := false

	for _, line := range lines {
		if strings.TrimSpace(line) == "[Crop]" {
			cropSectionFound = true
			inCropSection = true
			result = append(result, line)
			// Add missing settings right after section header
			if !enabledSet {
				result = append(result, "Enabled=true")
			}
			if !xSet {
				result = append(result, fmt.Sprintf("X=%d", cropFrame.X))
			}
			if !ySet {
				result = append(result, fmt.Sprintf("Y=%d", cropFrame.Y))
			}
			if !wSet {
				result = append(result, fmt.Sprintf("W=%d", cropFrame.Width))
			}
			if !hSet {
				result = append(result, fmt.Sprintf("H=%d", cropFrame.Height))
			}
			continue
		}

		// Check if we're leaving the crop section
		if inCropSection && strings.HasPrefix(line, "[") {
			inCropSection = false
		}

		result = append(result, line)
	}

	// If no crop section found, add it
	if !cropSectionFound {
		result = append(result, "")
		result = append(result, "[Crop]")
		result = append(result, "Enabled=true")
		result = append(result, fmt.Sprintf("X=%d", cropFrame.X))
		result = append(result, fmt.Sprintf("Y=%d", cropFrame.Y))
		result = append(result, fmt.Sprintf("W=%d", cropFrame.Width))
		result = append(result, fmt.Sprintf("H=%d", cropFrame.Height))
		result = append(result, "FixedRatio=false")
		result = append(result, "Ratio=As Image")
		result = append(result, "Orientation=As Image")
		result = append(result, "Guide=Frame")
	}

	return result
}