package mrs

import (
	"math"
	"math/rand"
	"testing"
)

// makeFlatNoise returns a width*height slice of unit-variance Gaussian noise
// from a fixed seed, so tests are reproducible.
func makeFlatNoise(width, height int, seed int64) []float64 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float64, width*height)
	for i := range out {
		out[i] = rng.NormFloat64()
	}
	return out
}

// setConvWorkers points the package-level ConvWorkers global at n for the
// duration of the test. Tests using it are not parallel: ConvWorkers is shared
// mutable state and concurrent writes would be a genuine data race.
func setConvWorkers(t *testing.T, n int) {
	t.Helper()
	prev := ConvWorkers
	ConvWorkers = n
	t.Cleanup(func() { ConvWorkers = prev })
}

func TestB3SplineFilter(t *testing.T) {
	// The exact B3-spline kernel is [1, 4, 6, 4, 1] / 16.
	want := []float64{1.0 / 16.0, 4.0 / 16.0, 6.0 / 16.0, 4.0 / 16.0, 1.0 / 16.0}
	if len(B3SplineFilter) != len(want) {
		t.Fatalf("len(B3SplineFilter) = %d, want %d", len(B3SplineFilter), len(want))
	}
	for i := range want {
		if B3SplineFilter[i] != want[i] {
			t.Errorf("B3SplineFilter[%d] = %v, want %v", i, B3SplineFilter[i], want[i])
		}
	}

	var sum float64
	for _, k := range B3SplineFilter {
		sum += k
	}
	// Unit sum is what makes the convolution preserve constant images, which
	// several other tests rely on.
	if math.Abs(sum-1.0) > 1e-15 {
		t.Errorf("kernel sum = %.17g, want 1", sum)
	}

	for i := 0; i < len(B3SplineFilter)/2; i++ {
		j := len(B3SplineFilter) - 1 - i
		if B3SplineFilter[i] != B3SplineFilter[j] {
			t.Errorf("kernel not symmetric at %d/%d: %v vs %v", i, j, B3SplineFilter[i], B3SplineFilter[j])
		}
	}
}

// TestConvolveConstantImage checks that a unit-sum kernel with symmetric
// boundary handling leaves a constant image untouched at every output pixel,
// including the corners where the reflected taps are mixed in.
func TestConvolveConstantImage(t *testing.T) {
	const (
		n     = 64
		value = 7.5
	)
	for _, step := range []int{1, 2, 4, 8, 16} {
		img := make([][]float64, n)
		for y := range img {
			img[y] = make([]float64, n)
			for x := range img[y] {
				img[y][x] = value
			}
		}
		out := Convolve2D(img, step)
		for y := range out {
			for x := range out[y] {
				if math.Abs(out[y][x]-value) > 1e-12 {
					t.Fatalf("step %d: out[%d][%d] = %.15f, want %v", step, y, x, out[y][x], value)
				}
			}
		}
	}
}

// minSafeDim returns the smallest image dimension Convolve2DFlat can handle at
// the given scale count.
//
// The boundary handling in Convolve2DFlat reflects each tap index exactly once,
// which is only sufficient while the filter footprint (2*step+1 taps, where
// step = 1<<(scales-1)) fits inside the image. Below that the reflected index
// goes negative and the convolution panics. Every test that feeds a real image
// through the transform must respect this, so it is expressed once here rather
// than repeated as magic numbers.
func minSafeDim(scales int) int {
	step := 1 << uint(scales-1)
	return 2*step + 1
}

// TestATrousPerfectReconstruction is the strongest structural check in the
// package: the coarsest smooth layer plus every detail layer must sum back to
// the input, for any scale count. It pins down the scale indexing in
// ATrousTransformFlat and would catch an off-by-one in the step schedule.
func TestATrousPerfectReconstruction(t *testing.T) {
	// Sized for the largest scale count under test (8 => step 128 => 257),
	// and deliberately non-square and not powers of two.
	const w, h = 259, 263

	for _, scales := range []int{1, 2, 3, 5, 8} {
		if min := minSafeDim(scales); w < min || h < min {
			t.Fatalf("test setup: %dx%d too small for scales=%d (need >= %d)", w, h, scales, min)
		}
		input := makeFlatNoise(w, h, int64(1000+scales))
		layers := ATrousTransformFlat(input, w, h, scales)
		if len(layers) != scales {
			t.Fatalf("scales=%d: got %d layers, want %d", scales, len(layers), scales)
		}

		recon := make([]float64, len(input))
		copy(recon, layers[scales-1].Smooth)
		for s := 0; s < scales; s++ {
			for i := range recon {
				recon[i] += layers[s].Detail[i]
			}
		}

		var maxErr float64
		for i := range recon {
			if d := math.Abs(recon[i] - input[i]); d > maxErr {
				maxErr = d
			}
		}
		if maxErr > 1e-12 {
			t.Errorf("scales=%d: max reconstruction error %.3e, want < 1e-12", scales, maxErr)
		}
	}
}

