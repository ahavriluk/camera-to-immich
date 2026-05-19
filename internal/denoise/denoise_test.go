package denoise

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// ---------- boxFilter ----------

func TestBoxFilter_FlatImageStaysFlat(t *testing.T) {
	w, h := 17, 13
	src := make([]float32, w*h)
	for i := range src {
		src[i] = 0.5
	}
	dst := make([]float32, w*h)
	boxFilter(src, dst, w, h, 3)
	for i, v := range dst {
		if math.Abs(float64(v-0.5)) > 1e-5 {
			t.Fatalf("flat input was not preserved at index %d: got %f", i, v)
		}
	}
}

func TestBoxFilter_RadiusZeroIsIdentity(t *testing.T) {
	w, h := 5, 4
	src := make([]float32, w*h)
	for i := range src {
		src[i] = float32(i)
	}
	dst := make([]float32, w*h)
	boxFilter(src, dst, w, h, 0)
	for i := range src {
		if dst[i] != src[i] {
			t.Fatalf("radius=0 should be identity, mismatch at %d: %f vs %f", i, dst[i], src[i])
		}
	}
}

func TestBoxFilter_KnownSmallCase(t *testing.T) {
	// 3x3 image with a single non-zero pixel at the center.
	// Box-filter with r=1 averages over a 3x3 window. For the center pixel,
	// the entire image is in the window -> mean = 9/9 = 1.0. For the top-left
	// corner, the in-bounds window is the 2x2 region (0,0)-(1,1) which
	// contains the center=9 -> mean = 9/4 = 2.25.
	w, h := 3, 3
	src := []float32{
		0, 0, 0,
		0, 9, 0,
		0, 0, 0,
	}
	dst := make([]float32, w*h)
	boxFilter(src, dst, w, h, 1)
	if math.Abs(float64(dst[1*w+1]-1.0)) > 1e-5 {
		t.Fatalf("center: expected 1.0, got %f", dst[1*w+1])
	}
	if math.Abs(float64(dst[0]-2.25)) > 1e-5 {
		t.Fatalf("corner: expected 2.25, got %f", dst[0])
	}
}

// ---------- median ----------

func TestMedianFilter3x3_RemovesIsolatedSpike(t *testing.T) {
	w, h := 5, 5
	src := make([]byte, w*h)
	for i := range src {
		src[i] = 100
	}
	src[2*w+2] = 250 // single spike in the center
	dst := make([]byte, w*h)
	medianFilter3x3(src, dst, w, h)
	if dst[2*w+2] != 100 {
		t.Fatalf("center spike not removed by 3x3 median: got %d, expected 100", dst[2*w+2])
	}
}

func TestMedianFilter3x3_PreservesEdge(t *testing.T) {
	// Sharp vertical edge: left half = 50, right half = 200.
	// A 3x3 median should preserve this edge perfectly (median is
	// edge-preserving by construction).
	w, h := 8, 6
	src := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				src[y*w+x] = 50
			} else {
				src[y*w+x] = 200
			}
		}
	}
	dst := make([]byte, w*h)
	medianFilter3x3(src, dst, w, h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			expected := byte(50)
			if x >= w/2 {
				expected = 200
			}
			if dst[y*w+x] != expected {
				t.Fatalf("edge not preserved at (%d,%d): got %d, expected %d",
					x, y, dst[y*w+x], expected)
			}
		}
	}
}

// ---------- guided filter ----------

func TestGuidedFilter_FlatChromaStaysFlat(t *testing.T) {
	w, h := 32, 24
	guide := make([]float32, w*h)
	p := make([]float32, w*h)
	q := make([]float32, w*h)
	for i := range guide {
		guide[i] = float32(i%17) / 17.0 // arbitrary texture in the guide
		p[i] = 0.3                      // perfectly flat chroma
	}
	guidedFilter(guide, p, q, w, h, 4, 0.01)
	for i, v := range q {
		if math.Abs(float64(v-0.3)) > 1e-3 {
			t.Fatalf("flat chroma should stay ~0.3 even with textured guide; at %d got %f", i, v)
		}
	}
}

