package cmd

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/astrogo/fitsio"
)

// goodFrameOpts produces a frame that clears every default gate, so each gate
// can be tripped in isolation. The field is dense enough to exceed minStars=680
// and sharp enough to stay under maxFWHM=5.
func goodFrameOpts() synthOpts {
	return synthOpts{
		width: 1400, height: 1400,
		level: 1000, noise: 5,
		amp: 3000, sigma: 1.5,
		spacing: 25, seed: 21,
	}
}

// TestDecisionCascadeIsApprovedOnAGoodFrame is the baseline every gate test
// compares against: if this stops being APPROUVEE, the individual gate tests
// below are measuring the wrong thing.
func TestDecisionCascadeIsApprovedOnAGoodFrame(t *testing.T) {
	dir := t.TempDir()
	path := writeFrame(t, dir, frameName("B", 0), goodFrameOpts())

	r := processImage(path, defaultOpts())
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}
	if r.Decision != decisionApproved {
		t.Fatalf("decision = %q, want %q (det=%d starCount=%d FWHM=%.3f ecc=%.4f SNR=%.2f score=%.3f)",
			r.Decision, decisionApproved, r.DetectedStars, r.StarCount,
			r.AvgFWHM, r.AvgEccentricity, r.SNR, r.Score)
	}
	if r.DetectedStars < 680 {
		t.Fatalf("detected %d stars, the fixture must exceed minStars=680 to exercise the gates", r.DetectedStars)
	}
	if r.StarCount == 0 {
		t.Fatal("starCount is 0; the fixture must produce measurable stars")
	}
}

// TestDecisionCascadeGates covers each of the six decision tokens by tripping
// exactly one gate. Every token in the closed set must be reachable.
func TestDecisionCascadeGates(t *testing.T) {
	dir := t.TempDir()
	path := writeFrame(t, dir, frameName("B", 0), goodFrameOpts())

	tests := []struct {
		name string
		mut  func(*analyzeOptions)
		want string
	}{
		{
			name: "approved when nothing trips",
			mut:  func(*analyzeOptions) {},
			want: decisionApproved,
		},
		{
			name: "star count gate",
			mut:  func(o *analyzeOptions) { o.minStars = 1 << 30 },
			want: decisionNoStars,
		},
		{
			name: "eccentricity gate",
			mut:  func(o *analyzeOptions) { o.maxEcc = 0.001 },
			want: decisionEcc,
		},
		{
			name: "fwhm gate",
			mut:  func(o *analyzeOptions) { o.maxFWHM = 0.5 },
			want: decisionFWHM,
		},
		{
			name: "snr gate",
			mut:  func(o *analyzeOptions) { o.minSNR = 1e9 },
			want: decisionSNR,
		},
		{
			name: "scoring gate",
			mut:  func(o *analyzeOptions) { o.minScore = 1e9 },
			want: decisionScoring,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := defaultOpts()
			tc.mut(&o)
			r := processImage(path, o)
			if r.Error != nil {
				t.Fatalf("unexpected error: %v", r.Error)
			}
			if r.Decision != tc.want {
				t.Errorf("decision = %q, want %q", r.Decision, tc.want)
			}
		})
	}
}

// TestDecisionCascadePrecedence checks the ordering of the else-if chain: when
// several gates fail at once, the earliest must win. Reordering the chain would
// change which reason a frame is reported for.
func TestDecisionCascadePrecedence(t *testing.T) {
	dir := t.TempDir()
	path := writeFrame(t, dir, frameName("B", 0), goodFrameOpts())

	tests := []struct {
		name string
		mut  func(*analyzeOptions)
		want string
	}{
		{
			name: "stars beats everything",
			mut: func(o *analyzeOptions) {
				o.minStars = 1 << 30
				o.maxEcc = 0.001
				o.maxFWHM = 0.5
				o.minSNR = 1e9
				o.minScore = 1e9
			},
			want: decisionNoStars,
		},
		{
			name: "eccentricity beats fwhm, snr and score",
			mut: func(o *analyzeOptions) {
				o.maxEcc = 0.001
				o.maxFWHM = 0.5
				o.minSNR = 1e9
				o.minScore = 1e9
			},
			want: decisionEcc,
		},
		{
			name: "fwhm beats snr and score",
			mut: func(o *analyzeOptions) {
				o.maxFWHM = 0.5
				o.minSNR = 1e9
				o.minScore = 1e9
			},
			want: decisionFWHM,
		},
		{
			name: "snr beats score",
			mut: func(o *analyzeOptions) {
				o.minSNR = 1e9
				o.minScore = 1e9
			},
			want: decisionSNR,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := defaultOpts()
			tc.mut(&o)
			r := processImage(path, o)
			if r.Error != nil {
				t.Fatalf("unexpected error: %v", r.Error)
			}
			if r.Decision != tc.want {
				t.Errorf("decision = %q, want %q", r.Decision, tc.want)
			}
		})
	}
}

