package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// TestAnalyzeConsoleShowsCloudThresholds pins the header pair. The console is
// often kept as the record of a run, so the thresholds that produced the
// decisions have to be in it; the CSV does not carry them at all.
func TestAnalyzeConsoleShowsCloudThresholds(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "a.FIT", synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.5, spacing: 25, seed: 1,
	})

	o := defaultOpts()
	out := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})

	wantCloud := formatThreshold(defaultThresholds.maxCloud)
	wantStruct := formatThreshold(defaultThresholds.maxStructure)
	for _, want := range []string{
		"max-cloud=" + wantCloud,
		"max-structure=" + wantStruct,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("console output missing %q:\n%s", want, out)
		}
	}

	// A single frame cannot supply a session baseline, so no ratio was measured
	// and none may be printed. "cloud=0.000" would read as a clean frame.
	if strings.Contains(out, ", cloud=") || strings.Contains(out, ", struct=") {
		t.Errorf("unmeasured cloud numbers were printed:\n%s", out)
	}
}

// TestAnalyzeConsoleShowsThresholdsDisabled checks the zero case: the flag
// means "off", and the console has to say so rather than print 0.
func TestAnalyzeConsoleShowsThresholdsDisabled(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.FIT", "b.FIT", "c.FIT", "d.FIT", "e.FIT"} {
		writeFrame(t, dir, name, synthOpts{
			width: 300, height: 300, level: 1000, noise: 5,
			amp: 3000, sigma: 1.5, spacing: 25, seed: 1,
		})
	}

	o := defaultOpts()
	o.maxCloud = 0
	o.maxStructure = 0

	out := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})

	for _, want := range []string{"max-cloud=disabled", "max-structure=disabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("console output missing %q:\n%s", want, out)
		}
	}
	// The header contains "max-cloud=disabled", so the per-frame check has to
	// match the field separator that only the ratio suffix uses.
	if strings.Contains(out, ", cloud=") || strings.Contains(out, ", struct=") {
		t.Errorf("ratios printed while the rule was disabled:\n%s", out)
	}
}

// TestAnalyzeConsoleCloudRatiosAgreeWithCSV checks that a session with a usable
// baseline prints both ratios, and that the printed cloud ratio is the CSV's
// value at the console's precision.
func TestAnalyzeConsoleCloudRatiosAgreeWithCSV(t *testing.T) {
	dir := t.TempDir()
	// Five frames with no acquisition stamp share the "unknown" session key, so
	// they do form a baseline; the rule needs four measured frames.
	for _, name := range []string{"a.FIT", "b.FIT", "c.FIT", "d.FIT", "e.FIT"} {
		writeFrame(t, dir, name, synthOpts{
			width: 300, height: 300, level: 1000, noise: 5,
			amp: 3000, sigma: 1.5, spacing: 25, seed: 1,
		})
	}

	out := t.TempDir()
	o := defaultOpts()
	o.format = "both"
	o.output = out

	stdout := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})

	if !strings.Contains(stdout, ", cloud=") {
		t.Fatalf("no cloud ratio on any frame line:\n%s", stdout)
	}
	if !strings.Contains(stdout, ", struct=") {
		t.Fatalf("no structure ratio on any frame line:\n%s", stdout)
	}

	// Search on the separator: the header also contains "cloud=" inside
	// "max-cloud=", so the bare label would find the threshold instead.
	printed := consoleField(t, stdout, ", cloud=")
	rec := readCSVFile(t, reportPath(out))[1]
	col := -1
	for i, h := range imageResultHeader {
		if h == "cloudRatio" {
			col = i
		}
	}
	if col < 0 {
		t.Fatalf("no cloudRatio column in %v", imageResultHeader)
	}
	want, err := strconv.ParseFloat(rec[col], 64)
	if err != nil {
		t.Fatalf("CSV cloudRatio %q is not a number: %v", rec[col], err)
	}
	if got := fmt.Sprintf("%.3f", want); got != printed {
		t.Errorf("console printed cloud=%s, the CSV records %q", printed, rec[col])
	}
}
