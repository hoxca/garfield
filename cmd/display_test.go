package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garfield/internal/suggeststore"
)

// displayableSuggestion is a finished record with the table --display replays:
// five nights, one excluded, and three frames rejected under the suggestion.
func displayableSuggestion(filter string) suggeststore.Suggestion {
	rec := suggestionForFilter(filter, suggeststore.Thresholds{
		MinStars: 100, MaxEcc: 0.61, MaxFWHM: 1.8, MinSNR: 10.48,
		MinScore: 0, MaxHaloRatio: 0, MaxStructure: 1.41, MinTransparency: 0.8,
	})
	rec.Population = suggeststore.Population{N: 75, Sessions: 5, From: "2026-08-12", To: "2026-08-18"}
	rec.Band = 4.0
	rec.Kept = 72.0 / 75.0
	rec.Sessions = []suggeststore.SessionNote{
		{Date: "2026-08-12", N: 18},
		{Date: "2026-08-13", N: 18, Flags: []string{"trailed", "sparse"}},
	}
	rec.Display = &suggeststore.Display{
		Current:   thresholdsFromQuality(defaultThresholds),
		Rejected:  map[string]int{"max-ecc": 1, "min-transparency": 2},
		NoMetrics: 0,
		Total:     75,
		Approved:  72,
	}
	return rec
}

// TestDisplayPrintsTheStoredTable is the whole point of the flag: the derived
// table, replayed from the record, with no frame lines and no analysis.
func TestDisplayPrintsTheStoredTable(t *testing.T) {
	const target = "SH2-142"
	out := t.TempDir()
	storeSuggestion(t, out, target, displayableSuggestion("O"))
	path := suggestionPath(targetMetricsDir(out, target))
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}

	o := defaultOpts()
	o.display = true
	o.target = target
	o.output = out
	o.filter = "O"
	// The acquisition volume is not consulted: --input names a directory that
	// does not exist, and --target names a tree that is not there either.
	o.input = filepath.Join(t.TempDir(), "unmounted")

	stdout := captureStdout(t, func() {
		if err := runAnalyze("", o); err != nil {
			t.Fatalf("runAnalyze --display: %v", err)
		}
	})

	for _, want := range []string{
		"suggested thresholds for filter O (75 frame(s), 5 night(s), 2026-08-12..2026-08-18)",
		"reference excludes 1 night(s): 2026-08-13 (trailed,sparse)",
		"band 4.0 sigma around the good stack: keeps 72/75 of the filter overall (96%)",
		"current  suggested  rejects",
		"min-stars",
		"max-ecc",
		"min-transparency",
		"3     (72/75 under the suggestion)",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("display does not contain %q:\n%s", want, stdout)
		}
	}
	// The counts come from the record, gate by gate.
	for _, want := range []string{"380", "100", "0.61", "10.48", "1.41"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("display does not show %q:\n%s", want, stdout)
		}
	}
	// Nothing from the analysis pass may leak in.
	for _, unwanted := range []string{"Processing", "FITS file(s)", "[001/"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("display printed %q:\n%s", unwanted, stdout)
		}
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat store after display: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("--display rewrote the suggestion store")
	}
}

func TestDisplayListsEveryStoredFilterInWheelOrder(t *testing.T) {
	const target = "SH2-142"
	out := t.TempDir()
	for _, filter := range []string{"H", "O", "R"} {
		storeSuggestion(t, out, target, displayableSuggestion(filter))
	}

	o := defaultOpts()
	o.display = true
	o.target = target
	o.output = out

	stdout := captureStdout(t, func() {
		if err := runAnalyze("", o); err != nil {
			t.Fatalf("runAnalyze --display: %v", err)
		}
	})

	if got := strings.Count(stdout, "suggested thresholds for filter"); got != 3 {
		t.Errorf("printed %d tables, want 3:\n%s", got, stdout)
	}
	// R sits at wheel position 2 and H at 6, so alphabetical order is wrong.
	r := strings.Index(stdout, "filter R (")
	h := strings.Index(stdout, "filter H (")
	oAt := strings.Index(stdout, "filter O (")
	if r < 0 || h < 0 || oAt < 0 || !(r < h && h < oAt) {
		t.Errorf("tables are not in R, H, O order (r=%d h=%d o=%d):\n%s", r, h, oAt, stdout)
	}
}

