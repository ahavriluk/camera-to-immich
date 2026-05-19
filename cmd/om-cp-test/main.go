// Command om-cp-test is a prototype driver that:
//
//  1. Optionally converts an OM-3 .ORF file to .DNG via Adobe DNG
//     Converter (or accepts an .ORF / .DNG directly).
//  2. Reads the OM-3 Color Profile EXIF metadata from the source file.
//  3. Stacks the OM-3 base PP3 (testdata/OM3_Default_DNG.pp3) with a
//     generated PP3 partial that encodes the per-hue saturation curve,
//     Tone Equalizer Shadows/Midtones/Highlights, contrast and
//     sharpening derived from EXIF.
//  4. Renders a JPG via rawtherapee-cli.
//  5. (Optional) Compares the rendered JPG against the in-camera OOC
//     JPG (same basename, .JPG extension next to the ORF) and prints an
//     accuracy report (RMSE + Lab dE76 statistics).
//
// Usage examples (PowerShell):
//
//	go run ./cmd/om-cp-test --inputs E:\DCIM\100OMSYS\P5170451.ORF
//
//	go run ./cmd/om-cp-test `
//	    --inputs "E:\DCIM\100OMSYS\P5170449.ORF,E:\DCIM\100OMSYS\P5170450.ORF" `
//	    --out ./testdata/om3-cp/out `
//	    --compare
//
//	go run ./cmd/om-cp-test --inputs-glob "E:\DCIM\100OMSYS\P517044*.ORF"
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ohavrylyuk/camera-to-immich/internal/cpprofile"
	"github.com/ohavrylyuk/camera-to-immich/internal/processor"
)