// TestConvolveThreadCountInvariance guards the row-partitioning logic and the
// ConvWorkers global. Output must be bitwise identical regardless of how many
// goroutines the convolution is split across.
func TestConvolveThreadCountInvariance(t *testing.T) {
	const w, h = 96, 80
	input := make([]float64, w*h)
	for i := range input {
		input[i] = math.Sin(float64(i)*0.01) * 100
	}

	setConvWorkers(t, 1)
	ref := Convolve2DFlat(input, w, h, 1, 1)

	for _, workers := range []int{2, 3, 8, 16} {
		got := Convolve2DFlat(input, w, h, 1, workers)
		for i := range ref {
			if got[i] != ref[i] {
				t.Fatalf("workers=%d: idx %d = %v, want bitwise %v", workers, i, got[i], ref[i])
			}
		}
	}
}

func TestConvolve2DFlatNumCPUClampedToAtLeastOne(t *testing.T) {
	// numCPU < 1 must not produce a zero-sized worker split.
	const w, h = 16, 16
	input := makeFlatNoise(w, h, 42)

	got := Convolve2DFlat(input, w, h, 1, 0)
	if len(got) != w*h {
		t.Fatalf("len = %d, want %d", len(got), w*h)
	}
	for i, v := range got {
		if math.IsNaN(v) {
			t.Fatalf("idx %d is NaN", i)
		}
	}
}

func TestATrousTransformFlatDimensions(t *testing.T) {
	const w, h, scales = 40, 30, 4
	input := makeFlatNoise(w, h, 7)
	layers := ATrousTransformFlat(input, w, h, scales)

	for s, l := range layers {
		if len(l.Smooth) != w*h {
			t.Errorf("scale %d: len(Smooth) = %d, want %d", s, len(l.Smooth), w*h)
		}
		if len(l.Detail) != w*h {
			t.Errorf("scale %d: len(Detail) = %d, want %d", s, len(l.Detail), w*h)
		}
	}
}

// TestATrousDetailShrinksWithScale verifies the qualitative property the whole
// algorithm depends on: coarser scales carry less signal power, which is what
// lets ScaleNoiseFactors be ordered.
func TestATrousDetailShrinksWithScale(t *testing.T) {
	const w, h, scales = 128, 128, 5
	input := makeFlatNoise(w, h, 2024)
	layers := ATrousTransformFlat(input, w, h, scales)

	std := func(v []float64) float64 {
		var s float64
		for _, x := range v {
			s += x
		}
		m := s / float64(len(v))
		var q float64
		for _, x := range v {
			q += (x - m) * (x - m)
		}
		return math.Sqrt(q / float64(len(v)))
	}

	prev := math.Inf(1)
	for s, l := range layers {
		sd := std(l.Detail)
		if sd >= prev {
			t.Errorf("scale %d: detail std %.6f did not shrink below previous %.6f", s, sd, prev)
		}
		prev = sd
	}
}

func TestATrousTransform2DShape(t *testing.T) {
	const w, h, scales = 24, 18, 3
	img := make([][]float64, h)
	for y := range img {
		img[y] = make([]float64, w)
		for x := range img[y] {
			img[y][x] = float64(y*w + x)
		}
	}

	layers := ATrousTransform(img, scales)
	if len(layers) != scales {
		t.Fatalf("got %d layers, want %d", len(layers), scales)
	}
	for s, l := range layers {
		if len(l.Smooth) != h || len(l.Detail) != h {
			t.Fatalf("scale %d: row count = %d/%d, want %d", s, len(l.Smooth), len(l.Detail), h)
		}
		if len(l.Smooth[0]) != w || len(l.Detail[0]) != w {
			t.Errorf("scale %d: column count = %d/%d, want %d", s, len(l.Smooth[0]), len(l.Detail[0]), w)
		}
	}
}
