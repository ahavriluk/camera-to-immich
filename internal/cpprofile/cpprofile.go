// Package cpprofile reads OM System Color Profile EXIF metadata from Olympus
// ORF / JPG / DNG files and generates RawTherapee PP3 partials that
// approximate the in-camera Color Profile rendering (per-hue saturation,
// contrast, sharpness, Shadows / Midtones / Highlights tone curve).
//
// Prototype scope -- see cmd/om-cp-test for the end-to-end driver.
package cpprofile

import (
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// ColorProfile is the decoded OM Color Profile metadata extracted from
// a single image (JPG, ORF or DNG that retained the Olympus MakerNotes).
type ColorProfile struct {
	// PictureMode is e.g. "Color Profile 1" / "Color Profile 2" / "Vivid".
	PictureMode string `json:"pictureMode"`
	// PictureModeNumber, if present (e.g. 1..5 for CP1..CP5), 0 if N/A.
	PictureModeNumber int `json:"pictureModeNumber"`

	// PictureModeSaturation / Contrast / Sharpness scoped to the Picture
	// Mode (range -2 .. +2 on OM-3).
	PMSaturation int `json:"pmSaturation"`
	PMContrast   int `json:"pmContrast"`
	PMSharpness  int `json:"pmSharpness"`

	// Gradation preset string, e.g. "Normal", "High Key", "Low Key",
	// "Auto"; sometimes "Normal; User-Selected".
	Gradation string `json:"gradation"`

	// Tone curve adjustments, range -7 .. +7 each.
	ToneHighlights int `json:"toneHighlights"`
	ToneMidtones   int `json:"toneMidtones"`
	ToneShadows    int `json:"toneShadows"`

	// 12 per-hue saturation offsets in range -5 .. +5 with the OM band
	// names. Bands populated from ColorProfileSettings tag; zero map
	// means no Color Profile data was present in the file.
	Bands map[string]int `json:"bands"`

	// Raw tag values, kept for debugging/inspection.
	Raw map[string]string `json:"raw"`
}

// OM Color Profile band names in the canonical order the camera lists
// them (yellow first, then proceeding clockwise on the HSV wheel: yellow,
// orange, ..., yellow-green).
var BandOrder = []string{
	"Yellow",
	"Orange",
	"Orange-red",
	"Red",
	"Magenta",
	"Violet",
	"Blue",
	"Blue-cyan",
	"Cyan",
	"Green-cyan",
	"Green",
	"Yellow-green",
}

// bandHueHSV maps each OM band name to the approximate HSV hue angle (deg)
// that the camera UI tints it with. Values are calibrated to give a
// reasonable visual match in the RawTherapee HSV Equalizer; refine after
// the first ColorChecker calibration.
var bandHueHSV = map[string]float64{
	"Yellow":       60,
	"Orange":       35,
	"Orange-red":   20,
	"Red":          0,
	"Magenta":      320,
	"Violet":       285,
	"Blue":         240,
	"Blue-cyan":    210,
	"Cyan":         180,
	"Green-cyan":   165,
	"Green":        120,
	"Yellow-green": 90,
}

// BandHueHSVDeg exposes the band->HSV-degree table for callers that want
// to render the mapping (e.g. for accuracy reports).
func BandHueHSVDeg(band string) (float64, bool) {
	v, ok := bandHueHSV[band]
	return v, ok
}

// Read extracts the Color Profile metadata from the given image file
// using exiftool. The file may be JPG/ORF/DNG/TIFF -- anything exiftool
// can read Olympus MakerNotes from.
func Read(path string) (*ColorProfile, error) {
	// exiftool -j -s emits a JSON array with one object keyed by short
	// tag names (no group prefix). Adding -G0 forces group-prefixed keys
	// like "MakerNotes:PictureMode" which our consumer doesn't want.
	cmd := exec.Command("exiftool",
		"-j", "-s",
		"-Olympus:PictureMode",
		"-Olympus:PictureModeSaturation",
		"-Olympus:PictureModeContrast",
		"-Olympus:PictureModeSharpness",
		"-Olympus:Gradation",
		"-Olympus:ToneLevel",
		"-Olympus:ColorProfileSettings",
		"-Olympus:CustomSaturation",
		"-Olympus:ContrastSetting",
		"-Olympus:SharpnessSetting",
		"-Olympus:ColorCreatorEffect",
		"-Olympus:ColorMatrix",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("exiftool: %v", err)
	}

	var arr []map[string]any
	if err := json.Unmarshal(out, &arr); err != nil {
		return nil, fmt.Errorf("parse exiftool json: %v", err)
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("exiftool returned empty result for %s", path)
	}
	rec := arr[0]

	cp := &ColorProfile{
		Bands: map[string]int{},
		Raw:   map[string]string{},
	}
	for k, v := range rec {
		if s, ok := v.(string); ok {
			cp.Raw[k] = s
		} else {
			cp.Raw[k] = fmt.Sprintf("%v", v)
		}
	}

	cp.PictureMode = asString(rec["PictureMode"])
	if n, ok := extractPictureModeNumber(cp.PictureMode); ok {
		cp.PictureModeNumber = n
	}
	cp.PMSaturation = parseLeadingInt(asString(rec["PictureModeSaturation"]))
	cp.PMContrast = parseLeadingInt(asString(rec["PictureModeContrast"]))
	cp.PMSharpness = parseLeadingInt(asString(rec["PictureModeSharpness"]))
	cp.Gradation = asString(rec["Gradation"])

	tl := asString(rec["ToneLevel"])
	cp.ToneHighlights, cp.ToneMidtones, cp.ToneShadows = parseToneLevel(tl)

	cps := asString(rec["ColorProfileSettings"])
	cp.Bands = parseColorProfileSettings(cps)

	return cp, nil
}

// IsActive reports whether the EXIF data describes one of the OM-3
// "Color Profile" picture modes (CP1..CP5). Other Picture Modes (Vivid,
// Natural, ...) will not populate ColorProfileSettings.
func (c *ColorProfile) IsActive() bool {
	return c != nil && strings.Contains(strings.ToLower(c.PictureMode), "color profile")
}

// ============================================================
// Parsing helpers
// ============================================================

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

// parseLeadingInt extracts the first integer (possibly negative) it sees
// in s. Returns 0 if none.
//
//	"1 (min -2, max 2)"   -> 1
//	"-2 (min -2, max 2)"  -> -2
//	""                    -> 0
func parseLeadingInt(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Walk char by char to capture optional sign + digits.
	start := -1
	end := -1
	for i, r := range s {
		if start == -1 {
			if r == '-' || r == '+' || (r >= '0' && r <= '9') {
				start = i
			}
			continue
		}
		if r < '0' || r > '9' {
			end = i
			break
		}
	}
	if start == -1 {
		return 0
	}
	if end == -1 {
		end = len(s)
	}
	n, _ := strconv.Atoi(s[start:end])
	return n
}

// extractPictureModeNumber pulls e.g. 2 out of "Color Profile 2; 2".
func extractPictureModeNumber(pm string) (int, bool) {
	// PictureMode tag often looks like "Color Profile 2; 2".
	parts := strings.Split(pm, ";")
	if len(parts) >= 2 {
		n, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
		if err == nil {
			return n, true
		}
	}
	// Fallback: trailing digit on the name.
	for i := len(pm) - 1; i >= 0; i-- {
		if pm[i] >= '0' && pm[i] <= '9' {
			j := i
			for j > 0 && pm[j-1] >= '0' && pm[j-1] <= '9' {
				j--
			}
			n, err := strconv.Atoi(pm[j : i+1])
			if err == nil {
				return n, true
			}
			break
		}
	}
	return 0, false
}

// parseToneLevel decodes the ToneLevel tag, e.g.:
//
//	"Highlights; 1; -7; 7; Shadows; -1; -7; 7; Midtones; 0; -7; 7; 0; 0; ..."
//
// Returns highlights, midtones, shadows in that order. Missing fields are 0.
func parseToneLevel(s string) (hi, mid, sh int) {
	parts := strings.Split(s, ";")
	for i := 0; i+1 < len(parts); i++ {
		label := strings.TrimSpace(parts[i])
		switch label {
		case "Highlights":
			if i+1 < len(parts) {
				hi = parseLeadingInt(parts[i+1])
			}
		case "Midtones":
			if i+1 < len(parts) {
				mid = parseLeadingInt(parts[i+1])
			}
		case "Shadows":
			if i+1 < len(parts) {
				sh = parseLeadingInt(parts[i+1])
			}
		}
	}
	return
}

// parseColorProfileSettings decodes the ColorProfileSettings tag, e.g.:
//
//	"Min -5; Max 5; Yellow 1; Orange 2; Orange-red 3; Red -1; Magenta -2;
//	 Violet -2; Blue -1; Blue-cyan 2; Cyan 3; Green-cyan -2; Green -3;
//	 Yellow-green -1"
//
// Returns a map keyed by band name with integer offsets.
func parseColorProfileSettings(s string) map[string]int {
	out := map[string]int{}
	if s == "" {
		return out
	}
	parts := strings.Split(s, ";")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Split on the last space: "Orange-red 3" -> "Orange-red", "3".
		idx := strings.LastIndex(p, " ")
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(p[:idx])
		val := strings.TrimSpace(p[idx+1:])
		if name == "Min" || name == "Max" {
			continue
		}
		n, err := strconv.Atoi(val)
		if err != nil {
			continue
		}
		// Only keep recognized band names.
		if _, ok := bandHueHSV[name]; ok {
			out[name] = n
		}
	}
	return out
}