func main() {
	var (
		inputsCSV   = flag.String("inputs", "", "Comma-separated list of input .ORF/.DNG files")
		inputsGlob  = flag.String("inputs-glob", "", "Glob pattern for input files (alternative to --inputs)")
		outDir      = flag.String("out", filepath.Join("testdata", "om3-cp", "out"), "Output directory (DNG/PP3/JPG)")
		basePP3     = flag.String("base-pp3", filepath.Join("testdata", "OM3_Default_DNG.pp3"), "Base RawTherapee PP3 profile")
		skipDNG     = flag.Bool("skip-dng", false, "Skip Adobe DNG conversion (use ORF directly with rawtherapee-cli)")
		dngDir      = flag.String("dng-dir", "", "Override DNG output directory (default: <out>/dng)")
		compare     = flag.Bool("compare", true, "Compare rendered JPG against in-camera OOC JPG if available")
		jpegQuality = flag.Int("quality", 92, "Output JPEG quality (1-100)")
		dryRun      = flag.Bool("dry-run", false, "Read EXIF and emit PP3 only; do not invoke RawTherapee")
		dumpEXIF    = flag.Bool("dump-exif", false, "Print parsed EXIF Color Profile to stdout for each input")
	)
	flag.Parse()

	inputs, err := collectInputs(*inputsCSV, *inputsGlob)
	if err != nil {
		exitErr(err)
	}
	if len(inputs) == 0 {
		exitErr(fmt.Errorf("no input files; use --inputs or --inputs-glob"))
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		exitErr(fmt.Errorf("create out dir: %v", err))
	}
	pp3Dir := filepath.Join(*outDir, "pp3")
	jpgDir := filepath.Join(*outDir, "jpg")
	for _, d := range []string{pp3Dir, jpgDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			exitErr(fmt.Errorf("mkdir %s: %v", d, err))
		}
	}

	resolvedDNGDir := *dngDir
	if resolvedDNGDir == "" {
		resolvedDNGDir = filepath.Join(*outDir, "dng")
	}

	// Verify base PP3 exists.
	if _, err := os.Stat(*basePP3); err != nil {
		exitErr(fmt.Errorf("base pp3 not found: %v", err))
	}

	// Prepare DNG converter (only if not skipping AND inputs are .ORF).
	var dngConv *processor.DNGConverter
	if !*skipDNG && hasAnyORF(inputs) {
		dngConv, err = processor.NewDNGConverter(processor.DNGConverterConfig{
			OutputDir:  resolvedDNGDir,
			Compressed: false,
			MaxRetries: 2,
			RetryDelay: 2 * time.Second,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[warn] DNG converter unavailable: %v\n", err)
			fmt.Fprintln(os.Stderr, "[warn] falling back to ORF-direct mode")
			*skipDNG = true
		}
	}

	// Prepare RawTherapee processor.
	var rt *processor.RawTherapee
	if !*dryRun {
		rt, err = processor.NewRawTherapee(processor.RawTherapeeConfig{
			OutputDir: jpgDir,
			Quality:   *jpegQuality,
			Timeout:   5 * time.Minute,
		})
		if err != nil {
			exitErr(fmt.Errorf("rawtherapee not available: %v", err))
		}
	}

	type result struct {
		Input     string
		PP3Path   string
		JPGPath   string
		OOCPath   string
		Accuracy  *cpprofile.AccuracyReport
		Err       error
	}
	var results []result

	for _, in := range inputs {
		r := result{Input: in}

		// 1) Read EXIF Color Profile metadata from the ORIGINAL file
		// (ORF/JPG retain the Olympus MakerNote in full; DNG may strip
		// some sub-tags depending on converter version).
		exifSrc := pickExifSource(in)
		cp, err := cpprofile.Read(exifSrc)
		if err != nil {
			r.Err = fmt.Errorf("read exif (%s): %v", exifSrc, err)
			results = append(results, r)
			continue
		}
		if *dumpEXIF {
			fmt.Printf("=== %s ===\n", in)
			fmt.Printf("  PictureMode=%q  IsColorProfile=%v\n", cp.PictureMode, cp.IsActive())
			fmt.Printf("  PMContrast=%d  PMSharpness=%d  PMSaturation=%d\n",
				cp.PMContrast, cp.PMSharpness, cp.PMSaturation)
			fmt.Printf("  Gradation=%q  Tone HL/Mid/Sh=%d/%d/%d\n",
				cp.Gradation, cp.ToneHighlights, cp.ToneMidtones, cp.ToneShadows)
			fmt.Printf("  Bands:\n")
			for _, n := range cpprofile.BandOrder {
				h, _ := cpprofile.BandHueHSVDeg(n)
				fmt.Printf("    %-14s (HSV %3.0f°) = %+d\n", n, h, cp.Bands[n])
			}
		}

		// 2) Build pp3 partial.
		opt := cpprofile.DefaultPP3Options()
		partial := cpprofile.BuildPP3Partial(cp, opt)
		base := filepath.Base(in)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		pp3Path := filepath.Join(pp3Dir, stem+".pp3")
		if err := os.WriteFile(pp3Path, []byte(partial), 0o644); err != nil {
			r.Err = fmt.Errorf("write pp3: %v", err)
			results = append(results, r)
			continue
		}
		r.PP3Path = pp3Path

		// 3) Convert to DNG if requested.
		rawForRT := in
		if !*skipDNG && dngConv != nil && strings.EqualFold(filepath.Ext(in), ".ORF") {
			dngPath, derr := dngConv.ConvertFile(in)
			if derr != nil {
				fmt.Fprintf(os.Stderr, "[warn] %s: DNG conversion failed: %v -- falling back to ORF\n", in, derr)
			} else {
				rawForRT = dngPath
			}
		}

		if *dryRun {
			r.JPGPath = "(dry-run)"
			results = append(results, r)
			continue
		}

		// 4) Render with rawtherapee-cli, stacking base + generated partial.
		jpgPath, rerr := rt.ProcessFileStacked(rawForRT, []string{*basePP3, pp3Path})
		if rerr != nil {
			r.Err = fmt.Errorf("rawtherapee: %v", rerr)
			results = append(results, r)
			continue
		}
		r.JPGPath = jpgPath

		// 5) Compare with OOC JPG if available and requested.
		if *compare {
			oocPath := findOOCJPG(in)
			if oocPath != "" {
				r.OOCPath = oocPath
				if rep, cerr := cpprofile.Compare(jpgPath, oocPath); cerr == nil {
					r.Accuracy = rep
				} else {
					fmt.Fprintf(os.Stderr, "[warn] compare failed for %s vs %s: %v\n", jpgPath, oocPath, cerr)
				}
			}
		}

		results = append(results, r)
	}

	// ------------ Summary ------------
	fmt.Println()
	fmt.Println("============== RESULTS ==============")
	for _, r := range results {
		fmt.Printf("\n%s\n", r.Input)
		if r.Err != nil {
			fmt.Printf("  ERROR: %v\n", r.Err)
			continue
		}
		fmt.Printf("  pp3 : %s\n", r.PP3Path)
		fmt.Printf("  jpg : %s\n", r.JPGPath)
		if r.OOCPath != "" {
			fmt.Printf("  ooc : %s\n", r.OOCPath)
		}
		if r.Accuracy != nil {
			a := r.Accuracy
			fmt.Printf("  acc : RMSE(sRGB)=%.2f  meanDE=%.2f  p95DE=%.2f  maxDE=%.2f  samples=%d\n",
				a.RMSESrgb, a.MeanDeltaE, a.P95DeltaE, a.MaxDeltaE, a.Samples)
		}
	}

	// Aggregate accuracy.
	var allDE []float64
	for _, r := range results {
		if r.Accuracy != nil {
			allDE = append(allDE, r.Accuracy.MeanDeltaE)
		}
	}
	if len(allDE) > 0 {
		var sum, max float64
		for _, d := range allDE {
			sum += d
			if d > max {
				max = d
			}
		}
		fmt.Println()
		fmt.Printf("=== Aggregate over %d compared files: mean(meanDE)=%.2f  max(meanDE)=%.2f ===\n",
			len(allDE), sum/float64(len(allDE)), max)
	}
}

