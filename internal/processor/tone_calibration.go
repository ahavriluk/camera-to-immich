package processor

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// OM3ToneSettings represents the tone settings extracted from OM-3 camera JPEG EXIF
type OM3ToneSettings struct {
	Highlights int // -7 to +7
	Shadows    int // -7 to +7
	Midtones   int // -7 to +7
	Contrast   int // -2 to +2 (PictureModeContrast)
	Profile    string
}

// ToneEqualizerSettings represents RawTherapee's Tone Equalizer settings
type ToneEqualizerSettings struct {
	Enabled       bool
	Band0         int // Blacks (-100 to +100)
	Band1         int // Shadows (-100 to +100)
	Band2         int // Midtones (-100 to +100)
	Band3         int // Highlights (-100 to +100)
	Band4         int // Whites (-100 to +100)
	Band5         int // (not displayed in UI)
	Regularization int
	Pivot         int
}

// LuminanceHistogram represents the tonal distribution of an image
type LuminanceHistogram struct {
	Bins       [256]int // Count of pixels at each luminance level (0-255)
	TotalPixels int
	Percentiles map[int]int // Key: percentile (1,5,10,25,50,75,90,95,99), Value: luminance level
}

// ToneMappingFormula holds the coefficients for the linear formula
// Band[i] = Coefficients[i][0] + Coefficients[i][1]*H + Coefficients[i][2]*S + Coefficients[i][3]*M + Coefficients[i][4]*C
type ToneMappingFormula struct {
	Version      string        `json:"version"`
	CameraModel  string        `json:"camera_model"`
	Coefficients [5][5]float64 `json:"coefficients"`  // [Band0-4][Constant, H, S, M, C]
	SampleCount  int           `json:"sample_count"`  // Number of images used for calibration
	RSquared     [5]float64    `json:"r_squared"`     // R² (coefficient of determination) for each band
	RMSE         [5]float64    `json:"rmse"`          // Root Mean Square Error for each band
	MeanError    [5]float64    `json:"mean_error"`    // Mean prediction error for each band
}

// DefaultToneMappingFormula returns the initial formula (before calibration)
func DefaultToneMappingFormula() *ToneMappingFormula {
	return &ToneMappingFormula{
		Version:     "1.0",
		CameraModel: "OM System OM-3",
		Coefficients: [5][5]float64{
			// Band0 (Blacks): follows shadows
			{0, 0, 3.5, 0, -5},
			// Band1 (Shadows): primarily controlled by OM-3 Shadows
			{0, 0, 7.0, 0, -10},
			// Band2 (Midtones): primarily controlled by OM-3 Midtones
			{0, 0, 0, 7.0, 0},
			// Band3 (Highlights): primarily controlled by OM-3 Highlights
			{0, 7.0, 0, 0, 10},
			// Band4 (Whites): follows highlights
			{0, 3.5, 0, 0, 5},
		},
		SampleCount: 0,
	}
}

// ToneCalibration handles the calibration process between camera settings and RawTherapee
type ToneCalibration struct {
	exiftoolPath    string
	rawtherapee     *RawTherapee
	dngConverter    *DNGConverter
	formula         *ToneMappingFormula
	calibrationData []CalibrationSample // Used during calibration
}

// CalibrationSample holds data from one calibration image
type CalibrationSample struct {
	OM3Settings *OM3ToneSettings
	TESettings  *ToneEqualizerSettings
	Score       float64
}

// NewToneCalibration creates a new tone calibration instance
func NewToneCalibration(rt *RawTherapee) (*ToneCalibration, error) {
	// Find exiftool
	exiftoolPath := findExifTool()
	if exiftoolPath == "" {
		return nil, fmt.Errorf("exiftool not found")
	}

	return &ToneCalibration{
		exiftoolPath:    exiftoolPath,
		rawtherapee:     rt,
		dngConverter:    nil,
		formula:         DefaultToneMappingFormula(),
		calibrationData: make([]CalibrationSample, 0),
	}, nil
}

