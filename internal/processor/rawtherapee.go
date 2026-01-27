package processor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// RawTherapeeConfig contains configuration for RawTherapee processing
type RawTherapeeConfig struct {
	ExecutablePath string // Path to rawtherapee-cli executable
	ProfilePath    string // Path to the PP3 profile file
	OutputDir      string // Directory for processed JPEGs
	Quality        int    // JPEG quality (1-100)
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
func (rt *RawTherapee) ProcessFile(inputPath string) (string, error) {
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
	if rt.config.ProfilePath != "" {
		args = append(args, "-p", rt.config.ProfilePath)
	}

	// Add input file
	args = append(args, "-c", inputPath)

	// Execute rawtherapee-cli
	cmd := exec.Command(rt.config.ExecutablePath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("rawtherapee-cli failed: %v\nOutput: %s", err, string(output))
	}

	// Verify output file was created
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		return "", fmt.Errorf("output file was not created: %s", outputPath)
	}

	return outputPath, nil
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