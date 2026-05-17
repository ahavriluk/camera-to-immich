package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ohavrylyuk/camera-to-immich/internal/config"
	"github.com/ohavrylyuk/camera-to-immich/internal/drive"
	"github.com/ohavrylyuk/camera-to-immich/internal/editor"
	customexif "github.com/ohavrylyuk/camera-to-immich/internal/exif"
	"github.com/ohavrylyuk/camera-to-immich/internal/processor"
	"github.com/ohavrylyuk/camera-to-immich/internal/scanner"
	"github.com/ohavrylyuk/camera-to-immich/internal/state"
	"github.com/ohavrylyuk/camera-to-immich/internal/uploader"
)

var (
	version = "2.1.0"
)

func main() {
	// Command-line flags
	configPath := flag.String("config", "", "Path to configuration file")
	profilePath := flag.String("profile", "", "Path to PP3 profile (overrides config)")
	serverURL := flag.String("server", "", "Immich server URL (overrides config)")
	apiKey := flag.String("key", "", "Immich API key (overrides config)")
	outputDir := flag.String("output", "", "Output directory for processed files (overrides config)")
	driveLabel := flag.String("drive", "", "Drive label to search for (overrides config)")
	dryRun := flag.Bool("dry-run", false, "Show what would be done without actually doing it")
	jpgOnly := flag.Bool("jpg-only", false, "Upload JPG files only, skip RAW processing")
	skipUpload := flag.Bool("skip-upload", false, "Process files but skip uploading to Immich")
	noCameraJPGs := flag.Bool("no-camera-jpgs", false, "Skip uploading camera-generated JPG files (only upload processed files)")
	limit := flag.Int("limit", 0, "Limit the number of files to process (0 = no limit)")
	workers := flag.Int("workers", 0, "Number of parallel workers for processing (0 = auto based on CPU cores)")
	listDrives := flag.Bool("list-drives", false, "List all available drives and exit")
	initConfig := flag.Bool("init", false, "Create a sample configuration file")
	showVersion := flag.Bool("version", false, "Show version information")
	verbose := flag.Bool("verbose", false, "Enable verbose output")
	keepFiles := flag.Bool("keep-files", false, "Keep processed files in output directory (don't clean up after upload)")
	clearState := flag.Bool("clear-state", false, "Clear the processed files state and exit")
	stateInfo := flag.Bool("state-info", false, "Show state file information and exit")
	editorMode := flag.Bool("editor", false, "Launch web editor for image preview and adjustment")
	editorPort := flag.String("editor-port", "8080", "Port for the web editor server")
	filmMode := flag.Bool("film", false, "Film scan mode: enables date assignment, JPG processing, roll grouping")
	sourcePath := flag.String("source", "", "Source folder path (for film scan mode, bypasses drive detection)")
	retryUpload := flag.Bool("retry-upload", false, "Upload existing processed files from output directory without reprocessing")
	noUploadUI := flag.Bool("no-upload-ui", false, "Suppress immich-go UI during upload (quiet mode)")
	clearCache := flag.Bool("clear-cache", false, "Clear the preview image cache and exit")
	cacheInfo := flag.Bool("cache-info", false, "Show cache information and exit")

	flag.Parse()

	// Show version
	if *showVersion {
		fmt.Printf("camera-to-immich version %s\n", version)
		os.Exit(0)
	}

	// List drives mode
	if *listDrives {
		listAllDrives()
		os.Exit(0)
	}

	// State info mode
	if *stateInfo {
		showStateInfo()
		os.Exit(0)
	}

	// Clear state mode
	if *clearState {
		clearStateFile()
		os.Exit(0)
	}

	// Cache info mode
	if *cacheInfo {
		showCacheInfo(*configPath, *outputDir)
		os.Exit(0)
	}

	// Clear cache mode
	if *clearCache {
		clearCacheFiles(*configPath, *outputDir)
		os.Exit(0)
	}

	// Editor mode - early exit path that doesn't require full config validation
	if *editorMode {
		runEditor(*configPath, *driveLabel, *outputDir, *profilePath, *editorPort, *limit, *skipUpload, *filmMode, *sourcePath)
		os.Exit(0)
	}

	// Retry upload mode - upload existing processed files without reprocessing
	if *retryUpload {
		runRetryUpload(*configPath, *outputDir, *verbose)
		os.Exit(0)
	}

	// Determine config path
	cfgPath := *configPath
	if cfgPath == "" {
		var err error
		cfgPath, err = config.DefaultConfigPath()
		if err != nil {
			log.Fatalf("Failed to determine config path: %v", err)
		}
	}

	// Init config mode
	if *initConfig {
		if err := config.CreateSampleConfig(cfgPath); err != nil {
			log.Fatalf("Failed to create sample config: %v", err)
		}
		fmt.Printf("Sample configuration created at: %s\n", cfgPath)
		fmt.Println("Please edit this file with your settings before running the processor.")
		os.Exit(0)
	}

	// Load configuration
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Apply command-line overrides
	if *profilePath != "" {
		cfg.PP3ProfilePath = *profilePath
	}
	if *serverURL != "" {
		cfg.ImmichServerURL = *serverURL
	}
	if *apiKey != "" {
		cfg.ImmichAPIKey = *apiKey
	}
	if *outputDir != "" {
		cfg.OutputDirectory = *outputDir
	}
	if *driveLabel != "" {
		cfg.DriveLabel = *driveLabel
	}
	if *dryRun {
		cfg.DryRun = true
	}
	if *jpgOnly {
		cfg.ProcessRAWFiles = false
	}
	if *skipUpload {
		cfg.SkipUpload = true
	}
	if *limit > 0 {
		cfg.Limit = *limit
	}
	if *noCameraJPGs {
		cfg.UploadCameraJPGs = false
	}
	if *keepFiles {
		cfg.CleanupAfterUpload = false
	}
	if *workers > 0 {
		cfg.Workers = *workers
	}
	if *noUploadUI {
		cfg.NoUploadUI = true
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	// Check required external tools
	if err := checkRequiredTools(cfg); err != nil {
		log.Fatalf("Missing tools: %v\nSee README.md for installation instructions.", err)
	}

	// Run the processor
	if err := run(cfg, *verbose); err != nil {
		log.Fatalf("Processing failed: %v", err)
	}
}

func listAllDrives() {
	drives, err := drive.ListAllDrives()
	if err != nil {
		log.Fatalf("Failed to list drives: %v", err)
	}

	fmt.Println("Available drives:")
	fmt.Println("─────────────────────────────────────")
	for _, d := range drives {
		label := d.VolumeLabel
		if label == "" {
			label = "(no label)"
		}
		if d.Letter != "" {
			fmt.Printf("  %s  %s  [%s]\n", d.Letter, label, d.Path)
		} else {
			fmt.Printf("  %s  [%s]\n", label, d.Path)
		}
	}
}

func showStateInfo() {
	statePath, err := state.DefaultStatePath()
	if err != nil {
		fmt.Printf("Error getting state path: %v\n", err)
		return
	}

	appState, err := state.Load(statePath)
	if err != nil {
		fmt.Printf("Error loading state: %v\n", err)
		return
	}

	stats := appState.GetStats()

	fmt.Println("State File Information")
	fmt.Println("======================")
	fmt.Printf("Path: %s\n", statePath)
	if !stats.LastProcessedTime.IsZero() {
		fmt.Printf("Last processed file time: %s\n", stats.LastProcessedTime.Format("2006-01-02 15:04:05"))
		fmt.Println("  (Files with mod time > this will be shown as unprocessed)")
	} else {
		fmt.Println("Last processed file time: not set (all files will be shown)")
	}
	if stats.FileSizeBytes > 0 {
		fmt.Printf("File size: %d bytes\n", stats.FileSizeBytes)
	}
	if !stats.LastRun.IsZero() {
		fmt.Printf("Last run: %s\n", stats.LastRun.Format("2006-01-02 15:04:05"))
	}
	if stats.CardID != "" {
		fmt.Printf("Card ID: %s\n", stats.CardID)
	}
}

func clearStateFile() {
	statePath, err := state.DefaultStatePath()
	if err != nil {
		fmt.Printf("Error getting state path: %v\n", err)
		return
	}

	appState, err := state.Load(statePath)
	if err != nil {
		fmt.Printf("Error loading state: %v\n", err)
		return
	}

	count := appState.Clear()
	if err := appState.Save(); err != nil {
		fmt.Printf("Error saving state: %v\n", err)
		return
	}

	fmt.Printf("Cleared %d processed file entries from state.\n", count)
}

func getCacheDir(configPath, outputDirOverride string) string {
	// Determine output directory
	var outputDir string
	if outputDirOverride != "" {
		outputDir = outputDirOverride
	} else {
		// Try to load from config
		cfgPath := configPath
		if cfgPath == "" {
			cfgPath, _ = config.DefaultConfigPath()
		}
		if cfgPath != "" {
			cfg, err := config.Load(cfgPath)
			if err == nil && cfg.OutputDirectory != "" {
				outputDir = cfg.OutputDirectory
			}
		}
	}
	
	// Fall back to default
	if outputDir == "" {
		homeDir, _ := os.UserHomeDir()
		outputDir = filepath.Join(homeDir, ".camera-to-immich", "output")
	}
	
	return filepath.Join(outputDir, ".cache")
}

func showCacheInfo(configPath, outputDirOverride string) {
	cacheDir := getCacheDir(configPath, outputDirOverride)
	
	fmt.Println("Cache Information")
	fmt.Println("=================")
	fmt.Printf("Path: %s\n", cacheDir)
	
	// Check if cache directory exists
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		fmt.Println("Cache directory does not exist (no previews generated)")
		return
	}
	
	// Count files and total size
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
	
	if err != nil {
		fmt.Printf("Error reading cache: %v\n", err)
		return
	}
	
	fmt.Printf("Cached files: %d\n", fileCount)
	fmt.Printf("Total size: %.2f MB\n", float64(totalSize)/(1024*1024))
}

