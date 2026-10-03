package starsmetrics

import (
	"math"
	"sort"
	"testing"
)

// blankImage returns a height x width image of zeros.
func blankImage(width, height int) [][]float64 {
	out := make([][]float64, height)
	for y := range out {
		out[y] = make([]float64, width)
	}
	return out
}

// flatImage returns a height x width image filled with value.
func flatImage(width, height int, value float64) [][]float64 {
	out := blankImage(width, height)
	for y := range out {
		for x := range out[y] {
			out[y][x] = value
		}
	}
	return out
}

// addGaussian stamps an amplitude-amp isotropic Gaussian of the given sigma
// into img at (cx, cy). Callers must ensure the stamp fits inside img.
func addGaussian(img [][]float64, cx, cy int, amp, sigma float64) {
	h, w := len(img), len(img[0])
	s2 := 2 * sigma * sigma
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := float64(x - cx)
			dy := float64(y - cy)
			img[y][x] += amp * math.Exp(-(dx*dx+dy*dy)/s2)
		}
	}
}

// addAnisotropicGaussian stamps an axis-aligned Gaussian with independent
// sigmas, used to give CalculateEccentricity a known eccentricity.
func addAnisotropicGaussian(img [][]float64, cx, cy int, amp, sx, sy float64) {
	h, w := len(img), len(img[0])
	den := 2 * sx * sx
	denY := 2 * sy * sy
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := float64(x - cx)
			dy := float64(y - cy)
			img[y][x] += amp * math.Exp(-(dx*dx/den + dy*dy/denY))
		}
	}
}

// fwhmFactor is 2*sqrt(2*ln2): the FWHM of a Gaussian as a multiple of sigma.
// fwhmFactor is 2*sqrt(2*ln2): the FWHM of a Gaussian as a multiple of sigma.
// A var rather than a const because math.Log is not a constant expression.
var fwhmFactor = 2 * math.Sqrt(2*math.Log(2))

func TestLocalBackgroundFlatImage(t *testing.T) {
	for _, level := range []float64{0, 1, 1000, 12345.5, -50} {
		got := LocalBackground(flatImage(40, 40, level), 20, 20)
		if got != level {
			t.Errorf("flat level %v: got %v, want %v", level, got, level)
		}
	}
}

// TestLocalBackgroundRejectsStars is the reason the estimator takes a median
// over an annulus rather than a mean: a bright source at the centre must not
// drag the local background up.
func TestLocalBackgroundRejectsStars(t *testing.T) {
	const (
		level = 1000.0
		amp   = 50000.0
		sigma = 1.5
	)
	img := flatImage(60, 60, level)
	addGaussian(img, 30, 30, amp, sigma)

	got := LocalBackground(img, 30, 30)
	// The star's wings do reach the inner annulus edge, so a small positive
	// residual is expected; measured at ~5.5 units here.
	if math.Abs(got-level) > 0.02*amp {
		t.Errorf("LocalBackground = %v, want close to level %v (star amp %v)", got, level, amp)
	}
	if got > level+100 {
		t.Errorf("LocalBackground = %v, star leaked into the median", got)
	}
}

// TestLocalBackgroundRejectsSingleHotPixel pins the median's robustness to an
// outlier that is not even a resolved source.
func TestLocalBackgroundRejectsSingleHotPixel(t *testing.T) {
	const level = 1000.0
	img := flatImage(60, 60, level)
	img[30][30] = 999999

	if got := LocalBackground(img, 30, 30); got != level {
		t.Errorf("LocalBackground = %v, want %v", got, level)
	}
}

// TestLocalBackgroundFollowsGradient checks that the estimator is genuinely
// local: on a linear ramp it must track the value at its own centre, not a
// global statistic.
func TestLocalBackgroundFollowsGradient(t *testing.T) {
	const (
		w, h  = 60, 60
		slope = 10.0
	)
	img := blankImage(w, h)
	for y := range img {
		for x := range img[y] {
			img[y][x] = slope * float64(x)
		}
	}

	for _, cx := range []int{20, 30, 40} {
		want := slope * float64(cx)
		if got := LocalBackground(img, cx, 30); math.Abs(got-want) > 1e-9 {
			t.Errorf("LocalBackground at x=%d = %v, want %v", cx, got, want)
		}
	}
}

// TestLocalBackgroundAnnulusPopulation pins the sampling geometry: innerR=5,
// outerR=8, both bounds inclusive, giving a fixed population for an interior
// centre. The Gaussian profile is irrelevant here; this counts the pixels the
// loop actually visits, which is what defines the estimator's behaviour.
func TestLocalBackgroundAnnulusPopulation(t *testing.T) {
	const (
		innerR = 5
		outerR = 8
	)
	// offsets holds the (dx, dy) pairs the estimator's loop visits.
	type offset struct{ dx, dy int }
	var offsets []offset
	for dy := -outerR; dy <= outerR; dy++ {
		for dx := -outerR; dx <= outerR; dx++ {
			r2 := dx*dx + dy*dy
			if r2 >= innerR*innerR && r2 <= outerR*outerR {
				offsets = append(offsets, offset{dx, dy})
			}
		}
	}
	if len(offsets) != 128 {
		t.Errorf("annulus population = %d, want 128", len(offsets))
	}

	// Recover the same population the estimator sees by stamping a distinct
	// value into each annulus pixel and reading back the median. The image
	// must be large enough that the full +/-8 window around the centre is in
	// bounds, otherwise those taps are skipped and the population shrinks.
	const (
		cx, cy = 20, 20
		size   = 60
	)
	// Stamp a distinct value per annulus pixel, 1-based so the untouched
	// background reads as 0 and cannot be mistaken for a member of the sample.
	img := flatImage(size, size, 0)
	for i, off := range offsets {
		img[cy+off.dy][cx+off.dx] = float64(i + 1)
	}

	got := LocalBackground(img, cx, cy)

	// The estimator sorts its sample and returns vals[len(vals)/2]. With an
	// even population that is the upper of the two middle elements, not their
	// mean, so build the expectation the same way.
	vals := make([]float64, len(offsets))
	for i := range vals {
		vals[i] = float64(i + 1)
	}
	sort.Float64s(vals)
	expect := vals[len(vals)/2]
	if got != expect {
		t.Errorf("LocalBackground = %v, want %v (population %d)", got, expect, len(vals))
	}
}

// TestLocalBackgroundNearEdge covers the clamping path: out-of-range taps are
// skipped, so the estimator must still return a value from the surviving
// pixels rather than panicking or returning zero.
func TestLocalBackgroundNearEdge(t *testing.T) {
	const level = 1000.0
	img := flatImage(40, 40, level)

	for _, c := range [][2]int{{0, 0}, {0, 20}, {39, 39}, {2, 2}, {39, 0}, {0, 39}} {
		if got := LocalBackground(img, c[0], c[1]); got != level {
			t.Errorf("LocalBackground(%d,%d) = %v, want %v", c[0], c[1], got, level)
		}
	}
}

// TestLocalBackgroundEmptyImage records the degenerate path. A 1x1 image has
// no annulus pixels at all, so the guard returns 0.
//
// Note: a truly empty (0x0) image panics, because the bounds check on the
// centre tap runs before the empty-collection guard. Callers must not pass an
// empty image; FindStars never does, since it iterates within the image.
func TestLocalBackgroundEmptyImage(t *testing.T) {
	if got := LocalBackground(blankImage(1, 1), 0, 0); got != 0 {
		t.Errorf("1x1 image: got %v, want 0", got)
	}
}
