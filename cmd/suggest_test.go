package cmd

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garfield/internal/suggeststore"
)

// uniformNights builds nights drawn from one distribution, so the suggestion
// has a clean stack to work from.
func uniformNights(count, perNight int) []nightSpec {
	nights := make([]nightSpec, 0, count)
	for i := 0; i < count; i++ {
		nights = append(nights, nightSpec{
			date: "2026091" + string(rune('0'+i)),
			n:    perNight,
			fwhm: 2.40, ecc: 0.310, snr: 31.0, halo: 1.00, bgstr: 0.0100,
			stars: 900, spread: 0.05,
		})
	}
	return nights
}

// suggestionFor runs the suggestion pass over a synthetic stack.
func suggestionFor(t *testing.T, results []ImageResult, filter string, keep float64) suggestedThresholds {
	t.Helper()
	prof := profileRun(results)
	opts := defaultOpts()
	opts.keep = keep
	s, err := suggestForFilter(results, prof, filter, opts)
	if err != nil {
		t.Fatalf("suggestForFilter: %v", err)
	}
	return s
}

func TestSuggestUsesTheDefaultBandWithoutABudget(t *testing.T) {
	results := buildStack(t, "H", uniformNights(3, 8)...)
	s := suggestionFor(t, results, "H", 0)

	if s.Band != defaultSuggestionBand {
		t.Errorf("band = %v, want the default %v", s.Band, defaultSuggestionBand)
	}
	ref := s.Reference["fwhm"]
	want := ref.Median + defaultSuggestionBand*ref.Sigma
	if math.Abs(s.Thresholds.MaxFWHM-want) > 0.01 {
		t.Errorf("max-fwhm = %.3f, want median %v + %v sigma = %.3f",
			s.Thresholds.MaxFWHM, ref.Median, defaultSuggestionBand, want)
	}
	if s.GoodCount != len(results) {
		t.Errorf("good stack = %d frames, want all %d on a clean stack", s.GoodCount, len(results))
	}
}

func TestSuggestTightensTheBandToKeepTheBudget(t *testing.T) {
	// A stack with a visible spread, so tightening the band actually costs
	// frames and the search has something to do.
	results := buildStack(t, "H", uniformNights(4, 10)...)
	for i := range results {
		results[i].AvgFWHM *= 1 + 0.02*float64(i%5)
	}

	tight := suggestionFor(t, results, "H", 0.6)
	loose := suggestionFor(t, results, "H", 0.95)

	if tight.Band >= loose.Band {
		t.Errorf("bands: %v for keep=0.6 and %v for keep=0.95; the tighter budget must give a tighter band",
			tight.Band, loose.Band)
	}
	if tight.Kept >= loose.Kept {
		t.Errorf("kept: %.2f for keep=0.6 and %.2f for keep=0.95, want the second larger", tight.Kept, loose.Kept)
	}
	if tight.Thresholds.MaxFWHM >= loose.Thresholds.MaxFWHM {
		t.Errorf("max-fwhm: %.2f (keep=0.6) not tighter than %.2f (keep=0.95)",
			tight.Thresholds.MaxFWHM, loose.Thresholds.MaxFWHM)
	}
}

func TestSuggestClampsWhatTheDataWouldOtherwiseCertify(t *testing.T) {
	results := buildStack(t, "H", uniformNights(3, 8)...)
	for i := range results {
		// A stack that is slow, elongated and poor in stars: every raw band would
		// come out beyond what the metric can mean.
		results[i].AvgEccentricity = 0.72
		results[i].DetectedStars = 150
		results[i].Score = 0.1
		results[i].SNR = 4.0
	}
	s := suggestionFor(t, results, "H", 0)

	if s.Thresholds.MaxEcc > maxEccCeiling {
		t.Errorf("max-ecc = %v, want it capped at %v", s.Thresholds.MaxEcc, maxEccCeiling)
	}
	if s.Thresholds.MinStars < minStarsFloor {
		t.Errorf("min-stars = %d, want at least %d", s.Thresholds.MinStars, minStarsFloor)
	}
	if s.Thresholds.MinSNR < minSNRFloor {
		t.Errorf("min-snr = %v, want at least %v", s.Thresholds.MinSNR, minSNRFloor)
	}
	if s.Thresholds.MinScore < 0 {
		t.Errorf("min-score = %v, want a non-negative floor", s.Thresholds.MinScore)
	}
}

