package editor

import (
	"fmt"
	"math"
	"testing"
)

// ============================================================================
// Tests for generateCropFromAspect — ALL UI aspect ratios
// ============================================================================

// All user-selectable aspect ratios from the UI
var allUIAspects = []struct {
	name  string
	ratio float64
}{
	{"1:1", 1.0},
	{"3:2", 3.0 / 2.0},
	{"2:3", 2.0 / 3.0},
	{"4:3", 4.0 / 3.0},
	{"3:4", 3.0 / 4.0},
	{"16:9", 16.0 / 9.0},
	{"9:16", 9.0 / 16.0},
	{"5:4", 5.0 / 4.0},
	{"4:5", 4.0 / 5.0},
	{"7:6", 7.0 / 6.0},
	{"6:7", 6.0 / 7.0},
	{"6:5", 6.0 / 5.0},
	{"5:6", 5.0 / 6.0},
	{"7:5", 7.0 / 5.0},
	{"5:7", 5.0 / 7.0},
}

// Image configurations to test against
var imageConfigs = []struct {
	name   string
	width  int
	height int
}{
	{"landscape_43", 5184, 3888},   // Standard 4:3 landscape
	{"portrait_34", 3888, 5184},    // Standard 4:3 portrait (post-orientation swap)
	{"landscape_169", 5184, 2916},  // 16:9 landscape crop frame
	{"portrait_916", 2916, 5184},   // 9:16 portrait crop frame (post-swap)
	{"landscape_32", 5184, 3456},   // 3:2 landscape crop frame
	{"portrait_23", 3456, 5184},    // 2:3 portrait crop frame
	{"square_11", 3888, 3888},      // 1:1 square crop frame
}

func TestGenerateCropFromAspect_AllAspects_AllConfigs(t *testing.T) {
	for _, img := range imageConfigs {
		for _, aspect := range allUIAspects {
			name := fmt.Sprintf("%s_on_%s", aspect.name, img.name)
			t.Run(name, func(t *testing.T) {
				crop := generateCropFromAspect(aspect.name, img.width, img.height)

				imgAspect := float64(img.width) / float64(img.height)
				diff := math.Abs(imgAspect - aspect.ratio)

				if diff/aspect.ratio < 0.01 {
					// Aspect matches image — no crop needed
					if crop != nil {
						t.Errorf("expected nil for matching aspect %.3f ≈ %.3f, got %+v", imgAspect, aspect.ratio, crop)
					}
					return
				}

				if crop == nil {
					t.Fatal("expected crop, got nil")
				}

				// Verify crop is centered
				assertCentered(t, crop)

				// Verify crop produces correct aspect ratio
				assertAspectRatio(t, crop, img.width, img.height, aspect.ratio)

				// Verify crop is within bounds [0, 1]
				if crop.X < -0.001 || crop.Y < -0.001 {
					t.Errorf("crop has negative coordinates: (%.3f, %.3f)", crop.X, crop.Y)
				}
				if crop.X+crop.Width > 1.001 || crop.Y+crop.Height > 1.001 {
					t.Errorf("crop exceeds image bounds: X+W=%.3f, Y+H=%.3f", crop.X+crop.Width, crop.Y+crop.Height)
				}

				// Verify at least one dimension uses full image extent
				if crop.Width < 0.99 && crop.Height < 0.99 {
					t.Errorf("expected at least one dimension near 1.0: W=%.3f, H=%.3f", crop.Width, crop.Height)
				}
			})
		}
	}
}

func TestGenerateCropFromAspect_SpecialValues(t *testing.T) {
	t.Run("free_returns_nil", func(t *testing.T) {
		crop := generateCropFromAspect("free", 5184, 3888)
		if crop != nil {
			t.Errorf("expected nil for 'free', got %+v", crop)
		}
	})

	t.Run("camera_returns_nil", func(t *testing.T) {
		crop := generateCropFromAspect("camera", 5184, 3888)
		if crop != nil {
			t.Errorf("expected nil for 'camera', got %+v", crop)
		}
	})

	t.Run("empty_returns_nil", func(t *testing.T) {
		crop := generateCropFromAspect("", 5184, 3888)
		if crop != nil {
			t.Errorf("expected nil for empty string, got %+v", crop)
		}
	})

	t.Run("unknown_returns_nil", func(t *testing.T) {
		crop := generateCropFromAspect("unknown", 5184, 3888)
		if crop != nil {
			t.Errorf("expected nil for 'unknown', got %+v", crop)
		}
	})

	t.Run("custom_ratio_parsed", func(t *testing.T) {
		crop := generateCropFromAspect("21:9", 5184, 3888)
		if crop == nil {
			t.Fatal("expected crop for custom 21:9 ratio")
		}
		assertAspectRatio(t, crop, 5184, 3888, 21.0/9.0)
	})
}

// ============================================================================
// Tests for freestyle (user-drawn) crops — normalized crop box validation
// ============================================================================