// NewToneCalibrationWithDNG creates a tone calibration instance with DNG conversion support
func NewToneCalibrationWithDNG(rt *RawTherapee, dngOutputDir string) (*ToneCalibration, error) {
	tc, err := NewToneCalibration(rt)
	if err != nil {
		return nil, err
	}

	// Initialize DNG converter
	dngConverter, err := NewDNGConverter(DNGConverterConfig{
		OutputDir:  dngOutputDir,
		Compressed: false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize DNG converter: %v", err)
	}
	tc.dngConverter = dngConverter

	return tc, nil
}

// NewToneCalibrationReadOnly creates a tone calibration instance that only needs exiftool
// (used for extracting settings without processing RAW files)
func NewToneCalibrationReadOnly() (*ToneCalibration, error) {
	// Find exiftool
	exiftoolPath := findExifTool()
	if exiftoolPath == "" {
		return nil, fmt.Errorf("exiftool not found")
	}

	return &ToneCalibration{
		exiftoolPath:    exiftoolPath,
		rawtherapee:     nil,
		formula:         DefaultToneMappingFormula(),
		calibrationData: make([]CalibrationSample, 0),
	}, nil
}

// ApplyFormula applies the calibrated formula to calculate ToneEqualizer settings
// This is the FAST path used in production (no calibration iteration)
func (f *ToneMappingFormula) ApplyFormula(om3 *OM3ToneSettings) *ToneEqualizerSettings {
	h := float64(om3.Highlights)
	s := float64(om3.Shadows)
	m := float64(om3.Midtones)
	c := float64(om3.Contrast)

	te := &ToneEqualizerSettings{Enabled: true}

	// Band[i] = Coefficients[i][0] + Coefficients[i][1]*H + Coefficients[i][2]*S + Coefficients[i][3]*M + Coefficients[i][4]*C
	te.Band0 = clampInt(int(f.Coefficients[0][0]+f.Coefficients[0][1]*h+f.Coefficients[0][2]*s+f.Coefficients[0][3]*m+f.Coefficients[0][4]*c), -100, 100)
	te.Band1 = clampInt(int(f.Coefficients[1][0]+f.Coefficients[1][1]*h+f.Coefficients[1][2]*s+f.Coefficients[1][3]*m+f.Coefficients[1][4]*c), -100, 100)
	te.Band2 = clampInt(int(f.Coefficients[2][0]+f.Coefficients[2][1]*h+f.Coefficients[2][2]*s+f.Coefficients[2][3]*m+f.Coefficients[2][4]*c), -100, 100)
	te.Band3 = clampInt(int(f.Coefficients[3][0]+f.Coefficients[3][1]*h+f.Coefficients[3][2]*s+f.Coefficients[3][3]*m+f.Coefficients[3][4]*c), -100, 100)
	te.Band4 = clampInt(int(f.Coefficients[4][0]+f.Coefficients[4][1]*h+f.Coefficients[4][2]*s+f.Coefficients[4][3]*m+f.Coefficients[4][4]*c), -100, 100)

	return te
}

// CalculateFromFormula calculates ToneEqualizer settings using the calibrated formula
// This is the FAST path - just applies the formula, no calibration
func (tc *ToneCalibration) CalculateFromFormula(om3 *OM3ToneSettings) *ToneEqualizerSettings {
	return tc.formula.ApplyFormula(om3)
}

// SaveFormula saves the calibrated formula to a JSON file
func (f *ToneMappingFormula) SaveFormula(filepath string) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal formula: %v", err)
	}
	return os.WriteFile(filepath, data, 0644)
}

// LoadFormula loads a calibrated formula from a JSON file
func LoadFormula(filepath string) (*ToneMappingFormula, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to read formula file: %v", err)
	}
	
	var formula ToneMappingFormula
	if err := json.Unmarshal(data, &formula); err != nil {
		return nil, fmt.Errorf("failed to parse formula: %v", err)
	}
	
	return &formula, nil
}

// SetFormula sets a custom formula (loaded from file)
func (tc *ToneCalibration) SetFormula(formula *ToneMappingFormula) {
	tc.formula = formula
}

// GetFormula returns the current formula
func (tc *ToneCalibration) GetFormula() *ToneMappingFormula {
	return tc.formula
}

// findExifTool locates the exiftool executable
func findExifTool() string {
	// Common locations
	paths := []string{
		"exiftool",
		"exiftool.exe",
	}

	// Check user-specific path on Windows
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, "AppData", "Local", "Programs", "ExifTool", "ExifTool.exe"))
	}

	for _, p := range paths {
		if path, err := exec.LookPath(p); err == nil {
			return path
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}

// ExtractToneSettings extracts OM-3 tone settings from a JPEG file's EXIF data
func (tc *ToneCalibration) ExtractToneSettings(jpegPath string) (*OM3ToneSettings, error) {
	// Run exiftool to extract Olympus-specific tags
	cmd := exec.Command(tc.exiftoolPath, "-ToneLevel", "-PictureModeContrast", "-PictureMode", jpegPath)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run exiftool: %v", err)
	}

	settings := &OM3ToneSettings{}
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Tone Level") {
			// Parse: "Tone Level : Highlights; -1; -7; 7; Shadows; -2; -7; 7; Midtones; 1; -7; 7; ..."
			settings.Highlights, settings.Shadows, settings.Midtones = parseToneLevel(line)
		} else if strings.HasPrefix(line, "Picture Mode Contrast") {
			// Parse: "Picture Mode Contrast : 1 (min -2, max 2)"
			settings.Contrast = parsePictureModeContrast(line)
		} else if strings.HasPrefix(line, "Picture Mode") && !strings.Contains(line, "Contrast") {
			// Parse: "Picture Mode : Color Profile 3; 2"
			settings.Profile = parsePictureMode(line)
		}
	}

	return settings, nil
}

