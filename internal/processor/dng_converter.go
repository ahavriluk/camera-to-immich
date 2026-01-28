package processor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DNGConverterConfig contains configuration for Adobe DNG Converter
type DNGConverterConfig struct {
	ExecutablePath string // Path to Adobe DNG Converter executable
	OutputDir      string // Directory for converted DNG files
	Compressed     bool   // Use compressed DNG format
	EmbedOriginal  bool   // Embed original raw file in DNG
}

// DNGConverter handles converting RAW files to DNG format using Adobe DNG Converter
type DNGConverter struct {
	config DNGConverterConfig
}

// NewDNGConverter creates a new DNG Converter processor
func NewDNGConverter(config DNGConverterConfig) (*DNGConverter, error) {
	// Set defaults
	if config.ExecutablePath == "" {
		config.ExecutablePath = findDNGConverterExecutable()
	}

	// Validate executable exists
	if config.ExecutablePath == "" {
		return nil, fmt.Errorf("Adobe DNG Converter not found. Please install it or specify the path in config")
	}

	if _, err := os.Stat(config.ExecutablePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("Adobe DNG Converter not found at '%s'", config.ExecutablePath)
	}

	// Ensure output directory exists
	if config.OutputDir != "" {
		if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create DNG output directory: %v", err)
		}
	}

	return &DNGConverter{config: config}, nil
}

// ConvertFile converts a single RAW file to DNG and returns the path to the output DNG
func (dc *DNGConverter) ConvertFile(inputPath string) (string, error) {
	// Determine output path
	baseName := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	outputPath := filepath.Join(dc.config.OutputDir, baseName+".dng")

	// Build command arguments
	// Adobe DNG Converter CLI arguments:
	// -c : Convert to DNG
	// -d : Output directory
	// -o : Output filename pattern
	// -cr7.1 : Camera Raw 7.1 compatibility
	// -dng1.4 : DNG version 1.4
	// -p0 : No preview (faster)
	// -fl : Fast load
	// -lossy : Use lossy compression (optional, smaller files)
	// The file to convert should be at the end
	
	args := []string{
		"-c",                          // Convert
		"-d", dc.config.OutputDir,     // Output directory
		"-o", baseName + ".dng",       // Output filename
	}

	// Add compression option
	if dc.config.Compressed {
		args = append(args, "-lossy") // Use lossy compression for smaller files
	}

	// Add embed original option  
	if dc.config.EmbedOriginal {
		args = append(args, "-e") // Embed original raw
	}

	// Add input file
	args = append(args, inputPath)

	// Execute Adobe DNG Converter
	cmd := exec.Command(dc.config.ExecutablePath, args...)
	
	// Run the command and wait for it to complete
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("Adobe DNG Converter failed: %v\nOutput: %s", err, string(output))
	}

	// Wait a bit for file to be fully written (DNG Converter can exit before file is complete)
	time.Sleep(500 * time.Millisecond)

	// Verify output file was created
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		// Try alternate output path patterns
		alternateOutputPath := filepath.Join(dc.config.OutputDir, baseName+".DNG")
		if _, err := os.Stat(alternateOutputPath); err == nil {
			return alternateOutputPath, nil
		}
		return "", fmt.Errorf("DNG output file was not created: %s\nCommand output: %s", outputPath, string(output))
	}

	return outputPath, nil
}

// GetOutputDir returns the output directory
func (dc *DNGConverter) GetOutputDir() string {
	return dc.config.OutputDir
}

// findDNGConverterExecutable tries to find the Adobe DNG Converter executable
func findDNGConverterExecutable() string {
	var paths []string

	switch runtime.GOOS {
	case "windows":
		paths = []string{
			`C:\Program Files\Adobe\Adobe DNG Converter\Adobe DNG Converter.exe`,
			`C:\Program Files (x86)\Adobe\Adobe DNG Converter\Adobe DNG Converter.exe`,
		}
	case "darwin":
		paths = []string{
			"/Applications/Adobe DNG Converter.app/Contents/MacOS/Adobe DNG Converter",
		}
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// IsDNGConverterAvailable checks if Adobe DNG Converter is available on the system
func IsDNGConverterAvailable() bool {
	return findDNGConverterExecutable() != ""
}