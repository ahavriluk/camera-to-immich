package main

import (
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// GrainAnalysis holds the results of grain analysis
type GrainAnalysis struct {
	Filename         string
	Width            int
	Height           int
	MeanLuminance    float64
	StdDevLuminance  float64 // This correlates with grain strength
	LocalVariance    float64 // Local variance indicates grain texture
	HighFreqEnergy   float64 // High frequency energy indicates grain size
	SuggestedISO     int
	SuggestedStrength int
	SuggestedScale   int
}

func main() {
	sourceDir := flag.String("source", "", "Source directory containing film scans")
	sampleSize := flag.Int("samples", 5, "Number of images to analyze")
	flag.Parse()

	if *sourceDir == "" {
		fmt.Println("Usage: grain-analyzer -source <directory>")
		fmt.Println("\nAnalyzes film scans to derive RawTherapee film grain settings")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Find image files
	var imageFiles []string
	err := filepath.Walk(*sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			ext := filepath.Ext(path)
			if ext == ".jpg" || ext == ".JPG" || ext == ".jpeg" || ext == ".JPEG" ||
				ext == ".png" || ext == ".PNG" {
				imageFiles = append(imageFiles, path)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning directory: %v\n", err)
		os.Exit(1)
	}

	if len(imageFiles) == 0 {
		fmt.Println("No image files found in directory")
		os.Exit(1)
	}

	fmt.Printf("Found %d images. Analyzing %d samples...\n\n", len(imageFiles), min(*sampleSize, len(imageFiles)))

	// Analyze a sample of images
	var analyses []GrainAnalysis
	step := max(1, len(imageFiles) / *sampleSize)
	for i := 0; i < len(imageFiles) && len(analyses) < *sampleSize; i += step {
		analysis, err := analyzeImage(imageFiles[i])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not analyze %s: %v\n", imageFiles[i], err)
			continue
		}
		analyses = append(analyses, analysis)
		
		fmt.Printf("Analyzed: %s\n", filepath.Base(analysis.Filename))
		fmt.Printf("  Resolution: %dx%d\n", analysis.Width, analysis.Height)
		fmt.Printf("  Mean Luminance: %.2f\n", analysis.MeanLuminance)
		fmt.Printf("  Std Dev (grain indicator): %.4f\n", analysis.StdDevLuminance)
		fmt.Printf("  Local Variance: %.4f\n", analysis.LocalVariance)
		fmt.Printf("  High Freq Energy: %.6f\n", analysis.HighFreqEnergy)
		fmt.Println()
	}

	if len(analyses) == 0 {
		fmt.Println("No images could be analyzed")
		os.Exit(1)
	}

	// Calculate average characteristics
	avgStdDev := 0.0
	avgLocalVar := 0.0
	avgHighFreq := 0.0
	for _, a := range analyses {
		avgStdDev += a.StdDevLuminance
		avgLocalVar += a.LocalVariance
		avgHighFreq += a.HighFreqEnergy
	}
	avgStdDev /= float64(len(analyses))
	avgLocalVar /= float64(len(analyses))
	avgHighFreq /= float64(len(analyses))

	// Map to RawTherapee Film Grain settings
	// Based on Kodak ColorPlus 200 characteristics
	settings := deriveSettings(avgStdDev, avgLocalVar, avgHighFreq, 200)

	fmt.Println("=" + string(make([]byte, 50)))
	fmt.Println("ANALYSIS RESULTS FOR KODAK COLORPLUS 200")
	fmt.Println("=" + string(make([]byte, 50)))
	fmt.Printf("\nAverage Std Dev: %.4f\n", avgStdDev)
	fmt.Printf("Average Local Variance: %.4f\n", avgLocalVar)
	fmt.Printf("Average High Freq Energy: %.6f\n", avgHighFreq)

	fmt.Println("\n" + "=" + string(make([]byte, 50)))
	fmt.Println("RECOMMENDED RAWTHERAPEE FILM GRAIN SETTINGS")
	fmt.Println("=" + string(make([]byte, 50)))
	fmt.Printf("\n[Film Grain]\n")
	fmt.Printf("Enabled=true\n")
	fmt.Printf("ISO=%d\n", settings.SuggestedISO)
	fmt.Printf("Strength=%d\n", settings.SuggestedStrength)
	fmt.Printf("Scale=%d\n", settings.SuggestedScale)
	fmt.Printf("# Distribution: Gaussian (default)\n")
	fmt.Printf("# Gamma: Use default or adjust based on shadow grain preference\n")

	fmt.Println("\nExplanation:")
	fmt.Printf("  - ISO %d: Controls grain size pattern (matched to film speed)\n", settings.SuggestedISO)
	fmt.Printf("  - Strength %d: Based on measured local variance (grain visibility)\n", settings.SuggestedStrength)
	fmt.Printf("  - Scale %d: Based on high frequency energy (grain coarseness)\n", settings.SuggestedScale)
}

func analyzeImage(path string) (GrainAnalysis, error) {
	file, err := os.Open(path)
	if err != nil {
		return GrainAnalysis{}, err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return GrainAnalysis{}, err
	}

	bounds := img.Bounds()
	width := bounds.Max.X - bounds.Min.X
	height := bounds.Max.Y - bounds.Min.Y

	// Sample the center region (avoid borders which may have artifacts)
	sampleX := width / 4
	sampleY := height / 4
	sampleW := width / 2
	sampleH := height / 2

	// Calculate luminance values for sample region
	var luminances []float64
	for y := sampleY; y < sampleY+sampleH; y++ {
		for x := sampleX; x < sampleX+sampleW; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// Convert to luminance (0-1 range)
			lum := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535.0
			luminances = append(luminances, lum)
		}
	}

	// Calculate mean
	mean := 0.0
	for _, l := range luminances {
		mean += l
	}
	mean /= float64(len(luminances))

	// Calculate standard deviation
	variance := 0.0
	for _, l := range luminances {
		diff := l - mean
		variance += diff * diff
	}
	variance /= float64(len(luminances))
	stdDev := math.Sqrt(variance)

	// Calculate local variance (3x3 window) - better indicator of grain
	localVar := calculateLocalVariance(img, sampleX, sampleY, sampleW, sampleH)

	// Calculate high frequency energy using Laplacian-like operator
	highFreq := calculateHighFreqEnergy(img, sampleX, sampleY, sampleW, sampleH)

	return GrainAnalysis{
		Filename:        path,
		Width:           width,
		Height:          height,
		MeanLuminance:   mean,
		StdDevLuminance: stdDev,
		LocalVariance:   localVar,
		HighFreqEnergy:  highFreq,
	}, nil
}

func calculateLocalVariance(img image.Image, startX, startY, w, h int) float64 {
	// Sample at regular intervals
	step := 10
	var localVars []float64

	for y := startY + 1; y < startY+h-1; y += step {
		for x := startX + 1; x < startX+w-1; x += step {
			// Get 3x3 neighborhood
			var neighborhood []float64
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					r, g, b, _ := img.At(x+dx, y+dy).RGBA()
					lum := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535.0
					neighborhood = append(neighborhood, lum)
				}
			}

			// Calculate local variance
			localMean := 0.0
			for _, l := range neighborhood {
				localMean += l
			}
			localMean /= 9.0

			localVar := 0.0
			for _, l := range neighborhood {
				diff := l - localMean
				localVar += diff * diff
			}
			localVar /= 9.0
			localVars = append(localVars, localVar)
		}
	}

	// Return median local variance
	sort.Float64s(localVars)
	return localVars[len(localVars)/2]
}