func TestDisplayReadsTheNonTargetStore(t *testing.T) {
	out := t.TempDir()
	store, err := suggeststore.Open(suggestionPath(out))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Put(displayableSuggestion("H")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	o := defaultOpts()
	o.display = true
	o.output = out
	o.filter = "H"

	stdout := captureStdout(t, func() {
		if err := runAnalyze("", o); err != nil {
			t.Fatalf("runAnalyze --display: %v", err)
		}
	})
	if !strings.Contains(stdout, "suggested thresholds for filter H") {
		t.Errorf("display did not read <output>/cache/suggestions.jsonl:\n%s", stdout)
	}
}

func TestDisplayRequiresOutputAndRefusesSuggest(t *testing.T) {
	o := defaultOpts()
	o.display = true
	err := runAnalyze("", o)
	if err == nil || !strings.Contains(err.Error(), "--display requires --output") {
		t.Errorf("error = %v, want it to require --output", err)
	}

	o.output = t.TempDir()
	o.suggest = true
	err = runAnalyze("", o)
	if err == nil || !strings.Contains(err.Error(), "--display shows the stored suggestion") {
		t.Errorf("error = %v, want it to refuse --suggest", err)
	}
}

func TestDisplayReportsMissingAndStaleRecords(t *testing.T) {
	const target = "SH2-142"
	out := t.TempDir()
	// A record written before --display existed: every field it has still
	// means what it meant, but there is no table to replay.
	storeSuggestion(t, out, target, suggestionForFilter("H", suggeststore.Thresholds{MaxFWHM: 2}))

	o := defaultOpts()
	o.display = true
	o.target = target
	o.output = out

	o.filter = "L"
	err := runAnalyze("", o)
	if err == nil || !strings.Contains(err.Error(), "no stored suggestion for filter L") {
		t.Errorf("error = %v, want it to name the missing record", err)
	}

	o.filter = "H"
	err = runAnalyze("", o)
	if err == nil || !strings.Contains(err.Error(), "predates --display") {
		t.Errorf("error = %v, want it to name the stale record", err)
	}

	// Listing every filter warns and reports that nothing could be shown.
	o.filter = ""
	stderr := captureStderr(t, func() {
		err = runAnalyze("", o)
	})
	if err == nil || !strings.Contains(err.Error(), "no displayable suggestions") {
		t.Errorf("error = %v, want it to say nothing could be displayed", err)
	}
	if !strings.Contains(stderr, "predates --display") {
		t.Errorf("no warning about the stale record:\n%s", stderr)
	}
}

func TestDisplayEmptyStoreIsAnError(t *testing.T) {
	o := defaultOpts()
	o.display = true
	o.output = t.TempDir()
	err := runAnalyze("", o)
	if err == nil || !strings.Contains(err.Error(), "no stored suggestions") {
		t.Errorf("error = %v, want it to say the store is empty", err)
	}
}

func TestDisplayIsNotAConfigKey(t *testing.T) {
	if knownConfigKeys()["analyze.display"] {
		t.Error("analyze.display is a config key; it is a per-run selection")
	}
}

// The snapshot written by --suggest and the table --display prints are the same
// numbers: the total row must agree with the record the derivation stored.
func TestDisplayReplaysWhatSuggestDerived(t *testing.T) {
	root, target := multiFilterTree(t, []string{"H"}, 8)
	out := t.TempDir()

	o := defaultOpts()
	o.target, o.input, o.output = target, root, out
	o.format = "csv"
	o.suggest = true
	o.quiet = true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0
	if err := runAnalyze("", o); err != nil {
		t.Fatalf("runAnalyze --suggest: %v", err)
	}

	store, err := suggeststore.Open(suggestionPath(targetMetricsDir(out, target)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rec, ok := store.Lookup("H")
	if !ok || rec.Display == nil {
		t.Fatalf("no displayable record for H: %+v", rec)
	}
	// Every frame is either approved or accounted for by a gate, so the table's
	// column sums to its total row.
	sum := rec.Display.NoMetrics
	for _, n := range rec.Display.Rejected {
		sum += n
	}
	if sum != rec.Display.Total-rec.Display.Approved {
		t.Errorf("gate counts sum to %d, want %d rejected", sum, rec.Display.Total-rec.Display.Approved)
	}

	d := defaultOpts()
	d.display = true
	d.target = target
	d.output = out
	d.filter = "H"
	d.input = filepath.Join(t.TempDir(), "unmounted")

	stdout := captureStdout(t, func() {
		if err := runAnalyze("", d); err != nil {
			t.Fatalf("runAnalyze --display: %v", err)
		}
	})
	want := fmt.Sprintf("%d/%d under the suggestion", rec.Display.Approved, rec.Display.Total)
	if !strings.Contains(stdout, want) {
		t.Errorf("display does not show %q:\n%s", want, stdout)
	}
	if !strings.Contains(stdout, "suggested thresholds for filter H") {
		t.Errorf("display did not replay filter H:\n%s", stdout)
	}
}
