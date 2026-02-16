package exif

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

// rawExtensions are formats that need exiftool for accurate EXIF reading
var rawExtensions = map[string]bool{
	".orf": true, // Olympus
	".arw": true, // Sony
	".cr2": true, // Canon
	".cr3": true, // Canon
	".nef": true, // Nikon
	".nrw": true, // Nikon
	".rw2": true, // Panasonic
	".raf": true, // Fuji
	".dng": true, // Adobe DNG
	".pef": true, // Pentax
	".srw": true, // Samsung
}

// GetDateTimeOriginal reads the DateTimeOriginal EXIF tag from an image file.
// For RAW formats that Go libraries don't support well, it uses exiftool.
// If the EXIF data cannot be read, it falls back to the file modification time.
func GetDateTimeOriginal(filePath string) (time.Time, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	// For RAW files, use exiftool for accurate reading
	if rawExtensions[ext] {
		return getDateTimeOriginalExiftool(filePath)
	}

	// For standard formats (JPEG, TIFF), use Go library
	return getDateTimeOriginalGoLib(filePath)
}

// getDateTimeOriginalGoLib uses the Go EXIF library (fast, works for JPEG/TIFF)
func getDateTimeOriginalGoLib(filePath string) (time.Time, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		// Fall back to file mod time if EXIF cannot be decoded
		return getFileModTime(filePath)
	}

	dt, err := x.DateTime()
	if err != nil {
		// Fall back to file mod time if DateTime tag is missing
		return getFileModTime(filePath)
	}

	return dt, nil
}

// getDateTimeOriginalExiftool uses exiftool command (slower but accurate for RAW)
func getDateTimeOriginalExiftool(filePath string) (time.Time, error) {
	cmd := exec.Command("exiftool", "-DateTimeOriginal", "-s3", "-d", "%Y-%m-%d %H:%M:%S", filePath)
	output, err := cmd.Output()
	if err != nil {
		// Fall back to file mod time if exiftool fails
		return getFileModTime(filePath)
	}

	dateStr := strings.TrimSpace(string(output))
	if dateStr == "" {
		return getFileModTime(filePath)
	}

	dt, err := time.ParseInLocation("2006-01-02 15:04:05", dateStr, time.Local)
	if err != nil {
		return getFileModTime(filePath)
	}

	return dt, nil
}

// getFileModTime returns the file modification time
func getFileModTime(filePath string) (time.Time, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// GetDateTimeOriginalWithFallback is like GetDateTimeOriginal but always returns a valid time.
// If both EXIF and file stat fail, it returns the current time.
func GetDateTimeOriginalWithFallback(filePath string) time.Time {
	dt, err := GetDateTimeOriginal(filePath)
	if err != nil {
		return time.Now()
	}
	return dt
}