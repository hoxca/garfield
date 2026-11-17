package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astrogo/fitsio"

	"garfield/internal/suggeststore"
)

// twilightFrames writes one frame taken while the Sun was about 11 degrees down
// (twilight) and one at about 26 degrees (night), at the same site.
const (
	twilightDateObs = "2026-08-17T20:00:00" // Sun ~10.9 degrees below the horizon
	nightDateObs    = "2026-08-17T22:00:00" // Sun ~26.3 degrees below
)

func writeSiteFrame(t *testing.T, dir, name, dateObs string, seed int64) string {
	t.Helper()
	return writeFrame(t, dir, name, synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: seed,
		cards: []fitsio.Card{
			{Name: "SITELAT", Value: "+44 20 14"},
			{Name: "SITELONG", Value: "+01 01 30"},
			{Name: "DATE-OBS", Value: dateObs},
		},
	})
}

func TestBaseDecisionTwilightGate(t *testing.T) {
	q := defaultThresholds
	base := ImageResult{
		StarCount: 400, DetectedStars: 900, AvgFWHM: 1.2,
		AvgEccentricity: 0.3, SNR: 20, Score: 5, SunBelow: 14.99,
	}

	if got := baseDecision(base, q); got != decisionTwilight {
		t.Errorf("sun 14.99 below: decision = %q, want %q", got, decisionTwilight)
	}

	// Exactly on the threshold passes; it is "less than", not "at most".
	base.SunBelow = 15.0
	if got := baseDecision(base, q); got != decisionApproved {
		t.Errorf("sun exactly 15.00 below: decision = %q, want APPROVED", got)
	}

	// A depth that was never recorded cannot be judged on.
	base.SunBelow = 0
	if got := baseDecision(base, q); got != decisionApproved {
		t.Errorf("unrecorded depth: decision = %q, want APPROVED", got)
	}

	// Daylight is a negative depth, and is rejected.
	base.SunBelow = -4
	if got := baseDecision(base, q); got != decisionTwilight {
		t.Errorf("daylight frame: decision = %q, want %q", got, decisionTwilight)
	}

	// Configured to zero, the gate is off.
	q.sunBelow = 0
	base.SunBelow = 5
	if got := baseDecision(base, q); got != decisionApproved {
		t.Errorf("disabled gate: decision = %q, want APPROVED", got)
	}

	// The twilight verdict is attributed before a concurrent quality failure.
	q = defaultThresholds
	base.SunBelow = 5
	base.AvgEccentricity = 0.9
	if got := baseDecision(base, q); got != decisionTwilight {
		t.Errorf("twilight and eccentricity: decision = %q, want %q", got, decisionTwilight)
	}
}

