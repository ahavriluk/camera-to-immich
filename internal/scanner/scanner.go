package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileInfo represents information about a found file
type FileInfo struct {
	Path        string
	Name        string
	Size        int64
	ModTime     int64  // Unix timestamp (file modification time)
	CaptureTime int64  // Unix timestamp (EXIF DateTimeOriginal, 0 if not loaded yet)
	IsRAW       bool   // True if this is a RAW file (based on configured extensions)
	IsJPG       bool
	BaseName    string // Filename without extension
	Extension   string // File extension (uppercase, with leading dot)
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

	// Track seen files to avoid duplicates
	seenFiles := make(map[string]bool)

	// Common camera image directories - search DCIM first, then basePath
	// If DCIM exists, we still scan basePath but will skip already-seen files
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

			// Skip if we've already seen this file (de-duplicate)
			if seenFiles[path] {
				return nil
			}
			seenFiles[path] = true

			ext := strings.ToUpper(filepath.Ext(path))
			baseName := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))

			// Don't read EXIF during scanning - it's too slow for thousands of files.
			// CaptureTime will be populated later for pre-filtered candidates only.
			// For now, set CaptureTime to 0 (indicating not loaded)

			fileInfo := FileInfo{
				Path:        path,
				Name:        info.Name(),
				Size:        info.Size(),
				ModTime:     info.ModTime().Unix(),
				CaptureTime: 0, // Will be loaded lazily for candidates only
				BaseName:    baseName,
				Extension:   ext,
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

// FilterNewFilesWithTime returns only files that haven't been processed yet
// based on both the processedFiles map AND the lastProcessedTime high-water mark.
// A file is considered "new" if:
// 1. It's not in the processedFiles map, AND
// 2. Its file modification time is after lastProcessedTime (or lastProcessedTime is zero)
//
// NOTE: This uses file ModTime for pre-filtering (fast). The caller should then
// load EXIF CaptureTime for candidates and do a final filter if needed.
func FilterNewFilesWithTime(files []FileInfo, processedFiles map[string]bool, lastProcessedTime time.Time) []FileInfo {
	var newFiles []FileInfo
	for _, f := range files {
		// Skip if already in processed files map
		if processedFiles[f.Name] {
			continue
		}

		// If we have a last processed time, skip files older than or equal to it
		// Uses ModTime for fast pre-filtering (EXIF will be checked later for candidates)
		if !lastProcessedTime.IsZero() {
			fileModTime := time.Unix(f.ModTime, 0)
			if !fileModTime.After(lastProcessedTime) {
				continue
			}
		}

		newFiles = append(newFiles, f)
	}
	return newFiles
}

// FilterNewFilesWithCaptureTime performs two-stage filtering against a high-water mark:
//
//  1. Pre-filter by file ModTime (cheap, no I/O beyond what's already loaded).
//  2. For surviving candidates, load the EXIF DateTimeOriginal via getCaptureTime
//     and do a final filter using the EXIF capture time.
//
// This is the AUTHORITATIVE filter — file mtime can be touched by the OS, card
// readers, antivirus, etc., causing already-uploaded photos to incorrectly appear
// "new" if filtered by mtime alone. EXIF DateTimeOriginal reflects the actual
// shutter time and is immutable.
//
// On return, surviving files have CaptureTime populated (Unix seconds).
//
// getCaptureTime should return a fallback (e.g. file mtime) if EXIF is unreadable;
// the helper exif.GetDateTimeOriginalWithFallback satisfies this contract.
//
// If lastProcessedTime is zero, all files are returned (CaptureTime still loaded
// for surviving files so callers can advance the high-water mark with EXIF time).
func FilterNewFilesWithCaptureTime(
	files []FileInfo,
	lastProcessedTime time.Time,
	getCaptureTime func(string) time.Time,
) []FileInfo {
	result := make([]FileInfo, 0, len(files))
	for i := range files {
		f := files[i] // copy so we can mutate CaptureTime safely

		// Stage 1: ModTime pre-filter (cheap)
		if !lastProcessedTime.IsZero() {
			fileModTime := time.Unix(f.ModTime, 0)
			if !fileModTime.After(lastProcessedTime) {
				continue
			}
		}

		// Stage 2: EXIF capture time (authoritative)
		captureTime := getCaptureTime(f.Path)
		f.CaptureTime = captureTime.Unix()

		if !lastProcessedTime.IsZero() && !captureTime.After(lastProcessedTime) {
			continue
		}

		result = append(result, f)
	}
	return result
}

// GetCaptureTime returns the capture time as a time.Time
// If CaptureTime is 0 (not loaded), returns ModTime as fallback
func (f *FileInfo) GetCaptureTime() time.Time {
	if f.CaptureTime == 0 {
		return time.Unix(f.ModTime, 0)
	}
	return time.Unix(f.CaptureTime, 0)
}

// GetModTime returns the modification time as a time.Time
func (f *FileInfo) GetModTime() time.Time {
	return time.Unix(f.ModTime, 0)
}

// HasCaptureTime returns true if CaptureTime has been loaded from EXIF
func (f *FileInfo) HasCaptureTime() bool {
	return f.CaptureTime != 0
}