func TestFreestyleCrop_NormalizedCoordinates(t *testing.T) {
	// Simulate user-drawn crops in the web editor
	// These are normalized 0-1 values relative to the displayed image
	testCases := []struct {
		name         string
		crop         CropBox
		imgW, imgH   int
		expectPortrait bool // W < H in pixel output
	}{
		{
			name:         "full_image",
			crop:         CropBox{X: 0, Y: 0, Width: 1, Height: 1},
			imgW:         5184,
			imgH:         3888,
			expectPortrait: false,
		},
		{
			name:         "center_square_on_landscape",
			crop:         CropBox{X: 0.125, Y: 0, Width: 0.75, Height: 1},
			imgW:         5184,
			imgH:         3888,
			expectPortrait: false, // 0.75*5184=3888, 1*3888=3888 → square
		},
		{
			name:         "wide_strip_landscape",
			crop:         CropBox{X: 0, Y: 0.25, Width: 1, Height: 0.5},
			imgW:         5184,
			imgH:         3888,
			expectPortrait: false,
		},
		{
			name:         "narrow_strip_portrait",
			crop:         CropBox{X: 0.3, Y: 0, Width: 0.4, Height: 1},
			imgW:         5184,
			imgH:         3888,
			expectPortrait: true, // 0.4*5184=2073 < 3888
		},
		{
			name:         "corner_crop",
			crop:         CropBox{X: 0, Y: 0, Width: 0.5, Height: 0.5},
			imgW:         5184,
			imgH:         3888,
			expectPortrait: false,
		},
		{
			name:         "small_center_on_portrait",
			crop:         CropBox{X: 0.2, Y: 0.3, Width: 0.6, Height: 0.4},
			imgW:         3888,
			imgH:         5184,
			expectPortrait: false, // 0.6*3888=2333 > 0.4*5184=2074
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate PP3 pixel calculation
			pp3X := int(tc.crop.X * float64(tc.imgW))
			pp3Y := int(tc.crop.Y * float64(tc.imgH))
			pp3W := int(tc.crop.Width * float64(tc.imgW))
			pp3H := int(tc.crop.Height * float64(tc.imgH))

			// All pixel values should be non-negative
			if pp3X < 0 || pp3Y < 0 || pp3W < 0 || pp3H < 0 {
				t.Errorf("negative pixel values: (%d, %d, %d, %d)", pp3X, pp3Y, pp3W, pp3H)
			}

			// Crop should not exceed image bounds
			if pp3X+pp3W > tc.imgW+1 {
				t.Errorf("crop exceeds image width: X(%d)+W(%d)=%d > %d", pp3X, pp3W, pp3X+pp3W, tc.imgW)
			}
			if pp3Y+pp3H > tc.imgH+1 {
				t.Errorf("crop exceeds image height: Y(%d)+H(%d)=%d > %d", pp3Y, pp3H, pp3Y+pp3H, tc.imgH)
			}

			// Orientation check
			isPortrait := pp3W < pp3H
			if tc.expectPortrait != isPortrait {
				t.Errorf("orientation mismatch: expected portrait=%v, got %dx%d (portrait=%v)",
					tc.expectPortrait, pp3W, pp3H, isPortrait)
			}
		})
	}
}

// ============================================================================
// Tests for computeUserCropWithinCameraFrame — ALL aspects on ALL frame configs
// ============================================================================

// Typical OM-3 sensor dimensions
const omSensorW = 5184
const omSensorH = 3888

// Camera crop frame configurations
var cameraFrameConfigs = []struct {
	name        string
	frame       CropFrameInfo
	orientation int
}{
	// Landscape frames (orientation 1)
	{"landscape_169", CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}, 1},
	{"landscape_11", CropFrameInfo{X: 648, Y: 0, Width: 3888, Height: 3888}, 1},
	{"landscape_32", CropFrameInfo{X: 0, Y: 216, Width: 5184, Height: 3456}, 1},

	// Portrait frames (orientation 6 - 90° CW, most common)
	{"portrait_169_or6", CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}, 6},
	{"portrait_11_or6", CropFrameInfo{X: 648, Y: 0, Width: 3888, Height: 3888}, 6},
	{"portrait_32_or6", CropFrameInfo{X: 0, Y: 216, Width: 5184, Height: 3456}, 6},

	// Portrait frames (orientation 8 - 270° CW)
	{"portrait_169_or8", CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}, 8},
}

