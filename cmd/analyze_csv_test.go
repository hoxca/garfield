package cmd

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestParseFilenameInstrumentConvention(t *testing.T) {
	// Names produced by the instrument: TARGET_LIGHT_<filter>_<exposure>_...
	tests := []struct {
		name       string
		file       string
		wantFilter string
		wantDate   string
	}{
		{
			name:       "B band",
			file:       "LBN527_LIGHT_B_300s_BIN1_-10C_GA0_20260910_025946_489_PA310_E.FIT",
			wantFilter: "B",
			wantDate:   "2026-09-10",
		},
		{
			name:       "S band with gain",
			file:       "LBN527_LIGHT_S_600s_BIN1_-10C_GA2750_20260916_062721_275_PA310_E.FIT",
			wantFilter: "S",
			wantDate:   "2026-09-16",
		},
		{
			name:       "R band west side",
			file:       "LBN527_LIGHT_R_300s_BIN1_-10C_GA0_20260910_213705_935_PA310_W.FIT",
			wantFilter: "R",
			wantDate:   "2026-09-10",
		},
		{
			name:       "month boundary",
			file:       "LBN527_LIGHT_G_300s_BIN1_-10C_GA0_20261231_235959_000_PA310_E.FIT",
			wantFilter: "G",
			wantDate:   "2026-12-31",
		},
		{
			name:       "day boundary",
			file:       "LBN527_LIGHT_L_300s_BIN1_-10C_GA0_20260101_000000_000_PA310_E.FIT",
			wantFilter: "L",
			wantDate:   "2026-01-01",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			filter, date := parseFilename(tc.file)
			if filter != tc.wantFilter {
				t.Errorf("filter = %q, want %q", filter, tc.wantFilter)
			}
			if date != tc.wantDate {
				t.Errorf("date = %q, want %q", date, tc.wantDate)
			}
		})
	}
}

// TestParseFilenameStripsExtension checks the extension is removed before
// splitting, so a multi-part extension does not leak into the last field.
func TestParseFilenameStripsExtension(t *testing.T) {
	// Without the TrimSuffix, parts would end in "double.ext" and the field
	// count would still happen to be enough; the trailing filter index is what
	// matters here.
	filter, date := parseFilename("A_B_C.fits")
	if filter != "C" {
		t.Errorf("filter = %q, want %q", filter, "C")
	}
	if date != "" {
		t.Errorf("date = %q, want empty for a name with too few fields", date)
	}
}

// TestParseFilenameMalformed covers names that do not follow the convention.
// parseFilename indexes parts[2] and parts[7] with length guards, so every one
// of these must return empty strings rather than panic.
func TestParseFilenameMalformed(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"no underscores", "single.FIT"},
		{"one underscore", "a_b.FIT"},
		{"two underscores", "a_b_c.FIT"},
		{"extensionless", "noextension"},
		{"empty string", ""},
		{"dot only", ".FIT"},
		{"underscore only", "_.FIT"},
		{"too few fields for a date", "x_y_z_1_2_3_4.fits"},
		{"short date field", "a_b_c_d_e_f_g_123.fits"},
		{"date field present but not a date", "a_b_c_d_e_f_g_hello.fits"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The contract is only that this does not panic and that anything
			// it does return is a substring of the input, never a garbled
			// slice of a date field.
			filter, date := parseFilename(tc.file)
			if len(date) != 0 && len(date) != 10 {
				t.Errorf("date = %q, want either empty or a 10-character YYYY-MM-DD", date)
			}
			if len(date) == 10 {
				if date[4] != '-' || date[7] != '-' {
					t.Errorf("date = %q, want dashes at positions 4 and 7", date)
				}
			}
			_ = filter
		})
	}
}

