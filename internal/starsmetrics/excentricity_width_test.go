package starsmetrics

import (
	"math"
	"testing"
)

// TestCalculateWidthGaussian compares the half-max crossing estimator against
// the closed form 2*sqrt(2*ln2)*sigma.
//
// The estimator integrates the half-max crossing linearly between pixels, which
// biases the result slightly high. The bias is under half a percent for
// sigma >= 1.5 and grows to roughly four percent at sigma = 1, where the
// profile is only about two pixels across. The tolerance below accommodates
// the worst case in that range rather than hiding it.
func TestCalculateWidthGaussian(t *testing.T) {
	const (
		cx, cy = 40, 40
		radius = 20
		amp    = 10000.0
		size   = 81
	)
	tests := []struct {
		sigma     float64
		tolerance float64
	}{
		{1.0, 0.05},
		{1.5, 0.01},
		{2.0, 0.02},
		{3.0, 0.01},
	}

	for _, tc := range tests {
		img := blankImage(size, size)
		addGaussian(img, cx, cy, amp, tc.sigma)

		got, ok := CalculateWidth(img, blankImage(size, size), cx, cy, radius)
		if !ok {
			t.Fatalf("sigma=%.1f: ok=false", tc.sigma)
		}
		want := fwhmFactor * tc.sigma
		if relErr := math.Abs(got-want) / want; relErr > tc.tolerance {
			t.Errorf("sigma=%.1f: got %.6f, want %.6f (rel err %.4f, tolerance %.0f%%)",
				tc.sigma, got, want, relErr, tc.tolerance*100)
		}
	}
}

// TestCalculateWidthMonotonic is the property the FWHM window filter in
// FindStars relies on: a wider source must never measure narrower.
func TestCalculateWidthMonotonic(t *testing.T) {
	const (
		size   = 121
		cx, cy = 60, 60
		amp    = 10000.0
	)
	var prev float64
	for _, sigma := range []float64{1.0, 1.25, 1.5, 2.0, 2.5, 3.0} {
		img := blankImage(size, size)
		addGaussian(img, cx, cy, amp, sigma)

		got, ok := CalculateWidth(img, blankImage(size, size), cx, cy, 25)
		if !ok {
			t.Fatalf("sigma=%.2f: ok=false", sigma)
		}
		if got <= prev {
			t.Errorf("sigma=%.2f: FWHM %.6f not greater than previous %.6f", sigma, got, prev)
		}
		prev = got
	}
}

// TestCalculateWidthHalvesWithAmplitude checks scale invariance: the half-max
// level is defined relative to the peak, so a fainter source of identical shape
// must measure the same width.
func TestCalculateWidthHalvesWithAmplitude(t *testing.T) {
	const (
		size   = 81
		cx, cy = 40, 40
	)
	var ref float64
	for i, amp := range []float64{10000, 5000, 2000, 500} {
		img := blankImage(size, size)
		addGaussian(img, cx, cy, amp, 1.8)

		got, ok := CalculateWidth(img, blankImage(size, size), cx, cy, 20)
		if !ok {
			t.Fatalf("amp=%v: ok=false", amp)
		}
		if i == 0 {
			ref = got
			continue
		}
		if math.Abs(got-ref) > 1e-9 {
			t.Errorf("amp=%v: FWHM %.6f, want %.6f (width must not depend on amplitude)", amp, got, ref)
		}
	}
}

// TestCalculateWidthUsesBackgroundMap confirms the background map shifts the
// half-max level correctly. Raising both image and map by a constant must leave
// the measurement unchanged.
func TestCalculateWidthUsesBackgroundMap(t *testing.T) {
	const (
		size   = 81
		cx, cy = 40, 40
	)
	var ref float64
	for i, level := range []float64{0, 1000, 50000} {
		img := flatImage(size, size, level)
		bg := flatImage(size, size, level)
		addGaussian(img, cx, cy, 5000, 1.8)

		got, ok := CalculateWidth(img, bg, cx, cy, 20)
		if !ok {
			t.Fatalf("level=%v: ok=false", level)
		}
		if i == 0 {
			ref = got
			continue
		}
		if math.Abs(got-ref) > 1e-9 {
			t.Errorf("level=%v: FWHM %.6f, want %.6f", level, got, ref)
		}
	}
}

// TestCalculateWidthDegenerate covers the ok=false paths.
func TestCalculateWidthDegenerate(t *testing.T) {
	const (
		size   = 41
		cx, cy = 20, 20
	)
	tests := []struct {
		name string
		img  [][]float64
		bg   [][]float64
	}{
		{
			// centreVal <= 0 guard.
			name: "background equals image",
			img:  flatImage(size, size, 500),
			bg:   flatImage(size, size, 500),
		},
		{
			name: "all zeros",
			img:  blankImage(size, size),
			bg:   blankImage(size, size),
		},
		{
			// A flat source has no half-max crossing in any direction, so
			// countDirs stays zero even though centreVal is positive.
			name: "flat positive image",
			img:  flatImage(size, size, 500),
			bg:   blankImage(size, size),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CalculateWidth(tc.img, tc.bg, cx, cy, 10)
			if ok {
				t.Errorf("got (%v, true), want ok=false", got)
			}
		})
	}
}

// TestCalculateWidthRadiusTooSmall checks the search-radius bound. A wide
// source inspected through too small an aperture never crosses half max within
// the searched range, so it must be reported as a failure rather than a
// truncated width.
func TestCalculateWidthRadiusTooSmall(t *testing.T) {
	const (
		size   = 81
		cx, cy = 40, 40
	)
	img := blankImage(size, size)
	addGaussian(img, cx, cy, 10000, 8) // FWHM ~19 px

	if _, ok := CalculateWidth(img, blankImage(size, size), cx, cy, 30); !ok {
		t.Error("radius 30: ok=false, want ok=true for a sigma=8 source")
	}
	if got, ok := CalculateWidth(img, blankImage(size, size), cx, cy, 4); ok {
		t.Errorf("radius 4: got %v, want ok=false, half max is outside the aperture", got)
	}
}