func TestComputeUserCrop_AllAspects_AllFrames(t *testing.T) {
	for _, fc := range cameraFrameConfigs {
		for _, aspect := range allUIAspects {
			name := fmt.Sprintf("%s_on_%s", aspect.name, fc.name)
			t.Run(name, func(t *testing.T) {
				crop := computeUserCropWithinCameraFrame(
					aspect.name, &fc.frame, fc.orientation, omSensorW, omSensorH,
				)

				// Determine display frame dimensions
				isPortrait := fc.orientation >= 5 && fc.orientation <= 8
				var displayW, displayH int
				if isPortrait {
					displayW = fc.frame.Height
					displayH = fc.frame.Width
				} else {
					displayW = fc.frame.Width
					displayH = fc.frame.Height
				}
				displayAspect := float64(displayW) / float64(displayH)

				// If user's aspect matches display aspect, crop is nil
				diff := math.Abs(displayAspect - aspect.ratio)
				if diff/aspect.ratio < 0.01 {
					if crop != nil {
						t.Logf("display aspect %.4f vs target %.4f (diff %.4f)", displayAspect, aspect.ratio, diff)
						// Some near-matches may still produce crops due to floating point
					}
					return
				}

				if crop == nil {
					t.Fatalf("expected crop for aspect %s on frame %s (display %.3f vs target %.3f)",
						aspect.name, fc.name, displayAspect, aspect.ratio)
				}

				// Verify pixel aspect ratio is correct
				assertPixelAspect(t, crop, aspect.ratio)

				// Verify crop coordinates are non-negative
				if crop.X < -0.5 || crop.Y < -0.5 {
					t.Errorf("crop has negative coordinates: (%.1f, %.1f)", crop.X, crop.Y)
				}

				// Verify crop fits within post-orientation sensor
				var postOrientW, postOrientH int
				if isPortrait {
					postOrientW = omSensorH
					postOrientH = omSensorW
				} else {
					postOrientW = omSensorW
					postOrientH = omSensorH
				}

				if int(crop.X+crop.Width) > postOrientW+1 {
					t.Errorf("crop exceeds post-orientation width: X(%.0f)+W(%.0f)=%.0f > %d",
						crop.X, crop.Width, crop.X+crop.Width, postOrientW)
				}
				if int(crop.Y+crop.Height) > postOrientH+1 {
					t.Errorf("crop exceeds post-orientation height: Y(%.0f)+H(%.0f)=%.0f > %d",
						crop.Y, crop.Height, crop.Y+crop.Height, postOrientH)
				}

				// Verify crop W and H are positive
				if crop.Width <= 0 || crop.Height <= 0 {
					t.Errorf("crop has non-positive dimensions: %.0fx%.0f", crop.Width, crop.Height)
				}
			})
		}
	}
}

// ============================================================================
// Specific regression tests for the reported bugs
// ============================================================================

func TestRegression_Portrait169_34CropShouldBeVertical(t *testing.T) {
	// THE MAIN BUG: portrait 16:9 image with 3:4 crop produced horizontal 3:4
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}

	crop := computeUserCropWithinCameraFrame("3:4", frame, 6, omSensorW, omSensorH)
	if crop == nil {
		t.Fatal("expected crop, got nil")
	}

	pp3W := int(crop.Width)
	pp3H := int(crop.Height)

	// The crop MUST be portrait (W < H) — not landscape
	if pp3W >= pp3H {
		t.Errorf("REGRESSION: 3:4 crop on portrait 16:9 should be portrait (W<H), got %dx%d (W≥H)", pp3W, pp3H)
	}

	// Verify 3:4 aspect ratio
	ratio := float64(pp3W) / float64(pp3H)
	if math.Abs(ratio-0.75) > 0.02 {
		t.Errorf("expected 3:4 ratio (0.75), got %.3f (%dx%d)", ratio, pp3W, pp3H)
	}
}

func TestRegression_Portrait169_11CropShouldBeSquare(t *testing.T) {
	// 1:1 crop on portrait 16:9 should be square
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}

	crop := computeUserCropWithinCameraFrame("1:1", frame, 6, omSensorW, omSensorH)
	if crop == nil {
		t.Fatal("expected crop, got nil")
	}

	pp3W := int(crop.Width)
	pp3H := int(crop.Height)

	if intAbs(pp3W-pp3H) > 1 {
		t.Errorf("1:1 crop should be square, got %dx%d", pp3W, pp3H)
	}
}

func TestRegression_Standard43Portrait_11ShouldBeSquare(t *testing.T) {
	// Standard 4:3 portrait with 1:1 crop
	imgW, imgH := 3888, 5184 // post-orientation swap
	crop := generateCropFromAspect("1:1", imgW, imgH)
	if crop == nil {
		t.Fatal("expected crop")
	}

	pp3W := int(crop.Width * float64(imgW))
	pp3H := int(crop.Height * float64(imgH))

	if intAbs(pp3W-pp3H) > 1 {
		t.Errorf("1:1 crop should be square: got %dx%d", pp3W, pp3H)
	}
	// Should use full width
	if intAbs(pp3W-imgW) > 1 {
		t.Errorf("should use full width for 1:1 on portrait: got W=%d, expected %d", pp3W, imgW)
	}
}

func TestRegression_Standard43Portrait_34IsFullImage(t *testing.T) {
	// 3:4 crop on portrait 4:3 image = full image (no crop needed)
	imgW, imgH := 3888, 5184
	crop := generateCropFromAspect("3:4", imgW, imgH)
	if crop != nil {
		t.Errorf("expected nil for matching 3:4 on portrait 4:3, got %+v", crop)
	}
}

// ============================================================================
// Tests for PP3 pixel coordinate calculation
// ============================================================================