// ============================================================
// PP3 partial generation
// ============================================================

// PP3Options controls scaling constants from OM units to RawTherapee
// units. Defaults are reasonable starting values; tune via calibration.
type PP3Options struct {
	// SatScale: per-band sat in OM units (-5..+5) maps to RT SCurve
	// offset of v/5 * SatScale around the neutral 0.5 baseline. 0.4 means
	// OM +5 -> offset 0.9, OM -5 -> 0.1.
	SatScale float64
	// ContrastScale: RT [Exposure] Contrast = pmContrast * ContrastScale.
	// 12 makes OM +-2 land at +-24.
	ContrastScale int
	// SharpnessBaseAmount and SharpnessStep: RT USM Amount =
	// SharpnessBaseAmount + pmSharpness * SharpnessStep.
	SharpnessBaseAmount int
	SharpnessStep       int
	// SharpnessRadius: fixed USM radius in pixels.
	SharpnessRadius float64
	// ToneEVScale: tone Highlights/Midtones/Shadows in OM units (-7..+7)
	// maps to RT Tone Equalizer band offset of v/7 * ToneEVScale EV.
	ToneEVScale float64
}

// DefaultPP3Options returns the starting-point scaling constants.
//
// Notes from initial visual smoke test on OM-3 P5170402/3/4 vs OOC JPGs:
//   - HSV Equalizer Saturation curve applies global per-hue saturation
//     but ALSO causes mild hue shifts at strong settings, which made
//     blues/greens look off.
//   - The base PP3 (OM3_Default_DNG.pp3) already encodes an aggressive
//     film-like Curve in [Exposure], so writing Contrast= here was double-
//     dipping. Default ContrastScale is now 6 (subtle).
//   - SatScale dropped to 0.20 -- OM ±5 maps to ±20% chroma shift, which
//     is closer to the OOC visual range than the earlier 0.4.
//   - ToneEVScale dropped to 0.8 because the base curve already supplies
//     strong contrast.
func DefaultPP3Options() PP3Options {
	return PP3Options{
		SatScale:            0.20,
		ContrastScale:       6,
		SharpnessBaseAmount: 100,
		SharpnessStep:       50,
		SharpnessRadius:     0.8,
		ToneEVScale:         0.8,
	}
}