func clearCacheFiles(configPath, outputDirOverride string) {
	cacheDir := getCacheDir(configPath, outputDirOverride)
	
	// Check if cache directory exists
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		fmt.Println("Cache directory does not exist (nothing to clear)")
		return
	}
	
	// Count files before deletion
	var fileCount int
	var totalSize int64
	
	filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			fileCount++
			totalSize += info.Size()
		}
		return nil
	})
	
	// Remove the cache directory
	if err := os.RemoveAll(cacheDir); err != nil {
		fmt.Printf("Error clearing cache: %v\n", err)
		return
	}
	
	// Recreate empty directory
	os.MkdirAll(cacheDir, 0755)
	
	fmt.Printf("Cleared %d cached files (%.2f MB freed)\n", fileCount, float64(totalSize)/(1024*1024))
}

func run(cfg *config.Config, verbose bool) error {
	totalStart := time.Now()

	// Step 1: Find the camera drive
	logStep("Searching for drive '%s'...", cfg.DriveLabel)
	driveStart := time.Now()

	driveInfo, err := drive.FindDriveByLabel(cfg.DriveLabel)
	if err != nil {
		return fmt.Errorf("camera drive not found: %v", err)
	}

	logSuccess("Found drive at: %s", driveInfo.Path)
	logTiming("Drive detection", driveStart)

	// Step 2: Load state
	statePath, err := state.DefaultStatePath()
	if err != nil {
		return fmt.Errorf("failed to determine state path: %v", err)
	}

	appState, err := state.Load(statePath)
	if err != nil {
		return fmt.Errorf("failed to load state: %v", err)
	}

	if verbose {
		lastProcessedTime := appState.GetLastProcessedTime()
		if !lastProcessedTime.IsZero() {
			logInfo("Files with mod time > %s will be processed", lastProcessedTime.Format("2006-01-02 15:04:05"))
		}
	}

	// Step 3: Scan for images
	rawExtensions := cfg.GetRawExtensionsMap()
	logStep("Scanning for RAW files (%v) and JPG files...", cfg.RawExtensions)
	scanStart := time.Now()

	scanResult, err := scanner.ScanForImages(driveInfo.Path, rawExtensions)
	if err != nil {
		return fmt.Errorf("failed to scan drive: %v", err)
	}

	logInfo("Found %d RAW files and %d JPG files", len(scanResult.RAWFiles), len(scanResult.JPGFiles))
	logTiming("File scanning", scanStart)

	// Step 4: Initialize Immich uploader (skip if upload is disabled)
	var im *uploader.Immich
	if !cfg.SkipUpload {
		logStep("Initializing Immich uploader...")

		immichConfig := uploader.ImmichConfig{
			ExecutablePath: cfg.ImmichExecutable,
			ServerURL:      cfg.ImmichServerURL,
			APIKey:         cfg.ImmichAPIKey,
			Album:          cfg.ImmichAlbum,
			Tags:           cfg.ImmichTags,
			ShowProgress:   verbose && !cfg.NoUploadUI, // Show upload progress in verbose mode unless no-upload-ui is set
		}

		var err error
		im, err = uploader.NewImmich(immichConfig)
		if err != nil {
			return fmt.Errorf("failed to initialize Immich uploader: %v", err)
		}

		logSuccess("Connected to Immich server")
	} else {
		logInfo("Skipping Immich initialization (--skip-upload flag)")
	}

	// Handle RAW processing mode vs JPG-only mode
	var runErr error
	if cfg.ProcessRAWFiles {
		runErr = runWithRAWProcessing(cfg, appState, scanResult, im, verbose)
	} else {
		runErr = runJPGOnlyMode(cfg, appState, scanResult, im, verbose)
	}

	// Log total execution time
	logTiming("TOTAL TIME", totalStart)

	return runErr
}