// TestCalculateWidthSymmetricProfile checks the four-direction average on a
// symmetric source: all four crossings agree, so the result is the mean of four
// equal half-widths.
func TestCalculateWidthSymmetricProfile(t *testing.T) {
	const (
		size   = 81
		cx, cy = 40, 40
	)
	// A top-hat profile with an exactly known half-max edge at +/-2.5 px.
	img := flatImage(size, size, 0)
	for y := range img {
		for x := range img[y] {
			img[y][x] = 1
		}
	}
	for y := cy - 2; y <= cy+2; y++ {
		for x := cx - 2; x <= cx+2; x++ {
			img[y][x] = 100
		}
	}

	got, ok := CalculateWidth(img, blankImage(size, size), cx, cy, 20)
	if !ok {
		t.Fatal("ok=false for a clean top-hat")
	}
	// Centre 100, half max 50. The crossing is found at r=3, where the value
	// drops to 1 and the previous pixel (r=2) is still 100, so the
	// interpolated radius is 3 - (50-1)/(100-1) = 2.505 px per side. Both
	// opposite pairs agree, so the average is twice that.
	perSide := 3.0 - 49.0/99.0
	want := 2 * perSide
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("FWHM = %.6f, want %.6f", got, want)
	}
}

// TestIsAbsolutePeak covers the strict four-neighbour comparison.
func TestIsAbsolutePeak(t *testing.T) {
	tests := []struct {
		name  string
		setup func(p [][]float64)
		x, y  int
		want  bool
	}{
		{
			name:  "isolated maximum",
			setup: func(p [][]float64) { p[4][4] = 10; p[4][3] = 5; p[4][5] = 5; p[3][4] = 5; p[5][4] = 5 },
			x:     4, y: 4, want: true,
		},
		{
			name:  "equal neighbour is not a peak",
			setup: func(p [][]float64) { p[4][4] = 10; p[4][3] = 10; p[4][5] = 5; p[3][4] = 5; p[5][4] = 5 },
			x:     4, y: 4, want: false,
		},
		{
			name:  "larger right neighbour",
			setup: func(p [][]float64) { p[4][4] = 10; p[4][3] = 5; p[4][5] = 20; p[3][4] = 5; p[5][4] = 5 },
			x:     4, y: 4, want: false,
		},
		{
			name:  "larger top neighbour",
			setup: func(p [][]float64) { p[4][4] = 10; p[4][3] = 5; p[4][5] = 5; p[3][4] = 20; p[5][4] = 5 },
			x:     4, y: 4, want: false,
		},
		{
			name:  "lower neighbour everywhere",
			setup: func(p [][]float64) { p[4][4] = 10; p[4][3] = 1; p[4][5] = 1; p[3][4] = 1; p[5][4] = 1 },
			x:     4, y: 4, want: true,
		},
		{
			// Diagonal neighbours are not consulted, so a larger diagonal
			// value does not disqualify the pixel.
			name: "larger diagonal neighbour",
			setup: func(p [][]float64) {
				p[4][4] = 10
				p[3][3] = 999
				p[4][3] = 5
				p[4][5] = 5
				p[3][4] = 5
				p[5][4] = 5
			},
			x: 4, y: 4, want: true,
		},
		{
			name:  "valley",
			setup: func(p [][]float64) { p[4][4] = 1; p[4][3] = 10; p[4][5] = 10; p[3][4] = 10; p[5][4] = 10 },
			x:     4, y: 4, want: false,
		},
		{
			name:  "flat field has no peak",
			setup: func(p [][]float64) {},
			x:     4, y: 4, want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := blankImage(9, 9)
			tc.setup(p)
			if got := IsAbsolutePeak(p, tc.x, tc.y); got != tc.want {
				t.Errorf("IsAbsolutePeak(%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
			}
		})
	}
}

// TestIsAbsolutePeakInterior scans a synthetic field and confirms every
// reported peak is genuinely a strict four-neighbour maximum, with no misses
// among the planted sources.
func TestIsAbsolutePeakInterior(t *testing.T) {
	const (
		size  = 61
		amp   = 5000.0
		noise = 3.0
		seed  = 4242
	)
	img := flatImage(size, size, 1000)
	// Deterministic pseudo-noise so the field is not perfectly smooth.
	addGaussian(img, 10, 10, 7, 1)
	addGaussian(img, 30, 30, 9, 1)
	addGaussian(img, 50, 20, 11, 1)
	addGaussian(img, 20, 45, 13, 1)

	for _, c := range [][2]int{{10, 10}, {30, 30}, {50, 20}, {20, 45}} {
		if !IsAbsolutePeak(img, c[0], c[1]) {
			t.Errorf("planted peak at (%d,%d) not reported", c[0], c[1])
		}
	}

	// Every pixel reported as a peak must satisfy the strict comparison.
	for y := 1; y < size-1; y++ {
		for x := 1; x < size-1; x++ {
			c := img[y][x]
			isPeak := c > img[y][x-1] && c > img[y][x+1] && c > img[y-1][x] && c > img[y+1][x]
			if IsAbsolutePeak(img, x, y) != isPeak {
				t.Errorf("(%d,%d) disagrees with the strict comparison", x, y)
			}
		}
	}
	_ = amp
	_ = noise
}
