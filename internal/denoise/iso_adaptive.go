package denoise

import (
	"math"
	"os"

	"github.com/rwcarlsen/goexif/exif"
)

// AutoTuneFromFile inspects the ISO speed recorded in the JPEG's EXIF metadata
// and returns a copy of base whose Radius and Epsilon are scaled accordingly.
// Higher ISO -> stronger denoising. If the ISO cannot be read, the supplied
// base options are returned unchanged.
//
// The scaling curve is calibrated for typical interchangeable-lens cameras
// (Olympus / Canon / Nikon / Sony / Fuji APS-C and m4/3 sensors), which is the
// target audience of this tool:
//
//	  ISO  100..400   -> barely any extra smoothing (most cameras are clean)
//	  ISO  800        -> mild extra smoothing
//	  ISO 1600..3200  -> moderate extra smoothing (clearly visible chroma noise)
//	  ISO 6400+       -> aggressive smoothing
//
// The curve uses log2(ISO/100) as the "stops over base" metric, which matches
// how sensor noise grows approximately linearly with each ISO doubling.
func AutoTuneFromFile(path string, base Options) Options {
	iso, ok := readISOSpeed(path)
	if !ok {
		return base
	}
	return AutoTuneFromISO(iso, base)
}

// AutoTuneFromISO returns a copy of base whose Radius and Epsilon are scaled
// for the given ISO value. See AutoTuneFromFile for the calibration rationale.
//
// Scaling formulas:
//
//	stops    = log2(max(ISO, 100) / 100)         -- "stops above ISO 100"
//	radius   = round(baseRadius * (1 + 0.35 * stops))
//	epsilon  = baseEpsilon * 2^(0.6 * stops)
//
// Both are clamped to sensible upper bounds so a wildly high ISO value
// cannot make the filter pathologically expensive or destructive.
func AutoTuneFromISO(iso int, base Options) Options {
	if iso <= 0 {
		return base
	}
	if iso < 100 {
		iso = 100
	}

	stops := math.Log2(float64(iso) / 100.0)
	if stops < 0 {
		stops = 0
	}

	r := float64(base.Radius) * (1.0 + 0.35*stops)
	if r < 1 {
		r = 1
	}
	if r > 32 {
		r = 32
	}

	eps := float64(base.Epsilon) * math.Pow(2.0, 0.6*stops)
	if eps < 1e-5 {
		eps = 1e-5
	}
	if eps > 0.2 {
		eps = 0.2
	}

	tuned := base
	tuned.Radius = int(math.Round(r))
	tuned.Epsilon = float32(eps)
	return tuned
}

// readISOSpeed reads the ISOSpeedRatings tag from a JPEG file. Returns
// (iso, true) on success; (0, false) if the file is not a JPEG, has no EXIF,
// or has no ISO tag.
func readISOSpeed(path string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return 0, false
	}
	tag, err := x.Get(exif.ISOSpeedRatings)
	if err != nil {
		return 0, false
	}
	v, err := tag.Int(0)
	if err != nil {
		return 0, false
	}
	if v <= 0 {
		return 0, false
	}
	return v, true
}