// TestParseFilenameMasterFlat documents a case the parser gets wrong. The
// master-flat naming convention differs from the science frames, so the
// fixed index lands on a dimension string rather than a filter.
//
// This is a known limitation rather than intended behaviour; the test exists so
// the current output is visible and a future fix is a deliberate change.
func TestParseFilenameMasterFlat(t *testing.T) {
	const name = "masterFlat_BIN-1_9576x6388_FILTER-S_mono_SESSION-1.fit"
	filter, date := parseFilename(name)

	if filter != "9576x6388" {
		t.Errorf("filter = %q, want %q (the parser indexes parts[2], which is a "+
			"dimension for master flats)", filter, "9576x6388")
	}
	if date != "" {
		t.Errorf("date = %q, want empty", date)
	}
}

// TestParseFilenameExtensionIndependence confirms the parser does not care
// which FITS extension is used.
func TestParseFilenameExtensionIndependence(t *testing.T) {
	const stem = "LBN527_LIGHT_B_300s_BIN1_-10C_GA0_20260910_025946_489_PA310_E"
	for _, ext := range []string{".FIT", ".fits", ".fts", ".FITS", ".Fts"} {
		t.Run(ext, func(t *testing.T) {
			filter, date := parseFilename(stem + ext)
			if filter != "B" {
				t.Errorf("filter = %q, want %q", filter, "B")
			}
			if date != "2026-09-10" {
				t.Errorf("date = %q, want %q", date, "2026-09-10")
			}
		})
	}
}

// TestParseFilenameRoundTripThroughProcessImage checks the parser and the
// result struct agree, since processImage populates both from one call.
func TestParseFilenameRoundTripThroughProcessImage(t *testing.T) {
	dir := t.TempDir()
	const filter = "V"
	name := "LBN527_LIGHT_" + filter + "_300s_BIN1_-10C_GA0_20260101_000000_000_PA310_E0.FIT"
	path := writeFrame(t, dir, name, goodFrameOpts())

	r := processImage(path, defaultOpts())
	if r.Filename != name {
		t.Errorf("filename = %q, want %q", r.Filename, name)
	}
	if r.Filter != filter {
		t.Errorf("filter = %q, want %q", r.Filter, filter)
	}
	if r.Date != "2026-01-01" {
		t.Errorf("date = %q, want %q", r.Date, "2026-01-01")
	}
}

func TestWriteResultsCSVHeader(t *testing.T) {
	want := []string{
		"filename", "filter", "date", "detectedStars", "starCount",
		"avgFWHM", "avgSignal", "avgEccentricity", "snr", "score",
		"decision", "error",
	}

	var sb strings.Builder
	if err := writeResultsCSV(nil, newCSVWriter(&sb)); err != nil {
		t.Fatalf("writeResultsCSV: %v", err)
	}

	recs, err := newCSVReader(strings.NewReader(sb.String())).ReadAll()
	if err != nil {
		t.Fatalf("header does not parse: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records for empty results, want just the header", len(recs))
	}
	if len(recs[0]) != len(want) {
		t.Fatalf("header has %d fields, want %d: %v", len(recs[0]), len(want), recs[0])
	}
	for i := range want {
		if recs[0][i] != want[i] {
			t.Errorf("header[%d] = %q, want %q", i, recs[0][i], want[i])
		}
	}
}