func TestSuggestDerivesTheReferenceFromTheGoodStackOnly(t *testing.T) {
	soft := nightSpec{date: "20260913", n: 8, fwhm: 3.60, ecc: 0.310, snr: 31.0, halo: 1.00,
		bgstr: 0.0100, stars: 900, spread: 0.02}
	results := buildStack(t, "H", append(uniformNights(3, 8), soft)...)

	s := suggestionFor(t, results, "H", 0)

	// The soft night is excluded, so the reference sits on the good nights and
	// the max-fwhm suggestion stays near them rather than reaching for 3.6.
	if s.GoodCount != 24 {
		t.Errorf("good stack = %d frames, want the 24 of the clean nights", s.GoodCount)
	}
	if len(s.Excluded) != 1 || s.Excluded[0].Date != "2026-09-13" {
		t.Fatalf("excluded = %+v, want the soft night", s.Excluded)
	}
	if s.Thresholds.MaxFWHM > 3.0 {
		t.Errorf("max-fwhm = %.2f, want a bound from the good nights (2.40 +/- band)", s.Thresholds.MaxFWHM)
	}
}

func TestRenderSuggestionYAMLIsAPasteableThresholdBlock(t *testing.T) {
	results := buildStack(t, "H", uniformNights(3, 8)...)
	s := suggestionFor(t, results, "H", 0)
	text := renderSuggestionYAML(s)

	for _, want := range []string{
		"thresholds:", "min-snr:", "max-fwhm:", "max-ecc:", "min-score:",
		"min-stars:", "max-fwhm-sigma:", "max-halo-ratio:", "max-structure:",
		"# garfield thresholds suggested for filter H",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("YAML does not contain %q:\n%s", want, text)
		}
	}
	// The provenance must say which filter and how many frames, or a stale file
	// cannot be recognised as stale.
	if !strings.Contains(text, "3 night(s)") {
		t.Errorf("YAML does not record the population:\n%s", text)
	}
	switch {
	case strings.Contains(text, "Band"):
		// no capitalised internal names must leak
		t.Errorf("YAML leaks an internal field name:\n%s", text)
	}
}

func TestWriteSuggestionPersistsAndWritesTheArtifact(t *testing.T) {
	results := buildStack(t, "H", uniformNights(3, 8)...)
	s := suggestionFor(t, results, "H", 0)
	dir := t.TempDir()

	if err := writeSuggestion(dir, s); err != nil {
		t.Fatalf("writeSuggestion: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "suggested_H.yaml")); err != nil {
		t.Errorf("YAML artifact missing: %v", err)
	}

	store, err := suggeststore.Open(suggestionPath(dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	rec, ok := store.Lookup("H")
	if !ok {
		t.Fatal("suggestion not stored")
	}
	if rec.SuggestVersion != suggeststore.SuggestVersion || rec.MeasureVersion != measureVersion {
		t.Errorf("versions = %d/%d, want %d/%d",
			rec.SuggestVersion, rec.MeasureVersion, suggeststore.SuggestVersion, measureVersion)
	}
	if rec.Thresholds.MaxFWHM != s.Thresholds.MaxFWHM {
		t.Errorf("stored max-fwhm = %v, want %v", rec.Thresholds.MaxFWHM, s.Thresholds.MaxFWHM)
	}
	// The reference travels with the thresholds: prepare needs it to apply the
	// relative rules without recomputing them from its own subset.
	if ref, ok := rec.Reference["fwhm"]; !ok || ref.Median == 0 || ref.Sigma == 0 {
		t.Errorf("stored reference = %+v, want the fwhm statistics", rec.Reference)
	}
	if rec.Population.N != len(results) {
		t.Errorf("population n = %d, want %d", rec.Population.N, len(results))
	}
}

func TestRunAnalyzeSuggestRequiresFilterAndOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts func(analyzeOptions) analyzeOptions
		want string
	}{
		{"no filter", func(o analyzeOptions) analyzeOptions { o.output = t.TempDir(); return o }, "--suggest requires --filter"},
		{"no output", func(o analyzeOptions) analyzeOptions { o.filter = "H"; return o }, "--suggest requires --output"},
		{"keep above one", func(o analyzeOptions) analyzeOptions {
			o.filter, o.output, o.keep = "H", t.TempDir(), 1.5
			return o
		}, "invalid --keep"},
	} {
		o := defaultOpts()
		o.suggest = true
		o = tc.opts(o)
		err := runAnalyze(t.TempDir(), o)
		if err == nil {
			t.Errorf("%s: runAnalyze accepted the flags", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}
