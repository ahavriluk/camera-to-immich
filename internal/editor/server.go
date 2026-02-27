package editor

import (
	"bufio"
	"embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	customexif "github.com/ohavrylyuk/camera-to-immich/internal/exif"
	"github.com/ohavrylyuk/camera-to-immich/internal/processor"
	"github.com/ohavrylyuk/camera-to-immich/internal/scanner"
	"github.com/ohavrylyuk/camera-to-immich/internal/state"
	"github.com/ohavrylyuk/camera-to-immich/internal/uploader"
)

//go:embed templates/*
var templatesFS embed.FS

// ImageInfo represents an image for the web editor
type ImageInfo struct {
	ID           string          `json:"id"`
	Filename     string          `json:"filename"`
	Path         string          `json:"path"`
	BaseName     string          `json:"baseName"` // Filename without extension (for sidecar matching)
	PreviewURL   string          `json:"previewUrl"`
	ThumbnailURL string          `json:"thumbnailUrl"`
	IsRAW        bool            `json:"isRaw"`
	Size         int64           `json:"size"`
	ModTime      int64           `json:"modTime"`           // File modification time (for display)
	CaptureTime  int64           `json:"captureTime"`       // EXIF capture time (for filtering/sorting)
	AspectRatio  string          `json:"aspectRatio"`       // Camera aspect ratio (e.g., "4:3", "3:2", "16:9")
	CropFrame    *CropFrameInfo  `json:"cropFrame"`         // Optional crop frame from EXIF
}

// CropFrameInfo represents crop coordinates from camera EXIF data
type CropFrameInfo struct {
	X      int `json:"x"`      // Left edge
	Y      int `json:"y"`      // Top edge
	Width  int `json:"width"`  // Crop width
	Height int `json:"height"` // Crop height
}

// EditData represents edit settings for a single image
type EditData struct {
	Exposure float64  `json:"exposure"` // EV adjustment (-2 to +2)
	Rotation float64  `json:"rotation"` // Degrees (-15 to +15)
	Crop     *CropBox `json:"crop"`     // Crop coordinates (normalized 0-1)
	Aspect   string   `json:"aspect"`   // Aspect ratio constraint
	BW       bool     `json:"bw"`       // Black & white
	Skip     bool     `json:"skip"`     // Skip this image
	Touched  bool     `json:"touched"`  // Has been edited
}

// CropBox represents normalized crop coordinates
type CropBox struct {
	X      float64 `json:"x"`      // Left edge (0-1)
	Y      float64 `json:"y"`      // Top edge (0-1)
	Width  float64 `json:"width"`  // Width (0-1)
	Height float64 `json:"height"` // Height (0-1)
}

// EditsFile represents the JSON structure for saved edits
type EditsFile struct {
	GlobalPreset string              `json:"globalPreset"` // "color" or "bw"
	Edits        map[string]EditData `json:"edits"`        // keyed by image ID
	SavedAt      time.Time           `json:"savedAt"`
}

// Server is the web editor HTTP server
type Server struct {
	images        []ImageInfo
	jpgMap        map[string]string // Map of RAW basename to JPG path for sidecar previews
	editsPath     string
	outputDir     string
	profilePath   string // Color PP3 profile path
	bwProfilePath string // B&W PP3 profile path (optional)
	rawExts       map[string]bool
	sourcePath    string // Path to scan for images (stored for rescan)
	imageLimit    int    // Limit number of images (stored for rescan)
	mu            sync.RWMutex
	mux           *http.ServeMux

	// Processing config
	processConfig ProcessConfig

	// Tone calibration (for OM-3 cameras)
	toneCalibration *processor.ToneCalibration
}

// ProcessConfig contains configuration for RAW processing
type ProcessConfig struct {
	ConvertToDNG     bool   // Convert RAW to DNG before RawTherapee
	DNGConverterPath string // Path to Adobe DNG Converter
	RawTherapeeExe   string // Path to rawtherapee-cli
	JPEGQuality      int    // JPEG quality (1-100)
	SkipUpload       bool   // Skip upload to Immich
	Workers          int    // Number of parallel workers
	UploadCameraJPGs bool   // Upload original camera-generated JPGs alongside processed files

	// B&W detection and processing
	PP3BWProfilePath string // Path to B&W PP3 profile (optional)

	// Tone calibration (for OM-3 cameras)
	ToneFormulaPath  string // Path to tone formula JSON
	ApplyToneFormula bool   // Apply tone formula based on sidecar JPEG EXIF

	// Immich config for uploads
	ImmichServerURL    string   // Immich server URL
	ImmichAPIKey       string   // Immich API key
	ImmichAlbum        string   // Optional album name
	ImmichTags         []string // Tags to apply
	TagWithProfileName bool     // Tag uploads with profile name
	NoUploadUI         bool     // Suppress immich-go UI during upload

	// State and cleanup
	CleanupAfterUpload bool   // Delete processed files after upload
	StatePath          string // Path to state file
}

// ServerConfig contains configuration for the editor server
type ServerConfig struct {
	SourcePath    string          // Path to scan for images (camera drive)
	OutputDir     string          // Directory for output files
	ProfilePath   string          // Base PP3 profile to modify (color)
	BWProfilePath string          // B&W PP3 profile path (optional)
	RawExtensions map[string]bool // RAW file extensions
	EditsPath     string          // Path to save edits JSON
	Limit         int             // Limit number of images to load (0 = no limit)

	// Processing options
	ProcessConfig ProcessConfig
}

// NewServer creates a new editor server
func NewServer(config ServerConfig) (*Server, error) {
	s := &Server{
		editsPath:     config.EditsPath,
		outputDir:     config.OutputDir,
		profilePath:   config.ProfilePath,
		bwProfilePath: config.BWProfilePath,
		rawExts:       config.RawExtensions,
		sourcePath:    config.SourcePath,
		imageLimit:    config.Limit,
		jpgMap:        make(map[string]string),
		mux:           http.NewServeMux(),
		processConfig: config.ProcessConfig,
	}

	// Set default edits path
	if s.editsPath == "" {
		s.editsPath = filepath.Join(config.OutputDir, "edits.json")
	}

	// Ensure output directory exists
	if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %v", err)
	}

	// Initialize tone calibration if enabled
	if config.ProcessConfig.ApplyToneFormula && config.ProcessConfig.ToneFormulaPath != "" {
		tc, err := processor.NewToneCalibrationForWorkflow(config.ProcessConfig.ToneFormulaPath)
		if err != nil {
			log.Printf("Note: Failed to initialize tone calibration: %v (continuing without)", err)
		} else {
			s.toneCalibration = tc
			log.Printf("Tone calibration enabled (formula: %s)", filepath.Base(config.ProcessConfig.ToneFormulaPath))
		}
	}

	// Scan for images
	if config.SourcePath != "" {
		if err := s.scanImages(config.SourcePath, config.Limit); err != nil {
			return nil, fmt.Errorf("failed to scan images: %v", err)
		}
	}

	// Setup routes
	s.setupRoutes()

	return s, nil
}

// RescanImages rescans the source path to refresh the image list
// This is useful after processing to get the updated list of unprocessed files
func (s *Server) RescanImages() error {
	if s.sourcePath == "" {
		return fmt.Errorf("no source path configured")
	}
	return s.scanImages(s.sourcePath, s.imageLimit)
}

