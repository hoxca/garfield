package cmd

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astrogo/fitsio"

	"garfield/internal/measurecache"
	"garfield/internal/sun"
)

func TestParseSiteDegrees(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		limit float64
		want  float64
	}{
		{"space-separated north", "+44 20 14", 90, 44 + 20.0/60 + 14.0/3600},
		{"space-separated east", "+01 01 30", 180, 1 + 1.0/60 + 30.0/3600},
		{"south of the equator", "-45 30 00", 90, -45.5},
		{"negative zero degrees", "-00 30 00", 180, -0.5},
		{"colon-separated", "+01:01:30", 180, 1.025},
		{"decimal", "44.3372", 90, 44.3372},
		{"negative decimal", "-0.5", 180, -0.5},
		{"symbols and spaces", ` +44° 20' 14" `, 90, 44 + 20.0/60 + 14.0/3600},
		{"degrees only", "+44", 90, 44},
		{"exactly on the limit", "90", 90, 90},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSiteDegrees(tc.value, tc.limit)
			if err != nil {
				t.Fatalf("parseSiteDegrees(%q): %v", tc.value, err)
			}
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("parseSiteDegrees(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		value string
		limit float64
	}{
		{"empty", "", 90},
		{"not a number", "north", 90},
		{"latitude over ninety", "91", 90},
		{"latitude under minus ninety", "-90.5", 90},
		{"longitude over one eighty", "181", 180},
		{"minutes out of range", "+10 60 00", 90},
		{"seconds out of range", "+10 10 60", 90},
		{"too many parts", "+10 10 10 10", 90},
	} {
		if _, err := parseSiteDegrees(tc.value, tc.limit); err == nil {
			t.Errorf("%s: parseSiteDegrees(%q) accepted the input", tc.name, tc.value)
		}
	}
}

func TestParseObservationTime(t *testing.T) {
	want := time.Date(2026, 8, 11, 22, 14, 30, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		value string
		want  time.Time
	}{
		{"no zone", "2026-08-11T22:14:30", want},
		{"Z", "2026-08-11T22:14:30Z", want},
		{"fractional seconds", "2026-08-11T22:14:30.123", want.Add(123 * time.Millisecond)},
		{"space separator", "2026-08-11 22:14:30", want},
		// A writer that appends an offset is obeyed rather than assumed UTC.
		{"offset", "2026-08-12T00:14:30+02:00", want},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseObservationTime(tc.value)
			if err != nil {
				t.Fatalf("parseObservationTime(%q): %v", tc.value, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseObservationTime(%q) = %v, want %v", tc.value, got, tc.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("parseObservationTime(%q) is in %v, want UTC", tc.value, got.Location())
			}
		})
	}

	for _, value := range []string{"", "yesterday", "2026-08-11"} {
		if _, err := parseObservationTime(value); err == nil {
			t.Errorf("parseObservationTime(%q) accepted the input", value)
		}
	}
}

// The statistic is kept at a hundredth of a degree. That is finer than the
// model and is exactly what the report prints, so the cached value and the CSV
// cell cannot disagree in a digit the user never sees.
func TestSunBelowIsRoundedToHundredths(t *testing.T) {
	below, ok := sunBelowFromCards(map[string]string{
		"SITELAT":  "+44 20 14",
		"SITELONG": "+01 01 30",
		"DATE-OBS": "2026-08-11T22:14:30",
	})
	if !ok {
		t.Fatal("sunBelowFromCards: not computed")
	}
	if scaled := below * 100; math.Abs(scaled-math.Round(scaled)) > 1e-9 {
		t.Errorf("sunBelow = %v, want at most %d decimal places", below, sunBelowDecimals)
	}
}

