package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astrogo/fitsio"

	"garfield/internal/sun"
)

// consoleHaloFrame writes a frame large enough for the halo rule's pedestal
// ring, with a broad halo on every star.
func consoleHaloFrame(t *testing.T, dir, name string, haloAmp float64) {
	t.Helper()
	writeFrame(t, dir, name, synthOpts{
		width: 1400, height: 1400, level: 1000, noise: 5,
		amp: 300, sigma: 1.3, spacing: 40,
		brightAmp: 5000, brightHalo: haloAmp,
		seed: 7,
	})
}

// A frame's star count can be two digits or four, and a field may be missing on
// some frames of a run. Neither may move the columns to its right.
func TestConsoleColumnsAlign(t *testing.T) {
	results := []ImageResult{
		{Filename: "a.FIT", StarCount: 90, DetectedStars: 850,
			AvgFWHM: 1.17, AvgEccentricity: 0.481, SNR: 14.42, Score: 6.383,
			FWHMRatio: 0.90, StructRatio: 0.997, SunBelow: 31.18, Decision: decisionApproved},
		{Filename: "b.FIT", StarCount: 1020, DetectedStars: 2672,
			AvgFWHM: 1.39, AvgEccentricity: 0.421, SNR: 14.13, Score: 5.885,
			FWHMRatio: 1.06, SunBelow: 27.66, Decision: decisionTransparency},
		{Filename: "c.FIT", StarCount: 442, DetectedStars: 2116,
			AvgFWHM: 1.39, AvgEccentricity: 0.444, SNR: 14.64, Score: 5.852,
			FWHMRatio: 1.07, StructRatio: 1.020, SunBelow: 29.10,
			Decision: decisionClouds, SuggestedDecision: decisionFWHM},
	}
	layout := newConsoleLayout(results)
	rows := make([]string, len(results))
	for i, r := range results {
		rows[i] = layout.row(i+1, len(results), r.Filename, r)
	}

	for _, label := range []string{"stars", "FWHM=", "Ecc=", "SNR=", "Score=", "fwhmRatio=", "struct=", "sunBelow="} {
		want := strings.Index(rows[0], label)
		for i, row := range rows {
			if got := strings.Index(row, label); got != want {
				t.Errorf("row %d: %q at column %d, want %d\n%s\n%s", i+1, label, got, want, rows[0], row)
			}
		}
	}

	// A frame that did not measure struct keeps the column, with a dash.
	if !strings.Contains(rows[1], "struct=") || !strings.Contains(rows[1], "-") {
		t.Errorf("a frame without struct did not print a dash:\n%s", rows[1])
	}
	if strings.Index(rows[1], "sunBelow=") <= strings.Index(rows[1], "struct=") {
		t.Errorf("sunBelow is not after struct:\n%s", rows[1])
	}

	// The suggestion marker follows a padded decision, so it starts at the same
	// column as the parentheses of the other lines' verdicts.
	if !strings.Contains(rows[2], "[sugg: "+decisionFWHM+"]") {
		t.Errorf("the suggestion marker is missing:\n%s", rows[2])
	}
	if strings.Index(rows[2], ") [sugg:") < 0 {
		t.Errorf("the suggestion marker does not follow the decision:\n%s", rows[2])
	}
}

// A field no frame measured is absent from every line, not a column of dashes:
// halo=0.00 would read as a clear sky rather than as an untested rule.
func TestConsoleOmitsFieldsNoFrameMeasured(t *testing.T) {
	results := []ImageResult{
		{Filename: "a.FIT", StarCount: 90, DetectedStars: 850,
			AvgFWHM: 1.17, AvgEccentricity: 0.481, SNR: 14.42, Score: 6.383,
			Decision: decisionApproved},
	}
	row := newConsoleLayout(results).row(1, 1, "a.FIT", results[0])
	for _, label := range []string{"fwhmRatio=", "halo=", "struct=", "sunBelow="} {
		if strings.Contains(row, label) {
			t.Errorf("unmeasured field %q was printed:\n%s", label, row)
		}
	}
}

// TestAnalyzeConsoleShowsSunBelow is the end-to-end check: the line reads the
// same rounded value the report carries, and a frame with no site gets the dash.
func TestAnalyzeConsoleShowsSunBelow(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "with_site.FIT", synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 1,
		cards: []fitsio.Card{
			{Name: "SITELAT", Value: "+44 20 14"},
			{Name: "SITELONG", Value: "+01 01 30"},
			{Name: "DATE-OBS", Value: "2026-08-11T22:14:30"},
		},
	})

	o := defaultOpts()
	o.minStars, o.minSNR, o.minScore = 0, 0, 0
	out := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})
	if !strings.Contains(out, ", sunBelow=") {
		t.Errorf("console has no sunBelow column:\n%s", out)
	}

	want := roundTo(sun.BelowHorizon(time.Date(2026, 8, 11, 22, 14, 30, 0, time.UTC),
		44+20.0/60+14.0/3600, 1+1.0/60+30.0/3600), sunBelowDecimals)
	if !strings.Contains(out, fmt.Sprintf("%.2f", want)) {
		t.Errorf("console does not show sunBelow %.2f:\n%s", want, out)
	}

	// A run whose frames carry no site has no column at all.
	plain := t.TempDir()
	writeFrame(t, plain, "no_site.FIT", synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 2,
	})
	out = captureStdout(t, func() {
		if err := runAnalyze(plain, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})
	if strings.Contains(out, "sunBelow=") {
		t.Errorf("a run without site cards printed sunBelow:\n%s", out)
	}
}

// TestAnalyzeConsoleShowsHaloThresholds pins the header pair. The console is
// often kept as the record of a run, so the thresholds that produced the
// decisions have to be in it; the CSV does not carry them at all.
func TestAnalyzeConsoleShowsHaloThresholds(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "a.FIT", synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 1,
	})

	o := defaultOpts()
	out := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})

	wantSigma := formatThreshold(defaultThresholds.maxFWHMSigma)
	wantHalo := formatThreshold(defaultThresholds.maxHaloRatio)
	wantStruct := formatThreshold(defaultThresholds.maxStructure)
	for _, want := range []string{
		"max-fwhm-sigma=" + wantSigma,
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
			amp: 3000, sigma: 1.3, spacing: 25, seed: 1,
		})
	}

	o := defaultOpts()
	o.maxFWHMSigma = 0
	o.maxHaloRatio = 0
	o.maxStructure = 0

	out := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})

	for _, want := range []string{"max-fwhm-sigma=disabled", "max-halo=disabled", "max-structure=disabled"} {
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