// scanImages scans the source path for images
func (s *Server) scanImages(sourcePath string, limit int) error {
	log.Printf("Scanning for images in: %s (limit: %d)", sourcePath, limit)

	result, err := scanner.ScanForImages(sourcePath, s.rawExts)
	if err != nil {
		return err
	}

	log.Printf("Scanner found %d RAW files and %d JPG files", len(result.RAWFiles), len(result.JPGFiles))

	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear existing caches for rescan
	s.jpgMap = make(map[string]string)

	// Build JPG map for fast sidecar lookup (RAW basename -> JPG path)
	// This enables using camera-generated JPGs as fast previews for RAW files
	allJpgFiles := result.JPGFiles
	for _, jpg := range allJpgFiles {
		s.jpgMap[jpg.BaseName] = jpg.Path
	}

	// Build map of all files on card for state sync
	filesOnCard := make(map[string]bool)
	for _, f := range result.RAWFiles {
		filesOnCard[f.Name] = true
	}
	for _, f := range result.JPGFiles {
		filesOnCard[f.Name] = true
	}

	// Load state and get the last processed file time (high-water mark)
	var lastProcessedFileModTime time.Time
	if s.processConfig.StatePath != "" {
		appState, err := state.Load(s.processConfig.StatePath)
		if err == nil {
			lastProcessedFileModTime = appState.GetLastProcessedTime()

			if !lastProcessedFileModTime.IsZero() {
				log.Printf("Last processed file time: %s", lastProcessedFileModTime.Format("2006-01-02 15:04:05"))
			} else {
				log.Printf("No last processed time set (first run or empty state)")
			}

			log.Printf("Files on card: %d RAW, %d JPG", len(result.RAWFiles), len(result.JPGFiles))
		} else {
			log.Printf("Failed to load state: %v", err)
		}
	} else {
		log.Printf("No state path configured")
	}

	// Two-stage filtering for performance:
	// Stage 1: Pre-filter by file ModTime (fast - no EXIF reading needed)
	// Stage 2: For candidates, load EXIF CaptureTime and do final filter

	// Stage 1: Pre-filter RAW files by ModTime
	rawCandidates := make([]scanner.FileInfo, 0)
	modTimeFilteredCount := 0
	for _, f := range result.RAWFiles {
		// Pre-filter by file modification time (fast)
		fileModTime := time.Unix(f.ModTime, 0)
		if !lastProcessedFileModTime.IsZero() && !fileModTime.After(lastProcessedFileModTime) {
			modTimeFilteredCount++
			continue
		}
		rawCandidates = append(rawCandidates, f)
	}

	// Stage 1: Pre-filter JPG files by ModTime
	jpgCandidates := make([]scanner.FileInfo, 0)
	for _, f := range result.JPGFiles {
		// Pre-filter by file modification time (fast)
		fileModTime := time.Unix(f.ModTime, 0)
		if !lastProcessedFileModTime.IsZero() && !fileModTime.After(lastProcessedFileModTime) {
			modTimeFilteredCount++
			continue
		}
		jpgCandidates = append(jpgCandidates, f)
	}

	log.Printf("Pre-filter (by ModTime): %d files filtered, %d RAW + %d JPG candidates remain",
		modTimeFilteredCount, len(rawCandidates), len(jpgCandidates))

	// Stage 2: Load EXIF CaptureTime for candidates and do final filter
	// This is the expensive part, but only runs on ~20 candidates instead of ~4600 files
	rawFiles := make([]scanner.FileInfo, 0, len(rawCandidates))
	exifFilteredCount := 0
	for i := range rawCandidates {
		f := &rawCandidates[i]
		// Load EXIF CaptureTime for this candidate
		captureTime := customexif.GetDateTimeOriginalWithFallback(f.Path)
		f.CaptureTime = captureTime.Unix()

		// Final filter by EXIF capture time
		if !lastProcessedFileModTime.IsZero() && !captureTime.After(lastProcessedFileModTime) {
			exifFilteredCount++
			continue
		}
		rawFiles = append(rawFiles, *f)
	}

	// Stage 2: Same for JPG files
	jpgFiles := make([]scanner.FileInfo, 0, len(jpgCandidates))
	for i := range jpgCandidates {
		f := &jpgCandidates[i]
		// Load EXIF CaptureTime for this candidate
		captureTime := customexif.GetDateTimeOriginalWithFallback(f.Path)
		f.CaptureTime = captureTime.Unix()

		// Final filter by EXIF capture time
		if !lastProcessedFileModTime.IsZero() && !captureTime.After(lastProcessedFileModTime) {
			exifFilteredCount++
			continue
		}
		jpgFiles = append(jpgFiles, *f)
	}

	filteredCount := modTimeFilteredCount + exifFilteredCount
	if exifFilteredCount > 0 {
		log.Printf("EXIF filter: %d additional files filtered (had ModTime > high-water but CaptureTime <= high-water)",
			exifFilteredCount)
	}

	newFilesCount := len(rawFiles) + len(jpgFiles)
	if filteredCount > 0 {
		log.Printf("Filtered %d already-processed files (by time), %d new files to review",
			filteredCount, newFilesCount)
	}

	// Apply limit to RAW files if specified
	if limit > 0 {
		if len(rawFiles) > limit {
			rawFiles = rawFiles[:limit]
		}
		// For editor, we primarily work with RAW files
		// JPGs are used as sidecars for preview, not listed separately
		jpgFiles = nil
	}

	s.images = make([]ImageInfo, 0, len(rawFiles)+len(jpgFiles))

	// Add RAW files (primary focus for the editor)
	// Use BaseName (without extension) as ID for stable identification across sessions
	for _, f := range rawFiles {
		imgInfo := ImageInfo{
			ID:           f.BaseName, // Use basename as stable ID
			Filename:     f.Name,
			Path:         f.Path,
			BaseName:     f.BaseName,
			PreviewURL:   fmt.Sprintf("/api/images/%s/preview", f.BaseName),
			ThumbnailURL: fmt.Sprintf("/api/images/%s/thumbnail", f.BaseName),
			IsRAW:        true,
			Size:         f.Size,
			ModTime:      f.ModTime,
			CaptureTime:  f.CaptureTime,
		}
		
		// Read aspect ratio from EXIF for OM System cameras
		if aspectInfo, err := customexif.GetAspectRatio(f.Path); err == nil && aspectInfo != nil {
			imgInfo.AspectRatio = aspectInfo.Ratio
			if aspectInfo.CropFrame != nil {
				imgInfo.CropFrame = &CropFrameInfo{
					X:      aspectInfo.CropFrame.X,
					Y:      aspectInfo.CropFrame.Y,
					Width:  aspectInfo.CropFrame.Width,
					Height: aspectInfo.CropFrame.Height,
				}
			}
		}
		
		s.images = append(s.images, imgInfo)
	}

	// Add standalone JPG files (only if no limit set, for JPG-only workflows)
	if limit == 0 {
		for _, f := range jpgFiles {
			// Skip JPGs that are sidecars for RAW files
			hasSidecar := false
			for _, raw := range rawFiles {
				if raw.BaseName == f.BaseName {
					hasSidecar = true
					break
				}
			}
			if !hasSidecar {
				s.images = append(s.images, ImageInfo{
					ID:           f.BaseName, // Use basename as stable ID
					Filename:     f.Name,
					Path:         f.Path,
					BaseName:     f.BaseName,
					PreviewURL:   fmt.Sprintf("/api/images/%s/preview", f.BaseName),
					ThumbnailURL: fmt.Sprintf("/api/images/%s/thumbnail", f.BaseName),
					IsRAW:        false,
					Size:         f.Size,
					ModTime:      f.ModTime,
					CaptureTime:  f.CaptureTime,
				})
			}
		}
	}

	sidecarCount := 0
	for _, raw := range rawFiles {
		if _, ok := s.jpgMap[raw.BaseName]; ok {
			sidecarCount++
		}
	}

	if limit > 0 && newFilesCount > limit {
		log.Printf("Loaded %d of %d new images (limit: %d), %d have JPG sidecars for fast preview", len(s.images), newFilesCount, limit, sidecarCount)
	} else {
		log.Printf("Found %d images (%d RAW, %d standalone JPG), %d RAW files have JPG sidecars", len(s.images), len(rawFiles), len(s.images)-len(rawFiles), sidecarCount)
	}
	return nil
}

// setupRoutes configures the HTTP routes
func (s *Server) setupRoutes() {
	// Serve static files from embedded templates
	templatesContent, _ := fs.Sub(templatesFS, "templates")
	s.mux.Handle("/", http.FileServer(http.FS(templatesContent)))

	// API endpoints
	s.mux.HandleFunc("/api/images", s.handleImages)
	s.mux.HandleFunc("/api/images/", s.handleImageFile)
	s.mux.HandleFunc("/api/edits", s.handleEdits)
	s.mux.HandleFunc("/api/process", s.handleProcess)
	s.mux.HandleFunc("/api/refresh", s.handleRefresh)
	s.mux.HandleFunc("/api/shutdown", s.handleShutdown)
	s.mux.HandleFunc("/api/cache", s.handleCache)
}

// GetCacheDir returns the path to the cache directory
func (s *Server) GetCacheDir() string {
	return filepath.Join(s.outputDir, ".cache")
}

// GetCacheStats returns statistics about the cache
func (s *Server) GetCacheStats() (int, int64, error) {
	cacheDir := s.GetCacheDir()
	
	var fileCount int
	var totalSize int64
	
	err := filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Ignore errors for individual files
		}
		if !info.IsDir() {
			fileCount++
			totalSize += info.Size()
		}
		return nil
	})
	
	return fileCount, totalSize, err
}

// ClearCache removes all cached preview files
func (s *Server) ClearCache() (int, int64, error) {
	cacheDir := s.GetCacheDir()
	
	// Get stats before clearing
	fileCount, totalSize, _ := s.GetCacheStats()
	
	// Remove the entire cache directory
	if err := os.RemoveAll(cacheDir); err != nil {
		return 0, 0, fmt.Errorf("failed to remove cache directory: %v", err)
	}
	
	// Recreate empty cache directory
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fileCount, totalSize, fmt.Errorf("failed to recreate cache directory: %v", err)
	}
	
	log.Printf("Cleared cache: %d files, %d bytes", fileCount, totalSize)
	return fileCount, totalSize, nil
}

// CleanupProcessedCache removes cache files for images that have been processed
// This helps keep the cache from growing indefinitely
func (s *Server) CleanupProcessedCache(processedIDs []string) (int, int64, error) {
	cacheDir := s.GetCacheDir()
	
	var deletedCount int
	var deletedSize int64
	
	// Create a map for fast lookup
	processedMap := make(map[string]bool)
	for _, id := range processedIDs {
		processedMap[id] = true
	}
	
	// Walk the cache directory and remove files for processed images
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil // No cache directory
		}
		return 0, 0, err
	}
	
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		
		// Cache files are named: {imageID}_{fileType}_{maxSize}.jpg
		// or {imageID}_embedded.jpg, {imageID}_rt.jpg, {imageID}_dcraw.jpg
		filename := entry.Name()
		
		// Extract the image ID (everything before the first underscore)
		parts := strings.SplitN(filename, "_", 2)
		if len(parts) < 2 {
			continue
		}
		imageID := parts[0]
		
		if processedMap[imageID] {
			filePath := filepath.Join(cacheDir, filename)
			info, err := entry.Info()
			if err == nil {
				deletedSize += info.Size()
			}
			if err := os.Remove(filePath); err == nil {
				deletedCount++
			}
		}
	}
	
	if deletedCount > 0 {
		log.Printf("Cleaned up cache for processed images: %d files, %d bytes", deletedCount, deletedSize)
	}
	
	return deletedCount, deletedSize, nil
}

// handleCache handles cache management API requests
func (s *Server) handleCache(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	
	switch r.Method {
	case http.MethodGet:
		// Return cache statistics
		fileCount, totalSize, err := s.GetCacheStats()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get cache stats: %v", err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"path":      s.GetCacheDir(),
			"fileCount": fileCount,
			"totalSize": totalSize,
			"totalSizeMB": float64(totalSize) / (1024 * 1024),
		})
		
	case http.MethodDelete:
		// Clear the entire cache
		fileCount, totalSize, err := s.ClearCache()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to clear cache: %v", err), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":      "cleared",
			"filesRemoved": fileCount,
			"bytesFreed":   totalSize,
			"bytesFreedMB": float64(totalSize) / (1024 * 1024),
		})
		
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRefresh rescans the source path and returns the updated image list
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	log.Printf("Refreshing image list...")
	if err := s.RescanImages(); err != nil {
		log.Printf("Failed to rescan images: %v", err)
		http.Error(w, fmt.Sprintf("Failed to refresh: %v", err), http.StatusInternalServerError)
		return
	}

	s.mu.RLock()
	count := len(s.images)
	s.mu.RUnlock()

	log.Printf("Refresh complete: %d images remaining", count)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "refreshed",
		"count":  count,
	})
}

// handleShutdown handles graceful shutdown request
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "shutting down"})

	// Exit the application after a short delay to allow response to be sent
	go func() {
		time.Sleep(500 * time.Millisecond)
		log.Println("Shutdown requested, exiting...")
		os.Exit(0)
	}()
}

// ServeHTTP implements http.Handler
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// handleImages returns the list of images
func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	images := s.images
	s.mu.RUnlock()

	// Prevent browser caching so fresh data is always loaded
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "application/json")

	log.Printf("API /api/images returning %d images", len(images))
	if len(images) > 0 {
		log.Printf("  First image: %s (ID: %s)", images[0].Filename, images[0].ID)
	}

	json.NewEncoder(w).Encode(images)
}

// handleImageFile serves preview/thumbnail for an image
func (s *Server) handleImageFile(w http.ResponseWriter, r *http.Request) {
	// Parse path: /api/images/{id}/{type}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/images/"), "/")
	if len(parts) != 2 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	imageID := parts[0]
	fileType := parts[1] // "preview" or "thumbnail"

	// Find image
	s.mu.RLock()
	var img *ImageInfo
	for i := range s.images {
		if s.images[i].ID == imageID {
			img = &s.images[i]
			break
		}
	}
	s.mu.RUnlock()

	if img == nil {
		http.Error(w, "Image not found", http.StatusNotFound)
		return
	}

	// For RAW files, we need to generate a preview
	if img.IsRAW {
		s.serveRAWPreview(w, r, img, fileType)
		return
	}

	// For JPG files, serve directly (with optional resize for thumbnails)
	s.serveJPGPreview(w, r, img, fileType)
}