// runWithRAWProcessing handles the workflow when RAW processing is enabled
func runWithRAWProcessing(cfg *config.Config, appState *state.State, scanResult *scanner.ScanResult, im *uploader.Immich, verbose bool) error {
	// Filter unprocessed RAW files using EXIF capture time (authoritative).
	// File mtime can be touched by OS/card readers/AV scans, so we always
	// validate candidates against EXIF DateTimeOriginal to avoid re-uploading
	// already-processed photos whose mtime was bumped.
	lastProcessedTime := appState.GetLastProcessedTime()
	preCount := len(scanResult.RAWFiles)
	newRAWFiles := scanner.FilterNewFilesWithCaptureTime(
		scanResult.RAWFiles,
		lastProcessedTime,
		customexif.GetDateTimeOriginalWithFallback,
	)
	if !lastProcessedTime.IsZero() && preCount != len(newRAWFiles) {
		logInfo("Filtered %d already-processed RAW files (by EXIF capture time, high-water mark: %s)",
			preCount-len(newRAWFiles), lastProcessedTime.Format("2006-01-02 15:04:05"))
	}

	if len(newRAWFiles) == 0 {
		logSuccess("No new RAW files to process!")
		return nil
	}

	// Apply limit if specified
	if cfg.Limit > 0 && len(newRAWFiles) > cfg.Limit {
		logInfo("Limiting to %d files (out of %d new files)", cfg.Limit, len(newRAWFiles))
		newRAWFiles = newRAWFiles[:cfg.Limit]
	}

	logInfo("%d new RAW files to process", len(newRAWFiles))

	if cfg.DryRun {
		logInfo("DRY RUN - Would process the following files:")
		for _, f := range newRAWFiles {
			fmt.Printf("  - %s\n", f.Name)
		}
		return nil
	}

	// Initialize DNG converter if enabled (for cameras not natively supported by RawTherapee)
	var dngConverter *processor.DNGConverter
	var dngOutputDir string
	var dngFilesToCleanup []string

	if cfg.ConvertToDNG {
		logStep("Initializing Adobe DNG Converter...")

		// Use temp directory for DNG files if not specified
		dngOutputDir = cfg.DNGOutputDirectory
		if dngOutputDir == "" {
			var err error
			dngOutputDir, err = os.MkdirTemp("", "camera-to-immich-dng-*")
			if err != nil {
				return fmt.Errorf("failed to create temp directory for DNG files: %v", err)
			}
			// Clean up temp directory on exit
			defer os.RemoveAll(dngOutputDir)
		} else {
			// Ensure directory exists
			if err := os.MkdirAll(dngOutputDir, 0755); err != nil {
				return fmt.Errorf("failed to create DNG output directory: %v", err)
			}
		}

		dngConfig := processor.DNGConverterConfig{
			ExecutablePath: cfg.DNGConverterPath,
			OutputDir:      dngOutputDir,
			Compressed:     cfg.DNGCompressed,
			EmbedOriginal:  cfg.DNGEmbedOriginal,
			MaxRetries:     cfg.DNGMaxRetries,
			RetryDelay:     time.Duration(cfg.DNGRetryDelaySeconds) * time.Second,
		}

		var err error
		dngConverter, err = processor.NewDNGConverter(dngConfig)
		if err != nil {
			return fmt.Errorf("failed to initialize DNG Converter: %v", err)
		}

		logSuccess("DNG Converter initialized (output: %s)", dngOutputDir)
	}

	// Initialize RawTherapee processor
	logStep("Initializing RawTherapee processor...")

	rtConfig := processor.RawTherapeeConfig{
		ExecutablePath: cfg.RawTherapeeExecutable,
		ProfilePath:    cfg.PP3ProfilePath,
		OutputDir:      cfg.OutputDirectory,
		Quality:        cfg.JPEGQuality,
	}

	rt, err := processor.NewRawTherapee(rtConfig)
	if err != nil {
		return fmt.Errorf("failed to initialize RawTherapee: %v", err)
	}

	profileName := rt.GetProfileName()
	logSuccess("Using profile: %s", profileName)

	// Initialize Tone Calibration if enabled (for OM-3 cameras)
	var toneCalibration *processor.ToneCalibration
	var basePP3Content string

	// Load base PP3 content if needed for tone calibration or aspect ratio processing
	if cfg.PP3ProfilePath != "" && (cfg.ApplyToneFormula || cfg.ApplyCameraAspectRatio) {
		data, err := os.ReadFile(cfg.PP3ProfilePath)
		if err != nil {
			logInfo("Note: Failed to load base PP3 file: %v (using minimal PP3)", err)
		} else {
			basePP3Content = string(data)
		}
	}

	if cfg.ApplyToneFormula && cfg.ToneFormulaPath != "" {
		logStep("Initializing Tone Calibration with formula...")
		var err error
		toneCalibration, err = processor.NewToneCalibrationForWorkflow(cfg.ToneFormulaPath)
		if err != nil {
			logInfo("Note: Failed to initialize tone calibration: %v (continuing without)", err)
			toneCalibration = nil
		} else {
			if basePP3Content != "" {
				logSuccess("Tone calibration enabled (formula: %s, base: %s)",
					filepath.Base(cfg.ToneFormulaPath), filepath.Base(cfg.PP3ProfilePath))
			} else {
				logSuccess("Tone calibration enabled (formula: %s)",
					filepath.Base(cfg.ToneFormulaPath))
			}
		}
	}

	if cfg.ApplyCameraAspectRatio && basePP3Content != "" {
		logSuccess("Camera aspect ratio processing enabled (base: %s)", filepath.Base(cfg.PP3ProfilePath))
	}
	// Silence unused warnings if tone calibration not used (will be used in processing loop)
	_ = toneCalibration
	_ = basePP3Content

	// Process and upload files
	var processedJPGs []string
	var cameraJPGs []string

	var totalRawProcessingTime time.Duration

	// Determine number of workers for parallel processing
	// Default to 4 workers max to avoid memory issues (RawTherapee uses ~1-2GB per instance)
	// Users can override with --workers flag or config for systems with more RAM
	const defaultMaxWorkers = 4
	numWorkers := cfg.Workers
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
		// Cap at default max to avoid memory exhaustion
		if numWorkers > defaultMaxWorkers {
			numWorkers = defaultMaxWorkers
		}
	}
	// Don't use more workers than files
	if numWorkers > len(newRAWFiles) {
		numWorkers = len(newRAWFiles)
	}

	logInfo("Processing %d files with %d parallel workers...", len(newRAWFiles), numWorkers)
	if cfg.ConvertToDNG {
		logInfo("DNG conversion enabled for camera compatibility")
	}

	// Define result structure for processed files
	type processResult struct {
		index        int
		rawFile      scanner.FileInfo
		outputPath   string
		dngPath      string // Path to intermediate DNG file (if conversion was used)
		elapsed      time.Duration
		err          error
		dngAttempts  int  // Number of DNG conversion attempts (0 if no DNG conversion)
	}

	// Create channels for job distribution and results
	jobs := make(chan struct {
		index   int
		rawFile scanner.FileInfo
	}, len(newRAWFiles))
	results := make(chan processResult, len(newRAWFiles))

	// Start worker goroutines
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for job := range jobs {
				rtStart := time.Now()
				var inputPath string
				var dngPath string
				var err error

				// Convert to DNG first if enabled
				var dngAttempts int
				if dngConverter != nil {
					dngResult := dngConverter.ConvertFileWithRetry(job.rawFile.Path)
					dngAttempts = dngResult.Attempts
					if dngResult.LastError != nil {
						results <- processResult{
							index:       job.index,
							rawFile:     job.rawFile,
							elapsed:     time.Since(rtStart),
							err:         fmt.Errorf("DNG conversion failed: %v", dngResult.LastError),
							dngAttempts: dngAttempts,
						}
						continue
					}
					dngPath = dngResult.OutputPath
					inputPath = dngPath
				} else {
					inputPath = job.rawFile.Path
				}

				// Check for per-image PP3 profile (generated by editor)
				// If exists, use it instead of the default profile
				perImageProfile := rt.GetPerImageProfilePath(job.rawFile.Path)

				var outputPath string
				if perImageProfile != "" {
					// Use per-image profile from editor
					outputPath, err = rt.ProcessFileWithProfile(inputPath, perImageProfile)
				} else if cfg.ApplyCameraAspectRatio {
					// Try to apply camera aspect ratio from EXIF (OM System cameras)
					aspectPP3Path, aspectErr := processor.GeneratePP3WithAspectRatio(job.rawFile.Path, basePP3Content, cfg.OutputDirectory)
					if aspectErr == nil && aspectPP3Path != "" {
						if verbose {
							logInfo("  Using aspect ratio crop PP3: %s", filepath.Base(aspectPP3Path))
						}
						outputPath, err = rt.ProcessFileWithProfile(inputPath, aspectPP3Path)
						// Clean up generated PP3 after processing
						defer os.Remove(aspectPP3Path)
					} else if toneCalibration != nil {
						// No aspect ratio crop, try tone calibration
						tonePP3Path, toneErr := toneCalibration.GenerateTonePP3ForRAW(job.rawFile.Path, basePP3Content, cfg.OutputDirectory)
						if toneErr == nil && tonePP3Path != "" {
							if verbose {
								logInfo("  Using tone-adjusted PP3: %s", filepath.Base(tonePP3Path))
							}
							outputPath, err = rt.ProcessFileWithProfile(inputPath, tonePP3Path)
							defer os.Remove(tonePP3Path)
						} else {
							outputPath, err = rt.ProcessFile(inputPath)
						}
					} else {
						// No aspect ratio crop needed, use default profile
						outputPath, err = rt.ProcessFile(inputPath)
					}
				} else if toneCalibration != nil {
					// Try to generate tone-adjusted profile from sidecar JPEG EXIF
					tonePP3Path, toneErr := toneCalibration.GenerateTonePP3ForRAW(job.rawFile.Path, basePP3Content, cfg.OutputDirectory)
					if toneErr == nil && tonePP3Path != "" {
						if verbose {
							logInfo("  Using tone-adjusted PP3: %s", filepath.Base(tonePP3Path))
						}
						outputPath, err = rt.ProcessFileWithProfile(inputPath, tonePP3Path)
						// Clean up generated PP3 after processing
						defer os.Remove(tonePP3Path)
					} else {
						// No sidecar JPEG or tone extraction failed, use default profile
						if verbose && toneErr != nil {
							logInfo("  Tone extraction note: %v", toneErr)
						}
						outputPath, err = rt.ProcessFile(inputPath)
					}
				} else {
					// Use default profile from config
					outputPath, err = rt.ProcessFile(inputPath)
				}
				rtElapsed := time.Since(rtStart)

				results <- processResult{
					index:       job.index,
					rawFile:     job.rawFile,
					outputPath:  outputPath,
					dngPath:     dngPath,
					elapsed:     rtElapsed,
					err:         err,
					dngAttempts: dngAttempts,
				}
			}
		}(w)
	}

	// Send jobs to workers
	for i, rawFile := range newRAWFiles {
		jobs <- struct {
			index   int
			rawFile scanner.FileInfo
		}{index: i, rawFile: rawFile}
	}
	close(jobs)

	// Wait for all workers to complete in a separate goroutine, then close results
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	processedCount := 0
	for result := range results {
		processedCount++
		totalRawProcessingTime += result.elapsed

		if result.err != nil {
			if result.dngAttempts > 1 {
				logError("[%d/%d] Failed to process %s after %d DNG conversion attempts: %v",
					processedCount, len(newRAWFiles), result.rawFile.Name, result.dngAttempts, result.err)
			} else {
				logError("[%d/%d] Failed to process %s: %v", processedCount, len(newRAWFiles), result.rawFile.Name, result.err)
			}
			continue
		}

		processedJPGs = append(processedJPGs, result.outputPath)

		// Track DNG files for cleanup
		if result.dngPath != "" {
			dngFilesToCleanup = append(dngFilesToCleanup, result.dngPath)
		}

		// Show retry info if DNG conversion required retries
		if result.dngAttempts > 1 {
			logSuccess("[%d/%d] Created: %s (%.1fs, DNG conversion succeeded on attempt %d)",
				processedCount, len(newRAWFiles), filepath.Base(result.outputPath), result.elapsed.Seconds(), result.dngAttempts)
		} else {
			logSuccess("[%d/%d] Created: %s (%.1fs)", processedCount, len(newRAWFiles), filepath.Base(result.outputPath), result.elapsed.Seconds())
		}

		// Find matching camera JPG if enabled
		if cfg.UploadCameraJPGs {
			if matchingJPG := scanner.FindMatchingJPG(result.rawFile, scanResult.JPGFiles); matchingJPG != nil {
				cameraJPGs = append(cameraJPGs, matchingJPG.Path)
				if verbose {
					logInfo("Found matching camera JPG: %s", matchingJPG.Name)
				}
			}
		}

		// Update high-water mark using EXIF capture time (authoritative);
		// FilterNewFilesWithCaptureTime already populated CaptureTime for
		// surviving files. Falls back to ModTime if CaptureTime unavailable.
		fileTime := time.Unix(result.rawFile.CaptureTime, 0)
		if result.rawFile.CaptureTime == 0 {
			fileTime = time.Unix(result.rawFile.ModTime, 0)
		}
		appState.MarkProcessed(fileTime)
	}

	// Log total processing time
	if len(processedJPGs) > 0 {
		if cfg.ConvertToDNG {
			logTiming(fmt.Sprintf("DNG conversion + RawTherapee processing (%d files)", len(processedJPGs)), time.Now().Add(-totalRawProcessingTime))
		} else {
			logTiming(fmt.Sprintf("RawTherapee processing (%d files)", len(processedJPGs)), time.Now().Add(-totalRawProcessingTime))
		}
	}

	// Upload processed JPGs (unless skip-upload is enabled)
	var totalUploadTime time.Duration

	if cfg.SkipUpload {
		logInfo("Upload skipped (--skip-upload flag)")
	} else if len(processedJPGs) > 0 {
		logStep("Uploading %d processed JPGs to Immich (batch upload)...", len(processedJPGs))

		// Build tags for processed files
		var tags []string
		if cfg.TagWithProfileName {
			tags = append(tags, fmt.Sprintf("profile:%s", profileName))
		}
		tags = append(tags, "processed")

		// Create a temp directory with ONLY the newly processed files for faster upload
		tempDir, err := os.MkdirTemp("", "processed-jpgs-*")
		if err != nil {
			logError("Failed to create temp directory for processed JPGs: %v", err)
		} else {
			defer os.RemoveAll(tempDir)

			// Copy only the newly processed JPGs to temp directory
			copyStart := time.Now()
			for _, jpgPath := range processedJPGs {
				destPath := filepath.Join(tempDir, filepath.Base(jpgPath))
				if err := copyFileSimple(jpgPath, destPath); err != nil {
					logError("Failed to copy %s: %v", filepath.Base(jpgPath), err)
				}
			}
			logTiming("Copy processed files to temp", copyStart)

			// Upload the temp directory at once
			uploadStart := time.Now()
			if err := im.UploadFolder(tempDir, tags, false); err != nil {
				logError("Failed to upload processed files: %v", err)
			} else {
				uploadElapsed := time.Since(uploadStart)
				totalUploadTime += uploadElapsed
				logSuccess("Uploaded %d processed JPGs (%.1fs)", len(processedJPGs), uploadElapsed.Seconds())
			}
		}
	}

	// Upload camera JPGs (unless skip-upload is enabled)
	if !cfg.SkipUpload && len(cameraJPGs) > 0 && cfg.UploadCameraJPGs {
		logStep("Uploading %d camera JPGs to Immich (batch upload)...", len(cameraJPGs))

		tags := []string{"camera-original"}

		// Create a temp directory and copy camera JPGs there for batch upload
		tempDir, err := os.MkdirTemp("", "camera-jpgs-*")
		if err != nil {
			logError("Failed to create temp directory for camera JPGs: %v", err)
		} else {
			defer os.RemoveAll(tempDir)

			// Copy camera JPGs to temp directory
			copyStart := time.Now()
			for _, jpgPath := range cameraJPGs {
				destPath := filepath.Join(tempDir, filepath.Base(jpgPath))
				if err := copyFileSimple(jpgPath, destPath); err != nil {
					logError("Failed to copy %s: %v", filepath.Base(jpgPath), err)
				}
			}
			logTiming("Copy camera JPGs to temp", copyStart)

			// Upload the temp directory at once
			uploadStart := time.Now()
			if err := im.UploadFolder(tempDir, tags, false); err != nil {
				logError("Failed to upload camera JPGs: %v", err)
			} else {
				uploadElapsed := time.Since(uploadStart)
				totalUploadTime += uploadElapsed
				logSuccess("Uploaded %d camera JPGs (%.1fs)", len(cameraJPGs), uploadElapsed.Seconds())
			}
		}
	}

	// Cleanup processed files after successful upload (if enabled)
	if cfg.CleanupAfterUpload && !cfg.SkipUpload && len(processedJPGs) > 0 {
		logStep("Cleaning up processed files from output directory...")
		cleanupCount := 0
		for _, jpgPath := range processedJPGs {
			if err := os.Remove(jpgPath); err != nil {
				logError("Failed to delete %s: %v", filepath.Base(jpgPath), err)
			} else {
				cleanupCount++
			}
		}
		logSuccess("Deleted %d processed files", cleanupCount)
	}

	// Cleanup intermediate DNG files (if conversion was used and cleanup is enabled)
	if cfg.ConvertToDNG && cfg.CleanupDNGFiles && len(dngFilesToCleanup) > 0 {
		logStep("Cleaning up intermediate DNG files...")
		dngCleanupCount := 0
		for _, dngPath := range dngFilesToCleanup {
			if err := os.Remove(dngPath); err != nil {
				logError("Failed to delete DNG %s: %v", filepath.Base(dngPath), err)
			} else {
				dngCleanupCount++
			}
		}
		logSuccess("Deleted %d intermediate DNG files", dngCleanupCount)
	}

	// Save state
	if err := appState.Save(); err != nil {
		return fmt.Errorf("failed to save state: %v", err)
	}

	logSuccess("Done! Processed %d files.", len(processedJPGs))

	return nil
}