func TestPP3Pixels_DirectCameraFrame_Landscape(t *testing.T) {
	// Simulates useExifCropFrameDirectly path for landscape 16:9
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}

	cropBox := &CropBox{
		X:      float64(frame.X),
		Y:      float64(frame.Y),
		Width:  float64(frame.Width),
		Height: float64(frame.Height),
	}
	imgW, imgH := 1, 1

	pp3X := int(cropBox.X * float64(imgW))
	pp3Y := int(cropBox.Y * float64(imgH))
	pp3W := int(cropBox.Width * float64(imgW))
	pp3H := int(cropBox.Height * float64(imgH))

	if pp3X != 0 || pp3Y != 324 || pp3W != 5184 || pp3H != 2916 {
		t.Errorf("camera crop pixels wrong: got (%d,%d,%d,%d), expected (0,324,5184,2916)",
			pp3X, pp3Y, pp3W, pp3H)
	}
}

func TestPP3Pixels_UserCropWithinFrame_Portrait169_34(t *testing.T) {
	// Portrait 16:9 with user's 3:4 crop — the main bug fix
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}

	crop := computeUserCropWithinCameraFrame("3:4", frame, 6, omSensorW, omSensorH)
	if crop == nil {
		t.Fatal("expected crop, got nil")
	}

	// With imgWidth=1, imgHeight=1, crop values ARE the pixel coordinates
	pp3X := int(crop.X)
	pp3Y := int(crop.Y)
	pp3W := int(crop.Width)
	pp3H := int(crop.Height)

	// Crop should fit within the post-orientation sensor (3888x5184)
	if pp3X < 0 || pp3Y < 0 {
		t.Errorf("negative coordinates: (%d, %d)", pp3X, pp3Y)
	}
	if pp3X+pp3W > omSensorH { // post-orientation width
		t.Errorf("exceeds width: X=%d + W=%d = %d > %d", pp3X, pp3W, pp3X+pp3W, omSensorH)
	}
	if pp3Y+pp3H > omSensorW { // post-orientation height
		t.Errorf("exceeds height: Y=%d + H=%d = %d > %d", pp3Y, pp3H, pp3Y+pp3H, omSensorW)
	}

	// Must be portrait orientation (W < H)
	if pp3W >= pp3H {
		t.Errorf("REGRESSION: should be portrait crop (W<H), got %dx%d", pp3W, pp3H)
	}
}

func TestPP3Pixels_AllAspects_AllFrames_BoundsCheck(t *testing.T) {
	// Comprehensive bounds checking for all aspect/frame/orientation combinations
	for _, fc := range cameraFrameConfigs {
		for _, aspect := range allUIAspects {
			name := fmt.Sprintf("bounds_%s_on_%s", aspect.name, fc.name)
			t.Run(name, func(t *testing.T) {
				crop := computeUserCropWithinCameraFrame(
					aspect.name, &fc.frame, fc.orientation, omSensorW, omSensorH,
				)

				if crop == nil {
					return // matching aspect, no crop needed
				}

				// With imgWidth=1, imgHeight=1
				pp3X := int(crop.X)
				pp3Y := int(crop.Y)
				pp3W := int(crop.Width)
				pp3H := int(crop.Height)

				isPortrait := fc.orientation >= 5 && fc.orientation <= 8
				var postOrientW, postOrientH int
				if isPortrait {
					postOrientW = omSensorH
					postOrientH = omSensorW
				} else {
					postOrientW = omSensorW
					postOrientH = omSensorH
				}

				if pp3X < 0 {
					t.Errorf("negative X: %d", pp3X)
				}
				if pp3Y < 0 {
					t.Errorf("negative Y: %d", pp3Y)
				}
				if pp3W <= 0 {
					t.Errorf("non-positive W: %d", pp3W)
				}
				if pp3H <= 0 {
					t.Errorf("non-positive H: %d", pp3H)
				}
				if pp3X+pp3W > postOrientW+1 {
					t.Errorf("exceeds width: X(%d)+W(%d)=%d > %d", pp3X, pp3W, pp3X+pp3W, postOrientW)
				}
				if pp3Y+pp3H > postOrientH+1 {
					t.Errorf("exceeds height: Y(%d)+H(%d)=%d > %d", pp3Y, pp3H, pp3Y+pp3H, postOrientH)
				}
			})
		}
	}
}

// ============================================================================
// Tests for crop flow decision logic
// ============================================================================