// serveRAWPreview generates and serves a preview for RAW files
func (s *Server) serveRAWPreview(w http.ResponseWriter, r *http.Request, img *ImageInfo, fileType string) {
	// Check if we have a cached preview
	cacheDir := filepath.Join(s.outputDir, ".cache")
	os.MkdirAll(cacheDir, 0755)

	var maxSize int
	if fileType == "thumbnail" {
		maxSize = 300
	} else {
		maxSize = 1200
	}

	cachePath := filepath.Join(cacheDir, fmt.Sprintf("%s_%s_%d.jpg", img.ID, fileType, maxSize))

	// Check cache
	if _, err := os.Stat(cachePath); err == nil {
		http.ServeFile(w, r, cachePath)
		return
	}

	// FAST PATH: Check for sidecar JPG first (camera-generated RAW+JPG pairs)
	// This is the fastest option - just use the existing JPG file
	s.mu.RLock()
	sidecarPath, hasSidecar := s.jpgMap[img.BaseName]
	s.mu.RUnlock()

	var previewPath string
	var err error

	if hasSidecar {
		// Use sidecar JPG directly - fastest option!
		previewPath = sidecarPath
		log.Printf("Using sidecar JPG for %s: %s", img.Filename, filepath.Base(sidecarPath))
	} else {
		// Generate preview using various methods in order of preference
		// 1. Try exiftool first (extracts embedded preview from RAW)
		// 2. Fall back to RawTherapee (slower but always works if installed)
		// 3. Fall back to dcraw if available
		previewPath, err = s.extractEmbeddedPreview(img.Path, cacheDir, img.ID)
		if err != nil {
			// Try RawTherapee (most likely to be installed for this app)
			previewPath, err = s.generatePreviewWithRawTherapee(img.Path, cacheDir, img.ID)
			if err != nil {
				// Fall back to dcraw
				previewPath, err = s.generateRAWPreview(img.Path, cacheDir, img.ID)
				if err != nil {
					log.Printf("Failed to generate preview for %s: %v", img.Filename, err)
					http.Error(w, "Failed to generate preview", http.StatusInternalServerError)
					return
				}
			}
		}
	}

	// Resize if needed
	if err := s.resizeImage(previewPath, cachePath, maxSize); err != nil {
		log.Printf("Failed to resize preview: %v", err)
		// Serve original if resize fails
		http.ServeFile(w, r, previewPath)
		return
	}

	http.ServeFile(w, r, cachePath)
}

// extractEmbeddedPreview extracts the embedded JPEG preview from a RAW file
func (s *Server) extractEmbeddedPreview(rawPath, cacheDir, id string) (string, error) {
	outputPath := filepath.Join(cacheDir, id+"_embedded.jpg")

	// Try exiftool to extract preview
	cmd := exec.Command("exiftool", "-b", "-PreviewImage", "-w", outputPath, rawPath)
	if err := cmd.Run(); err != nil {
		// Try JpgFromRaw tag (used by some cameras)
		cmd = exec.Command("exiftool", "-b", "-JpgFromRaw", rawPath)
		output, err := cmd.Output()
		if err != nil || len(output) == 0 {
			return "", fmt.Errorf("no embedded preview found")
		}
		if err := os.WriteFile(outputPath, output, 0644); err != nil {
			return "", err
		}
	}

	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		return "", fmt.Errorf("preview extraction failed")
	}

	return outputPath, nil
}

// generatePreviewWithRawTherapee generates a preview using rawtherapee-cli
func (s *Server) generatePreviewWithRawTherapee(rawPath, cacheDir, id string) (string, error) {
	jpgPath := filepath.Join(cacheDir, id+"_rt.jpg")

	// Find rawtherapee-cli
	rtPath := findRawTherapeeExecutable()
	if rtPath == "" {
		return "", fmt.Errorf("rawtherapee-cli not found")
	}

	// Use RawTherapee to generate a quick preview
	// -o output, -j quality, -Y overwrite, -c process file
	cmd := exec.Command(rtPath, "-o", jpgPath, "-j85", "-Y", "-c", rawPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("rawtherapee-cli failed: %v\nOutput: %s", err, string(output))
	}

	if _, err := os.Stat(jpgPath); os.IsNotExist(err) {
		return "", fmt.Errorf("rawtherapee-cli did not create output file")
	}

	return jpgPath, nil
}

// findRawTherapeeExecutable tries to find the rawtherapee-cli executable
func findRawTherapeeExecutable() string {
	// Try common names and paths
	names := []string{
		"rawtherapee-cli",
		"rawtherapee-cli.exe",
	}

	// Common installation paths on Windows
	commonPaths := []string{
		`C:\Program Files\RawTherapee\rawtherapee-cli.exe`,
		`C:\Program Files (x86)\RawTherapee\rawtherapee-cli.exe`,
		`C:\Program Files\RawTherapee\5.9\rawtherapee-cli.exe`,
	}

	// Check PATH first
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}

	// Check common installation paths
	for _, path := range commonPaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// generateRAWPreview generates a preview using dcraw
func (s *Server) generateRAWPreview(rawPath, cacheDir, id string) (string, error) {
	outputPath := filepath.Join(cacheDir, id+"_dcraw.ppm")
	jpgPath := filepath.Join(cacheDir, id+"_dcraw.jpg")

	// Use dcraw with embedded thumbnail extraction (-e) or half-size preview (-h)
	cmd := exec.Command("dcraw", "-e", "-c", rawPath)
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		// Try generating a half-size preview
		cmd = exec.Command("dcraw", "-h", "-c", rawPath)
		output, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("dcraw failed: %v", err)
		}
	}

	// Write PPM
	if err := os.WriteFile(outputPath, output, 0644); err != nil {
		return "", err
	}

	// Convert to JPEG using Go's image package
	ppmFile, err := os.Open(outputPath)
	if err != nil {
		return "", err
	}
	defer ppmFile.Close()

	img, _, err := image.Decode(ppmFile)
	if err != nil {
		return "", fmt.Errorf("failed to decode PPM: %v", err)
	}

	jpgFile, err := os.Create(jpgPath)
	if err != nil {
		return "", err
	}
	defer jpgFile.Close()

	if err := jpeg.Encode(jpgFile, img, &jpeg.Options{Quality: 85}); err != nil {
		return "", err
	}

	// Clean up PPM
	os.Remove(outputPath)

	return jpgPath, nil
}

// readExifOrientation reads the EXIF orientation tag from a JPEG file
// Returns orientation value 1-8, or 1 (normal) if not found or error
func readExifOrientation(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 1
	}
	defer file.Close()

	reader := bufio.NewReader(file)

	// Check JPEG magic bytes
	magic := make([]byte, 2)
	if _, err := io.ReadFull(reader, magic); err != nil {
		return 1
	}
	if magic[0] != 0xFF || magic[1] != 0xD8 {
		return 1 // Not a JPEG
	}

	// Scan through JPEG segments looking for EXIF (APP1)
	for {
		// Read segment marker
		marker := make([]byte, 2)
		if _, err := io.ReadFull(reader, marker); err != nil {
			return 1
		}

		if marker[0] != 0xFF {
			return 1 // Invalid marker
		}

		// Skip padding bytes
		for marker[1] == 0xFF {
			if _, err := io.ReadFull(reader, marker[1:]); err != nil {
				return 1
			}
		}

		// End of image or start of scan
		if marker[1] == 0xD9 || marker[1] == 0xDA {
			return 1
		}

		// Read segment length
		lengthBytes := make([]byte, 2)
		if _, err := io.ReadFull(reader, lengthBytes); err != nil {
			return 1
		}
		length := int(binary.BigEndian.Uint16(lengthBytes)) - 2

		if length < 0 {
			return 1
		}

		// APP1 marker (EXIF)
		if marker[1] == 0xE1 {
			segment := make([]byte, length)
			if _, err := io.ReadFull(reader, segment); err != nil {
				return 1
			}

			// Check for "Exif\x00\x00" header
			if length >= 6 && string(segment[:4]) == "Exif" && segment[4] == 0 && segment[5] == 0 {
				return parseExifOrientation(segment[6:])
			}
			continue
		}

		// Skip other segments
		if _, err := reader.Discard(length); err != nil {
			return 1
		}
	}
}

// parseExifOrientation parses TIFF/EXIF data to find orientation tag
func parseExifOrientation(data []byte) int {
	if len(data) < 8 {
		return 1
	}

	// Determine byte order
	var byteOrder binary.ByteOrder
	if data[0] == 'I' && data[1] == 'I' {
		byteOrder = binary.LittleEndian
	} else if data[0] == 'M' && data[1] == 'M' {
		byteOrder = binary.BigEndian
	} else {
		return 1
	}

	// Check TIFF magic number (42)
	if byteOrder.Uint16(data[2:4]) != 42 {
		return 1
	}

	// Get offset to first IFD
	ifdOffset := byteOrder.Uint32(data[4:8])
	if int(ifdOffset)+2 > len(data) {
		return 1
	}

	// Read number of directory entries
	numEntries := byteOrder.Uint16(data[ifdOffset : ifdOffset+2])
	offset := int(ifdOffset) + 2

	// Scan through IFD entries looking for Orientation tag (0x0112)
	for i := uint16(0); i < numEntries; i++ {
		if offset+12 > len(data) {
			return 1
		}

		tag := byteOrder.Uint16(data[offset : offset+2])
		if tag == 0x0112 { // Orientation tag
			// Type should be SHORT (3)
			tagType := byteOrder.Uint16(data[offset+2 : offset+4])
			if tagType != 3 {
				return 1
			}
			// Value is stored in the value/offset field
			orientation := byteOrder.Uint16(data[offset+8 : offset+10])
			if orientation >= 1 && orientation <= 8 {
				return int(orientation)
			}
			return 1
		}
		offset += 12
	}

	return 1
}

// applyOrientation applies EXIF orientation transformation to an image
// Orientation values:
// 1 = Normal (no transformation)
// 2 = Flip horizontal
// 3 = Rotate 180
// 4 = Flip vertical
// 5 = Transpose (rotate 90 CW + flip horizontal)
// 6 = Rotate 90 CW
// 7 = Transverse (rotate 90 CCW + flip horizontal)
// 8 = Rotate 90 CCW
func applyOrientation(img image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return img
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	var result *image.RGBA

	switch orientation {
	case 2: // Flip horizontal
		result = image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				result.Set(width-1-x, y, img.At(x+bounds.Min.X, y+bounds.Min.Y))
			}
		}
	case 3: // Rotate 180
		result = image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				result.Set(width-1-x, height-1-y, img.At(x+bounds.Min.X, y+bounds.Min.Y))
			}
		}
	case 4: // Flip vertical
		result = image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				result.Set(x, height-1-y, img.At(x+bounds.Min.X, y+bounds.Min.Y))
			}
		}
	case 5: // Transpose (rotate 90 CW + flip horizontal)
		result = image.NewRGBA(image.Rect(0, 0, height, width))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				result.Set(y, x, img.At(x+bounds.Min.X, y+bounds.Min.Y))
			}
		}
	case 6: // Rotate 90 CW
		result = image.NewRGBA(image.Rect(0, 0, height, width))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				result.Set(height-1-y, x, img.At(x+bounds.Min.X, y+bounds.Min.Y))
			}
		}
	case 7: // Transverse (rotate 90 CCW + flip horizontal)
		result = image.NewRGBA(image.Rect(0, 0, height, width))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				result.Set(height-1-y, width-1-x, img.At(x+bounds.Min.X, y+bounds.Min.Y))
			}
		}
	case 8: // Rotate 90 CCW
		result = image.NewRGBA(image.Rect(0, 0, height, width))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				result.Set(y, width-1-x, img.At(x+bounds.Min.X, y+bounds.Min.Y))
			}
		}
	default:
		return img
	}

	return result
}

