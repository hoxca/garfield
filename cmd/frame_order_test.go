package cmd

import (
	"path/filepath"
	"strings"
	"testing"
)

// frameNameWithIndex builds an acquisition name whose frame index and clock can
// disagree, which is what the ordering has to resolve in favour of the clock.
func frameNameWithIndex(index, date, clock string) string {
	return "LBN527_LIGHT_H_300s_BIN1_-10C_" + index + "_" + date + "_" + clock +
		"_000_GA_100_OF_50_PA97_W.FIT"
}

func frameOrderFixture(t *testing.T) (dir, older, newer, untimed string) {
	t.Helper()
	dir = t.TempDir()
	// The index runs opposite to the clock: 003 was taken first, 001 the next
	// night. Alphabetical order would put 001 before 003 and be wrong.
	older = frameNameWithIndex("003", "20260813", "232249")
	newer = frameNameWithIndex("001", "20260814", "003639")
	untimed = "flat.FIT"
	for i, name := range []string{older, newer, untimed} {
		writeFrame(t, dir, name, synthOpts{
			width: 300, height: 300, level: 1000, noise: 5,
			amp: 3000, sigma: 1.3, spacing: 25, seed: int64(i + 1),
		})
	}
	return dir, older, newer, untimed
}

func TestAnalyzeOrdersFramesByAcquisitionTime(t *testing.T) {
	dir, older, newer, untimed := frameOrderFixture(t)
	out := t.TempDir()

	o := defaultOpts()
	o.format, o.output, o.quiet = "csv", out, true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	recs := readCSVFile(t, filepath.Join(out, analyseReportName))
	if len(recs) != 4 {
		t.Fatalf("report has %d records, want header plus three frames", len(recs))
	}
	// Oldest first, and a name without a stamp after every timed one.
	if recs[1][0] != older || recs[2][0] != newer || recs[3][0] != untimed {
		t.Errorf("rows = %q, %q, %q; want %q, %q, %q",
			recs[1][0], recs[2][0], recs[3][0], older, newer, untimed)
	}

	// A cache hit must not disturb the order: the sort is on the input list, not
	// on how a frame was measured.
	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("second run: %v", err)
	}
	again := readCSVFile(t, filepath.Join(out, analyseReportName))
	cached := columnIndex(t, again[0], "cached")
	for i := 1; i < len(again); i++ {
		if again[i][0] != recs[i][0] {
			t.Errorf("cached row %d = %q, want %q", i, again[i][0], recs[i][0])
		}
	}
	if again[1][cached] != "true" {
		t.Errorf("the second run did not reuse the measurement: cached=%q", again[1][cached])
	}
}

func TestAnalyzeConsoleOrdersFramesByAcquisitionTime(t *testing.T) {
	dir, _, _, _ := frameOrderFixture(t)

	o := defaultOpts()
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	stdout := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Fatalf("runAnalyze: %v", err)
		}
	})

	older := strings.Index(stdout, "20260813_232249")
	newer := strings.Index(stdout, "20260814_003639")
	if older < 0 || newer < 0 {
		t.Fatalf("console is missing a frame line:\n%s", stdout)
	}
	if older > newer {
		t.Errorf("console printed the newer frame first:\n%s", stdout)
	}
}

func TestPrepareOrdersFramesCSVByAcquisitionTime(t *testing.T) {
	const target = "IC63"
	root := t.TempDir()
	// Same disagreement as analyze's fixture: the lower index is the later frame.
	older := "IC63_LIGHT_H_300s_BIN1_-10C_003_20260910_220000_000_GA0_PA310_E.FIT"
	newer := "IC63_LIGHT_H_300s_BIN1_-10C_001_20260910_230000_000_GA0_PA310_E.FIT"
	prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {"2026-09-10": {"H": {older, newer}}},
		},
	}.build(t, root)

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readFramesCSV(t, filepath.Join(out, target))
	if len(recs) != 3 {
		t.Fatalf("frames.csv has %d records, want header plus two frames", len(recs))
	}
	name := columnIndex(t, recs[0], "filename")
	if recs[1][name] != older || recs[2][name] != newer {
		t.Errorf("rows = %q, %q; want %q, %q", recs[1][name], recs[2][name], older, newer)
	}
}