func TestWriteResultsCSVFormatting(t *testing.T) {
	results := []ImageResult{
		{
			Filename: "a.FIT", Filter: "B", Date: "2026-01-01",
			DetectedStars: 100, StarCount: 50,
			AvgFWHM: 3.14159, AvgSignal: 1234.5678, AvgEccentricity: 0.123456,
			SNR: 12.5, Score: 3.7, Decision: decisionApproved,
		},
		{
			Filename: "b.FIT",
			Error:    errFake{},
		},
		{
			// A semicolon in the filename must be quoted, since that is the
			// separator.
			Filename: "c;semi.FIT", Decision: decisionApproved,
		},
		{
			// A comma is no longer special and must be written bare. This is
			// the inverse of the pre-semicolon behaviour, where the comma was
			// the separator and this field would have needed quotes.
			Filename: "d,comma.FIT", Decision: decisionApproved,
		},
		{
			Filename: "nan.FIT", AvgFWHM: math.NaN(), Decision: decisionApproved,
		},
	}

	var sb strings.Builder
	if err := writeResultsCSV(results, newCSVWriter(&sb)); err != nil {
		t.Fatalf("writeResultsCSV: %v", err)
	}

	recs, err := newCSVReader(strings.NewReader(sb.String())).ReadAll()
	if err != nil {
		t.Fatalf("CSV does not parse: %v\n%s", err, sb.String())
	}
	if len(recs) != 1+len(results) {
		t.Fatalf("got %d records, want %d", len(recs), 1+len(results))
	}

	width := len(recs[0])
	for i, rec := range recs {
		if len(rec) != width {
			t.Errorf("record %d has %d fields, want %d: %v", i, len(rec), width, rec)
		}
	}

	// Every numeric metric is written with exactly four decimal places.
	first := recs[1]
	for _, col := range []int{5, 6, 7, 8, 9} {
		v := first[col]
		dot := strings.IndexByte(v, '.')
		if dot < 0 || len(v)-dot-1 != 4 {
			t.Errorf("column %d = %q, want four decimal places", col, v)
		}
	}

	if recs[2][10] != "" {
		t.Errorf("error row decision = %q, want empty", recs[2][10])
	}
	if recs[2][11] != "fake failure" {
		t.Errorf("error column = %q, want %q", recs[2][11], "fake failure")
	}
	if recs[5][5] != "NaN" {
		t.Errorf("NaN formatted as %q, want %q", recs[5][5], "NaN")
	}

	// Both unusual filenames survive a round trip.
	if recs[3][0] != "c;semi.FIT" {
		t.Errorf("semicolon filename round-tripped as %q, want %q", recs[3][0], "c;semi.FIT")
	}
	if recs[4][0] != "d,comma.FIT" {
		t.Errorf("comma filename round-tripped as %q, want %q", recs[4][0], "d,comma.FIT")
	}

	// The semicolon is the only character needing quotes, so the comma must be
	// emitted bare rather than quoted.
	// The bare field starts a row and is followed by empty filter/date columns.
	if !strings.Contains(sb.String(), "\nd,comma.FIT;") {
		t.Errorf("comma-bearing field was quoted:\n%s", sb.String())
	}
	if !strings.Contains(sb.String(), `"c;semi.FIT"`) {
		t.Errorf("semicolon-bearing field was not quoted:\n%s", sb.String())
	}
}

// TestCSVSeparator pins the separator so a regression to the Go default is
// caught with a clear message rather than as a confusing parse error.
func TestCSVSeparator(t *testing.T) {
	if csvSeparator != ';' {
		t.Fatalf("csvSeparator = %q, want %q (must match the reference tool's report)", csvSeparator, ';')
	}

	var sb strings.Builder
	if err := writeResultsCSV([]ImageResult{{Filename: "a.FIT", Decision: decisionApproved}},
		newCSVWriter(&sb)); err != nil {
		t.Fatalf("writeResultsCSV: %v", err)
	}

	if !strings.HasPrefix(sb.String(), "filename;filter;") {
		t.Errorf("header is not semicolon-separated: %q", strings.SplitN(sb.String(), "\n", 2)[0])
	}
	if strings.Contains(strings.SplitN(sb.String(), "\n", 2)[0], ",") {
		t.Errorf("header still contains commas: %q", strings.SplitN(sb.String(), "\n", 2)[0])
	}

	// The reader must agree, or every consumer silently sees one giant field.
	recs, err := newCSVReader(strings.NewReader(sb.String())).ReadAll()
	if err != nil {
		t.Fatalf("output does not parse with the matching reader: %v", err)
	}
	if len(recs) != 2 || len(recs[0]) != 12 {
		t.Errorf("got %d records, first with %d fields; want 2 records of 12", len(recs), len(recs[0]))
	}
}

