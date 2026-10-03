package mrs

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

// refMedian returns the median via a full sort. Used as an independent
// oracle for medianSelect, which uses quickselect.
func refMedian(vals []float64) float64 {
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return math.NaN()
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func TestMedianSelect(t *testing.T) {
	tests := []struct {
		name string
		in   []float64
		want float64
	}{
		{"single element", []float64{42}, 42},
		{"odd length", []float64{5, 1, 3}, 3},
		{"even length", []float64{4, 1, 3, 2}, 2.5},
		{"already sorted", []float64{1, 2, 3, 4, 5}, 3},
		{"reverse sorted", []float64{5, 4, 3, 2, 1}, 3},
		{"all duplicates", []float64{7, 7, 7, 7, 7}, 7},
		{"two distinct repeated", []float64{2, 1, 2, 1, 2, 1}, 1.5},
		{"negative values", []float64{-1, -5, -3, -2, -4}, -3},
		{"mixed sign", []float64{-10, 0, 10, -1, 1}, 0},
		{"two elements", []float64{1, 2}, 1.5},
		{"infinite", []float64{math.Inf(-1), 1, math.Inf(1)}, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// medianSelect mutates its argument, so pass a copy.
			got := medianSelect(append([]float64(nil), tc.in...))
			if got != tc.want {
				t.Errorf("medianSelect(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMedianSelectEmpty(t *testing.T) {
	// medianSelect is never called with an empty slice in production
	// (EstimateNoiseSigmaFlat guards n == 0), so this only records the
	// current behaviour: it indexes vals[n/2-1] unconditionally.
	defer func() {
		if r := recover(); r == nil {
			t.Error("medianSelect([]) returned without panicking")
		}
	}()
	medianSelect(nil)
}

// TestMedianSelectAgainstSort is a differential fuzz of medianSelect against a
// full sort. medianSelect reorders in place via quickselect, so the two-index
// sequence for even-length inputs is the part most likely to break.
func TestMedianSelectAgainstSort(t *testing.T) {
	rng := rand.New(rand.NewSource(20240917))

	for iter := 0; iter < 20000; iter++ {
		n := 1 + rng.Intn(64)
		vals := make([]float64, n)
		for i := range vals {
			switch rng.Intn(4) {
			case 0: // general case
				vals[i] = rng.NormFloat64() * 100
			case 1: // small integers -> many ties
				vals[i] = float64(rng.Intn(5))
			case 2: // already sorted
				vals[i] = float64(i)
			default: // reverse sorted
				vals[i] = float64(n - i)
			}
		}

		want := refMedian(vals)
		got := medianSelect(append([]float64(nil), vals...))
		if math.Abs(got-want) > 1e-12 {
			t.Fatalf("iter %d: medianSelect(%v) = %v, want %v", iter, vals, got, want)
		}
	}
}

func TestQuickselect(t *testing.T) {
	rng := rand.New(rand.NewSource(777))

	for iter := 0; iter < 2000; iter++ {
		n := 1 + rng.Intn(50)
		vals := make([]float64, n)
		for i := range vals {
			vals[i] = rng.NormFloat64()
		}
		sorted := append([]float64(nil), vals...)
		sort.Float64s(sorted)

		for k := 0; k < n; k++ {
			work := append([]float64(nil), vals...)
			quickselect(work, k)
			if work[k] != sorted[k] {
				t.Fatalf("iter %d: quickselect(k=%d)[%d] = %v, want %v", iter, k, k, work[k], sorted[k])
			}
		}
	}
}

func TestToFlatTo2DRoundTrip(t *testing.T) {
	img := [][]float64{
		{1, 2, 3},
		{4, 5, 6},
	}

	flat, w, h := toFlat(img)
	if w != 3 || h != 2 {
		t.Fatalf("toFlat dims = %dx%d, want 3x2", w, h)
	}
	if len(flat) != 6 {
		t.Fatalf("toFlat len = %d, want 6", len(flat))
	}
	wantFlat := []float64{1, 2, 3, 4, 5, 6}
	for i := range wantFlat {
		if flat[i] != wantFlat[i] {
			t.Errorf("flat[%d] = %v, want %v", i, flat[i], wantFlat[i])
		}
	}

	back := to2D(flat, w, h)
	if len(back) != len(img) {
		t.Fatalf("to2D rows = %d, want %d", len(back), len(img))
	}
	for y := range img {
		for x := range img[y] {
			if back[y][x] != img[y][x] {
				t.Errorf("round trip [%d][%d] = %v, want %v", y, x, back[y][x], img[y][x])
			}
		}
	}
}

func TestBin2x2Mean(t *testing.T) {
	t.Run("even dimensions", func(t *testing.T) {
		// Distinct values so each 2x2 mean is unambiguous.
		flat := []float64{
			1, 2, 3, 4,
			5, 6, 7, 8,
			9, 10, 11, 12,
			13, 14, 15, 16,
		}
		binned, bw, bh := bin2x2Mean(flat, 4, 4)
		if bw != 2 || bh != 2 {
			t.Fatalf("dims = %dx%d, want 2x2", bw, bh)
		}
		want := []float64{
			(1.0 + 2 + 5 + 6) / 4,
			(3.0 + 4 + 7 + 8) / 4,
			(9.0 + 10 + 13 + 14) / 4,
			(11.0 + 12 + 15 + 16) / 4,
		}
		for i := range want {
			if binned[i] != want[i] {
				t.Errorf("binned[%d] = %v, want %v", i, binned[i], want[i])
			}
		}
	})

	t.Run("odd dimensions drop trailing row and column", func(t *testing.T) {
		flat := []float64{
			1, 2, 3,
			4, 5, 6,
			7, 8, 9,
		}
		binned, bw, bh := bin2x2Mean(flat, 3, 3)
		if bw != 1 || bh != 1 {
			t.Fatalf("dims = %dx%d, want 1x1", bw, bh)
		}
		if len(binned) != 1 {
			t.Fatalf("len = %d, want 1", len(binned))
		}
		want := (1.0 + 2 + 4 + 5) / 4
		if binned[0] != want {
			t.Errorf("binned[0] = %v, want %v", binned[0], want)
		}
	})
}

func TestUpsampleBilinear(t *testing.T) {
	// The sampler maps output pixel x to source coordinate
	// sx = x * sw / w, so output nodes land at x = 0, 2, 4, 6 for sw=2, w=4 --
	// not on every other output pixel. Pin the measured 4x4 expansion.
	t.Run("reproduces grid nodes", func(t *testing.T) {
		small := []float64{1, 2, 3, 4}
		sw, sh := 2, 2
		out := upsampleBilinear(small, sw, sh, 4, 4)
		want := []float64{
			1.0, 1.5, 2.0, 2.0,
			2.0, 2.5, 3.0, 3.0,
			3.0, 3.5, 4.0, 4.0,
			3.0, 3.5, 4.0, 4.0,
		}
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				if got := out[y*4+x]; math.Abs(got-want[y*4+x]) > 1e-12 {
					t.Errorf("out[%d][%d] = %v, want %v", y, x, got, want[y*4+x])
				}
			}
		}
	})

	t.Run("constant is preserved", func(t *testing.T) {
		small := []float64{3.5, 3.5, 3.5, 3.5}
		out := upsampleBilinear(small, 2, 2, 9, 7)
		for i, v := range out {
			if math.Abs(v-3.5) > 1e-12 {
				t.Fatalf("out[%d] = %v, want 3.5", i, v)
			}
		}
	})

	t.Run("bilinear on a linear ramp", func(t *testing.T) {
		// A 1-D ramp in x, constant in y, must stay linear after
		// upsampling (bilinear interpolation is exact on linear data).
		const sw, sh = 4, 2
		small := make([]float64, sw*sh)
		for y := 0; y < sh; y++ {
			for x := 0; x < sw; x++ {
				small[y*sw+x] = float64(x)
			}
		}
		const w, h = 16, 8
		out := upsampleBilinear(small, sw, sh, w, h)

		// Once sx reaches the last source node (sw-1) the sampler clamps both
		// x0 and x1 to it and zeroes fx, so the tail of the row is a flat
		// extension at that value rather than a continued ramp.
		clampFrom := w * (sw - 1) / sw

		// Bilinear interpolation is exact on data that is linear along each
		// axis, so every sample before the clamp must sit on the source ramp.
		for y := 0; y < h; y++ {
			for x := 0; x < clampFrom; x++ {
				want := float64(x) * float64(sw) / float64(w)
				if got := out[y*w+x]; math.Abs(got-want) > 1e-12 {
					t.Fatalf("out[%d][%d] = %v, want %v", y, x, got, want)
				}
			}
		}
		for y := 0; y < h; y++ {
			for x := clampFrom; x < w; x++ {
				want := float64(sw - 1)
				if got := out[y*w+x]; math.Abs(got-want) > 1e-12 {
					t.Fatalf("out[%d][%d] = %v, want clamped trailing value %v", y, x, got, want)
				}
			}
		}
	})

	t.Run("output has full-resolution dimensions", func(t *testing.T) {
		out := upsampleBilinear([]float64{1, 2, 3, 4, 5, 6}, 3, 2, 11, 5)
		if len(out) != 55 {
			t.Errorf("len = %d, want 55", len(out))
		}
	})
}
