package cpprofile

import (
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"

	// Side-effect: register additional decoders if available.
	_ "image/png"
)

// AccuracyReport summarizes pixel-level similarity between two images.
type AccuracyReport struct {
	PathA       string
	PathB       string
	Width       int
	Height      int
	Samples     int
	RMSESrgb    float64 // RMSE in sRGB 0..255 (perceptually rough)
	MeanDeltaE  float64 // mean CIE76 dE in Lab
	P95DeltaE   float64 // 95th-percentile dE
	MaxDeltaE   float64 // worst-pixel dE
}

// Compare loads two JPGs and computes RMSE + Lab dE76 statistics over a
// downsampled grid of pixels (default ~200k samples for speed). Returns
// an AccuracyReport ready to print. The two images must have identical
// dimensions; otherwise an error is returned.
//
// dE76 is used (rather than dE2000) for speed and because we want a
// stable, single-number metric for prototype iteration. Values < 2 are
// generally imperceptible; 2..5 perceivable on close inspection; 5..10
// clearly visible; >10 large.
func Compare(pathA, pathB string) (*AccuracyReport, error) {
	imgA, err := loadImage(pathA)
	if err != nil {
		return nil, fmt.Errorf("load A: %v", err)
	}
	imgB, err := loadImage(pathB)
	if err != nil {
		return nil, fmt.Errorf("load B: %v", err)
	}
	ba := imgA.Bounds()
	bb := imgB.Bounds()

	// If sizes differ (RT renders full sensor, OOC may crop slightly),
	// resample both onto a shared low-res grid for a same-orientation
	// per-pixel comparison. A center-crop to a common aspect is required
	// first to avoid corner offset bias.
	const cmpW, cmpH = 600, 450 // 4:3 grid
	if ba.Dx() != bb.Dx() || ba.Dy() != bb.Dy() {
		imgA = resampleCenterCrop(imgA, cmpW, cmpH)
		imgB = resampleCenterCrop(imgB, cmpW, cmpH)
		ba = imgA.Bounds()
		bb = imgB.Bounds()
	}

	rep := &AccuracyReport{
		PathA:  pathA,
		PathB:  pathB,
		Width:  ba.Dx(),
		Height: ba.Dy(),
	}

	// Choose stride so that we sample roughly 200k pixels.
	const targetSamples = 200_000
	total := ba.Dx() * ba.Dy()
	stride := 1
	if total > targetSamples {
		stride = int(math.Sqrt(float64(total) / float64(targetSamples)))
		if stride < 1 {
			stride = 1
		}
	}

	var sumSq float64
	deltas := make([]float64, 0, targetSamples+1024)
	var sumDe float64
	var maxDe float64

	for y := ba.Min.Y; y < ba.Max.Y; y += stride {
		for x := ba.Min.X; x < ba.Max.X; x += stride {
			ra, ga, ba2, _ := imgA.At(x, y).RGBA()
			rb, gb, bb2, _ := imgB.At(x, y).RGBA()
			r1, g1, b1 := float64(ra>>8), float64(ga>>8), float64(ba2>>8)
			r2, g2, b2 := float64(rb>>8), float64(gb>>8), float64(bb2>>8)

			dr := r1 - r2
			dg := g1 - g2
			db := b1 - b2
			sumSq += dr*dr + dg*dg + db*db

			L1, a1, B1 := srgbToLab(r1, g1, b1)
			L2, a2, B2 := srgbToLab(r2, g2, b2)
			dL := L1 - L2
			da := a1 - a2
			dB := B1 - B2
			de := math.Sqrt(dL*dL + da*da + dB*dB)
			deltas = append(deltas, de)
			sumDe += de
			if de > maxDe {
				maxDe = de
			}
		}
	}

	n := len(deltas)
	rep.Samples = n
	if n == 0 {
		return rep, nil
	}
	rep.RMSESrgb = math.Sqrt(sumSq / float64(n*3))
	rep.MeanDeltaE = sumDe / float64(n)
	rep.MaxDeltaE = maxDe
	rep.P95DeltaE = percentile(deltas, 0.95)
	rep.Samples = n
	return rep, nil
}