// runJPGOnlyMode handles the workflow when RAW processing is disabled (JPG upload only)
func runJPGOnlyMode(cfg *config.Config, appState *state.State, scanResult *scanner.ScanResult, im *uploader.Immich, verbose bool) error {
	logInfo("RAW processing disabled - uploading JPG files only")

	// Filter unprocessed JPG files using EXIF capture time (authoritative).
	// File mtime can be touched by OS/card readers/AV scans, so we always
	// validate candidates against EXIF DateTimeOriginal to avoid re-uploading
	// already-processed photos whose mtime was bumped.
	lastProcessedTime := appState.GetLastProcessedTime()
	preCount := len(scanResult.JPGFiles)
	newJPGFiles := scanner.FilterNewFilesWithCaptureTime(
		scanResult.JPGFiles,
		lastProcessedTime,
		customexif.GetDateTimeOriginalWithFallback,
	)
	if !lastProcessedTime.IsZero() && preCount != len(newJPGFiles) {
		logInfo("Filtered %d already-processed JPG files (by EXIF capture time, high-water mark: %s)",
			preCount-len(newJPGFiles), lastProcessedTime.Format("2006-01-02 15:04:05"))
	}

	if len(newJPGFiles) == 0 {
		logSuccess("No new JPG files to upload!")
		return nil
	}

	logInfo("%d new JPG files to upload", len(newJPGFiles))

	if cfg.DryRun {
		logInfo("DRY RUN - Would upload the following files:")
		for _, f := range newJPGFiles {
			fmt.Printf("  - %s\n", f.Name)
		}
		return nil
	}

	// Upload JPG files in a single batch.
	//
	// Previously this loop called im.UploadFile() per file, which spawns
	// immich-go once per file. immich-go has ~2-5s of fixed overhead per
	// invocation (process startup, TLS handshake, auth, album lookup), so
	// uploading N files took O(N) * overhead — minutes for a normal card.
	//
	// Now we stage all new JPGs into one temp directory and call
	// im.UploadFolder() once, matching the fast path used by the RAW
	// processing workflow. immich-go does server-side deduplication by
	// content hash, so if a batch is interrupted and retried the already-
	// uploaded assets are skipped cheaply on the next run.
	logStep("Uploading %d JPG files to Immich (batch upload)...", len(newJPGFiles))

	tags := []string{"camera-original"}

	tempDir, err := os.MkdirTemp("", "jpg-only-upload-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory for JPG upload: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Stage all JPGs into the temp directory.
	copyStart := time.Now()
	stagedFiles := make([]scanner.FileInfo, 0, len(newJPGFiles))
	var latestTime time.Time
	for _, jpgFile := range newJPGFiles {
		destPath := filepath.Join(tempDir, jpgFile.Name)
		if err := copyFileSimple(jpgFile.Path, destPath); err != nil {
			logError("Failed to stage %s: %v", jpgFile.Name, err)
			continue
		}
		stagedFiles = append(stagedFiles, jpgFile)

		// Track latest capture time (EXIF preferred, mtime fallback) so we
		// can update the high-water mark after a successful batch upload.
		fileTime := time.Unix(jpgFile.CaptureTime, 0)
		if jpgFile.CaptureTime == 0 {
			fileTime = time.Unix(jpgFile.ModTime, 0)
		}
		if fileTime.After(latestTime) {
			latestTime = fileTime
		}
	}
	logTiming("Stage JPG files to temp", copyStart)

	if len(stagedFiles) == 0 {
		return fmt.Errorf("no JPG files could be staged for upload")
	}

	// Upload the staged directory in a single immich-go invocation.
	uploadStart := time.Now()
	if err := im.UploadFolder(tempDir, tags, false); err != nil {
		return fmt.Errorf("failed to upload JPG files: %v", err)
	}
	uploadElapsed := time.Since(uploadStart)
	logSuccess("Uploaded %d JPG files (%.1fs)", len(stagedFiles), uploadElapsed.Seconds())

	// Advance the high-water mark only after the batch succeeded.
	if !latestTime.IsZero() {
		appState.MarkProcessed(latestTime)
	}
	if err := appState.Save(); err != nil {
		return fmt.Errorf("failed to save state: %v", err)
	}

	logSuccess("Done! Uploaded %d JPG files.", len(stagedFiles))

	return nil
}

// Logging helpers
func logStep(format string, args ...interface{}) {
	fmt.Printf("\n► "+format+"\n", args...)
}

func logSuccess(format string, args ...interface{}) {
	fmt.Printf("  ✓ "+format+"\n", args...)
}

func logInfo(format string, args ...interface{}) {
	fmt.Printf("  ℹ "+format+"\n", args...)
}

func logError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("  ✗ %s\n", msg)
}