func TestWriteResultsCSVNonFiniteValues(t *testing.T) {
	results := []ImageResult{{
		Filename:        "x.FIT",
		AvgFWHM:         1.0 / 3.0,
		AvgSignal:       -0.00004,
		AvgEccentricity: math.Inf(1),
		SNR:             math.NaN(),
		Score:           1e20,
		Decision:        decisionApproved,
	}}

	var sb strings.Builder
	if err := writeResultsCSV(results, newCSVWriter(&sb)); err != nil {
		t.Fatalf("writeResultsCSV: %v", err)
	}

	recs, err := newCSVReader(strings.NewReader(sb.String())).ReadAll()
	if err != nil {
		t.Fatalf("CSV with non-finite values does not parse: %v\n%s", err, sb.String())
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}

	// strconv.FormatFloat emits NaN, +Inf and a plain fixed-point rendering for
	// very large values. All must remain valid CSV.
	if got := recs[1][5]; got != "0.3333" {
		t.Errorf("avgFWHM = %q, want %q", got, "0.3333")
	}
	if got := recs[1][6]; got != "-0.0000" {
		t.Errorf("avgSignal = %q, want %q", got, "-0.0000")
	}
	if got := recs[1][7]; got != "+Inf" {
		t.Errorf("avgEccentricity = %q, want %q", got, "+Inf")
	}
	if got := recs[1][8]; got != "NaN" {
		t.Errorf("snr = %q, want %q", got, "NaN")
	}
	if got := recs[1][9]; got != "100000000000000000000.0000" {
		t.Errorf("score = %q, want the full fixed-point rendering", got)
	}
}

// TestRunAnalyzeDiscoversFITSFiles pins the extension filter. Matching is
// case-insensitive, only the three known extensions count, directories are
// skipped even when named like a FITS file, and subdirectories are not walked.
func TestRunAnalyzeDiscoversFITSFiles(t *testing.T) {
	dir := t.TempDir()

	// A valid frame so the run succeeds, plus decoys.
	// Valid frames in each accepted spelling, to prove the filter matches
	// case-insensitively.
	for _, name := range []string{
		"real.FIT",
		"upper.FITS",
		"lower.fits",
		"mixed.Fts",
		"plain.fts",
	} {
		writeFrame(t, dir, name, goodFrameOpts())
	}

	// Decoys that must not be picked up.
	for _, name := range []string{
		"notes.txt",
		"archive.tar.gz",
		"compressed.fit.gz",
		"noextension",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// A directory whose name looks like a FITS file must be skipped.
	if err := os.MkdirAll(filepath.Join(dir, "trap.FIT"), 0o755); err != nil {
		t.Fatalf("mkdir trap: %v", err)
	}
	// A nested frame must not be found by a non-recursive scan.
	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	writeFrame(t, nested, "deep.FIT", goodFrameOpts())

	out := filepath.Join(dir, "out.csv")
	o := defaultOpts()
	o.format = "csv"
	o.output = out
	o.quiet = true
	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}

	recs := readCSVFile(t, out)
	if len(recs) != 6 { // header + 5 frames
		t.Fatalf("got %d records, want 6 (header plus five frames)\n%v", len(recs), recs)
	}

	var names []string
	for _, r := range recs[1:] {
		names = append(names, r[0])
	}
	sort.Strings(names)
	want := []string{"lower.fits", "mixed.Fts", "plain.fts", "real.FIT", "upper.FITS"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("discovered %v, want %v", names, want)
	}

	for _, r := range recs[1:] {
		if r[11] != "" {
			t.Errorf("%s reported an error: %s", r[0], r[11])
		}
	}
}