func collectInputs(csv, glob string) ([]string, error) {
	var out []string
	if csv != "" {
		for _, p := range strings.Split(csv, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			abs, err := filepath.Abs(p)
			if err != nil {
				return nil, err
			}
			out = append(out, abs)
		}
	}
	if glob != "" {
		matches, err := filepath.Glob(glob)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			abs, err := filepath.Abs(m)
			if err != nil {
				return nil, err
			}
			out = append(out, abs)
		}
	}
	sort.Strings(out)
	return out, nil
}

func hasAnyORF(paths []string) bool {
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".ORF") {
			return true
		}
	}
	return false
}

// pickExifSource prefers the in-camera JPG sibling when the input is an
// ORF. Reason: on OM-3, the ORF's Olympus:PictureMode field reflects the
// *base* mode (e.g. "Natural") while the embedded/sibling JPG's
// PictureMode is the *applied* Color Profile slot (e.g. "Color Profile 3").
// Both files share the same ColorProfileSettings / ToneLevel block, so
// preferring the JPG keeps the PictureMode label aligned with the OOC
// rendering we're trying to match.
func pickExifSource(in string) string {
	ext := strings.ToLower(filepath.Ext(in))
	if ext == ".orf" {
		dir := filepath.Dir(in)
		stem := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))
		for _, c := range []string{
			filepath.Join(dir, stem+".JPG"),
			filepath.Join(dir, stem+".jpg"),
		} {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	return in
}

// findOOCJPG looks for an in-camera JPG sibling next to an ORF (same
// basename, .JPG/.jpg extension).
func findOOCJPG(in string) string {
	dir := filepath.Dir(in)
	base := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))
	candidates := []string{
		filepath.Join(dir, base+".JPG"),
		filepath.Join(dir, base+".jpg"),
		filepath.Join(dir, base+".jpeg"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func exitErr(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}