func logTiming(label string, start time.Time) {
	elapsed := time.Since(start)
	fmt.Printf("  ⏱ %s: %.2fs\n", label, elapsed.Seconds())
}

// copyFileSimple copies a file from src to dst
func copyFileSimple(src, dst string) error {
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

// runRetryUpload uploads existing processed files from the output directory without reprocessing
func runRetryUpload(configPath, outputDirOverride string, verbose bool) {
	fmt.Println("🔄 Camera-to-Immich Retry Upload")
	fmt.Println("================================")

	// Determine config path
	cfgPath := configPath
	if cfgPath == "" {
		var err error
		cfgPath, err = config.DefaultConfigPath()
		if err != nil {
			log.Fatalf("Failed to determine config path: %v", err)
		}
	}

	// Load configuration
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Apply output directory override
	if outputDirOverride != "" {
		cfg.OutputDirectory = outputDirOverride
	}

	// Validate we have what we need
	if cfg.ImmichServerURL == "" || cfg.ImmichAPIKey == "" {
		log.Fatalf("Immich server URL and API key are required for upload")
	}

	// Ensure output directory exists
	outputDir := cfg.OutputDirectory
	if outputDir == "" {
		homeDir, _ := os.UserHomeDir()
		outputDir = filepath.Join(homeDir, ".camera-to-immich", "output")
	}

	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		log.Fatalf("Output directory does not exist: %s", outputDir)
	}

	// Find all JPG files in output directory
	var jpgFiles []string
	err = filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".jpg" || ext == ".jpeg" {
			jpgFiles = append(jpgFiles, path)
		}
		return nil
	})
	if err != nil {
		log.Fatalf("Failed to scan output directory: %v", err)
	}

	if len(jpgFiles) == 0 {
		fmt.Println("No JPG files found in output directory to upload.")
		fmt.Printf("Output directory: %s\n", outputDir)
		return
	}

	fmt.Printf("Found %d JPG files to upload from: %s\n", len(jpgFiles), outputDir)

	// Initialize Immich uploader
	immichConfig := uploader.ImmichConfig{
		ServerURL:    cfg.ImmichServerURL,
		APIKey:       cfg.ImmichAPIKey,
		Album:        cfg.ImmichAlbum,
		Tags:         cfg.ImmichTags,
		ShowProgress: !cfg.NoUploadUI,
		MaxRetries:   3,
		RetryDelay:   5 * time.Second,
	}

	im, err := uploader.NewImmich(immichConfig)
	if err != nil {
		log.Fatalf("Failed to initialize Immich uploader: %v", err)
	}

	// Upload the output directory (batch upload)
	fmt.Println("\nUploading files to Immich...")
	tags := []string{"processed", "retry-upload"}

	if err := im.UploadFolder(outputDir, tags, false); err != nil {
		log.Fatalf("Upload failed: %v", err)
	}

	fmt.Printf("\n✓ Successfully uploaded %d files from %s\n", len(jpgFiles), outputDir)

	// Cleanup files after successful upload (if enabled in config)
	if cfg.CleanupAfterUpload {
		fmt.Println("\nCleaning up uploaded files from output directory...")
		cleanupCount := 0
		for _, jpgPath := range jpgFiles {
			if err := os.Remove(jpgPath); err != nil {
				fmt.Printf("  ✗ Failed to delete %s: %v\n", filepath.Base(jpgPath), err)
			} else {
				cleanupCount++
			}
			// Also clean up any associated PP3 files
			pp3Path := strings.TrimSuffix(jpgPath, filepath.Ext(jpgPath)) + ".pp3"
			if _, err := os.Stat(pp3Path); err == nil {
				os.Remove(pp3Path)
			}
		}
		fmt.Printf("  ✓ Deleted %d files\n", cleanupCount)
	} else {
		fmt.Println("\nNote: Files remain in output directory. Use --keep-files=false to auto-cleanup.")
	}
}