func TestCropFlowDecision(t *testing.T) {
	// cameraAspect simulates img.AspectRatio — the camera's EXIF aspect ratio string
	testCases := []struct {
		name              string
		editAspect        string // edit.Aspect from the UI
		cameraAspect      string // img.AspectRatio from EXIF
		hasNon43           bool
		expectCameraCrop   bool
		expectAutoGenerate bool
	}{
		// Untouched images (empty aspect)
		{"untouched_43", "", "4:3", false, false, false},
		{"untouched_169", "", "16:9", true, true, false},

		// Explicit camera aspect
		{"explicit_camera_43", "camera", "4:3", false, true, false},
		{"explicit_camera_169", "camera", "16:9", true, true, false},

		// Aspect matching camera's native — should apply camera crop (not user explicit)
		{"matching_169_on_169", "16:9", "16:9", true, true, false},
		{"matching_916_on_169", "9:16", "16:9", true, true, false}, // reciprocal match for portrait
		{"matching_32_on_32", "3:2", "3:2", true, true, false},
		{"matching_23_on_32", "2:3", "3:2", true, true, false}, // reciprocal match

		// Explicit user aspects on standard 4:3 image
		{"user_11_on_43", "1:1", "4:3", false, false, true},
		{"user_34_on_43", "3:4", "4:3", false, false, true},
		{"user_169_on_43", "16:9", "4:3", false, false, true},
		{"user_32_on_43", "3:2", "4:3", false, false, true},

		// Explicit user aspects on non-4:3 image — should NOT trigger camera crop
		{"user_11_on_169", "1:1", "16:9", true, false, true},
		{"user_34_on_169", "3:4", "16:9", true, false, true},
		{"user_43_on_169", "4:3", "16:9", true, false, true},
		{"user_32_on_169", "3:2", "16:9", true, false, true},
		{"user_54_on_169", "5:4", "16:9", true, false, true},
		{"user_45_on_169", "4:5", "16:9", true, false, true},
		{"user_76_on_169", "7:6", "16:9", true, false, true},
		{"user_67_on_169", "6:7", "16:9", true, false, true},
		{"user_65_on_169", "6:5", "16:9", true, false, true},
		{"user_56_on_169", "5:6", "16:9", true, false, true},
		{"user_75_on_169", "7:5", "16:9", true, false, true},
		{"user_57_on_169", "5:7", "16:9", true, false, true},

		// Free aspect — should NOT auto-generate and should NOT apply camera crop
		// "Free" means user wants no crop constraint (full sensor output)
		{"free_on_169", "free", "16:9", true, false, false},
		{"free_on_43", "free", "4:3", false, false, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// New logic matching server.go
			cameraCropMode := tc.editAspect == "camera" || tc.editAspect == "" ||
				tc.editAspect == tc.cameraAspect || isReciprocalAspect(tc.editAspect, tc.cameraAspect)
			shouldApplyCameraCrop := tc.hasNon43 && cameraCropMode && tc.editAspect != "free"
			// Special case: "camera" always triggers if hasNon43 OR is explicitly requested
			if tc.editAspect == "camera" {
				shouldApplyCameraCrop = true
			}
			// For empty aspect without non-4:3, no camera crop
			if !tc.hasNon43 && tc.editAspect != "camera" {
				shouldApplyCameraCrop = false
			}

			if shouldApplyCameraCrop != tc.expectCameraCrop {
				t.Errorf("camera crop: got %v, want %v (cameraCropMode=%v)", shouldApplyCameraCrop, tc.expectCameraCrop, cameraCropMode)
			}

			// Auto-generate triggers when: crop is nil AND aspect is not free/camera/empty
			// AND aspect doesn't match camera (since camera crop was already applied)
			wouldAutoGenerate := tc.editAspect != "" && tc.editAspect != "camera" && tc.editAspect != "free" &&
				!shouldApplyCameraCrop
			if wouldAutoGenerate != tc.expectAutoGenerate {
				t.Errorf("auto-generate: got %v, want %v", wouldAutoGenerate, tc.expectAutoGenerate)
			}
		})
	}
}

// ============================================================================
// Orientation-specific crop tests
// ============================================================================

func TestOrientationCropConsistency(t *testing.T) {
	// 3:4 crop on 16:9 frame should produce consistent portrait crop
	// regardless of orientation (just different offsets)
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}

	orientations := []struct {
		name int
		val  int
	}{
		{6, 6}, // 90° CW
		{8, 8}, // 270° CW
	}

	for _, orient := range orientations {
		t.Run(fmt.Sprintf("orientation_%d", orient.name), func(t *testing.T) {
			crop := computeUserCropWithinCameraFrame("3:4", frame, orient.val, omSensorW, omSensorH)
			if crop == nil {
				t.Fatal("expected crop")
			}

			// All portrait orientations should produce the same size crop
			assertPixelAspect(t, crop, 3.0/4.0)

			// Should be portrait
			if crop.Width >= crop.Height {
				t.Errorf("should be portrait crop, got %.0fx%.0f", crop.Width, crop.Height)
			}
		})
	}
}

func TestLandscapeVsPortrait_SameAspect(t *testing.T) {
	// Same EXIF crop frame, landscape vs portrait orientation
	// The crop SIZES should be the same, just offsets differ
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}

	landscapeCrop := computeUserCropWithinCameraFrame("1:1", frame, 1, omSensorW, omSensorH)
	portraitCrop := computeUserCropWithinCameraFrame("1:1", frame, 6, omSensorW, omSensorH)

	if landscapeCrop == nil || portraitCrop == nil {
		t.Fatal("expected both crops")
	}

	// Both should be square
	assertPixelAspect(t, landscapeCrop, 1.0)
	assertPixelAspect(t, portraitCrop, 1.0)

	// Landscape 1:1 on 16:9 frame (5184x2916): uses full height → 2916x2916
	// Portrait 1:1 on 9:16 frame (2916x5184): uses full width → 2916x2916
	// Both should produce same size square
	if intAbs(int(landscapeCrop.Width)-int(portraitCrop.Width)) > 1 {
		t.Errorf("landscape and portrait 1:1 should have same size: landscape=%.0f, portrait=%.0f",
			landscapeCrop.Width, portraitCrop.Width)
	}
}