// resizeImageWithOrientation resizes an image to fit within maxSize
func resizeImageWithOrientation(img image.Image, maxSize int) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Calculate new dimensions
	var newWidth, newHeight int
	if width > height {
		if width <= maxSize {
			return img // No resize needed
		}
		newWidth = maxSize
		newHeight = height * maxSize / width
	} else {
		if height <= maxSize {
			return img // No resize needed
		}
		newHeight = maxSize
		newWidth = width * maxSize / height
	}

	// Simple nearest-neighbor resize (for speed)
	resized := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	for y := 0; y < newHeight; y++ {
		for x := 0; x < newWidth; x++ {
			srcX := x * width / newWidth
			srcY := y * height / newHeight
			resized.Set(x, y, img.At(srcX+bounds.Min.X, srcY+bounds.Min.Y))
		}
	}

	return resized
}

// resizeImage resizes an image to fit within maxSize, respecting EXIF orientation
func (s *Server) resizeImage(inputPath, outputPath string, maxSize int) error {
	// Read EXIF orientation before decoding
	orientation := readExifOrientation(inputPath)
	if orientation > 1 {
		log.Printf("EXIF orientation for %s: %d", filepath.Base(inputPath), orientation)
	}

	file, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return err
	}

	// Apply EXIF orientation transformation
	if orientation > 1 {
		img = applyOrientation(img, orientation)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Check if resize is needed
	needsResize := false
	if width > height {
		needsResize = width > maxSize
	} else {
		needsResize = height > maxSize
	}

	// If no resize and no orientation change needed, just copy the file
	if !needsResize && orientation <= 1 {
		return copyFile(inputPath, outputPath)
	}

	// Resize if needed
	var result image.Image = img
	if needsResize {
		result = resizeImageWithOrientation(img, maxSize)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	return jpeg.Encode(outFile, result, &jpeg.Options{Quality: 85})
}

// serveJPGPreview serves a JPG file with optional resizing and EXIF orientation handling
func (s *Server) serveJPGPreview(w http.ResponseWriter, r *http.Request, img *ImageInfo, fileType string) {
	cacheDir := filepath.Join(s.outputDir, ".cache")
	os.MkdirAll(cacheDir, 0755)

	var maxSize int
	var cacheFilename string
	if fileType == "preview" {
		maxSize = 1200
		cacheFilename = fmt.Sprintf("%s_preview.jpg", img.ID)
	} else {
		maxSize = 300
		cacheFilename = fmt.Sprintf("%s_thumb.jpg", img.ID)
	}

	cachePath := filepath.Join(cacheDir, cacheFilename)

	// Check cache
	if _, err := os.Stat(cachePath); err == nil {
		http.ServeFile(w, r, cachePath)
		return
	}

	// Process image with EXIF orientation correction
	if err := s.resizeImage(img.Path, cachePath, maxSize); err != nil {
		log.Printf("Failed to process JPG %s: %v", img.Filename, err)
		// Fall back to serving original file
		http.ServeFile(w, r, img.Path)
		return
	}

	http.ServeFile(w, r, cachePath)
}

// handleEdits handles GET/POST for edits
func (s *Server) handleEdits(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getEdits(w, r)
	case http.MethodPost:
		s.saveEdits(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// getEdits returns saved edits, filtering out stale entries for images no longer in the list
func (s *Server) getEdits(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.editsPath)
	if os.IsNotExist(err) {
		// Return empty edits
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(EditsFile{
			GlobalPreset: "color",
			Edits:        make(map[string]EditData),
		})
		return
	}
	if err != nil {
		http.Error(w, "Failed to read edits", http.StatusInternalServerError)
		return
	}

	// Parse and filter stale entries
	var edits EditsFile
	if err := json.Unmarshal(data, &edits); err != nil {
		// If parsing fails, return empty
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(EditsFile{
			GlobalPreset: "color",
			Edits:        make(map[string]EditData),
		})
		return
	}

	// Build map of current image IDs
	s.mu.RLock()
	currentIDs := make(map[string]bool)
	for _, img := range s.images {
		currentIDs[img.ID] = true
	}
	s.mu.RUnlock()

	// Filter out edits for images that are no longer in the current list
	filteredEdits := make(map[string]EditData)
	staleCount := 0
	for id, edit := range edits.Edits {
		if currentIDs[id] {
			filteredEdits[id] = edit
		} else {
			staleCount++
		}
	}

	if staleCount > 0 {
		log.Printf("Filtered %d stale edits for images no longer in list", staleCount)
		edits.Edits = filteredEdits
		// Save cleaned up edits
		cleanedData, _ := json.MarshalIndent(edits, "", "  ")
		os.WriteFile(s.editsPath, cleanedData, 0644)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(edits)
}

// saveEdits saves edits to file
func (s *Server) saveEdits(w http.ResponseWriter, r *http.Request) {
	var edits EditsFile
	if err := json.NewDecoder(r.Body).Decode(&edits); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	edits.SavedAt = time.Now()

	data, err := json.MarshalIndent(edits, "", "  ")
	if err != nil {
		http.Error(w, "Failed to encode edits", http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(s.editsPath, data, 0644); err != nil {
		http.Error(w, "Failed to save edits", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
}

// handleProcess generates PP3 profiles and processes images with RawTherapee
func (s *Server) handleProcess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Load edits
	data, err := os.ReadFile(s.editsPath)
	if err != nil {
		http.Error(w, "No edits to process", http.StatusBadRequest)
		return
	}

	var edits EditsFile
	if err := json.Unmarshal(data, &edits); err != nil {
		http.Error(w, "Invalid edits file", http.StatusInternalServerError)
		return
	}

	// Collect images to process (non-skipped)
	var imagesToProcess []*ImageInfo
	for imageID, edit := range edits.Edits {
		if edit.Skip {
			continue
		}
		// Find image
		for i := range s.images {
			if s.images[i].ID == imageID {
				imagesToProcess = append(imagesToProcess, &s.images[i])
				break
			}
		}
	}

	// Also add images that weren't touched (use global preset)
	for i := range s.images {
		img := &s.images[i]
		if _, exists := edits.Edits[img.ID]; !exists {
			imagesToProcess = append(imagesToProcess, img)
		}
	}

	if len(imagesToProcess) == 0 {
		http.Error(w, "No images to process", http.StatusBadRequest)
		return
	}

	log.Printf("Processing %d images...", len(imagesToProcess))

	// Step 0: Detect B&W images from sidecar JPGs and separate them if no B&W profile
	// Images detected as B&W without a B&W profile will have their sidecar JPG uploaded instead
	bwDetected := make(map[string]bool)      // Track which images are detected as B&W
	bwSidecarOnly := make(map[string]string) // Images to upload sidecar JPG instead of processing (ID -> sidecar path)

	s.mu.RLock()
	for _, img := range imagesToProcess {
		if sidecarPath, hasSidecar := s.jpgMap[img.BaseName]; hasSidecar {
			isBW := detectBWFromSidecar(sidecarPath)
			if isBW {
				bwDetected[img.ID] = true
				// Check if we have a B&W profile to use
				if s.processConfig.PP3BWProfilePath == "" {
					// No B&W profile - upload sidecar JPG instead
					bwSidecarOnly[img.ID] = sidecarPath
					log.Printf("B&W detected for %s: no B&W profile configured, will upload sidecar JPG instead", img.Filename)
				} else {
					// Check if B&W profile file exists
					if _, err := os.Stat(s.processConfig.PP3BWProfilePath); os.IsNotExist(err) {
						bwSidecarOnly[img.ID] = sidecarPath
						log.Printf("B&W detected for %s: B&W profile not found at %s, will upload sidecar JPG instead", img.Filename, s.processConfig.PP3BWProfilePath)
					} else {
						log.Printf("B&W detected for %s: will use B&W profile %s", img.Filename, filepath.Base(s.processConfig.PP3BWProfilePath))
					}
				}
			}
		}
	}
	s.mu.RUnlock()

	// Filter out images that will use sidecar JPG (remove from processing list)
	var imagesToProcessFiltered []*ImageInfo
	for _, img := range imagesToProcess {
		if _, useSidecar := bwSidecarOnly[img.ID]; !useSidecar {
			imagesToProcessFiltered = append(imagesToProcessFiltered, img)
		}
	}

	log.Printf("B&W detection: %d B&W images detected, %d will use sidecar JPG, %d will be processed",
		len(bwDetected), len(bwSidecarOnly), len(imagesToProcessFiltered))

	// Step 1: Generate PP3 profiles for images to process
	pp3Paths := make(map[string]string)
	for _, img := range imagesToProcessFiltered {
		edit, hasEdit := edits.Edits[img.ID]
		if !hasEdit {
			// Create default edit for untouched images
			edit = EditData{
				Exposure: 0,
				Rotation: 0,
				BW:       edits.GlobalPreset == "bw",
				Touched:  false,
			}
		}

		// For B&W images with B&W profile, use the B&W profile as base
		var pp3Path string
		var err error
		if bwDetected[img.ID] && s.processConfig.PP3BWProfilePath != "" {
			pp3Path, err = s.generatePP3WithProfile(img, edit, edits.GlobalPreset, s.processConfig.PP3BWProfilePath)
		} else {
			pp3Path, err = s.generatePP3(img, edit, edits.GlobalPreset)
		}
		if err != nil {
			log.Printf("Failed to generate PP3 for %s: %v", img.Filename, err)
			continue
		}
		pp3Paths[img.ID] = pp3Path
	}

	// Step 2: Initialize RawTherapee processor
	quality := s.processConfig.JPEGQuality
	if quality == 0 {
		quality = 92
	}

	rtConfig := processor.RawTherapeeConfig{
		ExecutablePath: s.processConfig.RawTherapeeExe,
		ProfilePath:    s.profilePath, // Default profile (will be overridden per-image)
		OutputDir:      s.outputDir,
		Quality:        quality,
	}

	rt, err := processor.NewRawTherapee(rtConfig)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to initialize RawTherapee: %v", err), http.StatusInternalServerError)
		return
	}
	profileName := rt.GetProfileName()
	log.Printf("Using profile: %s", profileName)

	// Step 3: Initialize DNG converter if needed
	var dngConverter *processor.DNGConverter
	var dngTempDir string
	if s.processConfig.ConvertToDNG {
		log.Printf("DNG conversion enabled, initializing converter...")
		dngTempDir, err = os.MkdirTemp("", "camera-to-immich-dng-*")
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to create temp directory: %v", err), http.StatusInternalServerError)
			return
		}
		defer os.RemoveAll(dngTempDir)

		dngConfig := processor.DNGConverterConfig{
			ExecutablePath: s.processConfig.DNGConverterPath,
			OutputDir:      dngTempDir,
			Compressed:     false,
			EmbedOriginal:  false,
		}
		dngConverter, err = processor.NewDNGConverter(dngConfig)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to initialize DNG Converter: %v", err), http.StatusInternalServerError)
			return
		}
		log.Printf("DNG Converter initialized (output: %s)", dngTempDir)
	} else {
		log.Printf("DNG conversion disabled, processing RAW files directly")
	}

	// Step 4: Process images with parallel workers (only the filtered list, not B&W sidecar-only images)
	numWorkers := s.processConfig.Workers
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
		if numWorkers > 4 {
			numWorkers = 4 // Cap at 4 to avoid memory issues
		}
	}
	if len(imagesToProcessFiltered) > 0 && numWorkers > len(imagesToProcessFiltered) {
		numWorkers = len(imagesToProcessFiltered)
	}

	log.Printf("Using %d parallel workers for %d images (excluding %d B&W sidecar-only)", numWorkers, len(imagesToProcessFiltered), len(bwSidecarOnly))

	type processResult struct {
		imageID    string
		filename   string
		outputPath string
		isBW       bool // Flag to indicate if this is a B&W processed image
		err        error
	}

	jobs := make(chan *ImageInfo, len(imagesToProcessFiltered))
	results := make(chan processResult, len(imagesToProcessFiltered))

	// Start workers
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for img := range jobs {
				var inputPath string
				var err error

				// Convert to DNG if enabled
				if dngConverter != nil {
					log.Printf("[%s] Converting to DNG: %s", img.Filename, img.Path)
					inputPath, err = dngConverter.ConvertFile(img.Path)
					if err != nil {
						log.Printf("[%s] DNG conversion FAILED: %v", img.Filename, err)
						results <- processResult{
							imageID:  img.ID,
							filename: img.Filename,
							err:      fmt.Errorf("DNG conversion failed: %v", err),
						}
						continue
					}
					log.Printf("[%s] DNG conversion SUCCESS: %s → %s", img.Filename, img.Path, inputPath)
				} else {
					inputPath = img.Path
					log.Printf("[%s] Processing RAW directly (no DNG conversion): %s", img.Filename, inputPath)
				}

				// Get per-image PP3 profile
				pp3Path := pp3Paths[img.ID]
				if pp3Path == "" {
					pp3Path = s.profilePath // Fall back to default
				}
				log.Printf("[%s] Using PP3 profile: %s", img.Filename, pp3Path)

				// Process with RawTherapee
				log.Printf("[%s] Processing with RawTherapee: input=%s", img.Filename, inputPath)
				outputPath, err := rt.ProcessFileWithProfile(inputPath, pp3Path)
				if err != nil {
					log.Printf("[%s] RawTherapee FAILED: %v", img.Filename, err)
				} else {
					log.Printf("[%s] RawTherapee SUCCESS: → %s", img.Filename, outputPath)
				}
				results <- processResult{
					imageID:    img.ID,
					filename:   img.Filename,
					outputPath: outputPath,
					isBW:       bwDetected[img.ID],
					err:        err,
				}
			}
		}()
	}

	// Send jobs (only filtered images, not B&W sidecar-only)
	for _, img := range imagesToProcessFiltered {
		jobs <- img
	}
	close(jobs)

	// Wait and collect results
	go func() {
		wg.Wait()
		close(results)
	}()

	processedResults := make(map[string]interface{})
	successCount := 0
	errorCount := 0

	processedFiles := make([]string, 0)
	processedBWFiles := make([]string, 0) // Track B&W processed files separately for tagging
	for result := range results {
		if result.err != nil {
			processedResults[result.filename] = map[string]interface{}{
				"status": "error",
				"error":  result.err.Error(),
			}
			errorCount++
			log.Printf("Error processing %s: %v", result.filename, result.err)
		} else {
			processedResults[result.filename] = map[string]interface{}{
				"status": "success",
				"output": filepath.Base(result.outputPath),
				"bw":     result.isBW,
			}
			successCount++
			processedFiles = append(processedFiles, result.outputPath)
			if result.isBW {
				processedBWFiles = append(processedBWFiles, result.outputPath)
			}
			log.Printf("Processed: %s → %s (B&W: %v)", result.filename, filepath.Base(result.outputPath), result.isBW)
		}
	}

	log.Printf("Processing complete: %d success (%d B&W), %d errors", successCount, len(processedBWFiles), errorCount)

	// Step 5: Upload to Immich if not skipped (batch upload like main flow)
	uploadCount := 0
	cameraJPGUploadCount := 0
	bwSidecarUploadCount := 0
	uploadSuccess := false
	
	// Check if we have anything to upload (including B&W sidecar-only images)
	hasFilesToUpload := len(processedFiles) > 0 || len(bwSidecarOnly) > 0
	
	if !s.processConfig.SkipUpload && hasFilesToUpload {
		if s.processConfig.ImmichServerURL == "" || s.processConfig.ImmichAPIKey == "" {
			log.Printf("Skipping upload: Immich server URL or API key not configured")
		} else {
			log.Printf("Uploading files to Immich (batch upload)...")

			immichConfig := uploader.ImmichConfig{
				ServerURL:    s.processConfig.ImmichServerURL,
				APIKey:       s.processConfig.ImmichAPIKey,
				Album:        s.processConfig.ImmichAlbum,
				Tags:         s.processConfig.ImmichTags,
				ShowProgress: !s.processConfig.NoUploadUI,
			}

			im, err := uploader.NewImmich(immichConfig)
			if err != nil {
				log.Printf("Failed to initialize Immich uploader: %v", err)
			} else {
				// Collect camera JPGs (sidecars) for the processed RAW files if enabled
				// BUT exclude those that are B&W sidecar-only (they get uploaded separately with B&W tag)
				var cameraJPGs []string
				if s.processConfig.UploadCameraJPGs {
					s.mu.RLock()
					for _, img := range imagesToProcessFiltered {
						// Only include camera JPGs for non-B&W images (B&W processed files already have their own tagging)
						if sidecarPath, exists := s.jpgMap[img.BaseName]; exists {
							if !bwDetected[img.ID] {
								cameraJPGs = append(cameraJPGs, sidecarPath)
							}
						}
					}
					s.mu.RUnlock()
				}

				// Upload processed files first with their tags (separate color and B&W)
				// First, upload non-B&W processed files
				nonBWProcessedFiles := make([]string, 0)
				for _, f := range processedFiles {
					isBW := false
					for _, bwf := range processedBWFiles {
						if f == bwf {
							isBW = true
							break
						}
					}
					if !isBW {
						nonBWProcessedFiles = append(nonBWProcessedFiles, f)
					}
				}
				
				if len(nonBWProcessedFiles) > 0 {
					processedTempDir, err := os.MkdirTemp("", "editor-processed-*")
					if err != nil {
						log.Printf("Failed to create temp directory for processed files: %v", err)
					} else {
						defer os.RemoveAll(processedTempDir)

						// Copy processed files to temp directory
						for _, jpgPath := range nonBWProcessedFiles {
							destPath := filepath.Join(processedTempDir, filepath.Base(jpgPath))
							if err := copyFile(jpgPath, destPath); err != nil {
								log.Printf("Failed to copy %s: %v", filepath.Base(jpgPath), err)
							}
						}

						// Build tags for processed files (color)
						var processedTags []string
						if s.processConfig.TagWithProfileName && profileName != "" {
							processedTags = append(processedTags, fmt.Sprintf("profile:%s", profileName))
						}
						processedTags = append(processedTags, "processed")

						// Upload processed files
						log.Printf("Uploading %d color processed files to Immich...", len(nonBWProcessedFiles))
						if err := im.UploadFolder(processedTempDir, processedTags, false); err != nil {
							log.Printf("Failed to upload processed files: %v", err)
						} else {
							uploadCount += len(nonBWProcessedFiles)
							uploadSuccess = true
							log.Printf("Uploaded %d color processed files successfully", len(nonBWProcessedFiles))
						}
					}
				}

				// Upload B&W processed files with B&W tag and profile name
				if len(processedBWFiles) > 0 {
					bwProcessedTempDir, err := os.MkdirTemp("", "editor-bw-processed-*")
					if err != nil {
						log.Printf("Failed to create temp directory for B&W processed files: %v", err)
					} else {
						defer os.RemoveAll(bwProcessedTempDir)

						// Copy B&W processed files to temp directory
						for _, jpgPath := range processedBWFiles {
							destPath := filepath.Join(bwProcessedTempDir, filepath.Base(jpgPath))
							if err := copyFile(jpgPath, destPath); err != nil {
								log.Printf("Failed to copy B&W processed %s: %v", filepath.Base(jpgPath), err)
							}
						}

						// Build tags for B&W processed files
						var bwProcessedTags []string
						bwProcessedTags = append(bwProcessedTags, "b&w")
						bwProcessedTags = append(bwProcessedTags, "processed")
						// Add B&W profile name tag
						if s.processConfig.PP3BWProfilePath != "" {
							bwProfileName := strings.TrimSuffix(filepath.Base(s.processConfig.PP3BWProfilePath), filepath.Ext(s.processConfig.PP3BWProfilePath))
							bwProcessedTags = append(bwProcessedTags, fmt.Sprintf("profile:%s", bwProfileName))
						}

						// Upload B&W processed files
						log.Printf("Uploading %d B&W processed files to Immich with tags: %v", len(processedBWFiles), bwProcessedTags)
						if err := im.UploadFolder(bwProcessedTempDir, bwProcessedTags, false); err != nil {
							log.Printf("Failed to upload B&W processed files: %v", err)
						} else {
							uploadCount += len(processedBWFiles)
							uploadSuccess = true
							log.Printf("Uploaded %d B&W processed files successfully", len(processedBWFiles))
						}
					}
				}

				// Upload B&W sidecar-only images (detected as B&W but no B&W profile configured)
				if len(bwSidecarOnly) > 0 {
					bwSidecarTempDir, err := os.MkdirTemp("", "editor-bw-sidecar-*")
					if err != nil {
						log.Printf("Failed to create temp directory for B&W sidecar JPGs: %v", err)
					} else {
						defer os.RemoveAll(bwSidecarTempDir)

						// Copy B&W sidecar JPGs to temp directory
						log.Printf("Uploading %d B&W sidecar JPGs (no B&W profile configured)...", len(bwSidecarOnly))
						for _, sidecarPath := range bwSidecarOnly {
							destPath := filepath.Join(bwSidecarTempDir, filepath.Base(sidecarPath))
							if err := copyFile(sidecarPath, destPath); err != nil {
								log.Printf("Failed to copy B&W sidecar JPG %s: %v", filepath.Base(sidecarPath), err)
							}
						}

						// Upload with "b&w" and "camera-original" tags
						bwSidecarTags := []string{"b&w", "camera-original"}
						if err := im.UploadFolder(bwSidecarTempDir, bwSidecarTags, false); err != nil {
							log.Printf("Failed to upload B&W sidecar JPGs: %v", err)
						} else {
							bwSidecarUploadCount = len(bwSidecarOnly)
							uploadSuccess = true
							log.Printf("Uploaded %d B&W sidecar JPGs successfully", len(bwSidecarOnly))
						}
					}
				}

				// Upload camera JPGs separately with their own tags (if enabled and some uploads succeeded)
				if len(cameraJPGs) > 0 && uploadSuccess {
					cameraJPGTempDir, err := os.MkdirTemp("", "editor-camera-jpgs-*")
					if err != nil {
						log.Printf("Failed to create temp directory for camera JPGs: %v", err)
					} else {
						defer os.RemoveAll(cameraJPGTempDir)

						// Copy camera JPGs to temp directory
						log.Printf("Uploading %d camera-generated sidecar JPGs...", len(cameraJPGs))
						for _, jpgPath := range cameraJPGs {
							destPath := filepath.Join(cameraJPGTempDir, filepath.Base(jpgPath))
							if err := copyFile(jpgPath, destPath); err != nil {
								log.Printf("Failed to copy camera JPG %s: %v", filepath.Base(jpgPath), err)
							}
						}

						// Upload with "camera-original" tag only
						cameraJPGTags := []string{"camera-original"}
						if err := im.UploadFolder(cameraJPGTempDir, cameraJPGTags, false); err != nil {
							log.Printf("Failed to upload camera JPGs: %v", err)
						} else {
							cameraJPGUploadCount = len(cameraJPGs)
							log.Printf("Uploaded %d camera JPGs successfully", len(cameraJPGs))
						}
					}
				}
			}
		}
	} else if s.processConfig.SkipUpload {
		log.Printf("Skipping upload (--skip-upload flag)")
	}

	// Step 6: Update state to mark files as processed (using time-based high-water mark)
	if s.processConfig.StatePath != "" && uploadSuccess {
		appState, err := state.Load(s.processConfig.StatePath)
		if err != nil {
			log.Printf("Failed to load state: %v", err)
		} else {
			// Find the latest (newest) EXIF capture time among processed files
			var latestCaptureTime time.Time
			for _, img := range imagesToProcess {
				fileCaptureTime := time.Unix(img.CaptureTime, 0)
				if fileCaptureTime.After(latestCaptureTime) {
					latestCaptureTime = fileCaptureTime
				}
			}

			// Update the high-water mark for processed file time (based on EXIF capture time)
			if !latestCaptureTime.IsZero() {
				appState.UpdateLastProcessedTime(latestCaptureTime)
				log.Printf("Updated last processed file time to: %s (EXIF capture time)", latestCaptureTime.Format("2006-01-02 15:04:05"))
			}

			if err := appState.Save(); err != nil {
				log.Printf("Failed to save state: %v", err)
			} else {
				log.Printf("State updated: last capture time: %s", latestCaptureTime.Format("2006-01-02 15:04:05"))
			}
		}
	}

	// Step 7: Cleanup processed files after successful upload (if enabled)
	if s.processConfig.CleanupAfterUpload && uploadSuccess && len(processedFiles) > 0 {
		log.Printf("Cleaning up processed files from output directory...")
		cleanupCount := 0
		for _, jpgPath := range processedFiles {
			if err := os.Remove(jpgPath); err != nil {
				log.Printf("Failed to delete %s: %v", filepath.Base(jpgPath), err)
			} else {
				cleanupCount++
			}
			// Also clean up the PP3 file if it exists
			pp3Path := strings.TrimSuffix(jpgPath, filepath.Ext(jpgPath)) + ".pp3"
			if _, err := os.Stat(pp3Path); err == nil {
				os.Remove(pp3Path)
			}
		}
		log.Printf("Deleted %d processed files", cleanupCount)
	}

	// Step 8: Cleanup cache for processed images
	// This keeps the cache from growing indefinitely
	if successCount > 0 {
		processedIDs := make([]string, 0, len(imagesToProcess))
		for _, img := range imagesToProcess {
			processedIDs = append(processedIDs, img.ID)
		}
		cacheFilesRemoved, cacheBytesFreed, err := s.CleanupProcessedCache(processedIDs)
		if err != nil {
			log.Printf("Warning: failed to cleanup cache: %v", err)
		} else if cacheFilesRemoved > 0 {
			log.Printf("Cleaned up cache: %d files, %.2f MB freed", cacheFilesRemoved, float64(cacheBytesFreed)/(1024*1024))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":               "completed",
		"processed":            successCount,
		"bwProcessed":          len(processedBWFiles),
		"errors":               errorCount,
		"uploaded":             uploadCount,
		"cameraJPGsUploaded":   cameraJPGUploadCount,
		"bwSidecarsUploaded":   bwSidecarUploadCount,
		"results":              processedResults,
	})
}

