package denoise

// boxFilter applies a square box-average filter of half-radius r to a 2D
// single-channel float32 plane stored row-major in src of dimensions w x h.
// It writes the result to dst (same dimensions). src and dst MUST be different
// underlying slices (the implementation is not in-place safe).
//
// The output at (x, y) is the mean of all source pixels in the rectangle
// [x-r..x+r] x [y-r..y+r], clipped to image bounds. Each pixel near the
// boundary is divided by its actual sample count so that the filter does not
// darken the edges.
//
// Implementation: two separable 1-D running-sum passes (horizontal then
// vertical) using an integral-line trick that yields O(1) work per output
// pixel regardless of radius r. Total cost is O(w*h).
//
// This is the key primitive used by the Guided Filter; the entire guided
// filter is built from five boxFilter calls.
func boxFilter(src, dst []float32, w, h, r int) {
	if r <= 0 {
		copy(dst, src)
		return
	}
	if w == 0 || h == 0 {
		return
	}

	tmp := make([]float32, w*h)

	// --- Horizontal pass: src -> tmp ---
	// For each row, maintain a running window sum [x-r, x+r].
	for y := 0; y < h; y++ {
		rowOff := y * w

		// Prime the window for x=0: sum of src[0..min(r, w-1)].
		var sum float32
		windowEnd := r
		if windowEnd > w-1 {
			windowEnd = w - 1
		}
		for k := 0; k <= windowEnd; k++ {
			sum += src[rowOff+k]
		}
		count := windowEnd + 1
		tmp[rowOff] = sum / float32(count)

		for x := 1; x < w; x++ {
			// Add the new pixel entering on the right.
			addIdx := x + r
			if addIdx < w {
				sum += src[rowOff+addIdx]
				count++
			}
			// Remove the pixel leaving on the left.
			subIdx := x - r - 1
			if subIdx >= 0 {
				sum -= src[rowOff+subIdx]
				count--
			}
			tmp[rowOff+x] = sum / float32(count)
		}
	}

	// --- Vertical pass: tmp -> dst ---
	for x := 0; x < w; x++ {
		var sum float32
		windowEnd := r
		if windowEnd > h-1 {
			windowEnd = h - 1
		}
		for k := 0; k <= windowEnd; k++ {
			sum += tmp[k*w+x]
		}
		count := windowEnd + 1
		dst[x] = sum / float32(count)

		for y := 1; y < h; y++ {
			addIdx := y + r
			if addIdx < h {
				sum += tmp[addIdx*w+x]
				count++
			}
			subIdx := y - r - 1
			if subIdx >= 0 {
				sum -= tmp[subIdx*w+x]
				count--
			}
			dst[y*w+x] = sum / float32(count)
		}
	}
}