package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// consoleHaloFrame writes a frame large enough for the halo rule's pedestal
// ring, with a broad halo on every star.
func consoleHaloFrame(t *testing.T, dir, name string, haloAmp float64) {
	t.Helper()
	writeFrame(t, dir, name, synthOpts{
		width: 1400, height: 1400, level: 1000, noise: 5,
		amp: 300, sigma: 1.5, spacing: 40,
		brightAmp: 5000, brightHalo: haloAmp,
		seed: 7,
	})
}

// TestAnalyzeConsoleShowsHaloThresholds pins the header pair. The console is
// often kept as the record of a run, so the thresholds that produced the
// decisions have to be in it; the CSV does not carry them at all.
func TestAnalyzeConsoleShowsHaloThresholds(t *testing.T) {
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

	wantHalo := formatThreshold(defaultThresholds.maxHaloRatio)
	wantStruct := formatThreshold(defaultThresholds.maxStructure)
	for _, want := range []string{
		"max-halo=" + wantHalo,
		"max-structure=" + wantStruct,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("console output missing %q:\n%s", want, out)
		}
	}

	// A 300 px frame cannot supply the 350 px pedestal ring, so no halo was
	// measured and none may be printed. "halo=0.00" would read as a clean frame.
	if strings.Contains(out, ", halo=") || strings.Contains(out, ", struct=") {
		t.Errorf("unmeasured session numbers were printed:\n%s", out)
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
	o.maxHaloRatio = 0
	o.maxStructure = 0

	out := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})

	for _, want := range []string{"max-halo=disabled", "max-structure=disabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("console output missing %q:\n%s", want, out)
		}
	}
	// The header contains "max-halo=disabled", so the per-frame check has to
	// match the field separator that only the ratio suffix uses.
	if strings.Contains(out, ", halo=") || strings.Contains(out, ", struct=") {
		t.Errorf("ratios printed while the rules were disabled:\n%s", out)
	}
}

// TestAnalyzeConsoleHaloRatioAgreesWithCSV checks the whole path on a session
// with a measurable halo: one frame's wings are three times the session's, the
// rule rejects it as clouds, and the printed ratio is the CSV's value.
func TestAnalyzeConsoleHaloRatioAgreesWithCSV(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.FIT", "b.FIT", "c.FIT", "d.FIT"} {
		consoleHaloFrame(t, dir, name, 100)
	}
	consoleHaloFrame(t, dir, "e.FIT", 300)

	out := t.TempDir()
	o := defaultOpts()
	o.format = "both"
	o.output = out
	// The fixture is a dense grid: enough stars to score, but nowhere near the
	// production minimum star count, which is not what this test is about.
	o.minStars = 0
	o.minSNR = 0
	o.minScore = 0

	stdout := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})

	if !strings.Contains(stdout, ", halo=") {
		t.Fatalf("no halo ratio on any frame line:\n%s", stdout)
	}
	if !strings.Contains(stdout, decisionClouds) {
		t.Errorf("the halo outlier was not rejected as clouds:\n%s", stdout)
	}

	recs := readCSVFile(t, reportPath(out))
	if len(recs) != 6 {
		t.Fatalf("got %d records, want header plus five frames", len(recs))
	}

	cols := map[string]int{}
	for i, h := range imageResultHeader {
		cols[h] = i
	}
	ratioCol, ok := cols["haloRatio"]
	if !ok {
		t.Fatalf("no haloRatio column in %v", imageResultHeader)
	}
	decisionCol := cols["decision"]

	outlier := -1
	for i, r := range recs[1:] {
		if r[decisionCol] == decisionClouds {
			outlier = i
		}
	}
	if outlier < 0 {
		t.Fatalf("no frame carries %q:\n%v", decisionClouds, recs)
	}

	// consoleField finds the first matching line, and the four quiet frames
	// print the field too, so narrow to the rejected frame's line first.
	var outlierLine string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, decisionClouds) {
			outlierLine = line
		}
	}
	if outlierLine == "" {
		t.Fatalf("no console line carries %q", decisionClouds)
	}
	printed := consoleField(t, outlierLine, ", halo=")
	want, err := strconv.ParseFloat(recs[1+outlier][ratioCol], 64)
	if err != nil {
		t.Fatalf("CSV haloRatio %q is not a number: %v", recs[1+outlier][ratioCol], err)
	}
	if got := fmt.Sprintf("%.2f", want); got != printed {
		t.Errorf("console printed halo=%s, the CSV records %q", printed, recs[1+outlier][ratioCol])
	}
	if want < defaultThresholds.maxHaloRatio {
		t.Errorf("the rejected frame's haloRatio is %.2f, below the threshold; the fixture is wrong", want)
	}
}
