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
	MaxRetries     int    // Maximum number of retry attempts (default: 3)
	RetryDelay     time.Duration // Initial delay between retries (default: 2s, doubles each retry)
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

	// Set retry defaults
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3 // Default to 3 retries
	}
	if config.RetryDelay <= 0 {
		config.RetryDelay = 2 * time.Second // Default to 2 seconds initial delay
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

// ConvertResult contains the result of a DNG conversion including retry information
type ConvertResult struct {
	OutputPath   string // Path to the output DNG file
	Attempts     int    // Number of attempts made
	LastError    error  // Last error if any
}

// ConvertFile converts a single RAW file to DNG and returns the path to the output DNG
// Uses retry logic to handle transient failures
func (dc *DNGConverter) ConvertFile(inputPath string) (string, error) {
	result := dc.ConvertFileWithRetry(inputPath)
	return result.OutputPath, result.LastError
}

// ConvertFileWithRetry converts a single RAW file to DNG with retry logic
// Returns detailed result including retry information
func (dc *DNGConverter) ConvertFileWithRetry(inputPath string) ConvertResult {
	var lastErr error
	var lastOutput string
	
	for attempt := 1; attempt <= dc.config.MaxRetries; attempt++ {
		outputPath, output, err := dc.convertFileOnce(inputPath)
		if err == nil {
			return ConvertResult{
				OutputPath: outputPath,
				Attempts:   attempt,
				LastError:  nil,
			}
		}
		
		lastErr = err
		lastOutput = output
		
		// If this isn't the last attempt, wait before retrying
		if attempt < dc.config.MaxRetries {
			// Exponential backoff: delay doubles each retry
			delay := dc.config.RetryDelay * time.Duration(1<<(attempt-1))
			time.Sleep(delay)
		}
	}
	
	// All retries failed
	return ConvertResult{
		OutputPath: "",
		Attempts:   dc.config.MaxRetries,
		LastError:  fmt.Errorf("DNG conversion failed after %d attempts: %v\nLast output: %s", dc.config.MaxRetries, lastErr, lastOutput),
	}
}

// convertFileOnce performs a single conversion attempt
func (dc *DNGConverter) convertFileOnce(inputPath string) (string, string, error) {
	// Determine output path
	baseName := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	outputPath := filepath.Join(dc.config.OutputDir, baseName+".dng")

	// Remove any existing output file to ensure clean conversion
	os.Remove(outputPath)
	os.Remove(filepath.Join(dc.config.OutputDir, baseName+".DNG"))

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
	outputStr := string(output)
	if err != nil {
		return "", outputStr, fmt.Errorf("Adobe DNG Converter failed: %v", err)
	}

	// Wait a bit for file to be fully written (DNG Converter can exit before file is complete)
	time.Sleep(500 * time.Millisecond)

	// Verify output file was created
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		// Try alternate output path patterns
		alternateOutputPath := filepath.Join(dc.config.OutputDir, baseName+".DNG")
		if _, err := os.Stat(alternateOutputPath); err == nil {
			return alternateOutputPath, outputStr, nil
		}
		return "", outputStr, fmt.Errorf("DNG output file was not created: %s", outputPath)
	}

	return outputPath, outputStr, nil
}

// GetMaxRetries returns the configured maximum retry count
func (dc *DNGConverter) GetMaxRetries() int {
	return dc.config.MaxRetries
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
			`C:\Program Files\Adobe\Adobe DNG Converter.exe`,
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