package starsmetrics

import (
	"math"
	"math/rand"
	"testing"
)

// synthEllipse builds a point-sampled elliptical Gaussian on a constant
// background, the same model the fit assumes. With no 1/12 pixel-variance term
// the recovery should be near exact; that is the point of this test.
func synthEllipse(size int, bg, amp, sx, sy, angleDeg float64, noise float64, seed int64) [][]float64 {
	img := make([][]float64, size)
	rng := rand.New(rand.NewSource(seed))
	theta := angleDeg * math.Pi / 180
	ct, st := math.Cos(theta), math.Sin(theta)
	cx, cy := float64(size)/2+0.37, float64(size)/2+0.21 // deliberately sub-pixel
	for y := 0; y < size; y++ {
		img[y] = make([]float64, size)
		for x := 0; x < size; x++ {
			dx := float64(x) - cx
			dy := float64(y) - cy
			u := ct*dx + st*dy
			v := -st*dx + ct*dy
			v2 := bg + amp*math.Exp(-0.5*(u*u/(sx*sx)+v*v/(sy*sy)))
			if noise > 0 {
				v2 += rng.NormFloat64() * noise
			}
			img[y][x] = v2
		}
	}
	return img
}

// TestFitRecoversKnownShape checks the fit on its own model.
func TestFitRecoversKnownShape(t *testing.T) {
	const size = 61
	for _, tc := range []struct {
		name   string
		sx, sy float64
	}{
		{"round", 1.2, 1.2},
		{"mildly elongated", 1.2, 1.0},
		{"2:1", 1.2, 0.6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := synthEllipse(size, 500, 4000, tc.sx, tc.sy, 30, 0, 1)
			fit, ok := FitEllipticalGaussian(img, nil, size/2, size/2, 8)
			if !ok {
				t.Fatal("fit failed on clean synthetic data")
			}

			wantFWHM := fwhmPerSigma * math.Sqrt(tc.sx*tc.sy)
			if rel := math.Abs(fit.FWHM-wantFWHM) / wantFWHM; rel > 0.01 {
				t.Errorf("FWHM = %.4f, want %.4f (rel %.4f)", fit.FWHM, wantFWHM, rel)
			}
			wantEcc := math.Sqrt(1 - (tc.sy*tc.sy)/(tc.sx*tc.sx))
			if math.Abs(fit.Eccentricity-wantEcc) > 0.01 {
				t.Errorf("eccentricity = %.4f, want %.4f", fit.Eccentricity, wantEcc)
			}
			wantX, wantY := float64(size)/2+0.37, float64(size)/2+0.21
			if math.Abs(fit.X-wantX) > 0.05 || math.Abs(fit.Y-wantY) > 0.05 {
				t.Errorf("centroid = (%.3f, %.3f), want (%.3f, %.3f)",
					fit.X, fit.Y, wantX, wantY)
			}
		})
	}
}

// TestFitUndersampledFWHM is the case the moment estimator cannot do. A PSF of
// FWHM 1.2 px spans barely one pixel, and CalculateWidth reads it as ~2.0
// because it takes integer-axis half-max crossings. The fit has to recover it.
func TestFitUndersampledFWHM(t *testing.T) {
	const size = 41
	sigma := 1.2 / fwhmPerSigma // 0.5097
	img := synthEllipse(size, 500, 3000, sigma, sigma, 0, 0, 2)

	fit, ok := FitEllipticalGaussian(img, nil, size/2, size/2, 6)
	if !ok {
		t.Fatal("fit failed on an undersampled but clean source")
	}
	if math.Abs(fit.FWHM-1.2) > 0.1 {
		t.Errorf("FWHM = %.3f, want ~1.20", fit.FWHM)
	}
	if fit.Eccentricity > 0.05 {
		t.Errorf("round source measured eccentricity %.3f, want ~0", fit.Eccentricity)
	}

	// The moment estimator on the same data, for the comparison that motivated
	// the fit. It is allowed to be wrong; it must not silently start agreeing.
	if got, ok := CalculateWidth(img, zeroMap(size), size/2, size/2, 10); ok {
		if got < 1.5 {
			t.Logf("note: CalculateWidth now reads %.2f on this source", got)
		}
	}
}

// TestFitIsNotFooledByBackgroundPedestal is the bias the clipping introduced. A
// round star on a bright sky, with noise, must still fit round: the background
// is a fitted parameter, not a subtraction from a smoothed map.
func TestFitIsNotFooledByBackgroundPedestal(t *testing.T) {
	const size = 61
	// One noisy frame has a noise-driven shape error of a few per cent; the tool
	// averages hundreds of stars, so the mean is what has to be unbiased. This
	// checks the mean over seeds rather than a single draw.
	means := map[float64]float64{}
	for _, bg := range []float64{0, 1000, 5000} {
		var sumEcc, sumBg float64
		const seeds = 24
		for seed := int64(1); seed <= seeds; seed++ {
			img := synthEllipse(size, bg, 20000, 0.85, 0.85, 0, 40, seed)
			fit, ok := FitEllipticalGaussian(img, nil, size/2, size/2, 7)
			if !ok {
				t.Fatalf("bg %.0f seed %d: fit failed", bg, seed)
			}
			sumEcc += fit.Eccentricity
			sumBg += fit.Background
		}
		meanEcc := sumEcc / seeds
		meanBg := sumBg / seeds
		means[bg] = meanEcc
		t.Logf("bg %.0f: mean ecc %.3f, mean fitted bg %.1f", bg, meanEcc, meanBg)
		if math.Abs(meanBg-bg) > 15 {
			t.Errorf("bg %.0f: mean fitted background %.1f", bg, meanBg)
		}
	}

	// The claim under test is that the background level does not change the
	// measured shape. The residual ~0.08 is the nonlinearity of
	// sqrt(1 - (sy/sx)^2) over noisy axis estimates, not a pedestal effect: it
	// is identical at every background, which is exactly what the fitted
	// background is for.
	for bg, got := range means {
		if got > 0.10 {
			t.Errorf("bg %.0f: round sources average eccentricity %.3f, want below 0.10", bg, got)
		}
		if math.Abs(got-means[0]) > 0.01 {
			t.Errorf("bg %.0f measures %.3f but bg 0 measures %.3f; shape depends on sky level",
				bg, got, means[0])
		}
	}
}

// TestFitRejectsGarbage pins the failure path: no star, no result.
func TestFitRejectsGarbage(t *testing.T) {
	flat := make([][]float64, 41)
	for i := range flat {
		flat[i] = make([]float64, 41)
		for j := range flat[i] {
			flat[i][j] = 100
		}
	}
	if _, ok := FitEllipticalGaussian(flat, nil, 20, 20, 6); ok {
		t.Error("fit accepted a constant image with no source")
	}
	if _, ok := FitEllipticalGaussian(nil, nil, 0, 0, 5); ok {
		t.Error("fit accepted an empty image")
	}
}

func zeroMap(size int) [][]float64 {
	m := make([][]float64, size)
	for i := range m {
		m[i] = make([]float64, size)
	}
	return m
}
