package uploader

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ImmichConfig contains configuration for Immich uploads
type ImmichConfig struct {
	ExecutablePath string   // Path to immich-go executable
	ServerURL      string   // Immich server URL
	APIKey         string   // Immich API key
	Album          string   // Optional album name
	Tags           []string // Tags to apply to uploads
	ShowProgress   bool     // Show upload progress (stream immich-go output)
}

// Immich handles uploading files to Immich server
type Immich struct {
	config ImmichConfig
}

// NewImmich creates a new Immich uploader
func NewImmich(config ImmichConfig) (*Immich, error) {
	// Set defaults
	if config.ExecutablePath == "" {
		config.ExecutablePath = findImmichGoExecutable()
	}

	// Validate executable exists
	if _, err := exec.LookPath(config.ExecutablePath); err != nil {
		return nil, fmt.Errorf("immich-go not found at '%s': %v", config.ExecutablePath, err)
	}

	// Validate required config
	if config.ServerURL == "" {
		return nil, fmt.Errorf("immich server URL is required")
	}
	if config.APIKey == "" {
		return nil, fmt.Errorf("immich API key is required")
	}

	return &Immich{config: config}, nil
}

// UploadResult contains the result of an upload operation
type UploadResult struct {
	FilePath string
	Success  bool
	Error    error
}

// UploadFiles uploads multiple files to Immich
func (im *Immich) UploadFiles(filePaths []string, additionalTags []string) ([]UploadResult, error) {
	results := make([]UploadResult, 0, len(filePaths))

	for _, filePath := range filePaths {
		result := UploadResult{FilePath: filePath}
		
		err := im.uploadSingleFile(filePath, additionalTags)
		if err != nil {
			result.Error = err
			result.Success = false
		} else {
			result.Success = true
		}
		
		results = append(results, result)
	}

	return results, nil
}

// UploadFile uploads a single file to Immich
func (im *Immich) UploadFile(filePath string, additionalTags []string) error {
	return im.uploadSingleFile(filePath, additionalTags)
}

// uploadSingleFile performs the actual upload of a single file
// Note: immich-go works with folders, so this creates a temp directory with a symlink/copy
func (im *Immich) uploadSingleFile(filePath string, additionalTags []string) error {
	// Verify file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("file not found: %s", filePath)
	}

	// Create a temporary directory for this upload
	tempDir, err := os.MkdirTemp("", "immich-upload-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir) // Clean up after upload

	// Copy the file to the temp directory
	fileName := filepath.Base(filePath)
	destPath := filepath.Join(tempDir, fileName)
	
	if err := copyFile(filePath, destPath); err != nil {
		return fmt.Errorf("failed to copy file to temp directory: %v", err)
	}

	// Upload the temp directory
	return im.uploadDirectory(tempDir, additionalTags, false)
}

// UploadFolder uploads all files from a folder to Immich
func (im *Immich) UploadFolder(folderPath string, additionalTags []string, recursive bool) error {
	return im.uploadDirectory(folderPath, additionalTags, recursive)
}

// uploadDirectory performs the actual upload of a directory
func (im *Immich) uploadDirectory(dirPath string, additionalTags []string, recursive bool) error {
	// Build command arguments using new immich-go CLI syntax:
	// immich-go upload from-folder --server URL --api-key KEY [--tag TAG]... FOLDER
	args := []string{
		"upload",
		"from-folder",
		"--server", im.config.ServerURL,
		"--api-key", im.config.APIKey,
		"--on-errors", "continue",      // Continue on errors
		"--skip-verify-ssl",            // Skip SSL verification (faster handshake)
	}

	// Disable UI only if we're not showing progress
	if !im.config.ShowProgress {
		args = append(args, "--no-ui")
	}

	// Add recursive flag
	if !recursive {
		args = append(args, "--recursive=false")
	}

	// Combine configured tags with additional tags
	allTags := append(im.config.Tags, additionalTags...)
	for _, tag := range allTags {
		args = append(args, "--tag", tag)
	}

	// Add album if specified
	if im.config.Album != "" {
		args = append(args, "--into-album", im.config.Album)
	}

	// Add the folder path
	args = append(args, dirPath)

	// Execute immich-go
	cmd := exec.Command(im.config.ExecutablePath, args...)
	
	if im.config.ShowProgress {
		// Stream output to console in real-time for progress display
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			return fmt.Errorf("immich-go upload failed: %v", err)
		}
	} else {
		// Capture output (no progress display)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("immich-go upload failed: %v\nOutput: %s", err, string(output))
		}
	}

	return nil
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = destFile.ReadFrom(sourceFile)
	return err
}

// UploadDirectory uploads all JPEG files in a directory
func (im *Immich) UploadDirectory(dirPath string, additionalTags []string) ([]UploadResult, error) {
	var filePaths []string

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".jpg" || ext == ".jpeg" {
			filePaths = append(filePaths, path)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to scan directory: %v", err)
	}

	return im.UploadFiles(filePaths, additionalTags)
}

// findImmichGoExecutable tries to find the immich-go executable
func findImmichGoExecutable() string {
	// Try common names
	names := []string{"immich-go"}

	switch runtime.GOOS {
	case "windows":
		names = append(names,
			"immich-go.exe",
			filepath.Join(os.Getenv("USERPROFILE"), "go", "bin", "immich-go.exe"),
			filepath.Join(os.Getenv("GOPATH"), "bin", "immich-go.exe"),
		)
	case "darwin":
		names = append(names,
			"/usr/local/bin/immich-go",
			"/opt/homebrew/bin/immich-go",
			filepath.Join(os.Getenv("HOME"), "go", "bin", "immich-go"),
			filepath.Join(os.Getenv("GOPATH"), "bin", "immich-go"),
		)
	default: // Linux
		names = append(names,
			"/usr/local/bin/immich-go",
			filepath.Join(os.Getenv("HOME"), "go", "bin", "immich-go"),
			filepath.Join(os.Getenv("GOPATH"), "bin", "immich-go"),
		)
	}

	for _, name := range names {
		if name == "" {
			continue
		}
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
		// Also check if it's a direct path that exists
		if _, err := os.Stat(name); err == nil {
			return name
		}
	}

	return "immich-go" // Fall back to PATH lookup
}

// TestConnection tests the connection to the Immich server
func (im *Immich) TestConnection() error {
	// Create an empty temp directory for dry-run test
	tempDir, err := os.MkdirTemp("", "immich-test-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Use immich-go to test the connection with new CLI syntax
	args := []string{
		"upload",
		"from-folder",
		"--server", im.config.ServerURL,
		"--api-key", im.config.APIKey,
		"--dry-run",
		"--no-ui",
		tempDir, // Empty temp directory (won't upload anything)
	}

	cmd := exec.Command(im.config.ExecutablePath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's just a "no files to upload" error (which is expected)
		outputStr := string(output)
		if strings.Contains(outputStr, "no files") ||
		   strings.Contains(outputStr, "0 files") ||
		   strings.Contains(outputStr, "0 asset") ||
		   strings.Contains(outputStr, "Nothing to upload") {
			return nil // This is actually success
		}
		return fmt.Errorf("connection test failed: %v\nOutput: %s", err, outputStr)
	}

	return nil
}