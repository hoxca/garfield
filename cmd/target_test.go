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
	if _, err := os.Stat(filepath.Join(metrics, analyseReportName)); err != nil {
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
	recs := readCSVFile(t, filepath.Join(targetMetricsDir(out, target), analyseReportName))
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
