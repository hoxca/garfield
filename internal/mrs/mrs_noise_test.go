package mrs

import (
	"math"
	"math/rand"
	"testing"
)

// makeNoisyImage returns a width*height image of zero-mean Gaussian noise with
// the given standard deviation, from a fixed seed.
func makeNoisyImage(width, height int, sigma float64, seed int64) [][]float64 {
	rng := rand.New(rand.NewSource(seed))
	out := make([][]float64, height)
	for y := range out {
		out[y] = make([]float64, width)
		for x := range out[y] {
			out[y][x] = rng.NormFloat64() * sigma
		}
	}
	return out
}

func makeConstantImage(width, height int, value float64) [][]float64 {
	out := make([][]float64, height)
	for y := range out {
		out[y] = make([]float64, width)
		for x := range out[y] {
			out[y][x] = value
		}
	}
	return out
}

func TestEstimateNoiseSigmaFlatRecoversSigma(t *testing.T) {
	// MAD/0.6745 is a consistent estimator of sigma for Gaussian data, so the
	// recovered value should land within a few percent across a wide range of
	// magnitudes (the estimator is scale-equivariant, not just tuned to 1).
	for _, sigma := range []float64{0.01, 1.0, 3.7, 25.0, 1000.0} {
		const n = 200
		img := makeNoisyImage(n, n, sigma, 99)
		flat, w, h := toFlat(img)

		got := EstimateNoiseSigmaFlat(flat, w, h, 1.0)
		relErr := math.Abs(got-sigma) / sigma
		if relErr > 0.05 {
			t.Errorf("sigma=%v: got %v (rel err %.4f), want < 5%%", sigma, got, relErr)
		}
	}
}

func TestEstimateNoiseSigmaFlatIsLinearInScaleFactor(t *testing.T) {
	const n = 128
	img := makeNoisyImage(n, n, 1.0, 5)
	flat, w, h := toFlat(img)

	base := EstimateNoiseSigmaFlat(flat, w, h, 1.0)
	for _, sf := range []float64{0.5, 0.89079, 2.0, 4.0} {
		got := EstimateNoiseSigmaFlat(flat, w, h, sf)
		want := base / sf
		if math.Abs(got-want) > 1e-12 {
			t.Errorf("scaleFactor=%v: got %v, want base/sf = %v", sf, got, want)
		}
	}
}

func TestEstimateNoiseSigmaFlatStrideSamplingAgrees(t *testing.T) {
	// maxMadSamples caps the sample count, so images above the threshold are
	// subsampled with a stride. Both paths must estimate the same sigma.
	//
	// maxMadSamples is 200000, so the crossover sits between 400x400 (160000,
	// unstrided) and 512x512 (262144, strided).
	const sigma = 2.0
	seed := int64(31337)

	small := makeNoisyImage(400, 400, sigma, seed)
	smallFlat, sw, sh := toFlat(small)
	if sw*sh > maxMadSamples {
		t.Fatalf("test setup: %dx%d should be below maxMadSamples=%d", sw, sh, maxMadSamples)
	}

	large := makeNoisyImage(512, 512, sigma, seed)
	largeFlat, lw, lh := toFlat(large)
	if lw*lh <= maxMadSamples {
		t.Fatalf("test setup: %dx%d should exceed maxMadSamples=%d", lw, lh, maxMadSamples)
	}

	smallSigma := EstimateNoiseSigmaFlat(smallFlat, sw, sh, 1.0)
	largeSigma := EstimateNoiseSigmaFlat(largeFlat, lw, lh, 1.0)

	for _, v := range []struct {
		name string
		got  float64
	}{{"unstrided 400x400", smallSigma}, {"strided 512x512", largeSigma}} {
		if relErr := math.Abs(v.got-sigma) / sigma; relErr > 0.05 {
			t.Errorf("%s: got %v (rel err %.4f), want within 5%% of %v", v.name, v.got, relErr, sigma)
		}
	}

	if relErr := math.Abs(largeSigma-smallSigma) / sigma; relErr > 0.05 {
		t.Errorf("strided vs unstrided disagree: %v vs %v (rel %.4f)", largeSigma, smallSigma, relErr)
	}
}

func TestEstimateNoiseSigmaFlatConstantIsZero(t *testing.T) {
	// A constant detail layer has zero MAD, which EstimateNoiseSigmaFlat
	// reports as 0 rather than dividing by zero.
	const n = 64
	flat := make([]float64, n*n)
	for i := range flat {
		flat[i] = 5.0
	}
	if got := EstimateNoiseSigmaFlat(flat, n, n, 1.0); got != 0 {
		t.Errorf("constant detail layer: got %v, want 0", got)
	}
}

func TestEstimateNoiseSigmaFlatEmptyIsZero(t *testing.T) {
	if got := EstimateNoiseSigmaFlat(nil, 0, 0, 1.0); got != 0 {
		t.Errorf("empty detail layer: got %v, want 0", got)
	}
}

func TestEstimateNoiseSigmaFlatMatches2DWrapper(t *testing.T) {
	const n = 100
	img := makeNoisyImage(n, n, 1.5, 2468)
	flat, w, h := toFlat(img)

	fromFlat := EstimateNoiseSigmaFlat(flat, w, h, ScaleNoiseFactors[0])
	from2D := EstimateNoiseSigma(img, ScaleNoiseFactors[0])
	if fromFlat != from2D {
		t.Errorf("wrapper mismatch: flat %v vs 2D %v", fromFlat, from2D)
	}
}

func TestEstimateNoiseSigmaFromImageRecoversSigma(t *testing.T) {
	// End-to-end: sigma is estimated from the finest MRS detail layer.
	const scales = 5
	for _, sigma := range []float64{1.0, 3.7, 25.0} {
		img := makeNoisyImage(200, 200, sigma, 99)
		got := EstimateNoiseSigmaFromImage(img, scales)
		// Measured ratios on this estimator run ~1.014x high because sigma is
		// taken from the finest detail layer rather than the raw image.
		relErr := math.Abs(got-sigma) / sigma
		if relErr > 0.05 {
			t.Errorf("sigma=%v: got %v (rel err %.4f), want < 5%%", sigma, got, relErr)
		}
	}
}
