package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// targetFixture builds an acquisition tree for one target: <root>/Lights/<target>/
// <date>/<filter>/*.FIT, which is the shape prepare reads and --target scans.
func targetFixture(t *testing.T) (root, target string) {
	t.Helper()
	target = "LBN527"
	root = t.TempDir()
	simpleFixture(target).build(t, root)
	return root, target
}

func TestAnalyzeTargetWritesBesidePrepare(t *testing.T) {
	root, target := targetFixture(t)
	out := t.TempDir()

	o := defaultOpts()
	o.target = target
	o.input = root
	o.output = out
	o.format = "csv"
	o.cache = true
	o.quiet = true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	if err := runAnalyze("", o); err != nil {
		t.Fatalf("runAnalyze --target: %v", err)
	}

	// The reports land in the target's metrics directory, the one prepare writes
	// frames.csv into, and the store sits beside them rather than at the root.
	metrics := targetMetricsDir(out, target)
	// A target run is per filter, so the report carries the filter in its name even
	// though --filter was not given.
	if _, err := os.Stat(filepath.Join(metrics, "analyse_report_H.csv")); err != nil {
		t.Errorf("report not in the target's metrics directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(metrics, "sessions_H.csv")); err != nil {
		t.Errorf("session profile not in the target's metrics directory: %v", err)
	}
	if _, err := os.Stat(cachePath(metrics)); err != nil {
		t.Errorf("cache not in the target's metrics directory: %v", err)
	}
	if _, err := os.Stat(cachePath(out)); !os.IsNotExist(err) {
		t.Errorf("cache was written at the output root as well: %v", err)
	}
}

func TestAnalyzeTargetRejectedTreeFollowsTheTarget(t *testing.T) {
	root, target := targetFixture(t)
	out := t.TempDir()

	o := defaultOpts()
	o.target = target
	o.input = root
	o.output = out
	o.format = "csv"
	o.copyRejected = true
	o.quiet = true
	// A FWHM gate nothing can pass, so every frame is rejected into a category.
	o.maxFWHM = 0.1
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	if err := runAnalyze("", o); err != nil {
		t.Fatalf("runAnalyze --target: %v", err)
	}

	categories, err := os.ReadDir(filepath.Join(out, target, rejectedOutDir))
	if err != nil {
		t.Fatalf("rejected tree not under the target: %v", err)
	}
	if len(categories) == 0 {
		t.Error("rejected tree is empty")
	}
	if _, err := os.Stat(filepath.Join(out, rejectedOutDir)); !os.IsNotExist(err) {
		t.Errorf("rejected frames were also copied to the output root: %v", err)
	}
}

func TestAnalyzeTargetAndDirAreExclusive(t *testing.T) {
	root, target := targetFixture(t)

	o := defaultOpts()
	o.target = target
	o.input = root
	o.output = t.TempDir()
	o.explicit = map[string]bool{"dir": true}

	err := runAnalyze("", o)
	if err == nil {
		t.Fatal("runAnalyze accepted --target with --dir")
	}
	if !strings.Contains(err.Error(), "--target and --dir") {
		t.Errorf("error = %v, want it to name both flags", err)
	}
}

func TestAnalyzeTargetRequiresInputAndATree(t *testing.T) {
	o := defaultOpts()
	o.target = "LBN527"
	err := runAnalyze("", o)
	if err == nil || !strings.Contains(err.Error(), "--target requires --input") {
		t.Errorf("error = %v, want it to require --input", err)
	}

	o.input = t.TempDir() // exists but holds no Lights/
	o.output = t.TempDir()
	err = runAnalyze("", o)
	if err == nil || !strings.Contains(err.Error(), "no Lights/LBN527") {
		t.Errorf("error = %v, want it to name the missing tree", err)
	}
}

func TestAnalyzeTargetImpliesRecursion(t *testing.T) {
	root, target := targetFixture(t)
	out := t.TempDir()

	o := defaultOpts()
	o.target = target
	o.input = root
	o.output = out
	o.format = "csv"
	o.quiet = true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	// The frames sit two levels below Lights/<target>; without the implied walk
	// the scan would find nothing.
	if err := runAnalyze("", o); err != nil {
		t.Fatalf("runAnalyze --target: %v", err)
	}
	recs := readCSVFile(t, filepath.Join(targetMetricsDir(out, target), "analyse_report_H.csv"))
	if len(recs) != 4 { // header plus the fixture's three lights
		t.Errorf("report has %d records, want header plus 3 frames", len(recs))
	}
}

// TestPrepareReusesWhatAnalyzeTargetStored is the property the flag exists for: a
// measurement written by analyze --target is found by prepare with no extra flags.
func TestPrepareReusesWhatAnalyzeTargetStored(t *testing.T) {
	root, target := targetFixture(t)
	out := t.TempDir()

	aopts := defaultOpts()
	aopts.target = target
	aopts.input = root
	aopts.output = out
	aopts.cache = true
	aopts.format = "csv"
	aopts.quiet = true
	aopts.minStars, aopts.minSNR, aopts.minScore = 0, 0, 0

	if err := runAnalyze("", aopts); err != nil {
		t.Fatalf("runAnalyze --target: %v", err)
	}

	popts := prepareOptionsForTest(target, root, out)
	popts.cache = true
	popts.quiet = false
	stdout := captureStdout(t, func() {
		if err := runPrepare(popts); err != nil {
			t.Errorf("runPrepare: %v", err)
		}
	})
	if !strings.Contains(stdout, "0 measured") {
		t.Errorf("prepare measured again instead of reusing analyze's store:\n%s", stdout)
	}
}

func TestAnalyzeInputIsAConfigKeyAndTargetIsNot(t *testing.T) {
	keys := knownConfigKeys()
	if !keys["analyze.input"] {
		t.Error("analyze.input is not a known config key")
	}
	if keys["analyze.target"] {
		t.Error("analyze.target is a config key; it is a per-run selection")
	}
}

// multiFilterTree builds <root>/Lights/<target>/<date>/<filter> with n analysable
// frames per filter, which is what a target run has to discover.
func multiFilterTree(t *testing.T, filters []string, n int) (root, target string) {
	t.Helper()
	target = "LBN527"
	root = t.TempDir()
	for _, filter := range filters {
		dir := filepath.Join(root, lightsDir, target, "2026-09-10", filter)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		for i := 0; i < n; i++ {
			writeFrame(t, dir, frameName(filter, i), synthOpts{
				width: 300, height: 300, level: 1000, noise: 5,
				amp: 3000, sigma: 1.3, spacing: 25, seed: int64(i),
			})
		}
	}
	return root, target
}

func TestAnalyzeTargetWithoutFilterRunsPerFilter(t *testing.T) {
	root, target := multiFilterTree(t, []string{"H", "O"}, 8)
	out := t.TempDir()

	o := defaultOpts()
	o.target = target
	o.input = root
	o.output = out
	o.format = "csv"
	o.suggest = true
	o.quiet = true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	if err := runAnalyze("", o); err != nil {
		t.Fatalf("runAnalyze --target without --filter: %v", err)
	}

	metrics := targetMetricsDir(out, target)
	for _, filter := range []string{"H", "O"} {
		for _, name := range []string{
			"analyse_report_" + filter + ".csv",
			"sessions_" + filter + ".csv",
			"suggested_" + filter + ".yaml",
		} {
			if _, err := os.Stat(filepath.Join(metrics, name)); err != nil {
				t.Errorf("%s missing: %v", name, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(metrics, analyseReportName)); !os.IsNotExist(err) {
		t.Errorf("a mixed-filter report was written as well: %v", err)
	}
}

// A target's filters run in wheel order, L R G B S H O, because that is how the
// wheel is loaded and how every other filter list reads. Alphabetical order would
// put B before R and O before S.
func TestAnalyzeTargetRunsFiltersInWheelOrder(t *testing.T) {
	root, target := multiFilterTree(t, []string{"B", "G", "H", "O", "R", "S"}, 2)
	const wheel = "R G B S H O"

	o := defaultOpts()
	o.target = target
	o.input = root
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	stdout := captureStdout(t, func() {
		if err := runAnalyze("", o); err != nil {
			t.Fatalf("runAnalyze: %v", err)
		}
	})

	if !strings.Contains(stdout, "6 filter(s): "+wheel) {
		t.Errorf("filter list is not in wheel order; want %q in:\n%s", wheel, stdout)
	}
	// The pass loop consumes the discovered slice in order, so the Processing
	// lines are the execution order itself.
	prev := -1
	for _, filter := range strings.Fields(wheel) {
		at := strings.Index(stdout, "for filter "+filter+" in ")
		switch {
		case at < 0:
			t.Fatalf("no pass for filter %s in:\n%s", filter, stdout)
		case at < prev:
			t.Errorf("filter %s ran out of wheel order:\n%s", filter, stdout)
		}
		prev = at
	}
}

func TestAnalyzeTargetDiscoversFiltersFromFilenames(t *testing.T) {
	root, target := multiFilterTree(t, []string{"H"}, 8)
	// A frame filed under H but named O belongs to O: the name is what --filter
	// matches and what every rule is keyed on.
	misfiled := filepath.Join(root, lightsDir, target, "2026-09-10", "H")
	writeFrame(t, misfiled, frameName("O", 1), synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 99,
	})

	out := t.TempDir()
	o := defaultOpts()
	o.target = target
	o.input = root
	o.output = out
	o.format = "csv"
	o.quiet = true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	if err := runAnalyze("", o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}

	metrics := targetMetricsDir(out, target)
	oRows := readCSVFile(t, filepath.Join(metrics, "analyse_report_O.csv"))
	if len(oRows) != 2 {
		t.Errorf("O report has %d records, want header plus the misfiled frame", len(oRows))
	}
	for _, row := range oRows[1:] {
		if row[1] != "O" {
			t.Errorf("O report holds a %q frame", row[1])
		}
	}
}

func TestAnalyzeTargetWarnsAboutFramesWithNoFilter(t *testing.T) {
	root, target := multiFilterTree(t, []string{"H"}, 8)
	odd := filepath.Join(root, lightsDir, target, "2026-09-10", "H")
	writeFrame(t, odd, "LBN527_LIGHT_X_300s_BIN1_-10C_GA0_20260910_230000_000_PA310_E.FIT", synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 7,
	})

	out := t.TempDir()
	o := defaultOpts()
	o.target = target
	o.input = root
	o.output = out
	o.format = "csv"
	o.quiet = true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	stderr := captureStderr(t, func() {
		if err := runAnalyze("", o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})
	if !strings.Contains(stderr, "carry no known filter") {
		t.Errorf("no warning about the unfiltered frame:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(targetMetricsDir(out, target), "analyse_report_X.csv")); !os.IsNotExist(err) {
		t.Errorf("a report was written for the unknown filter: %v", err)
	}
}

func TestAnalyzeTargetSurvivesAThinFilter(t *testing.T) {
	// H has a stack worth suggesting from; O has three frames, too thin for a
	// population. The run must warn about O and still suggest for H.
	root, target := multiFilterTree(t, []string{"H"}, 8)
	thin := filepath.Join(root, lightsDir, target, "2026-09-10", "O")
	if err := os.MkdirAll(thin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 3; i++ {
		writeFrame(t, thin, frameName("O", i), synthOpts{
			width: 300, height: 300, level: 1000, noise: 5,
			amp: 3000, sigma: 1.3, spacing: 25, seed: int64(i),
		})
	}

	out := t.TempDir()
	o := defaultOpts()
	o.target = target
	o.input = root
	o.output = out
	o.format = "csv"
	o.suggest = true
	o.quiet = true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	stderr := captureStderr(t, func() {
		if err := runAnalyze("", o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})
	if !strings.Contains(stderr, "no suggestion for O") {
		t.Errorf("no warning about the thin filter:\n%s", stderr)
	}
	metrics := targetMetricsDir(out, target)
	if _, err := os.Stat(filepath.Join(metrics, "suggested_H.yaml")); err != nil {
		t.Errorf("H lost its suggestion because O was thin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(metrics, "suggested_O.yaml")); !os.IsNotExist(err) {
		t.Errorf("a suggestion was written for the thin filter: %v", err)
	}
}
