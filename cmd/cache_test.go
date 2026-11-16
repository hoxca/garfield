package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// synthFrames writes n usable frames into a fresh directory and returns it.
func synthFrames(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		writeFrame(t, dir, frameName("B", i), synthOpts{
			width: 300, height: 300, level: 1000, noise: 5,
			amp: 3000, sigma: 1.3, spacing: 25, seed: int64(i),
		})
	}
	return dir
}

// cacheOpts returns analyze options that write a report and a cache into out.
func cacheOpts(out string) analyzeOptions {
	o := defaultOpts()
	o.format = "csv"
	o.output = out
	o.quiet = true
	o.cache = true
	o.minStars = 0
	o.minSNR = 0
	o.minScore = 0
	return o
}

// columnIndex finds a column by name in a header row.
func columnIndex(t *testing.T, header []string, name string) int {
	t.Helper()
	for i, h := range header {
		if h == name {
			return i
		}
	}
	t.Fatalf("column %q not in header %v", name, header)
	return -1
}

func TestRunAnalyzeCacheRequiresOutput(t *testing.T) {
	o := defaultOpts()
	o.cache = true

	err := runAnalyze(synthFrames(t, 1), o)
	if err == nil {
		t.Fatal("runAnalyze accepted --cache without an output directory")
	}
	if !strings.Contains(err.Error(), "--cache requires --output") {
		t.Errorf("error = %v, want it to name the missing --output", err)
	}
}

func TestRunAnalyzeCacheReusesMeasurements(t *testing.T) {
	dir := synthFrames(t, 2)
	out := t.TempDir()
	o := cacheOpts(out)

	// The frames are inputs, not scratch space: nothing garfield does may change
	// a byte of them.
	before := make(map[string][]byte)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read frames dir: %v", err)
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		before[path] = b
	}

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("first run: %v", err)
	}
	store := cachePath(out)
	if _, err := os.Stat(store); err != nil {
		t.Fatalf("no cache written to %q: %v", store, err)
	}

	first := readCSVFile(t, reportPath(out))
	if len(first) != 3 {
		t.Fatalf("first report has %d records, want header plus 2 frames", len(first))
	}
	cachedCol := columnIndex(t, first[0], "cached")
	for _, r := range first[1:] {
		if r[cachedCol] != "false" {
			t.Errorf("%s reports cached=%s on the first run, want false", r[0], r[cachedCol])
		}
	}

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("second run: %v", err)
	}
	second := readCSVFile(t, reportPath(out))
	if len(second) != len(first) {
		t.Fatalf("second report has %d records, want %d", len(second), len(first))
	}
	for i := range first {
		if i == 0 {
			continue
		}
		if second[i][cachedCol] != "true" {
			t.Errorf("%s reports cached=%s on the second run, want true", second[i][0], second[i][cachedCol])
		}
		// Every other column must be identical: the point of the cache is to
		// reproduce the measurement, not to approximate it.
		for j := range first[i] {
			if j == cachedCol {
				continue
			}
			if second[i][j] != first[i][j] {
				t.Errorf("%s column %q: %q on the first run, %q on the second",
					first[i][0], first[0][j], first[i][j], second[i][j])
			}
		}
	}

	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("re-read frame: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s was modified by the run", path)
		}
	}
}

func TestRunAnalyzeCacheReportsReuseOnTheConsole(t *testing.T) {
	dir := synthFrames(t, 2)
	out := t.TempDir()
	o := cacheOpts(out)
	o.quiet = false
	o.format = "console"

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("first run: %v", err)
	}
	stdout := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("second run: %v", err)
		}
	})
	if !strings.Contains(stdout, "cache: 0 measured, 2 reused") {
		t.Errorf("console does not report the reuse:\n%s", stdout)
	}
}

func TestRunAnalyzeCacheInvalidatedByStarLimit(t *testing.T) {
	dir := synthFrames(t, 1)
	out := t.TempDir()
	o := cacheOpts(out)

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := readCSVFile(t, reportPath(out))
	cachedCol := columnIndex(t, first[0], "cached")

	// limitComputedStars decides which stars are fitted, so it changes the
	// measurement itself rather than only the verdict.
	o.limitComputedStars = 40
	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("second run: %v", err)
	}
	second := readCSVFile(t, reportPath(out))
	if second[1][cachedCol] != "false" {
		t.Errorf("entry survived a change of --limit-computed-stars: cached=%s", second[1][cachedCol])
	}
}

func TestRunAnalyzeCacheDoesNotStoreUnreadableFrames(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, frameName("B", 0)), []byte("not a fits file"), 0o644); err != nil {
		t.Fatalf("write garbage frame: %v", err)
	}
	out := t.TempDir()
	o := cacheOpts(out)

	for run := 1; run <= 2; run++ {
		if err := runAnalyze(dir, o); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		recs := readCSVFile(t, reportPath(out))
		cachedCol := columnIndex(t, recs[0], "cached")
		if recs[1][cachedCol] != "false" {
			t.Errorf("run %d: an unreadable frame was served from the cache", run)
		}
		if recs[1][columnIndex(t, recs[0], "error")] == "" {
			t.Errorf("run %d: no error recorded for the unreadable frame", run)
		}
	}
}

func TestPrepareCacheReusesMeasurements(t *testing.T) {
	const target = "LBN527"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.cache = true

	if err := runPrepare(opts); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := readFramesCSV(t, filepath.Join(out, target))
	if len(first) < 2 {
		t.Fatalf("frames.csv has %d records, want header plus frames", len(first))
	}
	cachedCol := columnIndex(t, first[0], "cached")
	for _, r := range first[1:] {
		if r[cachedCol] != "false" {
			t.Errorf("%s reports cached=%s on the first run, want false", r[4], r[cachedCol])
		}
	}

	if err := runPrepare(opts); err != nil {
		t.Fatalf("second run: %v", err)
	}
	second := readFramesCSV(t, filepath.Join(out, target))
	if len(second) != len(first) {
		t.Fatalf("second run wrote %d records, want %d", len(second), len(first))
	}
	for i := 1; i < len(first); i++ {
		if second[i][cachedCol] != "true" {
			t.Errorf("%s reports cached=%s on the second run, want true", second[i][4], second[i][cachedCol])
		}
		// The verdict is rebuilt from the stored measurement, so every other
		// column must come out the same.
		for j := range first[i] {
			if j == cachedCol {
				continue
			}
			if second[i][j] != first[i][j] {
				t.Errorf("%s column %q: %q then %q",
					first[i][4], first[0][j], first[i][j], second[i][j])
			}
		}
	}
}

func TestPrepareCacheDryRunWritesNothing(t *testing.T) {
	const target = "LBN527"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.cache = true
	opts.dryRun = true

	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}
	// A dry run shows the plan without writing; the store is a write.
	if _, err := os.Stat(cachePath(targetMetricsDir(out, target))); !os.IsNotExist(err) {
		t.Errorf("dry run left a measurement cache behind: %v", err)
	}
}
