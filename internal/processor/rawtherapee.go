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