func calculateHighFreqEnergy(img image.Image, startX, startY, w, h int) float64 {
	// Simple Laplacian-like high frequency detection
	step := 5
	var energies []float64

	for y := startY + 1; y < startY+h-1; y += step {
		for x := startX + 1; x < startX+w-1; x += step {
			// Laplacian: center pixel - average of neighbors
			r0, g0, b0, _ := img.At(x, y).RGBA()
			center := (0.299*float64(r0) + 0.587*float64(g0) + 0.114*float64(b0)) / 65535.0

			r1, g1, b1, _ := img.At(x-1, y).RGBA()
			r2, g2, b2, _ := img.At(x+1, y).RGBA()
			r3, g3, b3, _ := img.At(x, y-1).RGBA()
			r4, g4, b4, _ := img.At(x, y+1).RGBA()

			n1 := (0.299*float64(r1) + 0.587*float64(g1) + 0.114*float64(b1)) / 65535.0
			n2 := (0.299*float64(r2) + 0.587*float64(g2) + 0.114*float64(b2)) / 65535.0
			n3 := (0.299*float64(r3) + 0.587*float64(g3) + 0.114*float64(b3)) / 65535.0
			n4 := (0.299*float64(r4) + 0.587*float64(g4) + 0.114*float64(b4)) / 65535.0

			avgNeighbor := (n1 + n2 + n3 + n4) / 4.0
			energy := math.Abs(center - avgNeighbor)
			energies = append(energies, energy)
		}
	}

	// Return mean high frequency energy
	sum := 0.0
	for _, e := range energies {
		sum += e
	}
	return sum / float64(len(energies))
}

func deriveSettings(avgStdDev, avgLocalVar, avgHighFreq float64, filmISO int) GrainAnalysis {
	// Map measured values to RawTherapee Film Grain settings
	
	// ISO: Use the actual film ISO as base, this controls grain pattern size
	suggestedISO := filmISO
	
	// Strength (0-100): Based on local variance
	// Typical local variance for film grain: 0.0001 - 0.001
	// Map this to strength 10-50 range for natural look
	strength := int(avgLocalVar * 50000) // Scale factor
	if strength < 10 {
		strength = 10
	}
	if strength > 70 {
		strength = 70
	}
	
	// Scale (50-200): Based on high frequency energy
	// Higher energy = finer grain = lower scale
	// Lower energy = coarser grain = higher scale
	scale := int(100 - avgHighFreq*5000) // Inverse relationship
	if scale < 60 {
		scale = 60
	}
	if scale > 150 {
		scale = 150
	}
	
	return GrainAnalysis{
		SuggestedISO:      suggestedISO,
		SuggestedStrength: strength,
		SuggestedScale:    scale,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}