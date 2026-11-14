package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormaliseFilter(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"H", "H", false},
		{"h", "H", false},
		{" s ", "S", false},
		{"X", "", true},
		{"Ha", "", true},
		{"HH", "", true},
	}
	for _, tc := range tests {
		got, err := normaliseFilter(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normaliseFilter(%q) accepted an invalid value", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("normaliseFilter(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normaliseFilter(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAnalyseReportNameFor(t *testing.T) {
	if got := analyseReportNameFor(""); got != "analyse_report.csv" {
		t.Errorf("unfiltered report = %q", got)
	}
	if got := analyseReportNameFor("H"); got != "analyse_report_H.csv" {
		t.Errorf("filtered report = %q, want analyse_report_H.csv", got)
	}
}

func TestRunAnalyzeFilterSelectsFramesAndNamesReport(t *testing.T) {
	dir := t.TempDir()
	for i, filter := range []string{"B", "H", "B", "H"} {
		writeFrame(t, dir, frameName(filter, i), synthOpts{
			width: 300, height: 300, level: 1000, noise: 5,
			amp: 3000, sigma: 1.3, spacing: 25, seed: int64(i),
		})
	}

	out := t.TempDir()
	o := defaultOpts()
	o.format = "csv"
	o.output = out
	o.quiet = true
	o.filter = "h" // lower case is accepted and normalised
	o.minStars = 0
	o.minSNR = 0
	o.minScore = 0

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}

	report := filepath.Join(out, "analyse_report_H.csv")
	recs := readCSVFile(t, report)
	if len(recs) != 3 {
		t.Fatalf("report has %d records, want header plus 2 H frames", len(recs))
	}
	for _, r := range recs[1:] {
		if r[1] != "H" {
			t.Errorf("row %q carries filter %q, want H", r[0], r[1])
		}
	}
	if _, err := os.Stat(filepath.Join(out, "analyse_report.csv")); !os.IsNotExist(err) {
		t.Errorf("unfiltered report exists after a filtered run (stat err: %v)", err)
	}
}

func TestRunAnalyzeFilterConsoleHeader(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, frameName("H", 0), synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 1,
	})
	writeFrame(t, dir, frameName("B", 1), synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 2,
	})

	o := defaultOpts()
	o.filter = "H"
	o.minStars = 0
	o.minSNR = 0
	o.minScore = 0

	stdout := captureStdout(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})
	if !strings.Contains(stdout, "for filter H") {
		t.Errorf("console header does not name the filter:\n%s", stdout)
	}
	if strings.Contains(stdout, frameName("B", 1)) {
		t.Errorf("console reports the B frame despite --filter H:\n%s", stdout)
	}
}

func TestRunAnalyzeFilterRejectsUnknownValue(t *testing.T) {
	out := t.TempDir()
	o := defaultOpts()
	o.format = "csv"
	o.output = out
	o.filter = "X"

	err := runAnalyze(t.TempDir(), o)
	if err == nil {
		t.Fatal("runAnalyze accepted an invalid filter")
	}
	if !strings.Contains(err.Error(), "invalid --filter") {
		t.Errorf("error = %v, want it to name the invalid filter", err)
	}
	entries, statErr := os.ReadDir(out)
	if statErr != nil {
		t.Fatalf("read output dir: %v", statErr)
	}
	if len(entries) != 0 {
		t.Errorf("output dir holds %d entries after a rejected filter", len(entries))
	}
}

func TestRunAnalyzeFilterWithNoMatches(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, frameName("B", 0), synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 1,
	})

	o := defaultOpts()
	o.filter = "H"

	err := runAnalyze(dir, o)
	if err == nil {
		t.Fatal("runAnalyze succeeded with no frame of the selected filter")
	}
	if !strings.Contains(err.Error(), "no FITS file for filter H") {
		t.Errorf("error = %v, want it to name filter H", err)
	}
}