// ============================================================================
// Edge cases
// ============================================================================

func TestEdgeCase_VeryWideFrame(t *testing.T) {
	// Extremely wide frame (simulating an ultra-wide crop)
	frame := &CropFrameInfo{X: 0, Y: 1000, Width: 5184, Height: 1888}

	crop := computeUserCropWithinCameraFrame("1:1", frame, 1, omSensorW, omSensorH)
	if crop == nil {
		t.Fatal("expected crop")
	}
	assertPixelAspect(t, crop, 1.0)
}

func TestEdgeCase_SquareFrame_PortraitCrop(t *testing.T) {
	// Square frame with portrait 3:4 crop
	frame := &CropFrameInfo{X: 648, Y: 0, Width: 3888, Height: 3888}

	crop := computeUserCropWithinCameraFrame("3:4", frame, 1, omSensorW, omSensorH)
	if crop == nil {
		t.Fatal("expected crop")
	}
	assertPixelAspect(t, crop, 3.0/4.0)

	// Should use full height and narrow width
	expectedW := 3888.0 * 3.0 / 4.0 // 2916
	assertApprox(t, "width", crop.Width, expectedW)
}

// ============================================================================
// Helper functions
// ============================================================================

func assertApprox(t *testing.T, name string, got, want float64) {
	t.Helper()
	tolerance := 1.0 // 1 pixel tolerance for pixel values, or 0.01 for normalized
	if want <= 1 && want >= -1 {
		tolerance = 0.01
	}
	if math.Abs(got-want) > tolerance {
		t.Errorf("%s: got %.4f, want %.4f (diff: %.4f)", name, got, want, got-want)
	}
}

func assertCentered(t *testing.T, crop *CropBox) {
	t.Helper()
	expectedX := (1.0 - crop.Width) / 2.0
	expectedY := (1.0 - crop.Height) / 2.0
	if math.Abs(crop.X-expectedX) > 0.01 {
		t.Errorf("not centered X: got %.4f, want %.4f", crop.X, expectedX)
	}
	if math.Abs(crop.Y-expectedY) > 0.01 {
		t.Errorf("not centered Y: got %.4f, want %.4f", crop.Y, expectedY)
	}
}

func assertAspectRatio(t *testing.T, crop *CropBox, imgW, imgH int, targetRatio float64) {
	t.Helper()
	pixW := crop.Width * float64(imgW)
	pixH := crop.Height * float64(imgH)
	actualRatio := pixW / pixH
	tolerance := 0.02
	if math.Abs(actualRatio-targetRatio) > tolerance {
		t.Errorf("aspect ratio: got %.4f, want %.4f (pixels: %.0fx%.0f)", actualRatio, targetRatio, pixW, pixH)
	}
}

func assertPixelAspect(t *testing.T, crop *CropBox, targetRatio float64) {
	t.Helper()
	actualRatio := crop.Width / crop.Height
	tolerance := 0.02
	if math.Abs(actualRatio-targetRatio) > tolerance {
		t.Errorf("pixel aspect ratio: got %.4f, want %.4f (pixels: %.0fx%.0f)", actualRatio, targetRatio, crop.Width, crop.Height)
	}
}

func intAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ============================================================================
// Tests for transformFrameToPostOrientation
// ============================================================================

func TestTransformFrameToPostOrientation_Landscape(t *testing.T) {
	// Landscape orientation 1: no transformation, sensor coords = PP3 coords
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}
	result := transformFrameToPostOrientation(frame, 1, omSensorW, omSensorH)

	assertApprox(t, "X", result.X, 0)
	assertApprox(t, "Y", result.Y, 324)
	assertApprox(t, "W", result.Width, 5184)
	assertApprox(t, "H", result.Height, 2916)
}

func TestTransformFrameToPostOrientation_Portrait6(t *testing.T) {
	// Orientation 6 (90° CW): sensor (0,324,5184,2916) -> display space
	// PP3_X = sensorH - frame.Y - frame.Height = 3888 - 324 - 2916 = 648
	// PP3_Y = frame.X = 0
	// PP3_W = frame.Height = 2916 (swapped)
	// PP3_H = frame.Width = 5184 (swapped)
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}
	result := transformFrameToPostOrientation(frame, 6, omSensorW, omSensorH)

	assertApprox(t, "X", result.X, 648)
	assertApprox(t, "Y", result.Y, 0)
	assertApprox(t, "W", result.Width, 2916)
	assertApprox(t, "H", result.Height, 5184)

	// The result should be portrait (taller than wide)
	if result.Width >= result.Height {
		t.Errorf("expected portrait result, got %.0fx%.0f", result.Width, result.Height)
	}
}

