package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileInfo represents information about a found file
type FileInfo struct {
	Path      string
	Name      string
	Size      int64
	ModTime   int64  // Unix timestamp
	IsRAW     bool   // True if this is a RAW file (based on configured extensions)
	IsJPG     bool
	BaseName  string // Filename without extension
	Extension string // File extension (uppercase, with leading dot)
}

// ScanResult contains the results of scanning a drive
type ScanResult struct {
	RAWFiles []FileInfo
	JPGFiles []FileInfo
	BasePath string
}

// ScanForImages scans a directory for RAW and JPG files
// It looks in common camera directory structures like DCIM/
// rawExtensions is a map of uppercase extensions (with dot) that should be treated as RAW
func ScanForImages(basePath string, rawExtensions map[string]bool) (*ScanResult, error) {
	result := &ScanResult{
		BasePath: basePath,
		RAWFiles: make([]FileInfo, 0),
		JPGFiles: make([]FileInfo, 0),
	}

	// Common camera image directories
	searchPaths := []string{
		filepath.Join(basePath, "DCIM"),
		basePath,
	}

	for _, searchPath := range searchPaths {
		if _, err := os.Stat(searchPath); os.IsNotExist(err) {
			continue
		}

		err := filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip files we can't access
			}

			if info.IsDir() {
				return nil
			}

			// Skip macOS hidden files (start with "._")
			if strings.HasPrefix(info.Name(), "._") {
				return nil
			}

			ext := strings.ToUpper(filepath.Ext(path))
			baseName := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))

			fileInfo := FileInfo{
				Path:      path,
				Name:      info.Name(),
				Size:      info.Size(),
				ModTime:   info.ModTime().Unix(),
				BaseName:  baseName,
				Extension: ext,
			}

			// Check if it's a configured RAW extension
			if rawExtensions[ext] {
				fileInfo.IsRAW = true
				result.RAWFiles = append(result.RAWFiles, fileInfo)
			} else if ext == ".JPG" || ext == ".JPEG" {
				fileInfo.IsJPG = true
				result.JPGFiles = append(result.JPGFiles, fileInfo)
			}

			return nil
		})

		if err != nil {
			return nil, fmt.Errorf("error scanning %s: %v", searchPath, err)
		}
	}

	return result, nil
}

// FindMatchingJPG finds the camera-generated JPG that matches a RAW file
func FindMatchingJPG(rawFile FileInfo, jpgFiles []FileInfo) *FileInfo {
	for i, jpg := range jpgFiles {
		if jpg.BaseName == rawFile.BaseName {
			return &jpgFiles[i]
		}
	}
	return nil
}

// FilterNewFiles returns only files that haven't been processed yet
func FilterNewFiles(files []FileInfo, processedFiles map[string]bool) []FileInfo {
	var newFiles []FileInfo
	for _, f := range files {
		if !processedFiles[f.Name] {
			newFiles = append(newFiles, f)
		}
	}
	return newFiles
}