// TestEccentricityGateOnEllipticalField drives the eccentricity gate through
// processImage with physically elongated PSFs, rather than by moving the
// threshold. An elliptical field is what the gate exists to catch.
func TestEccentricityGateOnEllipticalField(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		ratio     float64
		want      string
		wantEcc   float64
		tolerance float64
	}{
		// Measured: circular gives ecc ~0.065, approved.
		{ratio: 1.0, want: decisionApproved, wantEcc: 0.065, tolerance: 0.03},
		// Measured 0.745 / 0.865 / 0.938, all rejected.
		{ratio: 1.5, want: decisionEcc, wantEcc: 0.745, tolerance: 0.05},
		{ratio: 2.0, want: decisionEcc, wantEcc: 0.865, tolerance: 0.05},
		{ratio: 3.0, want: decisionEcc, wantEcc: 0.938, tolerance: 0.05},
	}

	for _, tc := range tests {
		o := goodFrameOpts()
		o.ratio = tc.ratio
		path := writeFrame(t, dir, frameName("G", 0), o)

		r := processImage(path, defaultOpts())
		if r.Error != nil {
			t.Fatalf("ratio %.1f: unexpected error: %v", tc.ratio, r.Error)
		}
		if r.Decision != tc.want {
			t.Errorf("ratio %.1f: decision = %q, want %q (ecc %.4f)", tc.ratio, r.Decision, tc.want, r.AvgEccentricity)
		}
		if math.Abs(r.AvgEccentricity-tc.wantEcc) > tc.tolerance {
			t.Errorf("ratio %.1f: eccentricity = %.4f, want ~%.3f +/- %.3f", tc.ratio, r.AvgEccentricity, tc.wantEcc, tc.tolerance)
		}
	}
}

// TestFWHMGateOnDefocusedField does the same for the sharpness gate: a
// deliberately blurred PSF must be rejected once its measured FWHM crosses the
// limit.
func TestFWHMGateOnDefocusedField(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		sigma    float64
		want     string
		wantFWHM float64
	}{
		{sigma: 1.0, want: decisionApproved, wantFWHM: 2.43},
		{sigma: 1.5, want: decisionApproved, wantFWHM: 3.49},
		{sigma: 2.0, want: decisionApproved, wantFWHM: 4.63},
		// Measured 5.65 and 6.48, both past the default maxFWHM of 5.
		{sigma: 2.5, want: decisionFWHM, wantFWHM: 5.65},
		{sigma: 2.9, want: decisionFWHM, wantFWHM: 6.48},
	}

	for _, tc := range tests {
		o := goodFrameOpts()
		o.sigma = tc.sigma
		path := writeFrame(t, dir, frameName("B", int(tc.sigma*10)), o)

		r := processImage(path, defaultOpts())
		if r.Error != nil {
			t.Fatalf("sigma %.1f: unexpected error: %v", tc.sigma, r.Error)
		}
		if r.Decision != tc.want {
			t.Errorf("sigma %.1f: decision = %q, want %q (FWHM %.3f)", tc.sigma, r.Decision, tc.want, r.AvgFWHM)
		}
		if math.Abs(r.AvgFWHM-tc.wantFWHM) > 0.15 {
			t.Errorf("sigma %.1f: FWHM = %.3f, want ~%.2f", tc.sigma, r.AvgFWHM, tc.wantFWHM)
		}
	}
}