func TestGuidedFilter_SmoothsRandomNoiseOnFlatChroma(t *testing.T) {
	// Construct a constant guide and a noisy chroma plane. The guided
	// filter should reduce the per-pixel std-dev substantially.
	w, h := 64, 64
	guide := make([]float32, w*h)
	p := make([]float32, w*h)
	q := make([]float32, w*h)
	r := rand.New(rand.NewSource(42))
	const mean = 0.5
	for i := range guide {
		guide[i] = mean
		p[i] = mean + float32((r.Float64()-0.5)*0.2) // +/-0.1 noise
	}
	beforeStd := stdDev(p)
	guidedFilter(guide, p, q, w, h, 4, 0.02)
	afterStd := stdDev(q)
	if afterStd >= beforeStd*0.4 {
		t.Fatalf("guided filter should reduce noise std-dev substantially; before=%.4f after=%.4f", beforeStd, afterStd)
	}
}

func TestGuidedFilter_ParallelMatchesSerial(t *testing.T) {
	// On a small image (below the parallel threshold), parallel and serial
	// should give bit-identical results.
	w, h := 32, 32
	guide := make([]float32, w*h)
	p := make([]float32, w*h)
	r := rand.New(rand.NewSource(7))
	for i := range guide {
		guide[i] = float32(r.Float64())
		p[i] = float32(r.Float64())
	}
	q1 := make([]float32, w*h)
	q2 := make([]float32, w*h)
	guidedFilter(guide, p, q1, w, h, 3, 0.01)
	guidedFilterParallel(guide, p, q2, w, h, 3, 0.01)
	for i := range q1 {
		if math.Abs(float64(q1[i]-q2[i])) > 1e-5 {
			t.Fatalf("serial vs parallel mismatch at %d: %f vs %f", i, q1[i], q2[i])
		}
	}
}

func stdDev(xs []float32) float64 {
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

// ---------- chroma dimensions ----------

func TestChromaDimensions(t *testing.T) {
	cases := []struct {
		ratio              image.YCbCrSubsampleRatio
		w, h               int
		wantCW, wantCH     int
	}{
		{image.YCbCrSubsampleRatio444, 100, 80, 100, 80},
		{image.YCbCrSubsampleRatio422, 100, 80, 50, 80},
		{image.YCbCrSubsampleRatio420, 100, 80, 50, 40},
		{image.YCbCrSubsampleRatio440, 100, 80, 100, 40},
		{image.YCbCrSubsampleRatio420, 101, 81, 51, 41}, // odd dims
	}
	for _, c := range cases {
		img := image.NewYCbCr(image.Rect(0, 0, c.w, c.h), c.ratio)
		cw, ch := chromaDimensions(img)
		if cw != c.wantCW || ch != c.wantCH {
			t.Errorf("ratio=%v %dx%d: got chroma=%dx%d, want %dx%d",
				c.ratio, c.w, c.h, cw, ch, c.wantCW, c.wantCH)
		}
	}
}

// ---------- end-to-end JPEG round-trip ----------

func TestReduceChromaNoise_RoundTripReducesChromaNoise(t *testing.T) {
	// Build a small synthetic image: solid mid-gray with random Cb/Cr noise.
	// After denoising the chroma planes should be much smoother.
	w, h := 128, 96
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	r := rand.New(rand.NewSource(123))
	for i := range img.Y {
		img.Y[i] = 128 // flat gray
	}
	for i := range img.Cb {
		// 128 +/- 30 random chroma noise
		img.Cb[i] = byte(128 + (r.Intn(61) - 30))
		img.Cr[i] = byte(128 + (r.Intn(61) - 30))
	}

	beforeCbStd := byteStdDev(img.Cb)
	beforeCrStd := byteStdDev(img.Cr)

	opts := DefaultOptions()
	opts.Radius = 6
	opts.Epsilon = 0.02
	opts.PreserveEXIF = false // synthetic image has no EXIF

	// Encode -> denoise (in stream form) -> decode and inspect.
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode synthetic JPEG: %v", err)
	}
	var denoised bytes.Buffer
	if err := ReduceChromaNoise(&encoded, &denoised, opts); err != nil {
		t.Fatalf("ReduceChromaNoise: %v", err)
	}
	out, err := jpeg.Decode(&denoised)
	if err != nil {
		t.Fatalf("decode denoised: %v", err)
	}
	outYCbCr, ok := out.(*image.YCbCr)
	if !ok {
		t.Fatalf("decoded denoised image is not YCbCr: %T", out)
	}
	afterCbStd := byteStdDev(outYCbCr.Cb)
	afterCrStd := byteStdDev(outYCbCr.Cr)

	// Expect at least 40% reduction in chroma noise std-dev. (JPEG re-encoding
	// also smooths a bit on its own, but the guided filter is the dominant
	// effect here.)
	if afterCbStd >= beforeCbStd*0.6 {
		t.Errorf("Cb noise not sufficiently reduced: before=%.2f after=%.2f", beforeCbStd, afterCbStd)
	}
	if afterCrStd >= beforeCrStd*0.6 {
		t.Errorf("Cr noise not sufficiently reduced: before=%.2f after=%.2f", beforeCrStd, afterCrStd)
	}

	// Sanity: luma should still be ~128 everywhere (we never touch it).
	for i, y := range outYCbCr.Y {
		if y < 110 || y > 145 {
			t.Errorf("luma drifted at index %d: got %d (expected ~128)", i, y)
			break
		}
	}
}