// SCurvePoint is a single control point for the RawTherapee HSV
// Equalizer Saturation curve.
type SCurvePoint struct {
	Hue float64 // normalized 0..1
	Sat float64 // 0..1, where 0.5 = neutral
}

// BuildSCurvePoints converts the 12 OM band offsets into RT HSV Equalizer
// Saturation curve control points, sorted by ascending normalized hue.
func BuildSCurvePoints(bands map[string]int, opt PP3Options) []SCurvePoint {
	pts := make([]SCurvePoint, 0, 12)
	for _, name := range BandOrder {
		hueDeg, ok := bandHueHSV[name]
		if !ok {
			continue
		}
		v := bands[name] // 0 if missing -> neutral
		// Clamp v to [-5, +5].
		if v > 5 {
			v = 5
		}
		if v < -5 {
			v = -5
		}
		offset := 0.5 + (float64(v)/5.0)*opt.SatScale
		if offset < 0 {
			offset = 0
		}
		if offset > 1 {
			offset = 1
		}
		pts = append(pts, SCurvePoint{
			Hue: hueDeg / 360.0,
			Sat: offset,
		})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Hue < pts[j].Hue })
	return pts
}

// formatSCurve serializes points into the RT linear-piecewise curve
// string: "1;hue;sat;0.5;0.5;...". curve_type 1 = linear; 0.5/0.5 are
// the default left/right tangents (ignored for linear, kept for shape
// compatibility).
func formatSCurve(pts []SCurvePoint) string {
	var b strings.Builder
	b.WriteString("1")
	for _, p := range pts {
		fmt.Fprintf(&b, ";%.3f;%.3f;0.5;0.5", p.Hue, p.Sat)
	}
	b.WriteString(";")
	return b.String()
}