// generatePP3 creates a PP3 profile for the given edit settings
func (s *Server) generatePP3(img *ImageInfo, edit EditData, globalPreset string) (string, error) {
	// Read base profile if exists
	var baseProfile string
	if s.profilePath != "" {
		data, err := os.ReadFile(s.profilePath)
		if err == nil {
			baseProfile = string(data)
		}
	}

	// If no base profile, create minimal one
	if baseProfile == "" {
		baseProfile = `[Version]
AppVersion=5.9
Version=349

[General]
Rank=0
ColorLabel=0
InTrash=false

[Exposure]
Auto=false
Clip=0.02
Compensation=0
Brightness=0
Contrast=0
Saturation=0
Black=0
HighlightCompr=0
HighlightComprThreshold=0
ShadowCompr=50
HistogramMatching=false
CurveFromHistogramMatching=false
ClampOOG=true

[HLRecovery]
Enabled=false
Method=Blend

[Color Management]
InputProfile=(cameraICC)
ToneCurve=false
ApplyLookTable=true
ApplyBaselineExposureOffset=true
ApplyHueSatMap=true
DCPIlluminant=0
WorkingProfile=ProPhoto
WorkingTRC=none
WorkingTRCGamma=2.4
WorkingTRCSLope=12.92310
OutputProfile=RTv4_sRGB
OutputProfileIntent=Relative
OutputBPC=true

[Crop]
Enabled=false
X=0
Y=0
W=0
H=0
FixedRatio=false
Ratio=As Image
Orientation=As Image
Guide=Frame

[Coarse Transformation]
Rotate=0
HorizontalFlip=false
VerticalFlip=false

[Rotation]
Enabled=false
Degree=0
Fill=1

[Distortion]
Amount=0

[LensProfile]
LcMode=none
LCPFile=
UseDistortion=true
UseVignette=true
UseCA=false
LFCameraMake=
LFCameraModel=
LFLens=

[Perspective]
Method=simple
Horizontal=0
Vertical=0
CameraShiftHorizontal=0
CameraShiftVertical=0
CameraPitch=0
CameraRoll=0
CameraYaw=0
ProjectionShift=0
ProjectionRotate=0
ProjectionPitch=0
ProjectionYaw=0
ControlLineValues=
ControlLineTypes=

[Gradient]
Enabled=false
Degree=0
Feather=25
Strength=0.6
CenterX=0
CenterY=0

[PCVignette]
Enabled=false
Strength=0.6
Feather=50
Roundness=50

[CACorrection]
Red=0
Blue=0

[Vignetting Correction]
Amount=0
Radius=50
Strength=1
CenterX=0
CenterY=0

[Resize]
Enabled=false
Scale=1
AppliesTo=Cropped area
Method=Lanczos
DataSpecified=3
Width=900
Height=900
AllowUpscaling=false

[PostDemosaicSharpening]
Enabled=true
Contrast=15
AutoContrast=true
AutoRadius=true
DeconvRadius=0.75
DeconvRadiusOffset=0
DeconvIterCheck=true
DeconvIterations=30

[PostResizeSharpening]
Enabled=false
Contrast=15
Method=rld
Radius=0.5
Amount=200
Threshold=20;80;2000;1200;
OnlyEdges=false
EdgedetectionRadius=1.9
EdgeTolerance=1800
HalocontrolEnabled=false
HalocontrolAmount=85
DeconvRadius=0.45
DeconvAmount=100
DeconvDamping=0
DeconvIterations=100

[Color appearance]
Enabled=false
Degree=90
AutoDegree=true
Degreeout=90
AutoDegreeout=true
Surround=Average
AdaptLum=16
Badpixsl=0
Model=RawT
Algorithm=QM
J-Light=0
Q-Bright=0
C-Chroma=0
S-Chroma=0
M-Chroma=0
J-Contrast=0
Q-Contrast=0
H-Hue=0
RSTProtection=0
AdaptScene=2000
AutoAdapscen=true
SurrSource=false
Gamut=true
Tempout=5003
Autotempout=true
Greenout=1
Ybout=18
Datacie=false
Tonecie=false
Presetcat02=false
CurveMode=Lightness
CurveMode2=Lightness
CurveMode3=Chroma
Curve=0;
Curve2=0;
Curve3=0;

[Wavelet]
Enabled=false

[Directional Pyramid Denoising]
Enabled=false

[EPD]
Enabled=false

[Shadows & Highlights]
Enabled=false

[Gradient]
Enabled=false

[Local Contrast]
Enabled=false

[RGB Curves]
Enabled=false

[ColorToning]
Enabled=false

[RAW]
DarkFrame=
DarkFrameAuto=false
FlatFieldFile=
FlatFieldAutoSelect=false
FlatFieldBlurRadius=32
FlatFieldBlurType=Area Flatfield
FlatFieldAutoClipControl=false
FlatFieldClipControl=0
CA=false
CAAvoidColourshift=true
CAAutoIterations=2
CARed=0
CABlue=0
HotPixelFilter=false
DeadPixelFilter=false
HotDeadPixelThresh=100
PreExposure=1
PreExposureBlend=1

[RAW Bayer]
Method=amaze
Border=4
ImageNum=1
CcSteps=0
PreBlack0=0
PreBlack1=0
PreBlack2=0
PreBlack3=0
PreTwoGreen=true
LineDenoise=0
LineDenoiseDirection=Both
GreenEqThreshold=0
DCBIterations=2
DCBEnhance=false
LMMSEIterations=2
DualDemosaicAutoContrast=true
DualDemosaicContrast=20
PixelShiftMotionCorrectionMethod=auto
PixelShiftEperIso=0
PixelShiftSigma=1
PixelShiftShowMotion=false
PixelShiftShowMotionMaskOnly=false
pixelShiftHoleFill=true
pixelShiftMedian=false
pixelShiftGreen=true
pixelShiftBlur=true
pixelShiftSmoothFactor=0.7
pixelShiftEqualBright=false
pixelShiftEqualBrightChannel=false
pixelShiftNonGreenCross=true
pixelShiftDemosaicMethod=amaze
PDAFLinesFilter=false

[RAW X-Trans]
Method=3-pass (best)
DualDemosaicAutoContrast=true
DualDemosaicContrast=20
Border=7
CcSteps=0
PreBlackRed=0
PreBlackGreen=0
PreBlackBlue=0

[Film Negative]
Enabled=false

[MetaData]
Mode=1
ExifKeys=Exif.Image.Artist;Exif.Image.Copyright;Exif.Image.ImageDescription;Exif.Photo.UserComment;Exif.GPSInfo.*;

[IPTC]
`
	}

	// Get image dimensions for crop calculation
	// IMPORTANT: For RAW files with camera aspect ratio, we need to use the EXIF RAW dimensions
	// not the sidecar JPG dimensions (which are already cropped by the camera)
	imgWidth, imgHeight := 0, 0
	useExifCropFrameDirectly := false

	// Check if this image has a non-4:3 camera aspect ratio
	hasNon43AspectRatio := img.AspectRatio != "" && img.AspectRatio != "4:3" && img.CropFrame != nil

	if edit.Crop != nil || edit.Aspect == "camera" || hasNon43AspectRatio {
		if hasNon43AspectRatio && img.CropFrame != nil {
			// For non-4:3 aspect ratio images, calculate RAW sensor dimensions from crop frame
			// The crop frame defines the region within the full RAW sensor
			// For OM-3: full sensor is 5184x3888 (approximately), crop frame X+Width gives us one boundary
			// Use the EXIF crop frame directly for PP3 - no normalization needed
			useExifCropFrameDirectly = true
			// We still need dimensions for other crop operations, estimate from crop frame
			// The crop frame represents the usable area, and X+Width or Y+Height give max bounds
			imgWidth = img.CropFrame.X + img.CropFrame.Width
			imgHeight = img.CropFrame.Y + img.CropFrame.Height
			log.Printf("Using EXIF crop frame directly for %s (aspect %s): (%d,%d,%d,%d)",
				img.Filename, img.AspectRatio, img.CropFrame.X, img.CropFrame.Y, img.CropFrame.Width, img.CropFrame.Height)
		} else {
			var err error
			imgWidth, imgHeight, err = s.getImageDimensions(img.Path)
			if err != nil {
				log.Printf("Warning: failed to get image dimensions for %s: %v (crop may not work)", img.Filename, err)
			}
		}
	}

	// If camera aspect ratio is selected OR image has non-4:3 aspect ratio, use EXIF crop frame
	if (edit.Aspect == "camera" || hasNon43AspectRatio) && img.CropFrame != nil {
		// For untouched images with non-4:3 aspect ratio, auto-apply the camera crop
		// Don't apply if user has manually set a different crop
		if edit.Crop == nil || (edit.Crop.X == 0 && edit.Crop.Y == 0 && edit.Crop.Width == 1 && edit.Crop.Height == 1) {
			if useExifCropFrameDirectly {
				// Use EXIF crop frame pixel coordinates directly
				edit.Crop = &CropBox{
					X:      float64(img.CropFrame.X),
					Y:      float64(img.CropFrame.Y),
					Width:  float64(img.CropFrame.Width),
					Height: float64(img.CropFrame.Height),
				}
				// Mark that we're using pixel coordinates directly (not normalized)
				// by setting imgWidth/imgHeight to indicate direct pixel mode
				imgWidth = 1
				imgHeight = 1
				log.Printf("Applied camera aspect ratio crop for %s: using EXIF frame pixels (%d,%d,%d,%d)",
					img.Filename, img.CropFrame.X, img.CropFrame.Y, img.CropFrame.Width, img.CropFrame.Height)
			} else if imgWidth > 0 && imgHeight > 0 {
				// Convert camera's EXIF crop frame to normalized crop box
				edit.Crop = &CropBox{
					X:      float64(img.CropFrame.X) / float64(imgWidth),
					Y:      float64(img.CropFrame.Y) / float64(imgHeight),
					Width:  float64(img.CropFrame.Width) / float64(imgWidth),
					Height: float64(img.CropFrame.Height) / float64(imgHeight),
				}
				log.Printf("Applied camera aspect ratio crop for %s: EXIF frame (%d,%d,%d,%d) -> normalized (%.3f,%.3f,%.3f,%.3f)",
					img.Filename, img.CropFrame.X, img.CropFrame.Y, img.CropFrame.Width, img.CropFrame.Height,
					edit.Crop.X, edit.Crop.Y, edit.Crop.Width, edit.Crop.Height)
			}
		}
	}

	// Apply tone calibration if enabled and sidecar JPEG exists
	if s.toneCalibration != nil {
		sidecarPath := s.toneCalibration.FindSidecarJPEG(img.Path)
		if sidecarPath != "" {
			// Extract tone settings from sidecar JPEG
			om3Settings, toneErr := s.toneCalibration.ExtractToneSettings(sidecarPath)
			if toneErr == nil {
				// Calculate ToneEqualizer settings from formula
				teSettings := s.toneCalibration.CalculateFromFormula(om3Settings)
				// Apply ToneEqualizer to base profile
				baseProfile = s.toneCalibration.GeneratePP3WithToneEqualizer(teSettings, baseProfile)
				log.Printf("Applied tone calibration for %s: H=%d S=%d M=%d C=%d -> Band0=%d Band1=%d Band2=%d Band3=%d Band4=%d",
					img.Filename, om3Settings.Highlights, om3Settings.Shadows, om3Settings.Midtones, om3Settings.Contrast,
					teSettings.Band0, teSettings.Band1, teSettings.Band2, teSettings.Band3, teSettings.Band4)
			} else {
				log.Printf("Note: Failed to extract tone settings for %s: %v", img.Filename, toneErr)
			}
		}
	}

	// Apply edit settings to profile
	pp3Content := s.applyEditsToPP3(baseProfile, edit, globalPreset, imgWidth, imgHeight)

	// Write PP3 file
	baseName := strings.TrimSuffix(img.Filename, filepath.Ext(img.Filename))
	pp3Path := filepath.Join(s.outputDir, baseName+".pp3")

	if err := os.WriteFile(pp3Path, []byte(pp3Content), 0644); err != nil {
		return "", fmt.Errorf("failed to write PP3: %v", err)
	}

	return pp3Path, nil
}