// resampleCenterCrop centre-crops src to the (w/h) aspect ratio of the
// destination, then nearest-neighbour downscales to dstW x dstH. This is
// a quick-and-dirty resampler suitable for accuracy comparison; not for
// production image output.
func resampleCenterCrop(src image.Image, dstW, dstH int) image.Image {
	sb := src.Bounds()
	srcW, srcH := sb.Dx(), sb.Dy()
	if srcW == 0 || srcH == 0 {
		return image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	}
	// Center-crop to destination aspect.
	targetAspect := float64(dstW) / float64(dstH)
	srcAspect := float64(srcW) / float64(srcH)
	var cropW, cropH int
	if srcAspect > targetAspect {
		// Source too wide: crop sides.
		cropH = srcH
		cropW = int(float64(srcH) * targetAspect)
	} else {
		// Source too tall: crop top/bottom.
		cropW = srcW
		cropH = int(float64(srcW) / targetAspect)
	}
	cropX := sb.Min.X + (srcW-cropW)/2
	cropY := sb.Min.Y + (srcH-cropH)/2

	// Nearest-neighbour downscale.
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	for dy := 0; dy < dstH; dy++ {
		sy := cropY + dy*cropH/dstH
		for dx := 0; dx < dstW; dx++ {
			sx := cropX + dx*cropW/dstW
			r, g, b, a := src.At(sx, sy).RGBA()
			i := dst.PixOffset(dx, dy)
			dst.Pix[i+0] = uint8(r >> 8)
			dst.Pix[i+1] = uint8(g >> 8)
			dst.Pix[i+2] = uint8(b >> 8)
			dst.Pix[i+3] = uint8(a >> 8)
		}
	}
	return dst
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	// Simple in-place sort would mutate; copy is fine for ~200k floats.
	cp := make([]float64, len(xs))
	copy(cp, xs)
	// insertion-via-sort.Float64s requires import; use a quick sort fallback.
	quicksort(cp, 0, len(cp)-1)
	idx := int(float64(len(cp)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func quicksort(a []float64, lo, hi int) {
	if lo >= hi {
		return
	}
	pivot := a[(lo+hi)/2]
	i, j := lo, hi
	for i <= j {
		for a[i] < pivot {
			i++
		}
		for a[j] > pivot {
			j--
		}
		if i <= j {
			a[i], a[j] = a[j], a[i]
			i++
			j--
		}
	}
	quicksort(a, lo, j)
	quicksort(a, i, hi)
}

// ---------- sRGB <-> Lab (D65) ----------

func srgbToLinear(c float64) float64 {
	c /= 255.0
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func srgbToLab(r, g, b float64) (L, A, B float64) {
	lr := srgbToLinear(r)
	lg := srgbToLinear(g)
	lb := srgbToLinear(b)
	// Linear sRGB -> XYZ (D65)
	X := lr*0.4124564 + lg*0.3575761 + lb*0.1804375
	Y := lr*0.2126729 + lg*0.7151522 + lb*0.0721750
	Z := lr*0.0193339 + lg*0.1191920 + lb*0.9503041
	// Normalize by D65 white.
	const Xn, Yn, Zn = 0.95047, 1.0, 1.08883
	fx := labF(X / Xn)
	fy := labF(Y / Yn)
	fz := labF(Z / Zn)
	L = 116*fy - 16
	A = 500 * (fx - fy)
	B = 200 * (fy - fz)
	return
}

func labF(t float64) float64 {
	const d = 6.0 / 29.0
	if t > d*d*d {
		return math.Cbrt(t)
	}
	return t/(3*d*d) + 4.0/29.0
}

func loadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Try JPEG first (most common in this pipeline). image.Decode would
	// also work but jpeg.Decode is a little faster.
	if isJPEG(path) {
		return jpeg.Decode(f)
	}
	img, _, err := image.Decode(f)
	return img, err
}

func isJPEG(path string) bool {
	n := len(path)
	if n < 4 {
		return false
	}
	last4 := path[n-4:]
	last5 := ""
	if n >= 5 {
		last5 = path[n-5:]
	}
	return equalFold(last4, ".jpg") || equalFold(last5, ".jpeg")
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}