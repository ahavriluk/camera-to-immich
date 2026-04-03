package exif

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// GetOrientation reads the EXIF Orientation tag from an image file
// Returns orientation value 1-8, or 1 if not found/error
// Orientations 5-8 indicate the image is rotated 90° or 270° (portrait display)
func GetOrientation(filePath string) int {
	ext := strings.ToLower(filepath.Ext(filePath))
	
	// For RAW files, use exiftool
	if rawExtensions[ext] {
		return getOrientationExiftool(filePath)
	}
	
	// For standard formats, use Go library
	return getOrientationGoLib(filePath)
}

// getOrientationExiftool uses exiftool to read EXIF Orientation
func getOrientationExiftool(filePath string) int {
	cmd := exec.Command("exiftool", "-Orientation", "-n", "-s3", filePath)
	output, err := cmd.Output()
	if err != nil {
		return 1
	}
	val := strings.TrimSpace(string(output))
	orientation, err := strconv.Atoi(val)
	if err != nil || orientation < 1 || orientation > 8 {
		return 1
	}
	return orientation
}

// getOrientationGoLib uses the Go EXIF library for JPEG/TIFF
func getOrientationGoLib(filePath string) int {
	f, err := os.Open(filePath)
	if err != nil {
		return 1
	}
	defer f.Close()
	
	x, err := exif.Decode(f)
	if err != nil {
		return 1
	}
	
	orientTag, err := x.Get(exif.Orientation)
	if err != nil {
		return 1
	}
	
	val, err := orientTag.Int(0)
	if err != nil {
		return 1
	}
	
	if val >= 1 && val <= 8 {
		return val
	}
	return 1
}

// IsPortraitOrientation returns true if the EXIF orientation indicates
// the image should be displayed in portrait mode (rotated 90° or 270°)
func IsPortraitOrientation(orientation int) bool {
	// Orientations 5-8 involve a 90° or 270° rotation
	return orientation >= 5 && orientation <= 8
}

// GetDisplayAspectRatio returns the aspect ratio string adjusted for EXIF orientation
// For example, a "16:9" image with portrait orientation returns "9:16"
func GetDisplayAspectRatio(filePath string) string {
	ratio := GetAspectRatioWithFallback(filePath)
	orientation := GetOrientation(filePath)
	
	if IsPortraitOrientation(orientation) {
		// Swap the ratio components for portrait orientation
		parts := strings.Split(ratio, ":")
		if len(parts) == 2 {
			return parts[1] + ":" + parts[0]
		}
	}
	
	return ratio
}

// AspectRatioInfo contains the aspect ratio information from camera EXIF data
type AspectRatioInfo struct {
	Ratio      string    // Human-readable ratio like "3:2", "16:9", "4:3", etc.
	CropFrame  *CropFrame // Optional crop frame coordinates from AspectFrame tag
	SourceTag  string    // Which tag was used to get the info (for debugging)
}

// CropFrame represents the crop coordinates stored in the AspectFrame EXIF tag
type CropFrame struct {
	X      int // Left edge
	Y      int // Top edge
	Width  int // Crop width
	Height int // Crop height
}

// parseAspectRatioTag extracts the aspect ratio value from exiftool output
// OM System cameras output format: "4:3", "16:9 (RAW)", "1:1 (RAW)", "3:4 (RAW)", etc.
func parseAspectRatioTag(value string) string {
	// Remove "(RAW)" suffix if present
	value = strings.TrimSpace(value)
	if idx := strings.Index(value, " (RAW)"); idx != -1 {
		value = value[:idx]
	}
	return value
}

// GetAspectRatio reads the aspect ratio information from an image file
// For OM System cameras, this reads the AspectRatio and AspectFrame tags
// Returns nil if no aspect ratio information is found
func GetAspectRatio(filePath string) (*AspectRatioInfo, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	
	// Only RAW files from Olympus/OM System have the aspect ratio info
	if ext != ".orf" {
		return nil, nil
	}
	
	return getAspectRatioExiftool(filePath)
}

// getAspectRatioExiftool uses exiftool to read OM System aspect ratio tags
func getAspectRatioExiftool(filePath string) (*AspectRatioInfo, error) {
	// Read both AspectRatio and AspectFrame tags
	cmd := exec.Command("exiftool",
		"-Olympus:AspectRatio",
		"-Olympus:AspectFrame",
		"-s3",
		filePath)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("exiftool failed: %v", err)
	}
	
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return nil, nil
	}
	
	info := &AspectRatioInfo{}
	
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		
		if i == 0 {
			// First line is AspectRatio value (format: "4:3", "16:9 (RAW)", etc.)
			ratio := parseAspectRatioTag(line)
			if ratio != "" {
				info.Ratio = ratio
				info.SourceTag = "AspectRatio"
			}
		} else if i == 1 {
			// Second line is AspectFrame - format is "Left Top Right Bottom"
			// NOT "X Y Width Height" - we need to calculate width and height
			parts := strings.Fields(line)
			if len(parts) == 4 {
				left, _ := strconv.Atoi(parts[0])
				top, _ := strconv.Atoi(parts[1])
				right, _ := strconv.Atoi(parts[2])
				bottom, _ := strconv.Atoi(parts[3])
				
				// Calculate actual width and height from bounds
				// Using +1 because bounds are inclusive (e.g., 0-5183 = 5184 pixels)
				w := right - left + 1
				h := bottom - top + 1
				
				if w > 0 && h > 0 {
					info.CropFrame = &CropFrame{
						X:      left,
						Y:      top,
						Width:  w,
						Height: h,
					}
					
					// If AspectRatio tag wasn't found, calculate from frame
					if info.Ratio == "" {
						info.Ratio = calculateAspectRatioFromDimensions(w, h)
						info.SourceTag = "AspectFrame"
					}
				}
			}
		}
	}
	
	if info.Ratio == "" {
		return nil, nil
	}
	
	return info, nil
}

// calculateAspectRatioFromDimensions calculates a human-readable aspect ratio from width and height
func calculateAspectRatioFromDimensions(w, h int) string {
	if w == 0 || h == 0 {
		return ""
	}
	
	// Common aspect ratios to check
	ratios := []struct {
		name string
		w, h int
	}{
		{"4:3", 4, 3},
		{"3:2", 3, 2},
		{"16:9", 16, 9},
		{"1:1", 1, 1},
		{"5:4", 5, 4},
		{"7:6", 7, 6},
		{"6:5", 6, 5},
		{"7:5", 7, 5},
		{"3:4", 3, 4},
		{"2:3", 2, 3},
		{"9:16", 9, 16},
	}
	
	// Calculate the actual ratio
	actual := float64(w) / float64(h)
	
	// Find the closest matching ratio
	bestMatch := ""
	bestDiff := 1.0 // Maximum difference to consider a match
	
	for _, r := range ratios {
		target := float64(r.w) / float64(r.h)
		diff := abs(actual - target)
		if diff < bestDiff && diff < 0.01 { // Within 1% tolerance
			bestDiff = diff
			bestMatch = r.name
		}
	}
	
	if bestMatch != "" {
		return bestMatch
	}
	
	// If no match found, return the simplified ratio
	gcd := gcdFunc(w, h)
	return fmt.Sprintf("%d:%d", w/gcd, h/gcd)
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func gcdFunc(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// GetAspectRatioWithFallback returns the aspect ratio or a default value
func GetAspectRatioWithFallback(filePath string) string {
	info, err := GetAspectRatio(filePath)
	if err != nil || info == nil {
		return "4:3" // Default for OM System cameras (sensor is 4:3)
	}
	return info.Ratio
}