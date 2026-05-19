package denoise

import "sort"

// medianFilter3x3 applies a 3x3 median filter to a single-channel uint8 plane
// (typically a Cb or Cr chroma channel from a JPEG). It writes the result to
// dst (same dimensions as src). src and dst MUST be different underlying
// slices.
//
// The median filter is the cheapest effective tool against "color speckle" --
// isolated pixels with very different chroma from their neighbors, which is
// the dominant high-ISO chroma-noise pattern.  Running it as a pre-pass before
// the Guided Filter gives a noticeably cleaner result without softening
// legitimate color regions, because a 3x3 median preserves edges by
// construction.
//
// Pixels at the boundary are copied through unchanged (no padding artifacts).
func medianFilter3x3(src, dst []byte, w, h int) {
	if w < 3 || h < 3 {
		copy(dst, src)
		return
	}

	// Copy boundary rows / columns through unchanged.
	// Top row.
	copy(dst[0:w], src[0:w])
	// Bottom row.
	copy(dst[(h-1)*w:h*w], src[(h-1)*w:h*w])
	// Left and right columns of intermediate rows.
	for y := 1; y < h-1; y++ {
		dst[y*w] = src[y*w]
		dst[y*w+(w-1)] = src[y*w+(w-1)]
	}

	var window [9]byte
	for y := 1; y < h-1; y++ {
		rowM := (y - 1) * w
		rowC := y * w
		rowP := (y + 1) * w
		for x := 1; x < w-1; x++ {
			window[0] = src[rowM+x-1]
			window[1] = src[rowM+x]
			window[2] = src[rowM+x+1]
			window[3] = src[rowC+x-1]
			window[4] = src[rowC+x]
			window[5] = src[rowC+x+1]
			window[6] = src[rowP+x-1]
			window[7] = src[rowP+x]
			window[8] = src[rowP+x+1]
			dst[rowC+x] = median9(window)
		}
	}
}

// median9 returns the median of a 9-element byte array.
// Uses sort.Slice on a local copy; for hot paths a hand-rolled sorting
// network would be faster, but this keeps the code simple and the cost
// is dwarfed by the surrounding I/O for typical JPEGs.
func median9(w [9]byte) byte {
	s := w[:]
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[4]
}