func byteStdDev(xs []byte) float64 {
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

func TestReduceChromaNoiseYCbCr_StrengthZeroIsNoOp(t *testing.T) {
	w, h := 64, 48
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	r := rand.New(rand.NewSource(1))
	for i := range img.Y {
		img.Y[i] = byte(r.Intn(256))
	}
	for i := range img.Cb {
		img.Cb[i] = byte(r.Intn(256))
		img.Cr[i] = byte(r.Intn(256))
	}
	origCb := append([]byte(nil), img.Cb...)
	origCr := append([]byte(nil), img.Cr...)

	opts := DefaultOptions()
	opts.Strength = 0
	if err := ReduceChromaNoiseYCbCr(img, opts); err != nil {
		t.Fatalf("ReduceChromaNoiseYCbCr: %v", err)
	}
	if !bytes.Equal(origCb, img.Cb) || !bytes.Equal(origCr, img.Cr) {
		t.Fatalf("strength=0 should be a no-op but chroma was modified")
	}
}

// ---------- EXIF round-trip ----------

func TestExtractAndInjectEXIFSegment(t *testing.T) {
	// Build a minimal JPEG by hand:
	//   SOI | JFIF (APP0) | APP1-Exif (synthetic) | ... payload ... | EOI
	// We only need the marker structure to be valid; the payloads can be
	// arbitrary as long as their length fields are correct.

	exifPayload := []byte("Exif\x00\x00" + "<<fake exif body>>")
	exifSeg := buildSegment(0xFFE1, exifPayload)

	jfifPayload := []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00")
	jfifSeg := buildSegment(0xFFE0, jfifPayload)

	// A trivial SOS+entropy+EOI tail so the parser sees a complete file.
	tail := []byte{0xFF, 0xDA, 0x00, 0x02, 0x00, // SOS with minimal length
		0xAB, 0xCD, // fake entropy data
		0xFF, 0xD9} // EOI

	jpegA := []byte{0xFF, 0xD8} // SOI
	jpegA = append(jpegA, jfifSeg...)
	jpegA = append(jpegA, exifSeg...)
	jpegA = append(jpegA, tail...)

	// Extract should find our exif segment.
	extracted, err := extractEXIFSegment(jpegA)
	if err != nil {
		t.Fatalf("extractEXIFSegment failed: %v", err)
	}
	if !bytes.Equal(extracted, exifSeg) {
		t.Fatalf("extracted EXIF segment does not match original\n  got:  %x\n  want: %x", extracted, exifSeg)
	}

	// Build a second JPEG with NO EXIF (just SOI + JFIF + tail).
	jpegB := []byte{0xFF, 0xD8}
	jpegB = append(jpegB, jfifSeg...)
	jpegB = append(jpegB, tail...)

	injected, err := injectEXIFSegment(jpegB, exifSeg)
	if err != nil {
		t.Fatalf("injectEXIFSegment failed: %v", err)
	}

	// Now extracting from the injected JPEG should return the same segment.
	round, err := extractEXIFSegment(injected)
	if err != nil {
		t.Fatalf("round-trip extract failed: %v", err)
	}
	if !bytes.Equal(round, exifSeg) {
		t.Fatalf("round-trip EXIF mismatch")
	}

	// Injecting again should REPLACE rather than duplicate.
	injected2, err := injectEXIFSegment(injected, exifSeg)
	if err != nil {
		t.Fatalf("second inject failed: %v", err)
	}
	count := countAPP1EXIF(injected2)
	if count != 1 {
		t.Fatalf("after re-injection, expected exactly 1 APP1-EXIF segment, got %d", count)
	}
}

// buildSegment formats: [marker][2-byte length (incl length bytes)][payload]
func buildSegment(marker uint16, payload []byte) []byte {
	segLen := 2 + len(payload)
	out := make([]byte, 4+len(payload))
	out[0] = byte(marker >> 8)
	out[1] = byte(marker)
	out[2] = byte(segLen >> 8)
	out[3] = byte(segLen)
	copy(out[4:], payload)
	return out
}

func countAPP1EXIF(jpegBytes []byte) int {
	count := 0
	i := 2
	for i+4 <= len(jpegBytes) {
		if jpegBytes[i] != 0xFF {
			return count
		}
		for i < len(jpegBytes) && jpegBytes[i] == 0xFF {
			i++
		}
		if i >= len(jpegBytes) {
			break
		}
		marker := uint16(0xFF00) | uint16(jpegBytes[i])
		i++
		if marker == 0xFFD8 || marker == 0xFFD9 || (marker >= 0xFFD0 && marker <= 0xFFD7) {
			continue
		}
		if marker == 0xFFDA {
			break
		}
		if i+2 > len(jpegBytes) {
			return count
		}
		segLen := int(jpegBytes[i])<<8 | int(jpegBytes[i+1])
		if marker == 0xFFE1 && i+segLen <= len(jpegBytes) {
			payload := jpegBytes[i+2 : i+segLen]
			if len(payload) >= 6 && string(payload[0:6]) == "Exif\x00\x00" {
				count++
			}
		}
		i += segLen
	}
	return count
}

// ---------- ISO auto-tuning ----------

func TestAutoTuneFromISO_ScalesUpWithISO(t *testing.T) {
	base := Options{Radius: 6, Epsilon: 0.01}
	lo := AutoTuneFromISO(100, base)
	mid := AutoTuneFromISO(1600, base)
	hi := AutoTuneFromISO(12800, base)
	if !(lo.Radius <= mid.Radius && mid.Radius <= hi.Radius) {
		t.Errorf("radius should grow monotonically with ISO: %d, %d, %d", lo.Radius, mid.Radius, hi.Radius)
	}
	if !(lo.Epsilon <= mid.Epsilon && mid.Epsilon <= hi.Epsilon) {
		t.Errorf("epsilon should grow monotonically with ISO: %f, %f, %f", lo.Epsilon, mid.Epsilon, hi.Epsilon)
	}
	if hi.Radius == lo.Radius {
		t.Errorf("ISO 12800 should produce a larger radius than ISO 100, got both = %d", lo.Radius)
	}
}

func TestAutoTuneFromISO_ZeroReturnsBase(t *testing.T) {
	base := Options{Radius: 6, Epsilon: 0.01}
	got := AutoTuneFromISO(0, base)
	if got.Radius != base.Radius || got.Epsilon != base.Epsilon {
		t.Errorf("ISO=0 should return base unchanged, got %+v", got)
	}
}

// ---------- end-to-end file round-trip (no EXIF in source) ----------

func TestReduceChromaNoiseFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "in.jpg")
	dstPath := filepath.Join(dir, "out.jpg")

	// Build a small noisy JPEG on disk.
	w, h := 64, 48
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 128, G: 128, B: 128, A: 255})
		}
	}
	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatalf("create src: %v", err)
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode src: %v", err)
	}
	f.Close()

	opts := DefaultOptions()
	opts.PreserveEXIF = false // synthetic image has no EXIF; helper would no-op anyway
	if err := ReduceChromaNoiseFile(srcPath, dstPath, opts); err != nil {
		t.Fatalf("ReduceChromaNoiseFile: %v", err)
	}

	info, err := os.Stat(dstPath)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if info.Size() < 100 {
		t.Fatalf("denoised output suspiciously small: %d bytes", info.Size())
	}

	// Verify the output is decodable.
	df, err := os.Open(dstPath)
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer df.Close()
	if _, err := jpeg.Decode(df); err != nil {
		t.Fatalf("decode dst: %v", err)
	}
}