// parseToneLevel parses the ToneLevel EXIF field
func parseToneLevel(line string) (highlights, shadows, midtones int) {
	// Format: "Tone Level : Highlights; -1; -7; 7; Shadows; -2; -7; 7; Midtones; 1; -7; 7; ..."
	re := regexp.MustCompile(`Highlights;\s*(-?\d+);\s*-?\d+;\s*\d+;\s*Shadows;\s*(-?\d+);\s*-?\d+;\s*\d+;\s*Midtones;\s*(-?\d+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) >= 4 {
		highlights, _ = strconv.Atoi(matches[1])
		shadows, _ = strconv.Atoi(matches[2])
		midtones, _ = strconv.Atoi(matches[3])
	}
	return
}

// parsePictureModeContrast parses the contrast value
func parsePictureModeContrast(line string) int {
	// Format: "Picture Mode Contrast : 1 (min -2, max 2)"
	re := regexp.MustCompile(`:\s*(-?\d+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) >= 2 {
		contrast, _ := strconv.Atoi(matches[1])
		return contrast
	}
	return 0
}

// parsePictureMode extracts the profile name
func parsePictureMode(line string) string {
	// Format: "Picture Mode : Color Profile 3; 2"
	re := regexp.MustCompile(`:\s*(.+?)(?:;|$)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

// SettingsHash generates a unique hash for a set of tone settings
func (s *OM3ToneSettings) SettingsHash() string {
	return fmt.Sprintf("H%d_S%d_M%d_C%d_%s", s.Highlights, s.Shadows, s.Midtones, s.Contrast, s.Profile)
}

// CalculateInitialMapping creates an initial ToneEqualizer mapping from OM-3 settings
// This is a starting point that will be refined through calibration
func (tc *ToneCalibration) CalculateInitialMapping(om3 *OM3ToneSettings) *ToneEqualizerSettings {
	// Initial mapping formula based on analysis:
	// OM-3 ranges: Highlights/Shadows/Midtones: -7 to +7, Contrast: -2 to +2
	// RawTherapee ToneEqualizer: -100 to +100

	// Scale factor: approximately 7 for each band (50/7 ≈ 7)
	scaleFactor := 7.0
	contrastScale := 10.0

	te := &ToneEqualizerSettings{
		Enabled: true,
	}

	// Map shadows (OM-3) to Band1 (RT Shadows)
	// Negative shadows in OM-3 = darker shadows, positive = lighter
	te.Band1 = clampInt(int(float64(om3.Shadows)*scaleFactor-float64(om3.Contrast)*contrastScale), -100, 100)

	// Map midtones (OM-3) to Band2 (RT Midtones)
	te.Band2 = clampInt(int(float64(om3.Midtones)*scaleFactor), -100, 100)

	// Map highlights (OM-3) to Band3 (RT Highlights)
	// Positive highlights in OM-3 = brighter highlights
	te.Band3 = clampInt(int(float64(om3.Highlights)*scaleFactor+float64(om3.Contrast)*contrastScale), -100, 100)

	// Band0 (Blacks) follows shadow trend at 50%
	te.Band0 = clampInt(te.Band1/2, -100, 100)

	// Band4 (Whites) follows highlight trend at 50%
	te.Band4 = clampInt(te.Band3/2, -100, 100)

	// Band5 is not used in UI, keep at 0
	te.Band5 = 0

	return te
}

// clampInt clamps an integer value between min and max
func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// GeneratePP3WithToneEqualizer generates a PP3 profile string with ToneEqualizer settings
func (tc *ToneCalibration) GeneratePP3WithToneEqualizer(te *ToneEqualizerSettings, basePP3 string) string {
	toneEqSection := fmt.Sprintf(`[ToneEqualizer]
Enabled=%v
Band0=%d
Band1=%d
Band2=%d
Band3=%d
Band4=%d
Band5=%d
Regularization=%d
Pivot=%d
`, te.Enabled, te.Band0, te.Band1, te.Band2, te.Band3, te.Band4, te.Band5, te.Regularization, te.Pivot)

	// If basePP3 is provided, insert/replace the ToneEqualizer section
	if basePP3 != "" {
		// Check if ToneEqualizer section exists
		if strings.Contains(basePP3, "[ToneEqualizer]") {
			// Replace existing section
			re := regexp.MustCompile(`\[ToneEqualizer\][^\[]*`)
			return re.ReplaceAllString(basePP3, toneEqSection)
		}
		// Insert before [Spot removal] or at end
		if idx := strings.Index(basePP3, "[Spot removal]"); idx != -1 {
			return basePP3[:idx] + toneEqSection + "\n" + basePP3[idx:]
		}
		return basePP3 + "\n" + toneEqSection
	}

	// Generate minimal PP3
	return fmt.Sprintf(`[Version]
AppVersion=5.12
Version=352

[General]
ColorLabel=0
InTrash=false

%s
`, toneEqSection)
}

// WritePP3File writes a PP3 profile to a file
func (tc *ToneCalibration) WritePP3File(pp3Content string, outputPath string) error {
	return os.WriteFile(outputPath, []byte(pp3Content), 0644)
}

// AnalyzeLuminanceHistogram analyzes the luminance distribution of a JPEG image
func (tc *ToneCalibration) AnalyzeLuminanceHistogram(jpegPath string) (*LuminanceHistogram, error) {
	file, err := os.Open(jpegPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open image: %v", err)
	}
	defer file.Close()

	img, err := jpeg.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JPEG: %v", err)
	}

	hist := &LuminanceHistogram{
		Percentiles: make(map[int]int),
	}

	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := img.At(x, y)
			lum := luminance(c)
			hist.Bins[lum]++
			hist.TotalPixels++
		}
	}

	// Calculate percentiles
	percentiles := []int{1, 5, 10, 25, 50, 75, 90, 95, 99}
	cumulative := 0
	pIdx := 0
	for binIdx := 0; binIdx < 256 && pIdx < len(percentiles); binIdx++ {
		cumulative += hist.Bins[binIdx]
		threshold := (hist.TotalPixels * percentiles[pIdx]) / 100
		if cumulative >= threshold {
			hist.Percentiles[percentiles[pIdx]] = binIdx
			pIdx++
		}
	}

	return hist, nil
}

// luminance calculates the luminance (brightness) of a color using standard coefficients
func luminance(c color.Color) int {
	r, g, b, _ := c.RGBA()
	// Convert from 16-bit to 8-bit and apply luminance formula
	// Y = 0.299*R + 0.587*G + 0.114*B
	lum := (0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8))
	if lum > 255 {
		lum = 255
	}
	return int(lum)
}

// CompareLuminanceHistograms compares two histograms and returns a similarity score
// Lower score = more similar (0 = identical)
func (tc *ToneCalibration) CompareLuminanceHistograms(hist1, hist2 *LuminanceHistogram) float64 {
	// Compare using multiple metrics

	// 1. Mean Squared Error of percentiles
	percentileMSE := 0.0
	percentileCount := 0
	weights := map[int]float64{
		1: 0.5, 5: 1.0, 10: 1.5, 25: 2.0, 50: 2.5, 75: 2.0, 90: 1.5, 95: 1.0, 99: 0.5,
	}
	for p, w := range weights {
		if v1, ok1 := hist1.Percentiles[p]; ok1 {
			if v2, ok2 := hist2.Percentiles[p]; ok2 {
				diff := float64(v1 - v2)
				percentileMSE += w * diff * diff
				percentileCount++
			}
		}
	}
	if percentileCount > 0 {
		percentileMSE /= float64(percentileCount)
	}

	// 2. Chi-squared distance of histogram bins
	chiSquared := 0.0
	for i := 0; i < 256; i++ {
		n1 := float64(hist1.Bins[i]) / float64(hist1.TotalPixels)
		n2 := float64(hist2.Bins[i]) / float64(hist2.TotalPixels)
		if n1+n2 > 0 {
			chiSquared += (n1 - n2) * (n1 - n2) / (n1 + n2)
		}
	}

	// Combined score (weighted)
	return 0.6*percentileMSE + 0.4*chiSquared*10000
}

// CalibrateMapping refines ToneEqualizer settings by comparing processed RAW with reference JPEG
// The calibration workflow is:
// 1. ORF → DNG conversion (Adobe DNG Converter) [if DNG converter is configured]
// 2. DNG/ORF → JPG export (RawTherapee with ToneEqualizer settings)
// 3. Compare generated JPG histogram with original sidecar JPG histogram
// 4. Adjust ToneEqualizer settings and repeat until convergence
func (tc *ToneCalibration) CalibrateMapping(
	rawPath string,
	refJPEGPath string,
	initialTE *ToneEqualizerSettings,
	maxIterations int,
	threshold float64,
) (*ToneEqualizerSettings, float64, error) {
	// Step 1: Analyze the sidecar JPEG (camera's original output with its tone settings)
	refHist, err := tc.AnalyzeLuminanceHistogram(refJPEGPath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to analyze reference JPEG: %v", err)
	}

	bestTE := initialTE
	bestScore := math.MaxFloat64

	// Create temp directory for processing
	tempDir, err := os.MkdirTemp("", "tone_calibration")
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Step 2: Convert ORF to DNG if DNG converter is configured
	processPath := rawPath
	if tc.dngConverter != nil {
		// Create a temporary DNG converter that outputs to our temp directory
		tempDNGConverter, err := NewDNGConverter(DNGConverterConfig{
			OutputDir:  tempDir,
			Compressed: false,
		})
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create temp DNG converter: %v", err)
		}

		dngPath, err := tempDNGConverter.ConvertFile(rawPath)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to convert ORF to DNG: %v", err)
		}
		processPath = dngPath
		// DNG file will be cleaned up when tempDir is removed
	}

	// Iterative refinement loop
	for iter := 0; iter < maxIterations; iter++ {
		// Step 3: Generate PP3 with current ToneEqualizer settings
		pp3Content := tc.GeneratePP3WithToneEqualizer(bestTE, "")
		pp3Path := filepath.Join(tempDir, "calibration.pp3")
		if err := tc.WritePP3File(pp3Content, pp3Path); err != nil {
			return nil, 0, fmt.Errorf("failed to write PP3: %v", err)
		}

		// Step 4: Process DNG/ORF with RawTherapee to generate JPG
		outputPath, err := tc.rawtherapee.ProcessFileWithProfile(processPath, pp3Path)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to process with RawTherapee: %v", err)
		}

		// Step 5: Analyze the RawTherapee-generated JPG histogram
		processedHist, err := tc.AnalyzeLuminanceHistogram(outputPath)
		if err != nil {
			os.Remove(outputPath)
			return nil, 0, fmt.Errorf("failed to analyze RawTherapee output: %v", err)
		}

		// Step 6: Compare histograms (RawTherapee output vs camera sidecar JPEG)
		score := tc.CompareLuminanceHistograms(refHist, processedHist)
		
		// Clean up processed JPG file
		os.Remove(outputPath)

		// Track best result
		if score < bestScore {
			bestScore = score
		}

		// Check if we've reached acceptable threshold
		if score < threshold {
			break
		}

		// Step 7: Adjust ToneEqualizer settings to reduce the histogram difference
		newTE := tc.adjustSettings(bestTE, refHist, processedHist)
		bestTE = newTE
	}

	return bestTE, bestScore, nil
}

// adjustSettings tweaks ToneEqualizer settings based on histogram comparison
func (tc *ToneCalibration) adjustSettings(
	current *ToneEqualizerSettings,
	refHist, processedHist *LuminanceHistogram,
) *ToneEqualizerSettings {
	adjusted := &ToneEqualizerSettings{
		Enabled:       current.Enabled,
		Band0:         current.Band0,
		Band1:         current.Band1,
		Band2:         current.Band2,
		Band3:         current.Band3,
		Band4:         current.Band4,
		Band5:         current.Band5,
		Regularization: current.Regularization,
		Pivot:         current.Pivot,
	}

	// Adjust shadows (Band0/Band1) based on low percentiles
	if refHist.Percentiles[10] > processedHist.Percentiles[10] {
		// Reference has brighter shadows, increase Band1
		adjusted.Band1 = clampInt(current.Band1+5, -100, 100)
		adjusted.Band0 = clampInt(current.Band0+3, -100, 100)
	} else if refHist.Percentiles[10] < processedHist.Percentiles[10] {
		adjusted.Band1 = clampInt(current.Band1-5, -100, 100)
		adjusted.Band0 = clampInt(current.Band0-3, -100, 100)
	}

	// Adjust midtones (Band2) based on median
	if refHist.Percentiles[50] > processedHist.Percentiles[50] {
		adjusted.Band2 = clampInt(current.Band2+5, -100, 100)
	} else if refHist.Percentiles[50] < processedHist.Percentiles[50] {
		adjusted.Band2 = clampInt(current.Band2-5, -100, 100)
	}

	// Adjust highlights (Band3/Band4) based on high percentiles
	if refHist.Percentiles[90] > processedHist.Percentiles[90] {
		adjusted.Band3 = clampInt(current.Band3+5, -100, 100)
		adjusted.Band4 = clampInt(current.Band4+3, -100, 100)
	} else if refHist.Percentiles[90] < processedHist.Percentiles[90] {
		adjusted.Band3 = clampInt(current.Band3-5, -100, 100)
		adjusted.Band4 = clampInt(current.Band4-3, -100, 100)
	}

	return adjusted
}

// CalibrationResult represents a single calibration result
type CalibrationResult struct {
	OM3Settings      *OM3ToneSettings
	TESettings       *ToneEqualizerSettings
	SimilarityScore  float64
	RAWPath          string
	JPEGPath         string
}

// BatchCalibrate calibrates multiple RAW+JPEG pairs and builds a calibration database
func (tc *ToneCalibration) BatchCalibrate(pairs []struct{ RAW, JPEG string }, maxIterations int) ([]CalibrationResult, error) {
	results := make([]CalibrationResult, 0, len(pairs))

	for _, pair := range pairs {
		// Extract tone settings from JPEG
		om3Settings, err := tc.ExtractToneSettings(pair.JPEG)
		if err != nil {
			fmt.Printf("Warning: failed to extract settings from %s: %v\n", pair.JPEG, err)
			continue
		}

		// Calculate initial mapping
		initialTE := tc.CalculateInitialMapping(om3Settings)

		// Calibrate
		calibratedTE, score, err := tc.CalibrateMapping(
			pair.RAW,
			pair.JPEG,
			initialTE,
			maxIterations,
			100.0, // threshold
		)
		if err != nil {
			fmt.Printf("Warning: failed to calibrate %s: %v\n", pair.RAW, err)
			continue
		}

		results = append(results, CalibrationResult{
			OM3Settings:     om3Settings,
			TESettings:      calibratedTE,
			SimilarityScore: score,
			RAWPath:         pair.RAW,
			JPEGPath:        pair.JPEG,
		})

		// Store in calibration data for regression
		tc.calibrationData = append(tc.calibrationData, CalibrationSample{
			OM3Settings: om3Settings,
			TESettings:  calibratedTE,
			Score:       score,
		})
	}

	return results, nil
}

// CalculateFormulaFromSamples uses linear regression to find optimal formula coefficients
// from the calibration samples. This should be called after BatchCalibrate.
func (tc *ToneCalibration) CalculateFormulaFromSamples() error {
	n := len(tc.calibrationData)
	if n < 5 {
		return fmt.Errorf("need at least 5 samples for regression, have %d", n)
	}

	// For each band, solve:
	// Band[i] = a0 + a1*H + a2*S + a3*M + a4*C
	// Using least squares: X * a = y, where a = (X'X)^-1 * X'y

	// Build the design matrix X (n x 5) with columns: [1, H, S, M, C]
	X := make([][]float64, n)
	for i, sample := range tc.calibrationData {
		X[i] = []float64{
			1.0,
			float64(sample.OM3Settings.Highlights),
			float64(sample.OM3Settings.Shadows),
			float64(sample.OM3Settings.Midtones),
			float64(sample.OM3Settings.Contrast),
		}
	}

	// For each band (0-4), solve the regression
	for band := 0; band < 5; band++ {
		// Build y vector for this band
		y := make([]float64, n)
		for i, sample := range tc.calibrationData {
			switch band {
			case 0:
				y[i] = float64(sample.TESettings.Band0)
			case 1:
				y[i] = float64(sample.TESettings.Band1)
			case 2:
				y[i] = float64(sample.TESettings.Band2)
			case 3:
				y[i] = float64(sample.TESettings.Band3)
			case 4:
				y[i] = float64(sample.TESettings.Band4)
			}
		}

		// Solve least squares
		coeffs, err := leastSquares(X, y)
		if err != nil {
			return fmt.Errorf("regression failed for band %d: %v", band, err)
		}

		// Store coefficients
		for j := 0; j < 5; j++ {
			tc.formula.Coefficients[band][j] = coeffs[j]
		}

		// Calculate accuracy metrics for this band
		yMean := 0.0
		for _, val := range y {
			yMean += val
		}
		yMean /= float64(n)

		ssTotal := 0.0  // Total sum of squares
		ssResid := 0.0  // Residual sum of squares
		sumError := 0.0 // Sum of signed errors

		for i := 0; i < n; i++ {
			// Predicted value
			yPred := coeffs[0]
			for j := 1; j < 5; j++ {
				yPred += coeffs[j] * X[i][j]
			}
			
			// Residual (error)
			residual := y[i] - yPred
			
			ssTotal += (y[i] - yMean) * (y[i] - yMean)
			ssResid += residual * residual
			sumError += residual
		}

		// R² (coefficient of determination): 1 means perfect fit, 0 means no better than mean
		if ssTotal > 0 {
			tc.formula.RSquared[band] = 1.0 - (ssResid / ssTotal)
		} else {
			tc.formula.RSquared[band] = 1.0 // All values are the same
		}

		// RMSE (Root Mean Square Error): lower is better
		tc.formula.RMSE[band] = math.Sqrt(ssResid / float64(n))

		// Mean Error (bias): positive means formula predicts too low on average
		tc.formula.MeanError[band] = sumError / float64(n)
	}

	tc.formula.SampleCount = n
	return nil
}

// leastSquares solves the least squares problem X*a = y with ridge regularization
// Returns coefficients a such that ||X*a - y||^2 + λ||a||^2 is minimized
// Ridge regularization (λ > 0) prevents singular matrix issues
func leastSquares(X [][]float64, y []float64) ([]float64, error) {
	n := len(X)
	if n == 0 {
		return nil, fmt.Errorf("empty design matrix")
	}
	m := len(X[0]) // number of parameters

	// Ridge regularization parameter (small value to handle near-singular matrices)
	lambda := 0.01

	// Compute X'X (m x m) + λI (ridge regularization)
	XtX := make([][]float64, m)
	for i := range XtX {
		XtX[i] = make([]float64, m)
	}
	for i := 0; i < m; i++ {
		for j := 0; j < m; j++ {
			sum := 0.0
			for k := 0; k < n; k++ {
				sum += X[k][i] * X[k][j]
			}
			XtX[i][j] = sum
		}
		// Add ridge regularization to diagonal (except for bias term)
		if i > 0 {
			XtX[i][i] += lambda
		}
	}

	// Compute X'y (m x 1)
	Xty := make([]float64, m)
	for i := 0; i < m; i++ {
		sum := 0.0
		for k := 0; k < n; k++ {
			sum += X[k][i] * y[k]
		}
		Xty[i] = sum
	}

	// Solve (X'X + λI) * a = X'y using Gaussian elimination
	return solveLinearSystem(XtX, Xty)
}

// solveLinearSystem solves A*x = b using Gaussian elimination with partial pivoting
func solveLinearSystem(A [][]float64, b []float64) ([]float64, error) {
	n := len(b)
	
	// Create augmented matrix
	aug := make([][]float64, n)
	for i := range aug {
		aug[i] = make([]float64, n+1)
		copy(aug[i], A[i])
		aug[i][n] = b[i]
	}

	// Forward elimination with partial pivoting
	for i := 0; i < n; i++ {
		// Find pivot
		maxRow := i
		for k := i + 1; k < n; k++ {
			if math.Abs(aug[k][i]) > math.Abs(aug[maxRow][i]) {
				maxRow = k
			}
		}
		aug[i], aug[maxRow] = aug[maxRow], aug[i]

		if math.Abs(aug[i][i]) < 1e-10 {
			return nil, fmt.Errorf("singular matrix")
		}

		// Eliminate column
		for k := i + 1; k < n; k++ {
			factor := aug[k][i] / aug[i][i]
			for j := i; j <= n; j++ {
				aug[k][j] -= factor * aug[i][j]
			}
		}
	}

	// Back substitution
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		x[i] = aug[i][n]
		for j := i + 1; j < n; j++ {
			x[i] -= aug[i][j] * x[j]
		}
		x[i] /= aug[i][i]
	}

	return x, nil
}

// SaveFormula saves the calibrated formula to the specified file
func (tc *ToneCalibration) SaveFormula(filepath string) error {
	return tc.formula.SaveFormula(filepath)
}

// FindRAWJPEGPairs scans a directory for matching RAW+JPEG pairs
func FindRAWJPEGPairs(directory string) ([]struct{ RAW, JPEG string }, error) {
	var pairs []struct{ RAW, JPEG string }

	files, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}

	// Build maps of files by base name
	rawFiles := make(map[string]string)
	jpegFiles := make(map[string]string)

	rawExtensions := map[string]bool{".orf": true, ".ORF": true, ".raw": true, ".RAW": true}
	jpegExtensions := map[string]bool{".jpg": true, ".JPG": true, ".jpeg": true, ".JPEG": true}

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		ext := filepath.Ext(f.Name())
		baseName := strings.TrimSuffix(f.Name(), ext)
		fullPath := filepath.Join(directory, f.Name())

		if rawExtensions[ext] {
			rawFiles[baseName] = fullPath
		} else if jpegExtensions[ext] {
			jpegFiles[baseName] = fullPath
		}
	}

	// Find matching pairs
	for baseName, rawPath := range rawFiles {
		if jpegPath, ok := jpegFiles[baseName]; ok {
			pairs = append(pairs, struct{ RAW, JPEG string }{RAW: rawPath, JPEG: jpegPath})
		}
	}

	return pairs, nil
}

// QuickProcessRAW generates a PP3 file with ToneEqualizer settings based on the sidecar JPEG's EXIF
// rawPath: path to the RAW file (used to determine output PP3 path)
// jpegPath: path to the sidecar JPEG with OM-3 tone settings in EXIF
// outputPP3Path: where to write the generated PP3 file
// basePP3Content: optional base PP3 content to overlay (if empty, generates minimal PP3)
func (tc *ToneCalibration) QuickProcessRAW(rawPath, jpegPath, outputPP3Path string, basePP3Content string) error {
	// Extract settings from JPEG
	om3Settings, err := tc.ExtractToneSettings(jpegPath)
	if err != nil {
		return fmt.Errorf("failed to extract tone settings: %v", err)
	}

	// Use the formula to calculate ToneEqualizer settings (fast path)
	teSettings := tc.CalculateFromFormula(om3Settings)

	// Generate PP3 with ToneEqualizer settings overlaid on base
	pp3Content := tc.GeneratePP3WithToneEqualizer(teSettings, basePP3Content)
	return tc.WritePP3File(pp3Content, outputPP3Path)
}

// GenerateTonePP3ForRAW generates a PP3 file for a RAW file based on its sidecar JPEG
// Returns the path to the generated PP3 file, or empty string if no sidecar JPEG found
// This is the main integration point for the camera-to-immich workflow
func (tc *ToneCalibration) GenerateTonePP3ForRAW(rawPath string, basePP3Content string, outputDir string) (string, error) {
	// Find sidecar JPEG
	jpegPath := tc.FindSidecarJPEG(rawPath)
	if jpegPath == "" {
		return "", nil // No sidecar JPEG found, skip tone processing
	}

	// Extract settings from JPEG
	om3Settings, err := tc.ExtractToneSettings(jpegPath)
	if err != nil {
		return "", fmt.Errorf("failed to extract tone settings from %s: %v", jpegPath, err)
	}

	// Use the formula to calculate ToneEqualizer settings
	teSettings := tc.CalculateFromFormula(om3Settings)

	// Determine output PP3 path
	baseName := strings.TrimSuffix(filepath.Base(rawPath), filepath.Ext(rawPath))
	var pp3Path string
	if outputDir != "" {
		pp3Path = filepath.Join(outputDir, baseName+".pp3")
	} else {
		pp3Path = rawPath + ".pp3"
	}

	// Generate PP3 with ToneEqualizer settings overlaid on base
	pp3Content := tc.GeneratePP3WithToneEqualizer(teSettings, basePP3Content)
	if err := tc.WritePP3File(pp3Content, pp3Path); err != nil {
		return "", fmt.Errorf("failed to write PP3: %v", err)
	}

	return pp3Path, nil
}

// FindSidecarJPEG finds the sidecar JPEG for a RAW file
// Returns empty string if no sidecar is found
func (tc *ToneCalibration) FindSidecarJPEG(rawPath string) string {
	dir := filepath.Dir(rawPath)
	baseName := strings.TrimSuffix(filepath.Base(rawPath), filepath.Ext(rawPath))

	// Check for common JPEG extensions (case-insensitive handled by checking both)
	jpegExtensions := []string{".JPG", ".jpg", ".JPEG", ".jpeg"}
	for _, ext := range jpegExtensions {
		jpegPath := filepath.Join(dir, baseName+ext)
		if _, err := os.Stat(jpegPath); err == nil {
			return jpegPath
		}
	}

	return ""
}

// NewToneCalibrationForWorkflow creates a ToneCalibration instance configured for the main workflow
// This loads the formula from file and prepares for fast PP3 generation
func NewToneCalibrationForWorkflow(formulaPath string) (*ToneCalibration, error) {
	// Find exiftool
	exiftoolPath := findExifTool()
	if exiftoolPath == "" {
		return nil, fmt.Errorf("exiftool not found (needed for reading JPEG EXIF)")
	}

	// Load formula from file
	formula, err := LoadFormula(formulaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load tone formula: %v", err)
	}

	return &ToneCalibration{
		exiftoolPath:    exiftoolPath,
		rawtherapee:     nil, // Not needed for PP3 generation only
		formula:         formula,
		calibrationData: make([]CalibrationSample, 0),
	}, nil
}

// Ensure image package is used
var _ image.Image