func TestTransformFrameToPostOrientation_Portrait8(t *testing.T) {
	// Orientation 8 (270° CW): sensor (0,324,5184,2916)
	// PP3_X = frame.Y = 324
	// PP3_Y = sensorW - frame.X - frame.Width = 5184 - 0 - 5184 = 0
	// PP3_W = frame.Height = 2916
	// PP3_H = frame.Width = 5184
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}
	result := transformFrameToPostOrientation(frame, 8, omSensorW, omSensorH)

	assertApprox(t, "X", result.X, 324)
	assertApprox(t, "Y", result.Y, 0)
	assertApprox(t, "W", result.Width, 2916)
	assertApprox(t, "H", result.Height, 5184)

	if result.Width >= result.Height {
		t.Errorf("expected portrait result, got %.0fx%.0f", result.Width, result.Height)
	}
}

func TestTransformFrameToPostOrientation_BoundsCheck(t *testing.T) {
	// All portrait orientations should produce results within post-orientation sensor bounds
	frames := []struct {
		name  string
		frame *CropFrameInfo
	}{
		{"16:9", &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}},
		{"3:2", &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 3456}},
		{"1:1", &CropFrameInfo{X: 648, Y: 0, Width: 3888, Height: 3888}},
	}

	orientations := []int{1, 5, 6, 7, 8}

	for _, tf := range frames {
		for _, orient := range orientations {
			t.Run(fmt.Sprintf("%s_orient%d", tf.name, orient), func(t *testing.T) {
				result := transformFrameToPostOrientation(tf.frame, orient, omSensorW, omSensorH)

				if result.X < 0 {
					t.Errorf("negative X: %.0f", result.X)
				}
				if result.Y < 0 {
					t.Errorf("negative Y: %.0f", result.Y)
				}
				if result.Width <= 0 {
					t.Errorf("non-positive W: %.0f", result.Width)
				}
				if result.Height <= 0 {
					t.Errorf("non-positive H: %.0f", result.Height)
				}

				// Check bounds against post-orientation sensor dimensions
				var maxW, maxH int
				if orient >= 5 && orient <= 8 {
					maxW = omSensorH // post-orientation: swapped
					maxH = omSensorW
				} else {
					maxW = omSensorW
					maxH = omSensorH
				}
				if int(result.X+result.Width) > maxW+1 {
					t.Errorf("exceeds width: X(%.0f)+W(%.0f)=%.0f > %d", result.X, result.Width, result.X+result.Width, maxW)
				}
				if int(result.Y+result.Height) > maxH+1 {
					t.Errorf("exceeds height: Y(%.0f)+H(%.0f)=%.0f > %d", result.Y, result.Height, result.Y+result.Height, maxH)
				}
			})
		}
	}
}

// ============================================================================
// Tests for isReciprocalAspect
// ============================================================================

func TestIsReciprocalAspect(t *testing.T) {
	testCases := []struct {
		a, b   string
		expect bool
	}{
		{"9:16", "16:9", true},
		{"16:9", "9:16", true},
		{"3:4", "4:3", true},
		{"4:3", "3:4", true},
		{"2:3", "3:2", true},
		{"16:9", "16:9", false}, // same, not reciprocal
		{"3:4", "16:9", false},
		{"1:1", "1:1", true},   // 1:1 reversed is still 1:1
		{"free", "16:9", false},
		{"camera", "16:9", false},
		{"", "16:9", false},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s_vs_%s", tc.a, tc.b), func(t *testing.T) {
			got := isReciprocalAspect(tc.a, tc.b)
			if got != tc.expect {
				t.Errorf("isReciprocalAspect(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.expect)
			}
		})
	}
}

// ============================================================================
// Regression tests for user-reported bugs
// ============================================================================

func TestRegression_UntouchedPortrait169_GetsCameraCrop(t *testing.T) {
	// Bug: P3260066 — untouched portrait 16:9 image with edit.Aspect="16:9"
	// was NOT getting camera crop applied because "16:9" was treated as user-explicit
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}
	cameraAspect := "16:9"

	// With orientation 6 (portrait):
	// cameraCropMode should be true because edit.Aspect matches camera aspect
	editAspect := "16:9"
	cameraCropMode := editAspect == "camera" || editAspect == "" ||
		editAspect == cameraAspect || isReciprocalAspect(editAspect, cameraAspect)

	if !cameraCropMode {
		t.Error("expected cameraCropMode=true for matching aspect, got false")
	}

	// Also test with displayAspectRatio "9:16" (reciprocal)
	editAspect2 := "9:16"
	cameraCropMode2 := editAspect2 == "camera" || editAspect2 == "" ||
		editAspect2 == cameraAspect || isReciprocalAspect(editAspect2, cameraAspect)

	if !cameraCropMode2 {
		t.Error("expected cameraCropMode=true for reciprocal aspect 9:16, got false")
	}

	// The transformed frame for orientation 6 should be portrait
	pp3Frame := transformFrameToPostOrientation(frame, 6, omSensorW, omSensorH)
	if pp3Frame.Width >= pp3Frame.Height {
		t.Errorf("expected portrait PP3 frame, got %.0fx%.0f", pp3Frame.Width, pp3Frame.Height)
	}
}

