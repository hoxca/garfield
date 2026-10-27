package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRejectCategory pins the folder names. The decision strings are a
// documented contract that contains spaces, so they are mapped by table rather
// than slugified: a new value must be added deliberately.
func TestRejectCategory(t *testing.T) {
	tests := map[string]string{
		decisionNoStars:   "no-stars",
		decisionEcc:       "eccentricity",
		decisionFWHM:      "fwhm",
		decisionSNR:       "snr",
		decisionScoring:   "scoring",
		decisionClouds:    "clouds",
		decisionBadSeeing: "bad-seeing",
	}
	for decision, want := range tests {
		if got := rejectCategory(decision); got != want {
			t.Errorf("rejectCategory(%q) = %q, want %q", decision, got, want)
		}
	}
	// An unmapped value must still produce a folder, not drop the frame.
	if got := rejectCategory("REJECTED SOMETHING NEW"); got != "other" {
		t.Errorf("unknown decision mapped to %q, want %q", got, "other")
	}
	if got := rejectCategory(decisionApproved); got != "other" {
		t.Errorf("approved frame mapped to %q, want %q", got, "other")
	}
}

// TestRunAnalyzeCopiesRejectedFrames checks the copy, the category folders and
// the fixed report name in one end-to-end run.
func TestRunAnalyzeCopiesRejectedFrames(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "good.FIT", goodFrameOpts())

	defocused := goodFrameOpts()
	defocused.sigma = 2.9 // FWHM ~6.8, past max-fwhm 5
	writeFrame(t, dir, "blurred.FIT", defocused)

	out := t.TempDir()
	o := defaultOpts()
	o.format = "csv"
	o.output = out
	o.copyRejected = true
	o.quiet = true

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}

	if _, err := os.Stat(reportPath(out)); err != nil {
		t.Errorf("report missing at %q: %v", reportPath(out), err)
	}

	// Exactly one category folder, holding exactly the rejected frame.
	rejected := filepath.Join(out, rejectedOutDir)
	categories := subDirNames(t, rejected)
	if len(categories) != 1 || categories[0] != "fwhm" {
		t.Fatalf("rejected categories = %v, want [fwhm]", categories)
	}
	got := fileNames(t, filepath.Join(rejected, "fwhm"))
	if len(got) != 1 || got[0] != "blurred.FIT" {
		t.Fatalf("fwhm/ holds %v, want [blurred.FIT]", got)
	}
	if _, err := os.Stat(filepath.Join(rejected, "fwhm", "good.FIT")); err == nil {
		t.Error("an approved frame was copied into the rejected tree")
	}
}

// TestRunAnalyzeRejectedCollision checks the flat layout's one hazard: two
// different sources can share a basename when the scan is recursive. The first
// is kept and the second reported rather than silently overwritten.
func TestRunAnalyzeRejectedCollision(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defocused := goodFrameOpts()
	defocused.sigma = 2.9
	writeFrame(t, filepath.Join(dir, "a"), "same.FIT", defocused)
	writeFrame(t, filepath.Join(dir, "b"), "same.FIT", defocused)

	out := t.TempDir()
	o := defaultOpts()
	o.format = "csv"
	o.output = out
	o.copyRejected = true
	o.recursive = true
	o.quiet = true

	stderr := captureStderr(t, func() {
		if err := runAnalyze(dir, o); err != nil {
			t.Errorf("runAnalyze: %v", err)
		}
	})

	if !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr %q does not report the basename collision", stderr)
	}
	if got := fileNames(t, filepath.Join(out, rejectedOutDir, "fwhm")); len(got) != 1 {
		t.Errorf("category folder holds %v, want one file", got)
	}
}

// TestRunAnalyzeCopyRejectedNeedsOutput pins the flag combination: copying has
// nowhere to go without a directory.
func TestRunAnalyzeCopyRejectedNeedsOutput(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "a.FIT", goodFrameOpts())

	o := defaultOpts()
	o.copyRejected = true
	o.quiet = true

	err := runAnalyze(dir, o)
	if err == nil || !strings.Contains(err.Error(), "--copy-rejected requires --output") {
		t.Errorf("error = %v, want the missing-output error", err)
	}
}

// TestRunAnalyzeConsoleOnlyStillCopies checks the two outputs are independent:
// a directory can be given for the rejected copies without asking for a report.
func TestRunAnalyzeConsoleOnlyStillCopies(t *testing.T) {
	dir := t.TempDir()
	defocused := goodFrameOpts()
	defocused.sigma = 2.9
	writeFrame(t, dir, "blurred.FIT", defocused)

	out := t.TempDir()
	o := defaultOpts()
	o.format = "console"
	o.output = out
	o.copyRejected = true
	o.quiet = true

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	if _, err := os.Stat(reportPath(out)); err == nil {
		t.Error("console-only run wrote a report")
	}
	if _, err := os.Stat(filepath.Join(out, rejectedOutDir, "fwhm", "blurred.FIT")); err != nil {
		t.Errorf("nothing was copied: %v", err)
	}
}