// The statistic has to survive both paths: the fresh measurement and the cache
// hit, which does not read the pixels but must still read the header's site.
func TestAnalyzeReportsTheSunsDepth(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "frame.FIT", synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 1,
		cards: []fitsio.Card{
			{Name: "SITELAT", Value: "+44 20 14"},
			{Name: "SITELONG", Value: "+01 01 30"},
			{Name: "DATE-OBS", Value: "2026-08-11T22:14:30"},
		},
	})

	out := t.TempDir()
	o := defaultOpts()
	o.format, o.output, o.quiet = "csv", out, true
	o.cache = true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	read := func() (float64, bool) {
		t.Helper()
		recs := readCSVFile(t, filepath.Join(out, analyseReportName))
		col := columnIndex(t, recs[0], "sunBelow")
		if recs[1][col] == "" {
			return 0, false
		}
		v, err := strconv.ParseFloat(recs[1][col], 64)
		if err != nil {
			t.Fatalf("parse sunBelow %q: %v", recs[1][col], err)
		}
		return v, true
	}

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	first, ok := read()
	if !ok {
		t.Fatal("sunBelow is empty on a measured frame")
	}
	want := roundTo(sun.BelowHorizon(time.Date(2026, 8, 11, 22, 14, 30, 0, time.UTC),
		44+20.0/60+14.0/3600, 1+1.0/60+30.0/3600), sunBelowDecimals)
	if math.Abs(first-want) > 1e-9 {
		t.Errorf("sunBelow = %.2f, want %.2f", first, want)
	}
	// Mid-August at 44°N, 22:14 UTC is hours after sunset: deep astronomical
	// night, not the horizon.
	if first < sun.Astronomical {
		t.Errorf("sunBelow = %.2f, want at least %.0f at this hour", first, sun.Astronomical)
	}

	// Second run reuses the measurement. The depth is header metadata, so it must
	// still be there even though the pixels were not touched.
	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze with cache: %v", err)
	}
	recs := readCSVFile(t, filepath.Join(out, analyseReportName))
	cached := columnIndex(t, recs[0], "cached")
	if recs[1][cached] != "true" {
		t.Fatalf("second run did not reuse the measurement: cached=%q", recs[1][cached])
	}
	second, ok := read()
	if !ok {
		t.Fatal("sunBelow is empty on a cached frame")
	}
	if second != first {
		t.Errorf("sunBelow = %v on the cached run, want %v", second, first)
	}
}

// A store written before the Sun statistic existed has no depth in its entries.
// The first cached run must read the header and record it, so later runs are
// header-free and the column is not silently empty.
func TestCachedRunBackfillsTheSunsDepth(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "frame.FIT", synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 3,
		cards: []fitsio.Card{
			{Name: "SITELAT", Value: "+44 20 14"},
			{Name: "SITELONG", Value: "+01 01 30"},
			{Name: "DATE-OBS", Value: "2026-08-11T22:14:30"},
		},
	})

	out := t.TempDir()
	o := defaultOpts()
	o.format, o.output, o.quiet = "csv", out, true
	o.cache = true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Rewrite the store as an older build would have left it: no sunBelow.
	store := cachePath(out)
	data, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var entry measurecache.Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode entry: %v", err)
		}
		entry.SunBelow = nil
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("encode entry: %v", err)
		}
		lines = append(lines, string(encoded))
	}
	if err := os.WriteFile(store, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite store: %v", err)
	}

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("cached run: %v", err)
	}
	recs := readCSVFile(t, filepath.Join(out, analyseReportName))
	if recs[1][columnIndex(t, recs[0], "cached")] != "true" {
		t.Fatal("the second run did not reuse the measurement")
	}
	if got := recs[1][columnIndex(t, recs[0], "sunBelow")]; got == "" {
		t.Error("sunBelow is empty after the backfill run")
	}

	// And the header read is remembered: the store now carries the depth.
	data, err = os.ReadFile(store)
	if err != nil {
		t.Fatalf("read store after backfill: %v", err)
	}
	backfilled := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry measurecache.Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode entry: %v", err)
		}
		if entry.SunBelow != nil {
			backfilled = true
		}
	}
	if !backfilled {
		t.Error("the store still has no sunBelow after the cached run")
	}
}

func TestAnalyzeLeavesSunBelowEmptyWithoutASite(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "frame.FIT", synthOpts{
		width: 300, height: 300, level: 1000, noise: 5,
		amp: 3000, sigma: 1.3, spacing: 25, seed: 2,
	})

	out := t.TempDir()
	o := defaultOpts()
	o.format, o.output, o.quiet = "csv", out, true
	o.minStars, o.minSNR, o.minScore = 0, 0, 0

	if err := runAnalyze(dir, o); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	recs := readCSVFile(t, filepath.Join(out, analyseReportName))
	col := columnIndex(t, recs[0], "sunBelow")
	if got := recs[1][col]; got != "" {
		t.Errorf("sunBelow = %q, want empty when the header has no site", got)
	}
	if !strings.Contains(recs[0][col], "sunBelow") {
		t.Errorf("header %q is not the sun column", recs[0][col])
	}
}
