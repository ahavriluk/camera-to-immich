// chroma-measure prints chrominance noise statistics for a JPEG file.
//
// It decodes the JPEG, extracts the Cb and Cr planes, and reports:
//
//   - Global std-dev of each chroma plane (mixes real color variation with noise)
//   - High-frequency std-dev: std-dev of the *difference* between each pixel
//     and a 5x5 box-blurred version. This isolates noise from legitimate color
//     content -- low-frequency content cancels out, leaving the noise floor.
//
// Typical readings:
//
//	HF std-dev < 0.5   -> very clean, denoising would just hurt detail
//	HF std-dev 0.5-1.5 -> mild noise, denoising marginally useful
//	HF std-dev 1.5-3.0 -> visible noise, denoising clearly helps
//	HF std-dev > 3.0   -> heavy noise, aggressive denoising warranted
//
// Usage:
//
//	chroma-measure photo.jpg [photo2.jpg ...]
package main

import (
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: chroma-measure <jpeg> [jpeg ...]")
		os.Exit(2)
	}

	fmt.Printf("%-40s %8s %8s %12s %12s %10s\n",
		"file", "Cb std", "Cr std", "Cb HF noise", "Cr HF noise", "verdict")
	fmt.Println(repeat("-", 95))

	for _, path := range os.Args[1:] {
		cbStd, crStd, cbHF, crHF, err := measure(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		verdict := classify(math.Max(cbHF, crHF))
		fmt.Printf("%-40s %8.2f %8.2f %12.2f %12.2f %10s\n",
			truncate(filepath.Base(path), 40), cbStd, crStd, cbHF, crHF, verdict)
	}
}

func measure(path string) (cbStd, crStd, cbHF, crHF float64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return
	}
	yc, ok := img.(*image.YCbCr)
	if !ok {
		err = fmt.Errorf("decoded image is not YCbCr (got %T)", img)
		return
	}
	cbStd = byteStd(yc.Cb)
	crStd = byteStd(yc.Cr)

	cw, ch := chromaDims(yc)
	cb := packPlane(yc.Cb, yc.CStride, cw, ch)
	cr := packPlane(yc.Cr, yc.CStride, cw, ch)
	cbHF = hfNoise(cb, cw, ch, 2)
	crHF = hfNoise(cr, cw, ch, 2)
	return
}

// hfNoise returns the std-dev of (plane - boxBlurred(plane)).
// For a noise-free image this is ~0; for a noisy image it's the per-pixel
// noise amplitude.
func hfNoise(plane []byte, w, h, r int) float64 {
	if len(plane) == 0 {
		return 0
	}
	// Compute box-blurred version using a naive O(w*h*r) implementation
	// (good enough for a measurement tool; not a hot path).
	blurred := make([]byte, w*h)
	for y := 0; y < h; y++ {
		y0 := y - r
		y1 := y + r
		if y0 < 0 {
			y0 = 0
		}
		if y1 >= h {
			y1 = h - 1
		}
		for x := 0; x < w; x++ {
			x0 := x - r
			x1 := x + r
			if x0 < 0 {
				x0 = 0
			}
			if x1 >= w {
				x1 = w - 1
			}
			var sum, count int
			for yy := y0; yy <= y1; yy++ {
				row := yy * w
				for xx := x0; xx <= x1; xx++ {
					sum += int(plane[row+xx])
					count++
				}
			}
			blurred[y*w+x] = byte(sum / count)
		}
	}
	// HF residual std-dev.
	var mean, sum float64
	n := float64(len(plane))
	for i := range plane {
		d := float64(plane[i]) - float64(blurred[i])
		mean += d
	}
	mean /= n
	for i := range plane {
		d := float64(plane[i]) - float64(blurred[i]) - mean
		sum += d * d
	}
	return math.Sqrt(sum / n)
}

func byteStd(xs []byte) float64 {
	if len(xs) == 0 {
		return 0
	}
	var mean float64
	for _, v := range xs {
		mean += float64(v)
	}
	mean /= float64(len(xs))
	var sum float64
	for _, v := range xs {
		d := float64(v) - mean
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(xs)))
}

func chromaDims(img *image.YCbCr) (int, int) {
	w := img.Rect.Dx()
	h := img.Rect.Dy()
	switch img.SubsampleRatio {
	case image.YCbCrSubsampleRatio444:
		return w, h
	case image.YCbCrSubsampleRatio422:
		return (w + 1) / 2, h
	case image.YCbCrSubsampleRatio420:
		return (w + 1) / 2, (h + 1) / 2
	case image.YCbCrSubsampleRatio440:
		return w, (h + 1) / 2
	}
	return (w + 1) / 2, (h + 1) / 2
}

func packPlane(src []byte, stride, w, h int) []byte {
	if stride == w {
		out := make([]byte, w*h)
		copy(out, src[:w*h])
		return out
	}
	out := make([]byte, w*h)
	for y := 0; y < h; y++ {
		copy(out[y*w:(y+1)*w], src[y*stride:y*stride+w])
	}
	return out
}

func classify(hf float64) string {
	switch {
	case hf < 0.5:
		return "very clean"
	case hf < 1.5:
		return "mild"
	case hf < 3.0:
		return "visible"
	default:
		return "heavy"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}