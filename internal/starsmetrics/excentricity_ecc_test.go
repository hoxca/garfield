package starsmetrics

import (
	"math"
	"testing"
)

// analyticEccentricity is the eccentricity of an idealised 2D Gaussian with
// sigmas (sx, sy) along x and y. The second central moments are mu20 = sx^2,
// mu02 = sy^2 and mu11 = 0, so the major/minor axis ratio reduces to sy/sx.
func analyticEccentricity(sx, sy float64) float64 {
	if sx < sy {
		sx, sy = sy, sx
	}
	return math.Sqrt(1 - (sy*sy)/(sx*sx))
}

// TestCalculateEccentricityGaussian compares the image-moment estimator against
// the closed form for a Gaussian. Agreement is well under a percent for every
// ratio that fits comfortably in the aperture.
func TestCalculateEccentricityGaussian(t *testing.T) {
	const (
		cx, cy = 40, 40
		radius = 10
		amp    = 10000.0
	)

	tests := []struct {
		name   string
		sx, sy float64
	}{
		{"isotropic", 2, 2},
		{"mildly elongated", 3, 2},
		{"2:1", 2, 1},
		{"3:1", 3, 1},
		{"4:1", 4, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			img := blankImage(80, 80)
			addAnisotropicGaussian(img, cx, cy, amp, tc.sx, tc.sy)

			got, ok := CalculateEccentricity(img, blankImage(80, 80), cx, cy, radius)
			if !ok {
				t.Fatalf("CalculateEccentricity returned ok=false for a well-formed star")
			}

			want := analyticEccentricity(tc.sx, tc.sy)
			if want == 0 {
				// An isotropic profile measures as zero up to floating-point
				// round-off in the moment sums.
				if math.Abs(got) > 1e-6 {
					t.Errorf("isotropic: got %v, want ~0", got)
				}
				return
			}
			if relErr := math.Abs(got-want) / want; relErr > 0.005 {
				t.Errorf("sx=%.1f sy=%.1f: got %.6f, want %.6f (rel err %.4f, want < 0.5%%)",
					tc.sx, tc.sy, got, want, relErr)
			}
		})
	}
}

// TestCalculateEccentricityRotationInvariant checks that the estimator works on
// second moments rather than on axis-aligned profiles: an elongated source
// rotated by any angle must yield the same eccentricity.
func TestCalculateEccentricityRotationInvariant(t *testing.T) {
	const (
		cx, cy = 40, 40
		radius = 10
		amp    = 10000.0
		sx, sy = 3.0, 1.0
		size   = 81
	)
	want := analyticEccentricity(sx, sy)

	var results []float64
	for _, deg := range []float64{0, 15, 30, 45, 60, 75, 89} {
		theta := deg * math.Pi / 180
		cos, sin := math.Cos(theta), math.Sin(theta)

		img := blankImage(size, size)
		den := 2 * sx * sx
		denY := 2 * sy * sy
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				dx := float64(x - cx)
				dy := float64(y - cy)
				// Rotate the sample into the source frame, where the major
				// axis lies along x.
				rx := dx*cos + dy*sin
				ry := -dx*sin + dy*cos
				img[y][x] = amp * math.Exp(-(rx*rx/den + ry*ry/denY))
			}
		}

		got, ok := CalculateEccentricity(img, blankImage(size, size), cx, cy, radius)
		if !ok {
			t.Fatalf("%v deg: ok=false", deg)
		}
		results = append(results, got)
	}

	min, max := results[0], results[0]
	for _, v := range results {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if spread := max - min; spread > 1e-3 {
		t.Errorf("eccentricity varies with rotation: min %.6f max %.6f spread %.6f, want < 1e-3", min, max, spread)
	}
	if relErr := math.Abs((min+max)/2-want) / want; relErr > 0.005 {
		t.Errorf("mean %.6f, want %.6f (rel err %.4f)", (min+max)/2, want, relErr)
	}
}

// TestCalculateEccentricityUsesBackgroundMap confirms the background map is
// actually subtracted. The same source placed on two very different sky levels
// must produce the same eccentricity once each is background-subtracted.
func TestCalculateEccentricityUsesBackgroundMap(t *testing.T) {
	const (
		size   = 81
		cx, cy = 40, 40
		amp    = 5000.0
	)
	var got []float64
	for _, level := range []float64{0, 5000, 100000} {
		img := flatImage(size, size, level)
		bg := flatImage(size, size, level)
		addGaussian(img, cx, cy, amp, 2)

		ecc, ok := CalculateEccentricity(img, bg, cx, cy, 10)
		if !ok {
			t.Fatalf("level %v: ok=false", level)
		}
		got = append(got, ecc)
	}

	// An isotropic source measures as ~0 either way; the point is that the two
	// sky levels agree, not that they agree at full precision.
	for i := 1; i < len(got); i++ {
		if math.Abs(got[i]-got[0]) > 1e-6 {
			t.Errorf("eccentricity depends on sky level: %v vs %v", got[0], got[i])
		}
	}
}