func TestRunAnalyzeRowOrderIsSorted(t *testing.T) {
	dir := t.TempDir()
	// Files are created in deliberately non-alphabetical order, so the CSV row
	// order can only match if runAnalyze sorts the file list. Directory
	// iteration order is not specified, and on most systems follows creation
	// order, which is the reverse of what is expected here.
	//
	// The first file is also the slowest to process, so a naive implementation
	// that emitted rows as they completed would also produce the wrong order.
	slow := synthOpts{
		width: 600, height: 600, level: 1000, noise: 5,
		amp: 3000, sigma: 1.5, spacing: 25, seed: 1,
	}
	fast := synthOpts{
		width: 200, height: 200, level: 1000, noise: 5,
		amp: 3000, sigma: 1.5, spacing: 25, seed: 11,
	}

	// Written in the order: a_slow, e_fast, c_fast, d_fast, b_fast.
	writeFrame(t, dir, "a_slow.FIT", slow)
	writeFrame(t, dir, "e_fast.FIT", fast)
	writeFrame(t, dir, "c_fast.FIT", fast)
	writeFrame(t, dir, "d_fast.FIT", fast)
	writeFrame(t, dir, "b_fast.FIT", fast)

	out := filepath.Join(dir, "out.csv")
	o := defaultOpts()
	o.format = "csv"
	o.output = out
	o.quiet = true
	o.workers = 4
	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}

	recs := readCSVFile(t, out)
	if len(recs) != 6 {
		t.Fatalf("got %d records, want 6", len(recs))
	}

	var names []string
	for _, r := range recs[1:] {
		names = append(names, r[0])
	}
	want := []string{"a_slow.FIT", "b_fast.FIT", "c_fast.FIT", "d_fast.FIT", "e_fast.FIT"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("row order = %v, want %v (results must follow sorted file order, "+
			"not completion order)", names, want)
	}
}

// TestSaturatedStarsAreSkippedNotAveraged confirms the brightSkip actually
// removes the brightest sources from the metrics. processImage always skips the
// top brightSkip stars after sorting by descending flux, so a field with a few
// saturated stars must report an average close to the clean field rather than
// one inflated by them.
//
// This is the observable consequence of sorting by TotalFlux descending; if the
// sort were reversed, the saturated stars would land inside the measured set
// instead of the faint ones.
func TestSaturatedStarsAreSkippedNotAveraged(t *testing.T) {
	dir := t.TempDir()

	clean := goodFrameOpts()
	clean.width, clean.height = 300, 300
	clean.spacing = 25
	clean.amp = 3000
	base := processImage(writeFrame(t, dir, frameName("B", 0), clean), defaultOpts())

	withSat := goodFrameOpts()
	withSat.width, withSat.height = 300, 300
	withSat.spacing = 25
	withSat.amp = 3000
	got := processImage(writeFrameWithSaturation(t, dir, frameName("B", 1), withSat, 3), defaultOpts())

	if got.Error != nil {
		t.Fatalf("unexpected error: %v", got.Error)
	}
	// Saturated stars are found but must not reach the averages.
	if got.DetectedStars <= base.DetectedStars {
		t.Fatalf("saturated stars were not detected: %d vs %d clean",
			got.DetectedStars, base.DetectedStars)
	}
	if relDiff := math.Abs(got.AvgSignal-base.AvgSignal) / base.AvgSignal; relDiff > 0.10 {
		t.Errorf("avgSignal shifted %.1f%% after adding saturated stars (%.1f vs %.1f); "+
			"the brightSkip of the brightest 20 stars is not being applied",
			relDiff*100, got.AvgSignal, base.AvgSignal)
	}
}