// generatePP3WithProfile creates a PP3 profile using a custom profile as base
// This is used for B&W images when a B&W profile is configured
func (s *Server) generatePP3WithProfile(img *ImageInfo, edit EditData, globalPreset string, customProfilePath string) (string, error) {
	// Read the custom profile
	var baseProfile string
	data, err := os.ReadFile(customProfilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read custom profile %s: %v", customProfilePath, err)
	}
	baseProfile = string(data)
	log.Printf("Using custom B&W profile: %s", filepath.Base(customProfilePath))

	// Get image dimensions for crop calculation
	// IMPORTANT: For RAW files with camera aspect ratio, we need to use the EXIF RAW dimensions
	// not the sidecar JPG dimensions (which are already cropped by the camera)
	imgWidth, imgHeight := 0, 0
	useExifCropFrameDirectly := false

	// Check if this image has a non-4:3 camera aspect ratio
	hasNon43AspectRatio := img.AspectRatio != "" && img.AspectRatio != "4:3" && img.CropFrame != nil

	if edit.Crop != nil || edit.Aspect == "camera" || hasNon43AspectRatio {
		if hasNon43AspectRatio && img.CropFrame != nil {
			// For non-4:3 aspect ratio images, use EXIF crop frame directly
			useExifCropFrameDirectly = true
			imgWidth = img.CropFrame.X + img.CropFrame.Width
			imgHeight = img.CropFrame.Y + img.CropFrame.Height
			log.Printf("Using EXIF crop frame directly (B&W) for %s (aspect %s): (%d,%d,%d,%d)",
				img.Filename, img.AspectRatio, img.CropFrame.X, img.CropFrame.Y, img.CropFrame.Width, img.CropFrame.Height)
		} else {
			var err error
			imgWidth, imgHeight, err = s.getImageDimensions(img.Path)
			if err != nil {
				log.Printf("Warning: failed to get image dimensions for %s: %v (crop may not work)", img.Filename, err)
			}
		}
	}

	// If camera aspect ratio is selected OR image has non-4:3 aspect ratio, use EXIF crop frame
	if (edit.Aspect == "camera" || hasNon43AspectRatio) && img.CropFrame != nil {
		if edit.Crop == nil || (edit.Crop.X == 0 && edit.Crop.Y == 0 && edit.Crop.Width == 1 && edit.Crop.Height == 1) {
			if useExifCropFrameDirectly {
				// Use EXIF crop frame pixel coordinates directly
				edit.Crop = &CropBox{
					X:      float64(img.CropFrame.X),
					Y:      float64(img.CropFrame.Y),
					Width:  float64(img.CropFrame.Width),
					Height: float64(img.CropFrame.Height),
				}
				imgWidth = 1
				imgHeight = 1
				log.Printf("Applied camera aspect ratio crop (B&W) for %s: using EXIF frame pixels (%d,%d,%d,%d)",
					img.Filename, img.CropFrame.X, img.CropFrame.Y, img.CropFrame.Width, img.CropFrame.Height)
			} else if imgWidth > 0 && imgHeight > 0 {
				// Convert camera's EXIF crop frame to normalized crop box
				edit.Crop = &CropBox{
					X:      float64(img.CropFrame.X) / float64(imgWidth),
					Y:      float64(img.CropFrame.Y) / float64(imgHeight),
					Width:  float64(img.CropFrame.Width) / float64(imgWidth),
					Height: float64(img.CropFrame.Height) / float64(imgHeight),
				}
				log.Printf("Applied camera aspect ratio crop (B&W profile) for %s: EXIF frame (%d,%d,%d,%d) -> normalized (%.3f,%.3f,%.3f,%.3f)",
					img.Filename, img.CropFrame.X, img.CropFrame.Y, img.CropFrame.Width, img.CropFrame.Height,
					edit.Crop.X, edit.Crop.Y, edit.Crop.Width, edit.Crop.Height)
			}
		}
	}

	// Apply edit settings to profile
	pp3Content := s.applyEditsToPP3(baseProfile, edit, globalPreset, imgWidth, imgHeight)

	// Write PP3 file
	baseName := strings.TrimSuffix(img.Filename, filepath.Ext(img.Filename))
	pp3Path := filepath.Join(s.outputDir, baseName+".pp3")

	if err := os.WriteFile(pp3Path, []byte(pp3Content), 0644); err != nil {
		return "", fmt.Errorf("failed to write PP3: %v", err)
	}

	return pp3Path, nil
}