// TestCalculateEccentricityBackgroundMapMismatch covers the useBgMap fallback.
// When the map's dimensions do not line up with the image, the estimator must
// ignore it rather than index out of range.
func TestCalculateEccentricityBackgroundMapMismatch(t *testing.T) {
	const (
		cx, cy = 20, 20
		amp    = 10000.0
	)
	img := blankImage(41, 41)
	addAnisotropicGaussian(img, cx, cy, amp, 3, 1)
	want, ok := CalculateEccentricity(img, blankImage(41, 41), cx, cy, 10)
	if !ok {
		t.Fatal("reference call returned ok=false")
	}

	t.Run("nil map", func(t *testing.T) {
		got, ok := CalculateEccentricity(img, nil, cx, cy, 10)
		if !ok {
			t.Fatal("ok=false with nil map")
		}
		// Falling back to zero background raises the whole image's floor to
		// zero, which is exactly what the zero-valued map does, so the two
		// paths must agree.
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("nil map: got %v, want %v", got, want)
		}
	})

	t.Run("wrong dimensions", func(t *testing.T) {
		for _, dims := range [][2]int{{10, 10}, {40, 41}, {41, 40}, {80, 80}, {1, 1}} {
			got, ok := CalculateEccentricity(img, blankImage(dims[0], dims[1]), cx, cy, 10)
			if !ok {
				t.Errorf("dims %v: ok=false", dims)
				continue
			}
			if math.Abs(got-want) > 1e-9 {
				t.Errorf("dims %v: got %v, want %v", dims, got, want)
			}
		}
	})
}

// TestCalculateEccentricityDegenerate covers the failure paths that return
// ok=false. These matter because callers (processImage in cmd) skip a star
// whenever ok is false.
func TestCalculateEccentricityDegenerate(t *testing.T) {
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
			// m00 == 0: background fully cancels the image.
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
			// Every pixel is at or below the background: intensities clamp to
			// zero, so m00 collapses again.
			name: "image entirely below background",
			img:  flatImage(size, size, 10),
			bg:   flatImage(size, size, 9000),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CalculateEccentricity(tc.img, tc.bg, cx, cy, 10)
			if ok {
				t.Errorf("got (%v, true), want ok=false for a degenerate profile", got)
			}
		})
	}
}

// TestCalculateEccentricitySinglePixel documents what a one-pixel source
// yields. The second moments are all zero, so both eigenvalues vanish and the
// guard rejects it rather than reporting a division by zero.
func TestCalculateEccentricitySinglePixel(t *testing.T) {
	const (
		size   = 21
		cx, cy = 10, 10
	)
	img := blankImage(size, size)
	img[cy][cx] = 1000

	got, ok := CalculateEccentricity(img, blankImage(size, size), cx, cy, 10)
	if ok {
		t.Errorf("got (%v, true), want ok=false for a single-pixel source", got)
	}
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Errorf("got %v, want a finite value", got)
	}
}

// TestCalculateEccentricityRadiusAperture checks the aperture is honoured: the
// same source measured through a wider aperture must stay close, since the
// Gaussian wings carry progressively less weight.
func TestCalculateEccentricityRadiusAperture(t *testing.T) {
	const (
		size   = 101
		cx, cy = 50, 50
		amp    = 10000.0
		sx, sy = 3.0, 1.0
	)
	img := blankImage(size, size)
	addAnisotropicGaussian(img, cx, cy, amp, sx, sy)
	want := analyticEccentricity(sx, sy)

	var prev float64
	for i, radius := range []int{8, 12, 16, 20} {
		got, ok := CalculateEccentricity(img, blankImage(size, size), cx, cy, radius)
		if !ok {
			t.Fatalf("radius %d: ok=false", radius)
		}
		if relErr := math.Abs(got-want) / want; relErr > 0.01 {
			t.Errorf("radius %d: got %.6f, want %.6f (rel err %.4f)", radius, got, want, relErr)
		}
		if i > 0 {
			if d := math.Abs(got - prev); d > 0.02 {
				t.Errorf("radius %d: %.6f differs from previous %.6f by %.4f", radius, got, prev, d)
			}
		}
		prev = got
	}
}