func TestRunAnalyzeWorkerInvariance(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		writeFrame(t, dir, string(rune('a'+i))+".FIT", synthOpts{
			width: 300, height: 300, level: 1000, noise: 5,
			amp: 3000, sigma: 1.5, spacing: 25, seed: int64(i),
		})
	}

	var reference string
	for _, workers := range []int{1, 2, 4, 8} {
		out := filepath.Join(dir, "out.csv")
		o := defaultOpts()
		o.format = "csv"
		o.output = out
		o.quiet = true
		o.workers = workers
		if err := runAnalyze(dir, o); err != nil {
			t.Fatalf("workers=%d: %v", workers, err)
		}

		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("workers=%d: read output: %v", workers, err)
		}
		if workers == 1 {
			reference = string(b)
			continue
		}
		if string(b) != reference {
			t.Errorf("workers=%d produced different output than workers=1", workers)
		}
	}
}

func TestRunAnalyzeFormats(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "a.FIT", synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.5, spacing: 25, seed: 1,
	})

	t.Run("csv to stdout", func(t *testing.T) {
		o := defaultOpts()
		o.format = "csv"
		o.output = "" // routes to stdout
		o.quiet = true

		out := captureStdout(t, func() {
			if err := runAnalyze(dir, o); err != nil {
				t.Errorf("runAnalyze: %v", err)
			}
		})

		recs, err := newCSVReader(strings.NewReader(out)).ReadAll()
		if err != nil {
			t.Fatalf("stdout CSV does not parse: %v\n%q", err, out)
		}
		if len(recs) != 2 {
			t.Fatalf("got %d records, want 2\n%q", len(recs), out)
		}
		if recs[1][0] != "a.FIT" {
			t.Errorf("filename = %q, want %q", recs[1][0], "a.FIT")
		}
	})

	t.Run("console", func(t *testing.T) {
		o := defaultOpts()
		o.format = "console"

		out := captureStdout(t, func() {
			if err := runAnalyze(dir, o); err != nil {
				t.Errorf("runAnalyze: %v", err)
			}
		})

		for _, want := range []string{"Traitement de 1 fichier", "a.FIT", "Terminé"} {
			if !strings.Contains(out, want) {
				t.Errorf("console output missing %q:\n%s", want, out)
			}
		}
		// Console output must not contain a CSV header row, whatever the
		// separator happens to be.
		for _, header := range []string{"filename;filter", "filename,filter"} {
			if strings.Contains(out, header) {
				t.Errorf("console format emitted a CSV header (%q):\n%s", header, out)
			}
		}
	})

	t.Run("both writes console and a file", func(t *testing.T) {
		out := filepath.Join(dir, "both.csv")
		o := defaultOpts()
		o.format = "both"
		o.output = out

		stdout := captureStdout(t, func() {
			if err := runAnalyze(dir, o); err != nil {
				t.Errorf("runAnalyze: %v", err)
			}
		})
		if !strings.Contains(stdout, "Terminé") {
			t.Errorf("format=both did not print console output:\n%s", stdout)
		}
		if !strings.Contains(stdout, "Résultats CSV écrits") {
			t.Errorf("format=both did not report the CSV write:\n%s", stdout)
		}

		recs := readCSVFile(t, out)
		if len(recs) != 2 {
			t.Fatalf("got %d records in the CSV file, want 2", len(recs))
		}
	})

	t.Run("quiet suppresses console output", func(t *testing.T) {
		o := defaultOpts()
		o.format = "console"
		o.quiet = true

		out := captureStdout(t, func() {
			if err := runAnalyze(dir, o); err != nil {
				t.Errorf("runAnalyze: %v", err)
			}
		})
		if out != "" {
			t.Errorf("quiet mode still printed:\n%q", out)
		}
	})
}

