package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garfield/internal/measurecache"
)

func measureOptionsFor(dir, out string) measureOptions {
	return measureOptions{
		dir:    dir,
		output: out,
		moffat: defaultThresholds.moffat,
		limit:  analyzeDefaultLimitComputedStars,
		quiet:  true,
	}
}

func TestMeasureWritesTheStoreWithoutReports(t *testing.T) {
	root, target := multiFilterTree(t, []string{"H", "O"}, 2)
	out := t.TempDir()

	opts := measureOptions{
		target: target,
		input:  root,
		output: out,
		moffat: false,
		limit:  analyzeDefaultLimitComputedStars,
		quiet:  true,
	}
	if err := runMeasure(opts); err != nil {
		t.Fatalf("runMeasure: %v", err)
	}

	store := cachePath(targetMetricsDir(out, target))
	c, err := measurecache.Open(store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if got := c.Len(); got != 4 {
		t.Errorf("store has %d entries, want 4 (two frames of two filters)", got)
	}
	c.Close()

	// Measure writes no reports: the metrics directory holds only the store.
	entries, err := os.ReadDir(targetMetricsDir(out, target))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != cacheDir {
			t.Errorf("measure wrote %q into metrics/", e.Name())
		}
	}

	// A second run reuses every entry.
	stdout := captureStdout(t, func() {
		opts.quiet = false
		if err := runMeasure(opts); err != nil {
			t.Fatalf("second run: %v", err)
		}
	})
	if !strings.Contains(stdout, "0 measured, 4 reused") {
		t.Errorf("second run measured again:\n%s", stdout)
	}

	// Measuring one filter keeps the other filter's live entries.
	opts.filter = "H"
	if err := runMeasure(opts); err != nil {
		t.Fatalf("filtered run: %v", err)
	}
	c2, err := measurecache.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if got := c2.Len(); got != 4 {
		t.Errorf("filtered measure left %d entries, want all 4", got)
	}
}

// Pruning is about stale keys, not about the subset this run touched: a key whose
// file is gone must disappear, while another filter's live key stays.
func TestMeasurePrunesStaleEntries(t *testing.T) {
	dir := synthFrames(t, 2)
	out := t.TempDir()
	if err := runMeasure(measureOptionsFor(dir, out)); err != nil {
		t.Fatalf("runMeasure: %v", err)
	}

	store := cachePath(out)
	c, err := measurecache.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put(measurecache.Entry{
		Version: measureVersion,
		Key:     measurecache.Key{Path: filepath.Join(dir, "vanished.FIT"), Size: 1, Mod: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	if err := runMeasure(measureOptionsFor(dir, out)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	c2, err := measurecache.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if got := c2.Len(); got != 2 {
		t.Errorf("store has %d entries after the prune, want the 2 live frames", got)
	}
}

func TestMeasureRequiresOutputAndInputForTarget(t *testing.T) {
	if err := runMeasure(measureOptions{dir: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "--output is required") {
		t.Errorf("error = %v, want it to require --output", err)
	}

	if err := runMeasure(measureOptions{target: "X", output: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "--target requires --input") {
		t.Errorf("error = %v, want it to require --input", err)
	}
}

func TestAnalyzeTargetRequiresOutput(t *testing.T) {
	root, target := multiFilterTree(t, []string{"H"}, 1)
	o := defaultOpts()
	o.target = target
	o.input = root

	err := runAnalyze("", o)
	if err == nil || !strings.Contains(err.Error(), "--target requires --output") {
		t.Errorf("error = %v, want it to require --output", err)
	}
}

func TestMeasureConfigKeys(t *testing.T) {
	keys := knownConfigKeys()
	for _, key := range []string{"measure.input", "measure.output"} {
		if !keys[key] {
			t.Errorf("%s is not a config key", key)
		}
	}
}