// BuildPP3Partial returns a PP3 fragment (as a string) that:
//   - enables [Luminance Curve] with a `CHCurve` (per-hue chromaticity
//     curve) carrying the 12-band Color Profile saturation offsets.
//     This is the canonical RT module for "make X-colored areas more/
//     less saturated" -- it operates on Lab chromaticity and produces
//     no hue shifts, unlike the HSV Equalizer Saturation curve.
//   - enables [ToneEqualizer] with Shadows/Midtones/Highlights derived
//     from the OM ToneLevel tag (-7..+7 each).
//   - sets global [Exposure] Contrast scaled from OM's -2..+2.
//   - sets USM [Sharpening] scaled from OM's -2..+2.
//
// Caller is expected to merge this fragment over a base PP3 (RawTherapee
// applies later -p flags on top of earlier ones).
func BuildPP3Partial(cp *ColorProfile, opt PP3Options) string {
	scPoints := BuildSCurvePoints(cp.Bands, opt)
	chCurve := formatCHCurve(scPoints)

	contrast := cp.PMContrast * opt.ContrastScale

	// Sharpness mapping. At OM -2 turn USM off so we do not over-sharpen
	// a soft frame; otherwise enable USM with Amount = base + step * s.
	shEnabled := "true"
	shAmount := opt.SharpnessBaseAmount + cp.PMSharpness*opt.SharpnessStep
	if shAmount < 0 {
		shAmount = 0
	}
	if cp.PMSharpness <= -2 {
		shEnabled = "false"
	}

	// Tone equalizer bands in EV. ToneEqualizer has 6 bands in modern RT
	// (Band0..Band5). We place Shadows -> Band1, Midtones -> Band2,
	// Highlights -> Band3, leaving 0 and 4,5 neutral.
	bandShadows := float64(cp.ToneShadows) / 7.0 * opt.ToneEVScale
	bandMidtones := float64(cp.ToneMidtones) / 7.0 * opt.ToneEVScale
	bandHighlights := float64(cp.ToneHighlights) / 7.0 * opt.ToneEVScale

	tonEqEnabled := bandShadows != 0 || bandMidtones != 0 || bandHighlights != 0

	var sb strings.Builder
	sb.WriteString("# Auto-generated by camera-to-immich/cpprofile from EXIF\n")
	fmt.Fprintf(&sb, "# Source PictureMode=%q PMContrast=%d PMSharpness=%d Gradation=%q\n",
		cp.PictureMode, cp.PMContrast, cp.PMSharpness, cp.Gradation)
	fmt.Fprintf(&sb, "# Tone HL/Mid/Sh = %d/%d/%d  ToneEVScale=%.2f\n",
		cp.ToneHighlights, cp.ToneMidtones, cp.ToneShadows, opt.ToneEVScale)
	fmt.Fprintf(&sb, "# Bands: ")
	for _, n := range BandOrder {
		fmt.Fprintf(&sb, "%s=%d ", n, cp.Bands[n])
	}
	sb.WriteString("\n\n")

	sb.WriteString("[Exposure]\n")
	fmt.Fprintf(&sb, "Contrast=%d\n\n", contrast)

	sb.WriteString("[Sharpening]\n")
	fmt.Fprintf(&sb, "Enabled=%s\n", shEnabled)
	sb.WriteString("Method=usm\n")
	fmt.Fprintf(&sb, "Radius=%.2f\n", opt.SharpnessRadius)
	fmt.Fprintf(&sb, "Amount=%d\n", shAmount)
	sb.WriteString("Threshold=20;80;2000;1200;\n\n")

	if tonEqEnabled {
		// RT 5.12 ToneEqualizer rejects fractional Band values via the
		// GLib KeyFile parser ("value that cannot be interpreted"). The
		// Band* keys are integers in the RT schema where 100 ~ 1 EV.
		toInt := func(ev float64) int {
			n := int(math.Round(ev * 100))
			if n > 100 {
				n = 100
			}
			if n < -100 {
				n = -100
			}
			return n
		}
		sb.WriteString("[ToneEqualizer]\n")
		sb.WriteString("Enabled=true\n")
		fmt.Fprintf(&sb, "Band0=%d\n", 0)
		fmt.Fprintf(&sb, "Band1=%d\n", toInt(bandShadows))
		fmt.Fprintf(&sb, "Band2=%d\n", toInt(bandMidtones))
		fmt.Fprintf(&sb, "Band3=%d\n", toInt(bandHighlights))
		fmt.Fprintf(&sb, "Band4=%d\n", 0)
		fmt.Fprintf(&sb, "Band5=%d\n", 0)
		sb.WriteString("Regularization=4\n")
		sb.WriteString("Pivot=0\n\n")
	}

	// Use Lab Adjustments CCcurve via [Luminance Curve] CHCurve. Per
	// RT docs, CHcurve = "Chromaticity vs Hue" -- exactly the OM Color
	// Profile semantics. SCurve in HSV Equalizer was producing hue
	// shifts in blues/greens at the previous scale.
	sb.WriteString("[Luminance Curve]\n")
	sb.WriteString("Enabled=true\n")
	sb.WriteString("Brightness=0\n")
	sb.WriteString("Contrast=0\n")
	sb.WriteString("Chromaticity=0\n")
	fmt.Fprintf(&sb, "chCurve=%s\n", chCurve)

	return sb.String()
}

// formatCHCurve serializes points into RT's CHCurve (chromaticity vs hue)
// linear-piecewise curve string. Format: "1; hue; chroma; ltan; rtan; ...".
// Neutral chroma is 0.5; range 0..1 (0 = halve chroma, 1 = boost chroma).
func formatCHCurve(pts []SCurvePoint) string {
	var b strings.Builder
	b.WriteString("1")
	for _, p := range pts {
		fmt.Fprintf(&b, ";%.3f;%.3f;0.5;0.5", p.Hue, p.Sat)
	}
	b.WriteString(";")
	return b.String()
}

// Round helper used in tests.
func round3(x float64) float64 { return math.Round(x*1000) / 1000 }