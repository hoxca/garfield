package cmd

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
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

// storeSuggestion writes one suggestion into an output directory's store.
func storeSuggestion(t *testing.T, output string, rec suggeststore.Suggestion) {
	t.Helper()
	store, err := suggeststore.Open(suggestionPath(output))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// suggestionForFilter is a record with every gate disabled except the ones a
// test sets, so a routing outcome can only come from the gate under test.
func suggestionForFilter(filter string, th suggeststore.Thresholds) suggeststore.Suggestion {
	return suggeststore.Suggestion{
		SuggestVersion: suggeststore.SuggestVersion,
		MeasureVersion: measureVersion,
		Filter:         filter,
		Population:     suggeststore.Population{N: 100, Sessions: 3, From: "2026-09-01", To: "2026-12-31"},
		Thresholds:     th,
		Reference: map[string]suggeststore.Reference{
			"fwhm": {Median: 2.0, Sigma: 0.1},
		},
	}
}

func TestPrepareSuggestedAppliesTheStoredThresholds(t *testing.T) {
	const target = "LBN527"

	// A suggestion that rejects everything: the targets' frames have a FWHM
	// well above 0.5 px, so this can only come from the store.
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	storeSuggestion(t, out, suggestionForFilter("H", suggeststore.Thresholds{
		MaxFWHM: 0.5, MaxEcc: 0.9, MinSNR: 0, MinScore: 0, MinStars: 0,
		MaxFWHMSigma: 0, MaxHaloRatio: 0, MaxStructure: 0,
	}))

	opts := prepareOptionsForTest(target, root, out)
	opts.suggested = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readFramesCSV(t, filepath.Join(out, target))
	decision := columnIndex(t, recs[0], "decision")
	for _, r := range recs[1:] {
		if r[decision] == decisionApproved {
			t.Errorf("%s was APPROVED under a max-fwhm of 0.5: the suggestion was not applied", r[4])
		}
	}
}

func TestPrepareSuggestedUsesTheFrozenReference(t *testing.T) {
	const target = "LBN527"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	// The reference pins the FWHM median at 2.0 px. frames.csv records the ratio
	// against that median, so the column says whether the stored reference was
	// used or one was recomputed from the target's own frames.
	storeSuggestion(t, out, suggestionForFilter("H", suggeststore.Thresholds{
		MaxFWHM: 100, MaxEcc: 0.9, MinSNR: 0, MinScore: 0, MinStars: 0,
		MaxFWHMSigma: 0, MaxHaloRatio: 0, MaxStructure: 0,
	}))

	opts := prepareOptionsForTest(target, root, out)
	opts.suggested = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readFramesCSV(t, filepath.Join(out, target))
	fwhmCol := columnIndex(t, recs[0], "avgFWHM")
	ratioCol := columnIndex(t, recs[0], "fwhmRatio")
	for _, r := range recs[1:] {
		fwhm, err := strconv.ParseFloat(r[fwhmCol], 64)
		if err != nil {
			t.Fatalf("parse avgFWHM %q: %v", r[fwhmCol], err)
		}
		ratio, err := strconv.ParseFloat(r[ratioCol], 64)
		if err != nil {
			t.Fatalf("parse fwhmRatio %q: %v", r[ratioCol], err)
		}
		if want := fwhm / 2.0; math.Abs(ratio-want) > 1e-3 {
			t.Errorf("%s: fwhmRatio = %.4f, want %.4f (measured against the stored median of 2.0)",
				r[4], ratio, want)
		}
	}
}

func TestGatesFromSuggestionsWarnsAndFallsBack(t *testing.T) {
	dir := t.TempDir()
	store, err := suggeststore.Open(suggestionPath(dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Put(suggestionForFilter("H", suggeststore.Thresholds{
		MinSNR: 20, MaxFWHM: 2.5, MaxEcc: 0.5, MinScore: 1, MinStars: 200,
		MaxFWHMSigma: 3, MaxHaloRatio: 2, MaxStructure: 1.1,
	})); err != nil {
		t.Fatalf("Put: %v", err)
	}

	sessions := []sessionPlan{{
		date:    "2026-09-10",
		filters: map[string][]string{"H": {"a.FIT"}, "O": {"b.FIT"}},
	}}

	gates, warnings := gatesFromSuggestions(store, []string{"H", "O"}, sessions)
	if got := gates.thresholds("H"); got.maxFWHM != 2.5 || got.minSNR != 20 {
		t.Errorf("H thresholds = %+v, want the stored ones", got)
	}
	if got := gates.thresholds("H").maxFWHMSigma; got != 3 {
		t.Errorf("H max-fwhm-sigma = %v, want 3", got)
	}
	if _, ok := gates.frozen("H"); !ok {
		t.Error("H has no frozen reference, so the relative rules would recompute it")
	}
	// O has no suggestion: the config applies and the run says so.
	if got := gates.thresholds("O"); got.maxFWHM != defaultThresholds.maxFWHM {
		t.Errorf("O thresholds = %+v, want the config defaults", got)
	}
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "filter O") {
		t.Errorf("warnings = %v, want one naming filter O", warnings)
	}
}

func TestPrepareSuggestedRefusesWhenNothingIsStored(t *testing.T) {
	const target = "LBN527"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.suggested = true

	err := runPrepare(opts)
	if err == nil {
		t.Fatal("runPrepare --suggested accepted an empty suggestion store")
	}
	if !strings.Contains(err.Error(), "no suggested thresholds") {
		t.Errorf("error = %v, want it to name the missing suggestions", err)
	}
}

// judgedStack returns a synthetic stack whose gates have already run, so the
// simulation and the counts have verdicts to compare against.
func judgedStack(t *testing.T) ([]ImageResult, sessionProfiles) {
	t.Helper()
	soft := nightSpec{date: "20260913", n: 8, fwhm: 3.30, ecc: 0.310, snr: 31.0,
		halo: 1.00, bgstr: 0.0100, stars: 900, spread: 0.03}
	results := buildStack(t, "H", append(uniformNights(3, 8), soft)...)

	prof := profileRun(results)
	applyBaseDecision(results, defaultThresholds)
	applyFWHMRuleByFilter(results, defaultThresholds)
	applySessionRulesBySession(results, defaultThresholds)
	return results, prof
}

func TestCountRejectionsAttributesEveryRejection(t *testing.T) {
	results, _ := judgedStack(t)

	// Everything approved first, then one frame of each kind, so the attribution
	// is checked by name and not by what the rest of the stack happened to be.
	for i := range results {
		results[i].Decision = decisionApproved
	}
	results[0].Decision = decisionApproved
	results[1].Decision = decisionEcc
	results[2].Decision = decisionFWHM
	results[3].Decision = decisionSNR
	results[4].Decision = decisionScoring
	results[5].Decision = decisionClouds
	results[6].Decision = decisionBadSeeing
	results[7].Decision = decisionNoStars
	results[8].Error = errNoStarFound

	counts := countRejections(results, nil)
	for gate, want := range map[string]int{
		"min-ecc": 0, "max-ecc": 1, "max-fwhm": 1, "min-snr": 1,
		"min-score": 1, "max-halo-ratio": 1, "max-structure": 1, "min-stars": 1,
	} {
		if gate == "min-ecc" {
			continue // not a gate; guards against a typo in this table
		}
		if got := counts.byGate[gate]; got != want {
			t.Errorf("%s counted %d rejections, want %d", gate, got, want)
		}
	}
	if counts.noMetrics != 1 {
		t.Errorf("no metrics = %d, want 1", counts.noMetrics)
	}
	sum := 0
	for _, n := range counts.byGate {
		sum += n
	}
	if got, want := sum+counts.noMetrics, counts.rejected(); got != want {
		t.Errorf("attributed %d rejections, total says %d", got, want)
	}
}

func TestSimulateReproducesTheVerdictsWhenTheGatesAgree(t *testing.T) {
	results, prof := judgedStack(t)
	opts := defaultOpts()

	// A suggestion that is exactly the current thresholds with the run's own
	// reference must reproduce the run's verdicts. If this drifts, the counts in
	// the table are not describing the pipeline they claim to.
	s := suggestedThresholds{
		Filter: "H",
		Thresholds: suggeststore.Thresholds{
			MinSNR: opts.minSNR, MaxFWHM: opts.maxFWHM, MaxEcc: opts.maxEcc,
			MinScore: opts.minScore, MinStars: opts.minStars,
			MaxFWHMSigma: opts.maxFWHMSigma, MaxHaloRatio: opts.maxHaloRatio,
			MaxStructure: opts.maxStructure,
		},
		Reference: referenceFromRefs(prof.refs["H"]),
	}

	simulated := simulateSuggestion(results, s, opts, prof)
	for i, r := range results {
		if r.Error != nil {
			continue
		}
		if simulated[i] != r.Decision {
			t.Errorf("%s: simulated %q, run says %q", r.Filename, simulated[i], r.Decision)
		}
	}
}

func TestSimulateFollowsATighterSuggestion(t *testing.T) {
	results, prof := judgedStack(t)
	opts := defaultOpts()

	s := suggestedThresholds{
		Filter:     "H",
		Thresholds: suggeststore.Thresholds{MaxFWHM: 0.5, MaxEcc: 0.9, MinSNR: 0, MinScore: 0, MinStars: 0},
		Reference:  referenceFromRefs(prof.refs["H"]),
	}
	simulated := simulateSuggestion(results, s, opts, prof)

	rejected := 0
	for i, r := range results {
		if r.Error != nil {
			continue
		}
		if simulated[i] != decisionFWHM {
			t.Errorf("%s: simulated %q, want %q under a max-fwhm of 0.5", r.Filename, simulated[i], decisionFWHM)
		}
		rejected++
	}
	if rejected == 0 {
		t.Fatal("no frame was judged")
	}

	counts := countRejections(results, simulated)
	if counts.byGate["max-fwhm"] != rejected {
		t.Errorf("max-fwhm attributed %d rejections, want %d", counts.byGate["max-fwhm"], rejected)
	}
	if counts.approved != 0 {
		t.Errorf("approved = %d, want none under a max-fwhm of 0.5", counts.approved)
	}
}

func TestSuggestionDeltaCountsTheChanges(t *testing.T) {
	results, _ := judgedStack(t)
	for i := range results {
		results[i].Decision = decisionApproved
	}
	simulated := make([]string, len(results))
	for i := range simulated {
		simulated[i] = decisionApproved
	}
	simulated[0] = decisionFWHM
	simulated[1] = decisionClouds
	simulated[2] = decisionSNR

	changed, rejected, approved := suggestionDelta(results, simulated)
	if changed != 3 || rejected != 3 || approved != 0 {
		t.Errorf("delta = %d changed, %d rejected, %d approved; want 3/3/0", changed, rejected, approved)
	}

	results[3].Error = errNoStarFound
	if c, _, _ := suggestionDelta(results, simulated); c != 3 {
		t.Errorf("an unreadable frame was counted as a change: %d", c)
	}
}

func TestRunAnalyzeSuggestFillsTheCSVColumn(t *testing.T) {
	// A suggestion needs a good stack of at least fwhmMinSample frames.
	dir := synthFrames(t, fwhmMinSample+2)
	out := t.TempDir()
	o := cacheOpts(out)
	o.filter = "B"
	o.suggest = true

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}

	recs := readCSVFile(t, filepath.Join(out, "analyse_report_B.csv"))
	col := columnIndex(t, recs[0], "suggestedDecision")
	filled := 0
	for _, r := range recs[1:] {
		if r[col] != "" {
			filled++
		}
	}
	if filled == 0 {
		t.Errorf("suggestedDecision is empty on every row under --suggest:\n%v", recs)
	}

	// Without the flag the column exists but says "not asked", rather than
	// repeating the current verdict and implying the suggestion was applied.
	plain := cacheOpts(t.TempDir())
	plain.filter = "B"
	if err := runAnalyze(dir, plain); err != nil {
		t.Fatalf("runAnalyze without --suggest: %v", err)
	}
	recs = readCSVFile(t, filepath.Join(plain.output, "analyse_report_B.csv"))
	col = columnIndex(t, recs[0], "suggestedDecision")
	for _, r := range recs[1:] {
		if r[col] != "" {
			t.Errorf("%s carries suggestedDecision %q without --suggest", r[0], r[col])
		}
	}
}
