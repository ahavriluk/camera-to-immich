package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ohavrylyuk/camera-to-immich/internal/processor"
)

func main() {
	// Command line flags
	sourceDir := flag.String("source", "", "Source directory containing RAW+JPEG pairs (required)")
	outputDB := flag.String("output", "calibration.db", "Output calibration database file")
	maxIterations := flag.Int("iterations", 5, "Maximum calibration iterations per image")
	limit := flag.Int("limit", 0, "Limit number of pairs to process (0 = all)")
	startFrom := flag.String("start", "", "Start processing from file containing this substring (e.g., 'P1193660')")
	verbose := flag.Bool("verbose", false, "Verbose output")
	testMode := flag.Bool("test", false, "Test mode: extract and display tone settings only")
	singleJPEG := flag.String("single", "", "Single JPEG file to extract tone settings from")
	useDNG := flag.Bool("dng", false, "Use ORF → DNG → JPG workflow (requires Adobe DNG Converter)")
	applyFormula := flag.String("apply", "", "Apply formula to RAW file: -apply <raw_path> -formula <formula.json> produces JPG output")
	formulaFile := flag.String("formula", "tone_formula.json", "Formula file to use with -apply")
	outputJPG := flag.String("out", "", "Output JPG path for -apply mode (default: same directory as input)")
	basePP3 := flag.String("base-pp3", "", "Base PP3 profile to use as template (ToneEqualizer settings will be overlaid)")

	flag.Parse()

	// Handle apply formula mode - convert a single RAW using the formula
	if *applyFormula != "" {
		if err := applyFormulaToRAW(*applyFormula, *formulaFile, *outputJPG, *useDNG, *basePP3); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Handle single file test mode
	if *singleJPEG != "" {
		if err := testSingleFile(*singleJPEG); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Validate arguments
	if *sourceDir == "" {
		fmt.Fprintln(os.Stderr, "Error: -source is required")
		flag.Usage()
		os.Exit(1)
	}

	// Check source directory exists
	if _, err := os.Stat(*sourceDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: source directory does not exist: %s\n", *sourceDir)
		os.Exit(1)
	}

	fmt.Println("OM-3 Tone Calibration Tool")
	fmt.Println("==========================")
	fmt.Printf("Source: %s\n", *sourceDir)
	fmt.Printf("Output: %s\n", *outputDB)
	fmt.Printf("Max iterations: %d\n", *maxIterations)
	fmt.Println()

	// Find RAW+JPEG pairs
	fmt.Println("Scanning for RAW+JPEG pairs...")
	pairs, err := processor.FindRAWJPEGPairs(*sourceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning directory: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Found %d RAW+JPEG pairs\n", len(pairs))

	if len(pairs) == 0 {
		fmt.Println("No pairs found. Exiting.")
		os.Exit(0)
	}

	// Filter pairs starting from a specific file if -start is specified
	if *startFrom != "" {
		startIdx := -1
		for i, pair := range pairs {
			if strings.Contains(filepath.Base(pair.RAW), *startFrom) {
				startIdx = i
				break
			}
		}
		if startIdx == -1 {
			fmt.Fprintf(os.Stderr, "Warning: could not find file containing '%s', starting from beginning\n", *startFrom)
		} else {
			pairs = pairs[startIdx:]
			fmt.Printf("Starting from file containing '%s' (%d pairs remaining)\n", *startFrom, len(pairs))
		}
	}

	// Apply limit if set
	if *limit > 0 && len(pairs) > *limit {
		pairs = pairs[:*limit]
		fmt.Printf("Processing limited to %d pairs\n", *limit)
	}

	// Test mode: just extract and display settings
	if *testMode {
		if err := testModeRun(pairs, *verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Create RawTherapee processor
	tempDir, err := os.MkdirTemp("", "tone_calibrator")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating temp directory: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempDir)

	rt, err := processor.NewRawTherapee(processor.RawTherapeeConfig{
		OutputDir: tempDir,
		Quality:   92,
		Timeout:   5 * time.Minute,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing RawTherapee: %v\n", err)
		os.Exit(1)
	}

	// Create calibration instance (with or without DNG support)
	var tc *processor.ToneCalibration
	if *useDNG {
		fmt.Println("Using ORF → DNG → JPG workflow")
		tc, err = processor.NewToneCalibrationWithDNG(rt, tempDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error initializing ToneCalibration with DNG support: %v\n", err)
			fmt.Fprintln(os.Stderr, "Note: Adobe DNG Converter must be installed for -dng mode")
			os.Exit(1)
		}
	} else {
		fmt.Println("Using direct ORF → JPG workflow")
		tc, err = processor.NewToneCalibration(rt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error initializing ToneCalibration: %v\n", err)
			os.Exit(1)
		}
	}

	// Run calibration
	fmt.Println("\nStarting calibration...")
	startTime := time.Now()

	results, err := tc.BatchCalibrate(pairs, *maxIterations)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error during calibration: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(startTime)
	fmt.Printf("\nCalibration completed in %v\n", elapsed)
	fmt.Printf("Processed %d pairs successfully\n", len(results))

	// Display results
	if *verbose {
		fmt.Println("\nCalibration Results:")
		fmt.Println("====================")
		for _, r := range results {
			fmt.Printf("\nFile: %s\n", filepath.Base(r.RAWPath))
			fmt.Printf("  OM-3 Settings: H=%d, S=%d, M=%d, C=%d\n",
				r.OM3Settings.Highlights, r.OM3Settings.Shadows,
				r.OM3Settings.Midtones, r.OM3Settings.Contrast)
			fmt.Printf("  ToneEqualizer: B0=%d, B1=%d, B2=%d, B3=%d, B4=%d\n",
				r.TESettings.Band0, r.TESettings.Band1, r.TESettings.Band2,
				r.TESettings.Band3, r.TESettings.Band4)
			fmt.Printf("  Similarity Score: %.2f\n", r.SimilarityScore)
		}
	}

	// Calculate formula from calibration samples
	if err := tc.CalculateFormulaFromSamples(); err != nil {
		fmt.Fprintf(os.Stderr, "Error calculating formula: %v\n", err)
		os.Exit(1)
	}

	// Get the calculated formula and save to JSON file
	formula := tc.GetFormula()
	if err := formula.SaveFormula(*outputDB); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving formula: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nTone mapping formula saved to: %s\n", *outputDB)
	fmt.Printf("Formula coefficients (Band[i] = C0 + C1*H + C2*S + C3*M + C4*C):\n")
	for i := 0; i < 5; i++ {
		fmt.Printf("  Band%d: %.2f + %.2f*H + %.2f*S + %.2f*M + %.2f*C\n",
			i,
			formula.Coefficients[i][0],
			formula.Coefficients[i][1],
			formula.Coefficients[i][2],
			formula.Coefficients[i][3],
			formula.Coefficients[i][4])
	}

	fmt.Printf("\nFormula Accuracy Metrics:\n")
	fmt.Printf("  Band |   R²   | RMSE  | Mean Error\n")
	fmt.Printf("  -----|--------|-------|----------\n")
	bandNames := []string{"Black", "Shadow", "Mid", "Hi", "White"}
	for i := 0; i < 5; i++ {
		fmt.Printf("  %d %s | %.3f  | %.1f  | %+.1f\n",
			i, bandNames[i],
			formula.RSquared[i],
			formula.RMSE[i],
			formula.MeanError[i])
	}

	// Calculate overall accuracy
	avgR2 := 0.0
	avgRMSE := 0.0
	for i := 0; i < 5; i++ {
		avgR2 += formula.RSquared[i]
		avgRMSE += formula.RMSE[i]
	}
	avgR2 /= 5
	avgRMSE /= 5

	fmt.Printf("  -----|--------|-------|----------\n")
	fmt.Printf("  Avg  | %.3f  | %.1f  |\n", avgR2, avgRMSE)

	fmt.Printf("\nInterpretation:\n")
	fmt.Printf("  R² > 0.9 = Excellent fit, 0.7-0.9 = Good, < 0.7 = May need more samples\n")
	fmt.Printf("  RMSE < 5 = Very accurate, 5-10 = Good, > 10 = May need more samples\n")

	fmt.Printf("\nFormula can now be used to instantly map OM-3 settings to ToneEqualizer parameters\n")
}

func testSingleFile(jpegPath string) error {
	// For single file test, we can use ToneCalibration without RawTherapee
	tc, err := processor.NewToneCalibrationReadOnly()
	if err != nil {
		return fmt.Errorf("failed to initialize ToneCalibration: %v", err)
	}

	settings, err := tc.ExtractToneSettings(jpegPath)
	if err != nil {
		return fmt.Errorf("failed to extract settings: %v", err)
	}

	fmt.Printf("Tone Settings for: %s\n", filepath.Base(jpegPath))
	fmt.Printf("  Highlights: %d (range: -7 to +7)\n", settings.Highlights)
	fmt.Printf("  Shadows:    %d (range: -7 to +7)\n", settings.Shadows)
	fmt.Printf("  Midtones:   %d (range: -7 to +7)\n", settings.Midtones)
	fmt.Printf("  Contrast:   %d (range: -2 to +2)\n", settings.Contrast)
	fmt.Printf("  Profile:    %s\n", settings.Profile)
	fmt.Printf("  Hash:       %s\n", settings.SettingsHash())

	// Calculate and display initial mapping
	initialTE := tc.CalculateInitialMapping(settings)
	fmt.Println("\nInitial ToneEqualizer Mapping:")
	fmt.Printf("  Band0 (Blacks):     %d\n", initialTE.Band0)
	fmt.Printf("  Band1 (Shadows):    %d\n", initialTE.Band1)
	fmt.Printf("  Band2 (Midtones):   %d\n", initialTE.Band2)
	fmt.Printf("  Band3 (Highlights): %d\n", initialTE.Band3)
	fmt.Printf("  Band4 (Whites):     %d\n", initialTE.Band4)

	return nil
}

func testModeRun(pairs []struct{ RAW, JPEG string }, verbose bool) error {
	// Create a read-only calibration instance (no RawTherapee needed)
	tc, err := processor.NewToneCalibrationReadOnly()
	if err != nil {
		return fmt.Errorf("failed to initialize ToneCalibration: %v", err)
	}

	// Collect unique settings combinations
	settingsMap := make(map[string]struct {
		settings *processor.OM3ToneSettings
		count    int
		files    []string
	})

	fmt.Println("\nExtracting tone settings from JPEG files...")

	for _, pair := range pairs {
		settings, err := tc.ExtractToneSettings(pair.JPEG)
		if err != nil {
			if verbose {
				fmt.Printf("Warning: failed to extract from %s: %v\n", filepath.Base(pair.JPEG), err)
			}
			continue
		}

		hash := settings.SettingsHash()
		entry := settingsMap[hash]
		if entry.settings == nil {
			entry.settings = settings
		}
		entry.count++
		if verbose {
			entry.files = append(entry.files, filepath.Base(pair.JPEG))
		}
		settingsMap[hash] = entry
	}

	// Display summary
	fmt.Printf("\nFound %d unique tone settings combinations:\n", len(settingsMap))
	fmt.Println("===========================================")

	for hash, entry := range settingsMap {
		fmt.Printf("\n%s (%d images)\n", hash, entry.count)
		fmt.Printf("  Highlights: %d, Shadows: %d, Midtones: %d, Contrast: %d\n",
			entry.settings.Highlights, entry.settings.Shadows,
			entry.settings.Midtones, entry.settings.Contrast)
		fmt.Printf("  Profile: %s\n", entry.settings.Profile)

		// Show initial mapping
		initialTE := tc.CalculateInitialMapping(entry.settings)
		fmt.Printf("  Initial TE: B0=%d, B1=%d, B2=%d, B3=%d, B4=%d\n",
			initialTE.Band0, initialTE.Band1, initialTE.Band2,
			initialTE.Band3, initialTE.Band4)

		if verbose && len(entry.files) > 0 {
			fmt.Printf("  Files: %v\n", entry.files[:min(5, len(entry.files))])
		}
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// applyFormulaToRAW processes a RAW file using the formula and outputs a JPG
func applyFormulaToRAW(rawPath, formulaPath, outputPath string, useDNG bool, basePP3Path string) error {
	// Load the formula
	formula, err := processor.LoadFormula(formulaPath)
	if err != nil {
		return fmt.Errorf("failed to load formula: %v", err)
	}
	fmt.Printf("Loaded formula from %s (trained on %d samples)\n", formulaPath, formula.SampleCount)

	// Find the corresponding JPEG sidecar to extract OM-3 settings
	rawDir := filepath.Dir(rawPath)
	baseName := strings.TrimSuffix(filepath.Base(rawPath), filepath.Ext(rawPath))
	jpegPath := filepath.Join(rawDir, baseName+".JPG")
	if _, err := os.Stat(jpegPath); os.IsNotExist(err) {
		jpegPath = filepath.Join(rawDir, baseName+".jpg")
		if _, err := os.Stat(jpegPath); os.IsNotExist(err) {
			return fmt.Errorf("no sidecar JPEG found for %s", rawPath)
		}
	}

	// Create temp directory for outputs
	tempDir, err := os.MkdirTemp("", "tone_apply")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Initialize RawTherapee
	rt, err := processor.NewRawTherapee(processor.RawTherapeeConfig{
		OutputDir: tempDir,
		Quality:   92,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize RawTherapee: %v", err)
	}

	// Create ToneCalibration instance with DNG support if needed
	var tc *processor.ToneCalibration
	if useDNG {
		tc, err = processor.NewToneCalibrationWithDNG(rt, tempDir)
	} else {
		tc, err = processor.NewToneCalibration(rt)
	}
	if err != nil {
		return fmt.Errorf("failed to initialize ToneCalibration: %v", err)
	}
	tc.SetFormula(formula)

	// Extract OM-3 settings from JPEG
	om3Settings, err := tc.ExtractToneSettings(jpegPath)
	if err != nil {
		return fmt.Errorf("failed to extract tone settings: %v", err)
	}

	fmt.Printf("\nOM-3 Settings from %s:\n", filepath.Base(jpegPath))
	fmt.Printf("  Highlights: %d, Shadows: %d, Midtones: %d, Contrast: %d\n",
		om3Settings.Highlights, om3Settings.Shadows, om3Settings.Midtones, om3Settings.Contrast)
	fmt.Printf("  Profile: %s\n", om3Settings.Profile)

	// Apply formula to get ToneEqualizer settings
	teSettings := formula.ApplyFormula(om3Settings)
	fmt.Printf("\nCalculated ToneEqualizer settings:\n")
	fmt.Printf("  Band0 (Blacks):     %d\n", teSettings.Band0)
	fmt.Printf("  Band1 (Shadows):    %d\n", teSettings.Band1)
	fmt.Printf("  Band2 (Midtones):   %d\n", teSettings.Band2)
	fmt.Printf("  Band3 (Highlights): %d\n", teSettings.Band3)
	fmt.Printf("  Band4 (Whites):     %d\n", teSettings.Band4)

	// Determine output path
	if outputPath == "" {
		outputPath = filepath.Join(rawDir, baseName+"_formula.jpg")
	}

	// Convert ORF to DNG if needed
	inputPath := rawPath
	if useDNG && strings.EqualFold(filepath.Ext(rawPath), ".orf") {
		fmt.Printf("\nConverting ORF to DNG...\n")
		dngConverter, err := processor.NewDNGConverter(processor.DNGConverterConfig{
			OutputDir:  tempDir,
			Compressed: false,
		})
		if err != nil {
			return fmt.Errorf("failed to initialize DNG converter: %v", err)
		}
		dngPath, err := dngConverter.ConvertFile(rawPath)
		if err != nil {
			return fmt.Errorf("DNG conversion failed: %v", err)
		}
		inputPath = dngPath
		fmt.Printf("  DNG created: %s\n", dngPath)
	}

	// Load base PP3 if provided, otherwise use minimal PP3
	var basePP3Content string
	if basePP3Path != "" {
		data, err := os.ReadFile(basePP3Path)
		if err != nil {
			return fmt.Errorf("failed to read base PP3 file: %v", err)
		}
		basePP3Content = string(data)
		fmt.Printf("\nUsing base PP3: %s\n", basePP3Path)
	}

	// Generate PP3 file with ToneEqualizer settings overlaid on base
	pp3Content := tc.GeneratePP3WithToneEqualizer(teSettings, basePP3Content)
	pp3Path := inputPath + ".pp3"
	if err := tc.WritePP3File(pp3Content, pp3Path); err != nil {
		return fmt.Errorf("failed to write PP3 file: %v", err)
	}
	fmt.Printf("PP3 created: %s\n", pp3Path)

	// Process with RawTherapee using the generated PP3 file
	fmt.Printf("\nProcessing with RawTherapee using PP3 profile...\n")
	processedPath, err := rt.ProcessFileWithProfile(inputPath, pp3Path)
	if err != nil {
		return fmt.Errorf("RawTherapee processing failed: %v", err)
	}

	// Move processed file to output path
	if err := os.Rename(processedPath, outputPath); err != nil {
		// If rename fails (cross-device), try copy
		data, err := os.ReadFile(processedPath)
		if err != nil {
			return fmt.Errorf("failed to read processed file: %v", err)
		}
		if err := os.WriteFile(outputPath, data, 0644); err != nil {
			return fmt.Errorf("failed to write output file: %v", err)
		}
	}

	fmt.Printf("\nOutput saved to: %s\n", outputPath)
	fmt.Printf("\nYou can now compare:\n")
	fmt.Printf("  Camera JPEG: %s\n", jpegPath)
	fmt.Printf("  Formula JPG: %s\n", outputPath)

	return nil
}