// TestNoNaNWhenNoStarIsMeasurable is the regression test for the bug where a
// frame with detectable but unmeasurable stars divided 0/0, producing NaN
// metrics. Because every NaN comparison is false, the NaN silently bypassed the
// FWHM, eccentricity and scoring gates and the frame was reported APPROUVEE.
//
// The fixture stars sit above the detector's 25*noise floor but below the 50*sky
// noise floor processImage requires to measure a star, so filteredCount ends up
// zero. Under every SNR threshold the frame must be rejected with finite
// metrics.
func TestNoNaNWhenNoStarIsMeasurable(t *testing.T) {
	dir := t.TempDir()
	o := goodFrameOpts()
	// 45x the read noise: detected, but below the 50x measurement floor.
	o.amp = 45 * o.noise
	o.width, o.height = 1200, 1200
	path := writeFrame(t, dir, frameName("S", 0), o)

	for _, minSNR := range []float64{11.0, 2.0, 0.5, 0.0, -1.0} {
		opts := defaultOpts()
		opts.minSNR = minSNR
		opts.minScore = -1e9 // would pass any computed score, including NaN
		opts.maxFWHM = 1e9   // would pass any FWHM, including NaN
		opts.maxEcc = 1.0    // would pass any eccentricity, including NaN
		opts.minStars = 1

		r := processImage(path, opts)

		if r.Decision == decisionApproved {
			t.Errorf("minSNR=%.1f: frame with zero measurable stars was APPROUVEE "+
				"(FWHM=%v ecc=%v score=%v) - the NaN guard is gone",
				minSNR, r.AvgFWHM, r.AvgEccentricity, r.Score)
		}
		if r.Decision != decisionNoStars {
			t.Errorf("minSNR=%.1f: decision = %q, want %q", minSNR, r.Decision, decisionNoStars)
		}
		for name, v := range map[string]float64{
			"avgFWHM": r.AvgFWHM, "avgSignal": r.AvgSignal,
			"avgEccentricity": r.AvgEccentricity, "snr": r.SNR, "score": r.Score,
		} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Errorf("minSNR=%.1f: %s = %v, want a finite value", minSNR, name, v)
			}
		}
		if r.StarCount != 0 {
			t.Errorf("minSNR=%.1f: starCount = %d, want 0", minSNR, r.StarCount)
		}
		// Every metric must be the exact zero of the struct's zero value,
		// not merely finite. A guard that caught the NaN division but still
		// reported a partial average would slip past an IsNaN-only check.
		if r.AvgFWHM != 0 || r.AvgSignal != 0 || r.AvgEccentricity != 0 || r.Score != 0 {
			t.Errorf("minSNR=%.1f: metrics = (FWHM %v, signal %v, ecc %v, score %v), "+
				"want all zero when no star was measured",
				minSNR, r.AvgFWHM, r.AvgSignal, r.AvgEccentricity, r.Score)
		}
		if r.DetectedStars == 0 {
			t.Errorf("minSNR=%.1f: no stars detected, the fixture must exercise the "+
				"zero-measurable path rather than the no-stars path", minSNR)
		}
	}
}

// TestLimitComputedStarsBoundary pins the interaction between the flag and the
// brightSkip / minUsableStars constants. processImage skips the brightest 20
// stars and refuses to measure when fewer than 80 remain, so a limit of 99
// yields nothing measurable while 100 yields exactly 80.
func TestLimitComputedStarsBoundary(t *testing.T) {
	dir := t.TempDir()
	path := writeFrame(t, dir, frameName("B", 0), goodFrameOpts())

	tests := []struct {
		limit     int
		wantStars int
	}{
		{limit: 99, wantStars: 0},
		{limit: 100, wantStars: 80},
		{limit: 101, wantStars: 81},
		{limit: 120, wantStars: 100},
		{limit: 500, wantStars: 480},
	}

	detected := processImage(path, defaultOpts()).DetectedStars
	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			o := defaultOpts()
			o.limitComputedStars = tc.limit
			r := processImage(path, o)
			if r.Error != nil {
				t.Fatalf("unexpected error: %v", r.Error)
			}
			if r.StarCount != tc.wantStars {
				t.Errorf("limit %d: starCount = %d, want %d (detected %d)", tc.limit, r.StarCount, tc.wantStars, detected)
			}
			if r.DetectedStars != detected {
				t.Errorf("limit %d: detectedStars = %d, want %d", tc.limit, r.DetectedStars, detected)
			}
		})
	}
}