// getImageDimensions extracts width and height from an image
// Tries multiple methods: sidecar JPG, exiftool, or embedded preview
func (s *Server) getImageDimensions(imagePath string) (width, height int, err error) {
	// Method 1: Try reading the sidecar JPG if available
	baseName := strings.TrimSuffix(filepath.Base(imagePath), filepath.Ext(imagePath))
	s.mu.RLock()
	sidecarPath, hasSidecar := s.jpgMap[baseName]
	s.mu.RUnlock()

	if hasSidecar {
		file, err := os.Open(sidecarPath)
		if err == nil {
			defer file.Close()
			img, _, err := image.DecodeConfig(file)
			if err == nil {
				log.Printf("Got dimensions from sidecar JPG %s: %dx%d", filepath.Base(sidecarPath), img.Width, img.Height)
				return img.Width, img.Height, nil
			}
		}
	}

	// Method 2: Try exiftool if available
	cmd := exec.Command("exiftool", "-ImageWidth", "-ImageHeight", "-s3", imagePath)
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) >= 2 {
			fmt.Sscanf(lines[0], "%d", &width)
			fmt.Sscanf(lines[1], "%d", &height)
			if width > 0 && height > 0 {
				return width, height, nil
			}
		}
	}

	return 0, 0, fmt.Errorf("failed to extract dimensions from %s", imagePath)
}

