// chroma-denoise is a standalone CLI that applies chrominance noise
// reduction to JPEG files using a Guided Filter on the Cb/Cr channels.
//
// Usage:
//
//	chroma-denoise -in photo.jpg -out photo_dn.jpg
//	chroma-denoise -in dir/ -out out/ -recursive
//	chroma-denoise -in photo.jpg -out photo_dn.jpg -auto-iso
//	chroma-denoise -in photo.jpg -out photo_dn.jpg -radius 12 -epsilon 0.02
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ohavrylyuk/camera-to-immich/internal/denoise"
)

func main() {
	var (
		inPath      = flag.String("in", "", "Input JPEG file or directory")
		outPath     = flag.String("out", "", "Output JPEG file or directory")
		recursive   = flag.Bool("recursive", false, "Recurse into subdirectories when -in is a directory")
		radius      = flag.Int("radius", 8, "Guided-filter half-radius in chroma pixels (typical 4..16)")
		epsilon     = flag.Float64("epsilon", 0.01, "Guided-filter regularization (typical 0.001..0.05). Higher = more smoothing.")
		strength    = flag.Float64("strength", 1.0, "Blend factor in [0..1]. 0=no change, 1=fully denoised.")
		median      = flag.Bool("median", true, "Apply 3x3 median pre-pass on chroma (kills color speckle)")
		quality     = flag.Int("quality", 92, "Output JPEG quality (1..100)")
		keepEXIF    = flag.Bool("exif", true, "Preserve EXIF metadata from source")
		autoISO     = flag.Bool("auto-iso", false, "Scale -radius and -epsilon based on the EXIF ISO speed")
		workers     = flag.Int("workers", 0, "Parallel file workers (0 = NumCPU/2, capped at 4). Each worker uses ~50MB per 24MP image.")
		dryRun      = flag.Bool("dry-run", false, "List files that would be processed; don't actually denoise")
		showVersion = flag.Bool("version", false, "Print version and exit")
		verbose     = flag.Bool("verbose", false, "Verbose per-file output")
		suffix      = flag.String("suffix", "_dn", "When -in is a directory and -out is the same directory, append this suffix to output filenames")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("chroma-denoise 0.1.0")
		fmt.Println("camera-to-immich / internal/denoise (Guided Filter on YCbCr Cb/Cr)")
		return
	}

	if *inPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "Error: -in and -out are required.")
		fmt.Fprintln(os.Stderr)
		flag.Usage()
		os.Exit(2)
	}

	baseOpts := denoise.Options{
		Radius:       *radius,
		Epsilon:      float32(*epsilon),
		Strength:     float32(*strength),
		ApplyMedian:  *median,
		JPEGQuality:  *quality,
		PreserveEXIF: *keepEXIF,
	}

	inInfo, err := os.Stat(*inPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot stat input: %v\n", err)
		os.Exit(1)
	}

	if !inInfo.IsDir() {
		// Single-file mode.
		if err := processOne(*inPath, *outPath, baseOpts, *autoISO, *dryRun, *verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Directory mode.
	jobs, err := collectJobs(*inPath, *outPath, *recursive, *suffix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning input directory: %v\n", err)
		os.Exit(1)
	}
	if len(jobs) == 0 {
		fmt.Println("No JPEG files found.")
		return
	}

	fmt.Printf("Found %d JPEG file(s).\n", len(jobs))
	if *dryRun {
		for _, j := range jobs {
			fmt.Printf("[dry-run] %s -> %s\n", j.src, j.dst)
		}
		return
	}

	// Decide worker count. Each worker holds a decoded 24MP YCbCr (~36MB)
	// plus the guided-filter buffers (~50MB), so we cap by default.
	nw := *workers
	if nw <= 0 {
		nw = runtime.NumCPU() / 2
		if nw < 1 {
			nw = 1
		}
		if nw > 4 {
			nw = 4
		}
	}

	// Ensure the output directory exists.
	if err := os.MkdirAll(*outPath, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
		os.Exit(1)
	}

	start := time.Now()
	var (
		jobCh           = make(chan job, len(jobs))
		errCh           = make(chan error, len(jobs))
		successN, failN int
		mu              sync.Mutex
	)
	var wg sync.WaitGroup
	for w := 0; w < nw; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range jobCh {
				if err := os.MkdirAll(filepath.Dir(j.dst), 0o755); err != nil {
					errCh <- fmt.Errorf("%s: mkdir: %w", j.src, err)
					mu.Lock()
					failN++
					mu.Unlock()
					continue
				}
				if err := processOne(j.src, j.dst, baseOpts, *autoISO, false, *verbose); err != nil {
					errCh <- fmt.Errorf("%s: %w", j.src, err)
					mu.Lock()
					failN++
					mu.Unlock()
					continue
				}
				mu.Lock()
				successN++
				mu.Unlock()
			}
		}(w)
	}
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		fmt.Fprintln(os.Stderr, "Warning:", err)
	}

	elapsed := time.Since(start)
	fmt.Printf("\nDone. %d succeeded, %d failed in %.1fs (%d worker(s)).\n",
		successN, failN, elapsed.Seconds(), nw)
	if failN > 0 {
		os.Exit(1)
	}
}

type job struct {
	src string
	dst string
}

// collectJobs walks the input directory and produces a job per JPEG found.
// The destination path mirrors the input directory structure under outDir.
// If inDir == outDir, the suffix is appended to filenames so we don't
// overwrite the source.
func collectJobs(inDir, outDir string, recursive bool, suffix string) ([]job, error) {
	absIn, _ := filepath.Abs(inDir)
	absOut, _ := filepath.Abs(outDir)
	sameDir := absIn == absOut

	var jobs []job
	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if !recursive && path != inDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !isJPEG(path) {
			return nil
		}
		// Skip files we just produced (avoid double-processing on re-runs).
		base := strings.ToLower(filepath.Base(path))
		if suffix != "" && strings.Contains(base, strings.ToLower(suffix)+".") {
			return nil
		}

		rel, err := filepath.Rel(inDir, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(outDir, rel)
		if sameDir {
			ext := filepath.Ext(rel)
			stem := strings.TrimSuffix(rel, ext)
			dst = filepath.Join(outDir, stem+suffix+ext)
		}
		jobs = append(jobs, job{src: path, dst: dst})
		return nil
	}

	if err := filepath.Walk(inDir, walkFn); err != nil {
		return nil, err
	}
	return jobs, nil
}

func isJPEG(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".jpg" || ext == ".jpeg"
}

func processOne(src, dst string, base denoise.Options, autoISO, dryRun, verbose bool) error {
	opts := base
	tunedNote := ""
	if autoISO {
		tuned := denoise.AutoTuneFromFile(src, base)
		if tuned.Radius != base.Radius || tuned.Epsilon != base.Epsilon {
			tunedNote = fmt.Sprintf(" (auto-iso: radius=%d epsilon=%.4f)", tuned.Radius, tuned.Epsilon)
		}
		opts = tuned
	}

	if dryRun {
		fmt.Printf("[dry-run] %s -> %s%s\n", src, dst, tunedNote)
		return nil
	}

	start := time.Now()
	if err := denoise.ReduceChromaNoiseFile(src, dst, opts); err != nil {
		return err
	}
	if verbose {
		fmt.Printf("OK  %s -> %s  (%.2fs)%s\n", src, dst, time.Since(start).Seconds(), tunedNote)
	} else {
		fmt.Printf("OK  %s%s\n", filepath.Base(src), tunedNote)
	}
	return nil
}