// TestAnalyzeRejectsTwilightFrames is the pipeline check: the token, the CSV,
// the console and the rejected tree.
func TestAnalyzeRejectsTwilightFrames(t *testing.T) {
	dir := t.TempDir()
	twilight := filepath.Base(writeSiteFrame(t, dir, "twilight.FIT", twilightDateObs, 1))
	night := filepath.Base(writeSiteFrame(t, dir, "night.FIT", nightDateObs, 2))

	out := t.TempDir()
	o := defaultOpts()
	o.format, o.output, o.quiet = "csv", out, true
	o.copyRejected = true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	recs := readCSVFile(t, filepath.Join(out, analyseReportName))
	decision := columnIndex(t, recs[0], "decision")
	got := map[string]string{}
	for _, r := range recs[1:] {
		got[r[0]] = r[decision]
	}
	if got[twilight] != decisionTwilight {
		t.Errorf("%s decision = %q, want %q", twilight, got[twilight], decisionTwilight)
	}
	if got[night] != decisionApproved {
		t.Errorf("%s decision = %q, want APPROVED", night, got[night])
	}

	if _, err := os.Stat(filepath.Join(out, rejectedOutDir, "twilight", twilight)); err != nil {
		t.Errorf("twilight frame not copied to rejected/twilight: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, rejectedOutDir, "twilight", night)); !os.IsNotExist(err) {
		t.Errorf("night frame was copied to rejected/twilight: %v", err)
	}

	// The flag is the override: 5 degrees lets the 10.9-degree frame through,
	// and 0 disables the gate.
	for _, below := range []float64{5, 0} {
		o.sunBelow = below
		if err := runAnalyze(dir, o); err != nil {
			t.Fatalf("runAnalyze --sun-below %v: %v", below, err)
		}
		recs = readCSVFile(t, filepath.Join(out, analyseReportName))
		decision = columnIndex(t, recs[0], "decision")
		for _, r := range recs[1:] {
			if r[0] == twilight && r[decision] != decisionApproved {
				t.Errorf("--sun-below %v: twilight decision = %q, want APPROVED", below, r[decision])
			}
		}
	}
}

func prepareTwilightTree(t *testing.T) (root, target, twilight, night string) {
	t.Helper()
	target = "IC63"
	root = t.TempDir()
	dir := filepath.Join(root, lightsDir, target, "2026-08-17", "H")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	twilight = "IC63_LIGHT_H_300s_BIN1_-10C_001_20260817_220000_000_GA0_PA310_E.FIT"
	night = "IC63_LIGHT_H_300s_BIN1_-10C_002_20260817_230000_000_GA0_PA310_E.FIT"
	writeSiteFrame(t, dir, twilight, twilightDateObs, 3)
	writeSiteFrame(t, dir, night, nightDateObs, 4)
	return root, target, twilight, night
}

func TestPrepareRejectsTwilightFrames(t *testing.T) {
	root, target, twilight, night := prepareTwilightTree(t)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.minStars, opts.minSNR, opts.minScore = 0, 0, 0
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readFramesCSV(t, filepath.Join(out, target))
	decision := columnIndex(t, recs[0], "decision")
	name := columnIndex(t, recs[0], "filename")
	got := map[string]string{}
	for _, r := range recs[1:] {
		got[r[name]] = r[decision]
	}
	if got[twilight] != decisionTwilight {
		t.Errorf("%s decision = %q, want %q", twilight, got[twilight], decisionTwilight)
	}
	if got[night] != decisionApproved {
		t.Errorf("%s decision = %q, want APPROVED", night, got[night])
	}

	lights := filepath.Join(out, target, "Sessions", "Session_01", "lights", "H")
	if _, err := os.Stat(filepath.Join(lights, night)); err != nil {
		t.Errorf("night frame not copied into lights: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lights, twilight)); !os.IsNotExist(err) {
		t.Errorf("twilight frame was copied into lights: %v", err)
	}
	rejected := filepath.Join(out, target, rejectedDir, "Session_01", "H", twilight)
	if _, err := os.Stat(rejected); err != nil {
		t.Errorf("twilight frame not in prepare's rejected tree: %v", err)
	}
}

func TestPrepareOldSuggestionsFallBackToTheConfiguredSunBelow(t *testing.T) {
	root, target, twilight, _ := prepareTwilightTree(t)

	for _, tc := range []struct {
		name    string
		version int
		sun     float64
		want    string
	}{
		{"schema 2 has no value, config applies", 2, 0, decisionTwilight},
		{"schema 3 with zero disables the gate", 3, 0, decisionApproved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			storeSuggestion(t, out, target, suggeststore.Suggestion{
				SuggestVersion: tc.version,
				MeasureVersion: measureVersion,
				Filter:         "H",
				Population:     suggeststore.Population{N: 2, Sessions: 1, From: "2026-08-17", To: "2026-08-17"},
				Thresholds: suggeststore.Thresholds{
					MaxFWHM: 100, MaxEcc: 0.9, SunBelow: tc.sun,
				},
				Reference: map[string]suggeststore.Reference{},
			})

			opts := prepareOptionsForTest(target, root, out)
			opts.suggested = true
			if err := runPrepare(opts); err != nil {
				t.Fatalf("runPrepare --suggested: %v", err)
			}
			recs := readFramesCSV(t, filepath.Join(out, target))
			decision := columnIndex(t, recs[0], "decision")
			name := columnIndex(t, recs[0], "filename")
			for _, r := range recs[1:] {
				if r[name] == twilight && r[decision] != tc.want {
					t.Errorf("twilight decision = %q, want %q", r[decision], tc.want)
				}
			}
		})
	}
}

func TestSuggestionTableShowsTheTwilightGateFirst(t *testing.T) {
	s := suggestedThresholds{
		Filter:     "H",
		Population: suggeststore.Population{N: 10, Sessions: 2, From: "2026-09-01", To: "2026-09-02"},
		Thresholds: suggeststore.Thresholds{SunBelow: 15, MinStars: 100},
		Band:       4, Kept: 0.7, KeptCount: 7, GoodCount: 8, KeptGood: 6, Total: 10,
	}
	now := rejectionCounts{byGate: map[string]int{"sun-below": 3}, total: 10, approved: 7}
	then := rejectionCounts{byGate: map[string]int{"sun-below": 3}, total: 10, approved: 7}

	stdout := captureStdout(t, func() {
		printSuggestion(s, defaultThresholds, now, then)
	})

	sun := strings.Index(stdout, "sun-below")
	stars := strings.Index(stdout, "min-stars")
	if sun < 0 || stars < 0 || sun > stars {
		t.Errorf("sun-below is not the first gate row:\n%s", stdout)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "sun-below") {
			if !strings.Contains(line, "15") || !strings.Contains(line, "3") {
				t.Errorf("twilight row does not show the value and count: %q", line)
			}
		}
	}
}