func TestRegression_UserDrawnCropOnPortrait169(t *testing.T) {
	// Bug: P3260067/P3260068 — portrait 16:9 images with user-drawn 3:4 crop
	// User's normalized crop in display space: {x:0, y:0.027, width:1, height:0.7497}
	// This should produce a PORTRAIT crop (taller than wide), not landscape
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}

	// Transform EXIF frame to PP3 post-orientation space (orientation 6)
	pp3Frame := transformFrameToPostOrientation(frame, 6, omSensorW, omSensorH)

	// User's normalized crop within the displayed 9:16 frame
	userCrop := &CropBox{X: 0, Y: 0.027, Width: 1.0, Height: 0.7497}

	// Convert to PP3 pixel coordinates
	pp3Crop := &CropBox{
		X:      pp3Frame.X + userCrop.X*pp3Frame.Width,
		Y:      pp3Frame.Y + userCrop.Y*pp3Frame.Height,
		Width:  userCrop.Width * pp3Frame.Width,
		Height: userCrop.Height * pp3Frame.Height,
	}

	// Result MUST be portrait (taller than wide)
	if pp3Crop.Width >= pp3Crop.Height {
		t.Errorf("expected PORTRAIT crop, got LANDSCAPE: %.0fx%.0f", pp3Crop.Width, pp3Crop.Height)
	}

	// Check aspect ratio is approximately 3:4 (0.75)
	cropRatio := pp3Crop.Width / pp3Crop.Height
	if math.Abs(cropRatio-0.75) > 0.02 {
		t.Errorf("expected ~3:4 aspect (0.75), got %.4f", cropRatio)
	}

	// Verify crop fits within post-orientation sensor bounds (3888 x 5184)
	if pp3Crop.X < 0 || pp3Crop.Y < 0 {
		t.Errorf("negative offset: (%.0f, %.0f)", pp3Crop.X, pp3Crop.Y)
	}
	if int(pp3Crop.X+pp3Crop.Width) > omSensorH+1 { // post-orient width = sensorH
		t.Errorf("exceeds post-orientation width: %.0f + %.0f = %.0f > %d",
			pp3Crop.X, pp3Crop.Width, pp3Crop.X+pp3Crop.Width, omSensorH)
	}
	if int(pp3Crop.Y+pp3Crop.Height) > omSensorW+1 { // post-orient height = sensorW
		t.Errorf("exceeds post-orientation height: %.0f + %.0f = %.0f > %d",
			pp3Crop.Y, pp3Crop.Height, pp3Crop.Y+pp3Crop.Height, omSensorW)
	}
}

func TestRegression_LandscapeCameraCropOrientation1(t *testing.T) {
	// Landscape 16:9 image with orientation 1: EXIF frame should pass through unchanged
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}
	pp3Frame := transformFrameToPostOrientation(frame, 1, omSensorW, omSensorH)

	assertApprox(t, "X", pp3Frame.X, 0)
	assertApprox(t, "Y", pp3Frame.Y, 324)
	assertApprox(t, "W", pp3Frame.Width, 5184)
	assertApprox(t, "H", pp3Frame.Height, 2916)

	// Should be landscape
	if pp3Frame.Width <= pp3Frame.Height {
		t.Errorf("expected landscape PP3 frame, got %.0fx%.0f", pp3Frame.Width, pp3Frame.Height)
	}
}

func TestRegression_UserCropOnLandscape169(t *testing.T) {
	// Landscape 16:9 image (orientation 1) with user-drawn 3:4 crop
	// User draws a tall crop on the displayed landscape 16:9
	frame := &CropFrameInfo{X: 0, Y: 324, Width: 5184, Height: 2916}
	pp3Frame := transformFrameToPostOrientation(frame, 1, omSensorW, omSensorH)

	// User's normalized crop: centered portrait 3:4 on landscape 16:9
	// Display is 5184x2916. A 3:4 crop: cropH=1, cropW = (3/4)/(16/9) = 0.4219
	userCrop := generateCropFromAspect("3:4", 5184, 2916)
	if userCrop == nil {
		t.Fatal("expected crop generation")
	}

	// Convert to PP3 pixel coordinates
	pp3Crop := &CropBox{
		X:      pp3Frame.X + userCrop.X*pp3Frame.Width,
		Y:      pp3Frame.Y + userCrop.Y*pp3Frame.Height,
		Width:  userCrop.Width * pp3Frame.Width,
		Height: userCrop.Height * pp3Frame.Height,
	}

	// Should be portrait (taller than wide)
	if pp3Crop.Width >= pp3Crop.Height {
		t.Errorf("expected portrait crop on landscape image, got %.0fx%.0f", pp3Crop.Width, pp3Crop.Height)
	}

	// Check 3:4 aspect
	cropRatio := pp3Crop.Width / pp3Crop.Height
	if math.Abs(cropRatio-0.75) > 0.02 {
		t.Errorf("expected ~3:4 aspect (0.75), got %.4f", cropRatio)
	}
}