// applyEditsToPP3 modifies PP3 content with edit settings
func (s *Server) applyEditsToPP3(pp3Content string, edit EditData, globalPreset string, imgWidth, imgHeight int) string {
	lines := strings.Split(pp3Content, "\n")
	result := make([]string, 0, len(lines))

	// Determine if B&W
	isBW := edit.BW
	if !edit.Touched && globalPreset == "bw" {
		isBW = true
	}

	// Calculate crop pixel coordinates if crop is set
	var cropX, cropY, cropW, cropH int
	if edit.Crop != nil && imgWidth > 0 && imgHeight > 0 {
		cropX = int(edit.Crop.X * float64(imgWidth))
		cropY = int(edit.Crop.Y * float64(imgHeight))
		cropW = int(edit.Crop.Width * float64(imgWidth))
		cropH = int(edit.Crop.Height * float64(imgHeight))
		log.Printf("Crop: normalized (%.2f,%.2f,%.2f,%.2f) -> pixels (%d,%d,%d,%d) for image %dx%d",
			edit.Crop.X, edit.Crop.Y, edit.Crop.Width, edit.Crop.Height,
			cropX, cropY, cropW, cropH, imgWidth, imgHeight)
	}

	inSection := ""
	exposureSet := false
	rotationSet := false
	cropSet := false
	cropXSet := false
	cropYSet := false
	cropWSet := false
	cropHSet := false
	saturationSet := false

	for _, line := range lines {
		// Track current section
		if strings.HasPrefix(line, "[") && strings.HasSuffix(strings.TrimSpace(line), "]") {
			inSection = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(line), "]"), "[")
		}

		// Modify settings based on section
		switch inSection {
		case "Exposure":
			if strings.HasPrefix(line, "Compensation=") && edit.Exposure != 0 {
				line = fmt.Sprintf("Compensation=%.2f", edit.Exposure)
				exposureSet = true
			}
			if strings.HasPrefix(line, "Saturation=") && isBW {
				line = "Saturation=-100"
				saturationSet = true
			}

		case "Rotation":
			if strings.HasPrefix(line, "Enabled=") && edit.Rotation != 0 {
				line = "Enabled=true"
			}
			if strings.HasPrefix(line, "Degree=") && edit.Rotation != 0 {
				// Negate the rotation: CSS rotate() is clockwise for positive values,
				// but RawTherapee's Degree is counter-clockwise for positive values
				line = fmt.Sprintf("Degree=%.2f", -edit.Rotation)
				rotationSet = true
			}

		case "Crop":
			if edit.Crop != nil && imgWidth > 0 {
				if strings.HasPrefix(line, "Enabled=") {
					line = "Enabled=true"
					cropSet = true
				}
				if strings.HasPrefix(line, "X=") {
					line = fmt.Sprintf("X=%d", cropX)
					cropXSet = true
				}
				if strings.HasPrefix(line, "Y=") {
					line = fmt.Sprintf("Y=%d", cropY)
					cropYSet = true
				}
				if strings.HasPrefix(line, "W=") {
					line = fmt.Sprintf("W=%d", cropW)
					cropWSet = true
				}
				if strings.HasPrefix(line, "H=") {
					line = fmt.Sprintf("H=%d", cropH)
					cropHSet = true
				}
				// Note: PP3 uses pixel coordinates, we'll need image dimensions
				// For now, store normalized values and convert during processing
			}
		}

		result = append(result, line)
	}

	// Add settings that weren't found in base profile
	if edit.Exposure != 0 && !exposureSet {
		result = s.ensureSectionSetting(result, "Exposure", "Compensation", fmt.Sprintf("%.2f", edit.Exposure))
	}
	if isBW && !saturationSet {
		result = s.ensureSectionSetting(result, "Exposure", "Saturation", "-100")
	}
	if edit.Rotation != 0 && !rotationSet {
		result = s.ensureSectionSetting(result, "Rotation", "Enabled", "true")
		// Negate the rotation: CSS rotate() is clockwise for positive values,
		// but RawTherapee's Degree is counter-clockwise for positive values
		result = s.ensureSectionSetting(result, "Rotation", "Degree", fmt.Sprintf("%.2f", -edit.Rotation))
	}
	if edit.Crop != nil && imgWidth > 0 && !cropSet {
		result = s.ensureSectionSetting(result, "Crop", "Enabled", "true")
	}
	if edit.Crop != nil && imgWidth > 0 && !cropXSet {
		result = s.ensureSectionSetting(result, "Crop", "X", fmt.Sprintf("%d", cropX))
	}
	if edit.Crop != nil && imgWidth > 0 && !cropYSet {
		result = s.ensureSectionSetting(result, "Crop", "Y", fmt.Sprintf("%d", cropY))
	}
	if edit.Crop != nil && imgWidth > 0 && !cropWSet {
		result = s.ensureSectionSetting(result, "Crop", "W", fmt.Sprintf("%d", cropW))
	}
	if edit.Crop != nil && imgWidth > 0 && !cropHSet {
		result = s.ensureSectionSetting(result, "Crop", "H", fmt.Sprintf("%d", cropH))
	}

	return strings.Join(result, "\n")
}

// ensureSectionSetting adds or updates a setting in a section
func (s *Server) ensureSectionSetting(lines []string, section, key, value string) []string {
	sectionHeader := "[" + section + "]"
	sectionFound := false
	keyFound := false

	for i, line := range lines {
		if strings.TrimSpace(line) == sectionHeader {
			sectionFound = true
			continue
		}
		if sectionFound {
			if strings.HasPrefix(line, "[") {
				// End of section, key not found
				if !keyFound {
					// Insert before next section
					newLines := make([]string, len(lines)+1)
					copy(newLines[:i], lines[:i])
					newLines[i] = key + "=" + value
					copy(newLines[i+1:], lines[i:])
					return newLines
				}
				break
			}
			if strings.HasPrefix(line, key+"=") {
				lines[i] = key + "=" + value
				keyFound = true
			}
		}
	}

	// Section not found, add it
	if !sectionFound {
		lines = append(lines, "", sectionHeader, key+"="+value)
	}

	return lines
}

// Start starts the HTTP server and opens the browser
func (s *Server) Start(addr string) error {
	// Build proper URL with localhost if only port specified
	url := "http://" + addr
	if strings.HasPrefix(addr, ":") {
		url = "http://localhost" + addr
	}

	log.Printf("Starting editor server at %s", url)
	log.Printf("Open your browser to %s to edit images", url)

	// Auto-launch browser
	go func() {
		time.Sleep(500 * time.Millisecond) // Wait for server to start
		openBrowser(url)
	}()

	return http.ListenAndServe(addr, s)
}

// openBrowser opens the default browser with the given URL
func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default: // linux, etc.
		err = exec.Command("xdg-open", url).Start()
	}
	if err != nil {
		log.Printf("Failed to open browser: %v", err)
	}
}

// GetImageCount returns the number of images loaded
func (s *Server) GetImageCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.images)
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

	_, err = io.Copy(destFile, sourceFile)
	return err
}

// Helper to parse image ID index
func parseImageIndex(id string) (int, error) {
	parts := strings.Split(id, "_")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid image ID format")
	}
	return strconv.Atoi(parts[1])
}

// detectBWFromSidecar analyzes a sidecar JPG image to determine if it's black & white.
// An image is considered B&W if the R, G, and B channels are very similar for most pixels.
// This is useful for detecting when a camera shot in B&W mode (the JPG is grayscale but RAW has color).
// Returns true if the image is detected as B&W, false otherwise.
func detectBWFromSidecar(jpgPath string) bool {
	// Open the image file
	file, err := os.Open(jpgPath)
	if err != nil {
		log.Printf("Failed to open sidecar JPG for B&W detection: %v", err)
		return false
	}
	defer file.Close()

	// Decode the image
	img, _, err := image.Decode(file)
	if err != nil {
		log.Printf("Failed to decode sidecar JPG for B&W detection: %v", err)
		return false
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Sample pixels to determine if image is B&W
	// We don't need to check every pixel - sampling is faster and sufficient
	sampleStep := 10 // Sample every 10th pixel in each direction
	if width < 100 || height < 100 {
		sampleStep = 1 // For very small images, check all pixels
	}

	totalSamples := 0
	bwSamples := 0
	const threshold = 10 // Allow small differences due to compression artifacts

	for y := bounds.Min.Y; y < bounds.Max.Y; y += sampleStep {
		for x := bounds.Min.X; x < bounds.Max.X; x += sampleStep {
			r, g, b, _ := img.At(x, y).RGBA()
			// Convert from 16-bit to 8-bit values
			r8 := int(r >> 8)
			g8 := int(g >> 8)
			b8 := int(b >> 8)

			// Check if R ≈ G ≈ B (grayscale)
			maxDiff := max(abs(r8-g8), max(abs(g8-b8), abs(r8-b8)))
			totalSamples++
			if maxDiff <= threshold {
				bwSamples++
			}
		}
	}

	// Consider it B&W if at least 95% of sampled pixels are grayscale
	ratio := float64(bwSamples) / float64(totalSamples)
	isBW := ratio >= 0.95

	if isBW {
		log.Printf("B&W detection: %s is B&W (%.1f%% grayscale pixels)", filepath.Base(jpgPath), ratio*100)
	}

	return isBW
}

// abs returns the absolute value of an integer
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
