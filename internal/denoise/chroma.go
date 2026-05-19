// Package denoise implements chrominance noise reduction for JPEG files.
//
// The core algorithm is a guided filter (He, Sun & Tang, 2010) applied to
// the Cb/Cr chroma channels of the image while leaving the luma (Y) channel
// untouched. The luma channel is used as the edge-guide for the guided filter,
// which means color smoothing aligns with luma edges and does not bleed across
// boundaries that the human eye perceives as sharp. A small 3x3 median
// pre-pass on the chroma channels removes isolated "color speckle" pixels
// before the guided filter runs.
//
// The pipeline:
//
//	JPEG -> decode (image.YCbCr) -> optional median pre-pass on Cb, Cr
//	     -> guided filter on Cb, Cr (with Y as the guide, downsampled to chroma
//	        resolution if the JPEG uses 4:2:0 or 4:2:2 subsampling)
//	     -> blend with the original chroma by `strength`
//	     -> re-encode JPEG -> copy EXIF (APP1) marker from source
package denoise

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
)

// Options controls the chroma noise reduction pipeline.
type Options struct {
	// Radius is the half-radius of the guided-filter window, in *chroma*
	// pixels. For a 4:2:0 JPEG the chroma planes are half the image size,
	// so a Radius of 8 corresponds to roughly a 16-pixel window in the
	// original full-resolution image. Typical values: 4..16.
	Radius int

	// Epsilon is the guided-filter regularization parameter. Larger values
	// produce smoother chroma (more denoising) at the cost of preserving
	// fewer subtle color details. Typical values: 0.001..0.05 (in the
	// normalized [0, 1] chroma space).
	Epsilon float32

	// Strength is the final blend factor: 0.0 leaves chroma untouched,
	// 1.0 uses the fully-filtered chroma. Typical values: 0.6..1.0.
	Strength float32

	// ApplyMedian, if true, runs a 3x3 median pre-pass on the chroma
	// channels before the guided filter. This is cheap and effectively
	// removes isolated color-speckle pixels.
	ApplyMedian bool

	// JPEGQuality is the output JPEG quality (1..100). 92 is a good
	// default for re-encoding camera JPEGs.
	JPEGQuality int

	// PreserveEXIF, if true, copies the EXIF (APP1) marker from the source
	// JPEG into the destination JPEG so that camera metadata is preserved.
	PreserveEXIF bool
}

// DefaultOptions returns a balanced set of options suitable for typical
// high-ISO camera JPEGs.
func DefaultOptions() Options {
	return Options{
		Radius:       8,
		Epsilon:      0.01,
		Strength:     1.0,
		ApplyMedian:  true,
		JPEGQuality:  92,
		PreserveEXIF: true,
	}
}

// ReduceChromaNoiseFile reads a JPEG file, applies chrominance noise
// reduction, and writes the result to dstPath. EXIF metadata is preserved
// when opts.PreserveEXIF is true.
func ReduceChromaNoiseFile(srcPath, dstPath string, opts Options) error {
	srcBytes, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}

	img, err := jpeg.Decode(bytes.NewReader(srcBytes))
	if err != nil {
		return fmt.Errorf("decode jpeg: %w", err)
	}

	ycbcr, ok := img.(*image.YCbCr)
	if !ok {
		return fmt.Errorf("decoded image is not YCbCr (got %T) -- only JPEG-native YCbCr images are supported", img)
	}

	if err := ReduceChromaNoiseYCbCr(ycbcr, opts); err != nil {
		return fmt.Errorf("chroma denoise: %w", err)
	}

	// Encode to an in-memory buffer first; we may need to splice EXIF in.
	var outBuf bytes.Buffer
	if err := jpeg.Encode(&outBuf, ycbcr, &jpeg.Options{Quality: opts.JPEGQuality}); err != nil {
		return fmt.Errorf("encode jpeg: %w", err)
	}

	finalBytes := outBuf.Bytes()
	if opts.PreserveEXIF {
		exifSeg, err := extractEXIFSegment(srcBytes)
		if err == nil && len(exifSeg) > 0 {
			finalBytes, err = injectEXIFSegment(finalBytes, exifSeg)
			if err != nil {
				return fmt.Errorf("inject exif: %w", err)
			}
		}
	}

	if err := os.WriteFile(dstPath, finalBytes, 0o644); err != nil {
		return fmt.Errorf("write destination: %w", err)
	}
	return nil
}

// ReduceChromaNoise reads a JPEG from r, applies chrominance noise reduction,
// and writes the resulting JPEG to w. EXIF preservation is not supported in
// the stream variant -- use ReduceChromaNoiseFile if EXIF preservation is
// required.
func ReduceChromaNoise(r io.Reader, w io.Writer, opts Options) error {
	img, err := jpeg.Decode(r)
	if err != nil {
		return fmt.Errorf("decode jpeg: %w", err)
	}
	ycbcr, ok := img.(*image.YCbCr)
	if !ok {
		return fmt.Errorf("decoded image is not YCbCr (got %T)", img)
	}
	if err := ReduceChromaNoiseYCbCr(ycbcr, opts); err != nil {
		return err
	}
	return jpeg.Encode(w, ycbcr, &jpeg.Options{Quality: opts.JPEGQuality})
}

