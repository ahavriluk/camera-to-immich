package denoise

import (
	"runtime"
	"sync"
)

// guidedFilter implements the edge-preserving Guided Filter of He, Sun & Tang
// (CVPR 2010 / TPAMI 2013, "Guided Image Filtering").
//
// Inputs (all row-major float32 planes of dimensions w x h):
//
//	guide  - the guidance image (luminance / Y plane), values in [0, 1]
//	p      - the input plane to filter (e.g. a chroma channel), values in [0, 1]
//	r      - half-radius of the box window in pixels (typical 4-16 for chroma)
//	eps    - regularization parameter (smoothing strength); typical 0.001 - 0.05
//	         Larger eps -> more aggressive smoothing.
//
// Output:
//
//	q      - the filtered plane (same dimensions as p)
//
// Algorithm in five box-filter passes (each O(1) per pixel):
//
//	mu_I    = boxFilter(I)
//	mu_p    = boxFilter(p)
//	corr_I  = boxFilter(I .* I)
//	corr_Ip = boxFilter(I .* p)
//	var_I   = corr_I  - mu_I .* mu_I
//	cov_Ip  = corr_Ip - mu_I .* mu_p
//	a       = cov_Ip ./ (var_I + eps)
//	b       = mu_p - a .* mu_I
//	mu_a    = boxFilter(a)
//	mu_b    = boxFilter(b)
//	q       = mu_a .* I + mu_b
//
// Because q is a local linear transform of I, chroma edges in q
// snap to luminance edges in I -- which is exactly what we want for
// chrominance noise reduction (color smoothing should not bleed across
// edges that the eye sees in luma).
func guidedFilter(guide, p, q []float32, w, h, r int, eps float32) {
	n := w * h

	// Temporary buffers. We keep them all in one allocation block to be
	// nice to the allocator on large images.
	buf := make([]float32, 9*n)
	muI := buf[0*n : 1*n]
	muP := buf[1*n : 2*n]
	corrI := buf[2*n : 3*n]
	corrIp := buf[3*n : 4*n]
	II := buf[4*n : 5*n]
	IP := buf[5*n : 6*n]
	a := buf[6*n : 7*n]
	b := buf[7*n : 8*n]
	tmp := buf[8*n : 9*n]

	// Precompute element-wise products I*I and I*p.
	for i := 0; i < n; i++ {
		II[i] = guide[i] * guide[i]
		IP[i] = guide[i] * p[i]
	}

	boxFilter(guide, muI, w, h, r)
	boxFilter(p, muP, w, h, r)
	boxFilter(II, corrI, w, h, r)
	boxFilter(IP, corrIp, w, h, r)

	for i := 0; i < n; i++ {
		varI := corrI[i] - muI[i]*muI[i]
		covIp := corrIp[i] - muI[i]*muP[i]
		a[i] = covIp / (varI + eps)
		b[i] = muP[i] - a[i]*muI[i]
	}

	boxFilter(a, tmp, w, h, r)
	copy(a, tmp)
	boxFilter(b, tmp, w, h, r)
	copy(b, tmp)

	for i := 0; i < n; i++ {
		q[i] = a[i]*guide[i] + b[i]
	}
}

// guidedFilterParallel is the same as guidedFilter but splits the per-pixel
// element-wise stages across goroutines for large images. The box filter
// itself is already O(1) per pixel and very fast, so the per-pixel arithmetic
// stages (multiplies, divides) are where parallelism helps most on multi-core
// CPUs.
func guidedFilterParallel(guide, p, q []float32, w, h, r int, eps float32) {
	n := w * h
	if n < 1<<16 { // < ~256k pixels: not worth spinning goroutines
		guidedFilter(guide, p, q, w, h, r, eps)
		return
	}

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}

	buf := make([]float32, 9*n)
	muI := buf[0*n : 1*n]
	muP := buf[1*n : 2*n]
	corrI := buf[2*n : 3*n]
	corrIp := buf[3*n : 4*n]
	II := buf[4*n : 5*n]
	IP := buf[5*n : 6*n]
	a := buf[6*n : 7*n]
	b := buf[7*n : 8*n]
	tmp := buf[8*n : 9*n]

	parallelFor(n, workers, func(start, end int) {
		for i := start; i < end; i++ {
			II[i] = guide[i] * guide[i]
			IP[i] = guide[i] * p[i]
		}
	})

	// Box filters could in principle be parallelized across rows
	// (horizontal pass) and columns (vertical pass), but each call is
	// already linear-time and memory-bandwidth bound. We run them
	// sequentially for simplicity.
	boxFilter(guide, muI, w, h, r)
	boxFilter(p, muP, w, h, r)
	boxFilter(II, corrI, w, h, r)
	boxFilter(IP, corrIp, w, h, r)

	parallelFor(n, workers, func(start, end int) {
		for i := start; i < end; i++ {
			varI := corrI[i] - muI[i]*muI[i]
			covIp := corrIp[i] - muI[i]*muP[i]
			a[i] = covIp / (varI + eps)
			b[i] = muP[i] - a[i]*muI[i]
		}
	})

	boxFilter(a, tmp, w, h, r)
	copy(a, tmp)
	boxFilter(b, tmp, w, h, r)
	copy(b, tmp)

	parallelFor(n, workers, func(start, end int) {
		for i := start; i < end; i++ {
			q[i] = a[i]*guide[i] + b[i]
		}
	})
}

// parallelFor splits the index range [0, n) into roughly equal chunks across
// the given number of worker goroutines and calls fn(start, end) on each.
func parallelFor(n, workers int, fn func(start, end int)) {
	if workers <= 1 {
		fn(0, n)
		return
	}
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		start := w * chunk
		if start >= n {
			break
		}
		end := start + chunk
		if end > n {
			end = n
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			fn(s, e)
		}(start, end)
	}
	wg.Wait()
}