// TestProcessImageErrorResults pins the shape of the two error paths. Both
// return a populated ImageResult with a nil Decision, which means the CSV
// decision column comes out blank for those rows. That is the current contract;
// if it changes, the seven-token vocabulary and the CSV consumers both need
// revisiting.
func TestProcessImageErrorResults(t *testing.T) {
	dir := t.TempDir()

	garbage := filepath.Join(dir, "garbage.FIT")
	if err := os.WriteFile(garbage, []byte("not a FITS file"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	t.Run("unreadable file", func(t *testing.T) {
		r := processImage(garbage, defaultOpts())
		if r.Error == nil {
			t.Fatal("expected an error")
		}
		if r.Decision != "" {
			t.Errorf("decision = %q, want empty alongside an error", r.Decision)
		}
		if r.Filename != "garbage.FIT" {
			t.Errorf("filename = %q, want %q", r.Filename, "garbage.FIT")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		r := processImage(filepath.Join(dir, "absent.FIT"), defaultOpts())
		if r.Error == nil {
			t.Fatal("expected an error")
		}
		if r.Decision != "" {
			t.Errorf("decision = %q, want empty alongside an error", r.Decision)
		}
	})

	t.Run("filename metadata survives a read error", func(t *testing.T) {
		// parseFilename runs before the read, so a bad file with a
		// well-formed name still reports its filter and date.
		name := "LBN527_LIGHT_B_300s_BIN1_-10C_GA0_20260910_025946_489_PA310_E.FIT"
		bad := filepath.Join(dir, name)
		if err := os.WriteFile(bad, []byte("junk"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		r := processImage(bad, defaultOpts())
		if r.Error == nil {
			t.Fatal("expected an error")
		}
		if r.Filter != "B" {
			t.Errorf("filter = %q, want %q", r.Filter, "B")
		}
		if r.Date != "2026-09-10" {
			t.Errorf("date = %q, want %q", r.Date, "2026-09-10")
		}
	})

	t.Run("frame with no stars", func(t *testing.T) {
		o := goodFrameOpts()
		o.amp = 0
		o.width, o.height = 200, 200
		path := writeFrame(t, dir, frameName("V", 0), o)

		r := processImage(path, defaultOpts())
		if r.Error == nil {
			t.Fatal("expected an error for a frame with no detectable stars")
		}
		if r.Decision != "" {
			t.Errorf("decision = %q, want empty alongside an error", r.Decision)
		}
		if r.DetectedStars != 0 {
			t.Errorf("detectedStars = %d, want 0", r.DetectedStars)
		}
	})
}

// TestProcessImageConstantFrame checks a noiseless, starless frame. With zero
// noise the MRS background is exact and no source is ever detected.
func TestProcessImageConstantFrame(t *testing.T) {
	dir := t.TempDir()
	o := goodFrameOpts()
	o.amp = 0
	o.noise = 0
	o.width, o.height = 200, 200
	path := writeFrame(t, dir, frameName("G", 0), o)

	r := processImage(path, defaultOpts())
	if r.Error == nil {
		t.Fatal("expected an error for a constant frame")
	}
	if r.DetectedStars != 0 {
		t.Errorf("detectedStars = %d, want 0", r.DetectedStars)
	}
}

// writeFrameWithSaturation builds a frame like writeFrame but stamps n
// exceptionally bright sources, standing in for saturated stars.
func writeFrameWithSaturation(t *testing.T, dir, name string, o synthOpts, n int) string {
	t.Helper()

	o.ratio = 1
	rng := rand.New(rand.NewSource(o.seed))
	pix := make([]float32, o.width*o.height)
	for i := range pix {
		pix[i] = float32(o.level + rng.NormFloat64()*o.noise)
	}

	stamp := func(cx, cy int, amp, sigma float64) {
		for dy := -8; dy <= 8; dy++ {
			for dx := -8; dx <= 8; dx++ {
				r2 := float64(dx*dx+dy*dy) / (2 * sigma * sigma)
				pix[(cy+dy)*o.width+cx+dx] += float32(amp * math.Exp(-r2))
			}
		}
	}

	for cy := o.spacing + 5; cy < o.height-o.spacing-5; cy += o.spacing {
		for cx := o.spacing + 5; cx < o.width-o.spacing-5; cx += o.spacing {
			stamp(cx, cy, o.amp, o.sigma)
		}
	}
	// Saturated stars, well inside the frame.
	for i := 0; i < n; i++ {
		stamp(40+i*60, o.height-20, 300000, 1.5)
	}

	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	tf, err := fitsio.Create(f)
	if err != nil {
		t.Fatalf("fitsio.Create: %v", err)
	}
	defer tf.Close()
	img := fitsio.NewImage(-32, []int{o.width, o.height})
	if err := img.Write(&pix); err != nil {
		t.Fatalf("write pixels: %v", err)
	}
	if err := tf.Write(img); err != nil {
		t.Fatalf("write HDU: %v", err)
	}
	return path
}