// ReduceChromaNoiseYCbCr applies chrominance noise reduction in-place on a
// YCbCr image. The Y (luma) plane is read but not written; only the Cb and
// Cr planes are modified. This is the lowest-level entry point and is what
// the editor / interactive preview pipeline should call after decoding.
func ReduceChromaNoiseYCbCr(img *image.YCbCr, opts Options) error {
	if img == nil {
		return fmt.Errorf("nil image")
	}
	if opts.Radius < 1 {
		opts.Radius = 1
	}
	if opts.Epsilon <= 0 {
		opts.Epsilon = 0.01
	}
	if opts.Strength <= 0 {
		// Nothing to do.
		return nil
	}
	if opts.Strength > 1 {
		opts.Strength = 1
	}

	// Chroma plane dimensions depend on the JPEG subsampling.
	// For 4:4:4: Cb/Cr have the same size as Y.
	// For 4:2:2: Cb/Cr have half width, full height.
	// For 4:2:0: Cb/Cr have half width and half height (the common case).
	cw, ch := chromaDimensions(img)
	if cw == 0 || ch == 0 {
		return nil
	}

	// Build a luma plane at the chroma resolution by box-downsampling Y.
	// Using a box filter (average of the 1, 2, or 4 corresponding Y pixels)
	// is what JPEG itself does logically when upsampling chroma, so it is
	// the natural choice for the inverse direction here.
	guide := luminanceAtChromaResolution(img)

	// Stride may differ from width when the YCbCr was allocated by the
	// jpeg decoder; we work with packed [cw*ch]byte slices for both
	// inputs and outputs and re-pack at the end.
	cbPacked := packPlane(img.Cb, img.CStride, cw, ch)
	crPacked := packPlane(img.Cr, img.CStride, cw, ch)

	if opts.ApplyMedian {
		tmp := make([]byte, cw*ch)
		medianFilter3x3(cbPacked, tmp, cw, ch)
		copy(cbPacked, tmp)
		medianFilter3x3(crPacked, tmp, cw, ch)
		copy(crPacked, tmp)
	}

	// Run the guided filter on each chroma plane.
	cbDen := runGuidedOnPlane(guide, cbPacked, cw, ch, opts.Radius, opts.Epsilon)
	crDen := runGuidedOnPlane(guide, crPacked, cw, ch, opts.Radius, opts.Epsilon)

	// Blend with the original by `strength`.
	if opts.Strength < 1 {
		blendBytes(cbDen, cbPacked, opts.Strength)
		blendBytes(crDen, crPacked, opts.Strength)
	}

	// Write back into the image, respecting the original CStride.
	unpackPlane(cbDen, img.Cb, img.CStride, cw, ch)
	unpackPlane(crDen, img.Cr, img.CStride, cw, ch)
	return nil
}

// runGuidedOnPlane runs the guided filter on a single chroma plane.
// `chroma` is uint8 in [0, 255] and is converted to normalized float32
// [0, 1] for the filter, then converted back.
func runGuidedOnPlane(guide []float32, chroma []byte, w, h, r int, eps float32) []byte {
	n := w * h
	p := make([]float32, n)
	q := make([]float32, n)
	for i := 0; i < n; i++ {
		p[i] = float32(chroma[i]) / 255.0
	}
	guidedFilterParallel(guide, p, q, w, h, r, eps)
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		v := q[i] * 255.0
		if v < 0 {
			v = 0
		} else if v > 255 {
			v = 255
		}
		out[i] = byte(v + 0.5)
	}
	return out
}

// blendBytes computes dst = strength*dst + (1-strength)*orig in-place on dst.
func blendBytes(dst, orig []byte, strength float32) {
	w := strength
	w0 := 1 - strength
	for i := range dst {
		v := float32(dst[i])*w + float32(orig[i])*w0
		if v < 0 {
			v = 0
		} else if v > 255 {
			v = 255
		}
		dst[i] = byte(v + 0.5)
	}
}

// chromaDimensions returns the width and height of the Cb/Cr planes of the
// given YCbCr image, based on its subsampling ratio.
func chromaDimensions(img *image.YCbCr) (int, int) {
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
	case image.YCbCrSubsampleRatio411:
		return (w + 3) / 4, h
	case image.YCbCrSubsampleRatio410:
		return (w + 3) / 4, (h + 1) / 2
	}
	// Fallback: assume 4:2:0 which is by far the most common.
	return (w + 1) / 2, (h + 1) / 2
}

// luminanceAtChromaResolution returns the Y plane resampled (by box-average)
// to the chroma plane resolution, packed and normalized to float32 [0, 1].
func luminanceAtChromaResolution(img *image.YCbCr) []float32 {
	cw, ch := chromaDimensions(img)
	yw := img.Rect.Dx()
	yh := img.Rect.Dy()

	// Determine the subsample factor in each dimension.
	sx := (yw + cw - 1) / cw // typically 1 or 2
	sy := (yh + ch - 1) / ch
	if sx < 1 {
		sx = 1
	}
	if sy < 1 {
		sy = 1
	}

	out := make([]float32, cw*ch)
	for cy := 0; cy < ch; cy++ {
		for cx := 0; cx < cw; cx++ {
			var sum int
			var count int
			for dy := 0; dy < sy; dy++ {
				yy := cy*sy + dy
				if yy >= yh {
					break
				}
				rowOff := yy * img.YStride
				for dx := 0; dx < sx; dx++ {
					xx := cx*sx + dx
					if xx >= yw {
						break
					}
					sum += int(img.Y[rowOff+xx])
					count++
				}
			}
			if count == 0 {
				continue
			}
			out[cy*cw+cx] = float32(sum) / float32(count) / 255.0
		}
	}
	return out
}

// packPlane copies a strided byte plane into a packed [w*h]byte slice.
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

// unpackPlane copies a packed [w*h]byte slice into a strided byte plane.
func unpackPlane(src, dst []byte, stride, w, h int) {
	if stride == w {
		copy(dst[:w*h], src)
		return
	}
	for y := 0; y < h; y++ {
		copy(dst[y*stride:y*stride+w], src[y*w:(y+1)*w])
	}
}