func TestRunAnalyzeErrors(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name string
		dir  string
		opts func(*analyzeOptions)
		want string
	}{
		{
			name: "invalid format",
			dir:  dir,
			opts: func(o *analyzeOptions) { o.format = "json" },
			want: `format invalide "json"`,
		},
		{
			name: "both without output",
			dir:  dir,
			opts: func(o *analyzeOptions) { o.format = "both"; o.output = "" },
			want: "--output est requis avec --format both",
		},
		{
			name: "missing directory",
			dir:  filepath.Join(dir, "absent"),
			opts: func(*analyzeOptions) {},
			want: "impossible de lire le dossier",
		},
		{
			name: "no FITS files",
			dir:  dir,
			opts: func(*analyzeOptions) {},
			want: "aucun fichier FITS trouvé",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := defaultOpts()
			o.quiet = true
			tc.opts(&o)

			err := runAnalyze(tc.dir, o)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}

	t.Run("only non-FITS files", func(t *testing.T) {
		nonFits := t.TempDir()
		if err := os.WriteFile(filepath.Join(nonFits, "a.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		o := defaultOpts()
		o.quiet = true
		err := runAnalyze(nonFits, o)
		if err == nil || !strings.Contains(err.Error(), "aucun fichier FITS trouvé") {
			t.Errorf("error = %v, want the no-FITS-files error", err)
		}
	})

	t.Run("CSV to an unwritable path", func(t *testing.T) {
		frameDir := t.TempDir()
		writeFrame(t, frameDir, "a.FIT", synthOpts{
			width: 200, height: 200, level: 1000, noise: 5,
			amp: 3000, sigma: 1.5, spacing: 25, seed: 1,
		})

		o := defaultOpts()
		o.format = "csv"
		o.output = filepath.Join(frameDir, "no-such-dir", "x.csv")
		o.quiet = true

		err := runAnalyze(frameDir, o)
		if err == nil || !strings.Contains(err.Error(), "impossible de créer le fichier CSV") {
			t.Errorf("error = %v, want a CSV creation failure", err)
		}
	})
}

// TestRunAnalyzeRecordsPerFileError confirms a bad file in a batch does not
// abort the run: it becomes a row with the error in the error column.
func TestRunAnalyzeRecordsPerFileError(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "good.FIT", synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.5, spacing: 25, seed: 1,
	})
	if err := os.WriteFile(filepath.Join(dir, "bad.FIT"), []byte("garbage"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	out := filepath.Join(dir, "out.csv")
	o := defaultOpts()
	o.format = "csv"
	o.output = out
	o.quiet = true
	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}

	recs := readCSVFile(t, out)
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3 (header plus two files)", len(recs))
	}

	byName := map[string][]string{}
	for _, r := range recs[1:] {
		byName[r[0]] = r
	}

	good, ok := byName["good.FIT"]
	if !ok {
		t.Fatalf("good.FIT missing from output: %v", byName)
	}
	if good[11] != "" {
		t.Errorf("good.FIT reported an error: %s", good[11])
	}
	if good[10] == "" {
		t.Error("good.FIT has an empty decision")
	}

	// starCount is the number of stars the metrics were actually computed
	// from, which is capped by limitComputedStars minus the brightSkip and so
	// is always strictly below the detected count for a dense field. Comparing
	// the two columns catches a swapped write.
	if good[3] == good[4] {
		t.Errorf("detectedStars and starCount are both %s; they must differ for a "+
			"dense field, since the metrics use a capped subset", good[3])
	}

	bad, ok := byName["bad.FIT"]
	if !ok {
		t.Fatalf("bad.FIT missing from output: %v", byName)
	}
	if bad[11] == "" {
		t.Error("bad.FIT has an empty error column")
	}
	if bad[10] != "" {
		t.Errorf("bad.FIT decision = %q, want empty alongside an error", bad[10])
	}
}

// errFake is a stand-in error for CSV formatting tests.
type errFake struct{}

func (errFake) Error() string { return "fake failure" }

// readCSVFile reads and parses a CSV file written by runAnalyze.
func readCSVFile(t *testing.T, path string) [][]string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	recs, err := newCSVReader(strings.NewReader(string(b))).ReadAll()
	if err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, b)
	}
	return recs
}