// getProfileTag returns a sanitized tag from the profile name
func getProfileTag(profilePath string) string {
	name := filepath.Base(profilePath)
	name = strings.TrimSuffix(name, ".pp3")
	name = strings.TrimSuffix(name, ".PP3")
	// Replace spaces and special characters
	name = strings.ReplaceAll(name, " ", "-")
	return "profile:" + name
}

// runEditor launches the web-based image editor
func runEditor(configPath, driveLabel, outputDir, profilePath, port string, limit int, skipUpload bool, filmMode bool, sourcePath string) {
	if filmMode {
		fmt.Println("🎞️  Camera-to-Immich Film Scan Editor")
		fmt.Println("======================================")
	} else {
		fmt.Println("🖼  Camera-to-Immich Web Editor")
		fmt.Println("================================")
	}

	// Load config for defaults
	cfgPath := configPath
	if cfgPath == "" {
		var err error
		cfgPath, err = config.DefaultConfigPath()
		if err != nil {
			log.Printf("Warning: Failed to determine config path: %v", err)
		}
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("Warning: Failed to load config, using defaults: %v", err)
		cfg = config.DefaultConfig()
	}

	// Apply overrides
	if driveLabel != "" {
		cfg.DriveLabel = driveLabel
	}
	if outputDir != "" {
		cfg.OutputDirectory = outputDir
	}
	if profilePath != "" {
		cfg.PP3ProfilePath = profilePath
	}
	if skipUpload {
		cfg.SkipUpload = true
	}

	// Determine source path
	var imageSourcePath string

	if filmMode && sourcePath != "" {
		// Film scan mode with explicit source path — bypass drive detection
		// Validate source path exists
		if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
			log.Fatalf("Source path does not exist: %s", sourcePath)
		}
		imageSourcePath = sourcePath
		fmt.Printf("✓ Using source folder: %s\n", imageSourcePath)
	} else if sourcePath != "" {
		// Source path provided without film mode — use it directly
		if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
			log.Fatalf("Source path does not exist: %s", sourcePath)
		}
		imageSourcePath = sourcePath
		fmt.Printf("✓ Using source folder: %s\n", imageSourcePath)
	} else {
		// Standard mode — find camera drive
		// Check required external tools (only for standard mode, film mode doesn't need RawTherapee)
		if !filmMode {
			if err := checkRequiredTools(cfg); err != nil {
				log.Fatalf("Missing tools: %v\nSee README.md for installation instructions.", err)
			}
		}

		fmt.Printf("Searching for drive '%s'...\n", cfg.DriveLabel)
		driveInfo, err := drive.FindDriveByLabel(cfg.DriveLabel)
		if err != nil {
			log.Fatalf("Camera drive not found: %v\nUse --drive to specify a different drive label, or --list-drives to see available drives", err)
		}
		fmt.Printf("✓ Found drive at: %s\n", driveInfo.Path)
		imageSourcePath = driveInfo.Path
	}

	// In film mode, only check for exiftool (needed for date stamping)
	if filmMode {
		if _, err := exec.LookPath("exiftool"); err != nil {
			log.Fatalf("exiftool is required for film scan mode.\nDownload from https://exiftool.org/")
		}
	}

	// Ensure output directory
	if cfg.OutputDirectory == "" {
		homeDir, _ := os.UserHomeDir()
		cfg.OutputDirectory = filepath.Join(homeDir, ".camera-to-immich", "output")
	}
	os.MkdirAll(cfg.OutputDirectory, 0755)

	// Get state path
	statePath, err := state.DefaultStatePath()
	if err != nil {
		log.Printf("Warning: Failed to determine state path: %v", err)
		statePath = ""
	}

	// Create editor server
	serverConfig := editor.ServerConfig{
		SourcePath:    imageSourcePath,
		OutputDir:     cfg.OutputDirectory,
		ProfilePath:   cfg.PP3ProfilePath,
		BWProfilePath: cfg.PP3BWProfilePath,
		RawExtensions: cfg.GetRawExtensionsMap(),
		EditsPath:     filepath.Join(cfg.OutputDirectory, "edits.json"),
		Limit:         limit,
		FilmScanMode:  filmMode,
		ProcessConfig: editor.ProcessConfig{
			ConvertToDNG:       cfg.ConvertToDNG,
			DNGConverterPath:   cfg.DNGConverterPath,
			RawTherapeeExe:     cfg.RawTherapeeExecutable,
			JPEGQuality:        cfg.JPEGQuality,
			SkipUpload:         skipUpload || cfg.SkipUpload,
			Workers:            cfg.Workers,
			UploadCameraJPGs:   cfg.UploadCameraJPGs,
			PP3BWProfilePath:   cfg.PP3BWProfilePath,
			ToneFormulaPath:    cfg.ToneFormulaPath,
			ApplyToneFormula:   cfg.ApplyToneFormula,
			ImmichServerURL:    cfg.ImmichServerURL,
			ImmichAPIKey:       cfg.ImmichAPIKey,
			ImmichAlbum:        cfg.ImmichAlbum,
			ImmichTags:         cfg.ImmichTags,
			FilmScanTags:       cfg.FilmScanTags,
			TagWithProfileName: cfg.TagWithProfileName,
			NoUploadUI:         cfg.NoUploadUI,
			CleanupAfterUpload: cfg.CleanupAfterUpload,
			StatePath:          statePath,
			FilmScanMode:       filmMode,
		},
	}

	srv, err := editor.NewServer(serverConfig)
	if err != nil {
		log.Fatalf("Failed to create editor server: %v", err)
	}

	fmt.Printf("✓ Found %d images\n", srv.GetImageCount())
	fmt.Println()
	fmt.Printf("🌐 Starting editor at http://localhost:%s\n", port)
	fmt.Println("   Press Ctrl+C to stop")
	fmt.Println()

	// Start server
	addr := ":" + port
	if err := srv.Start(addr); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}


// checkRequiredTools verifies that all required external tools are available
func checkRequiredTools(cfg *config.Config) error {
	var missing []string
	var warnings []string

	// Check exiftool (required for reading EXIF from RAW files)
	if _, err := exec.LookPath("exiftool"); err != nil {
		missing = append(missing, "exiftool - Download from https://exiftool.org/")
	}

	// Check RawTherapee (required for RAW processing)
	if cfg.ProcessRAWFiles {
		rtPath := cfg.RawTherapeeExecutable
		if rtPath == "" {
			// Try to find it
			names := []string{"rawtherapee-cli", "rawtherapee-cli.exe"}
			found := false
			for _, name := range names {
				if _, err := exec.LookPath(name); err == nil {
					found = true
					break
				}
			}
			if !found {
				// Check common installation paths
				commonPaths := []string{
					"C:\\Program Files\\RawTherapee\\rawtherapee-cli.exe",
					"/Applications/RawTherapee.app/Contents/MacOS/rawtherapee-cli",
					"/usr/bin/rawtherapee-cli",
				}
				for _, p := range commonPaths {
					if _, err := os.Stat(p); err == nil {
						found = true
						break
					}
				}
			}
			if !found {
				missing = append(missing, "rawtherapee-cli - Download from https://rawtherapee.com/")
			}
		} else if _, err := os.Stat(rtPath); err != nil {
			missing = append(missing, fmt.Sprintf("rawtherapee-cli (configured path not found: %s)", rtPath))
		}
	}

	// Check Adobe DNG Converter (required only if convert_to_dng is enabled)
	if cfg.ConvertToDNG {
		dngPath := cfg.DNGConverterPath
		if dngPath == "" {
			// Try common paths
			commonPaths := []string{
				"C:\\Program Files\\Adobe\\Adobe DNG Converter\\Adobe DNG Converter.exe",
				"/Applications/Adobe DNG Converter.app/Contents/MacOS/Adobe DNG Converter",
			}
			found := false
			for _, p := range commonPaths {
				if _, err := os.Stat(p); err == nil {
					found = true
					break
				}
			}
			if !found {
				missing = append(missing, "Adobe DNG Converter - Download from https://www.adobe.com/support/downloads/dng/dng_converter.html")
			}
		} else if _, err := os.Stat(dngPath); err != nil {
			missing = append(missing, fmt.Sprintf("Adobe DNG Converter (configured path not found: %s)", dngPath))
		}
	}

	// Check immich-go (required for uploading, unless skip-upload is set)
	if !cfg.SkipUpload {
		imPath := cfg.ImmichExecutable
		if imPath == "" {
			names := []string{"immich-go", "immich-go.exe"}
			found := false
			for _, name := range names {
				if _, err := exec.LookPath(name); err == nil {
					found = true
					break
				}
			}
			if !found {
				warnings = append(warnings, "immich-go not found - Install with: go install github.com/simulot/immich-go@latest")
			}
		} else if _, err := os.Stat(imPath); err != nil {
			warnings = append(warnings, fmt.Sprintf("immich-go (configured path not found: %s)", imPath))
		}
	}

	// Report results
	if len(warnings) > 0 {
		fmt.Println("\n⚠️  Warnings:")
		for _, w := range warnings {
			fmt.Printf("   • %s\n", w)
		}
	}

	if len(missing) > 0 {
		fmt.Println("\n❌ Missing required tools:")
		for _, m := range missing {
			fmt.Printf("   • %s\n", m)
		}
		return fmt.Errorf("missing %d required tool(s)", len(missing))
	}

	return nil
}
