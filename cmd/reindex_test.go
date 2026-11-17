package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReindexedName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index int
		want  string
	}{
		{
			name:  "SH2-142_LIGHT_H_300s_BIN1_-10C_001_20260812_001427_410_GA_100_OF_50_PA97_W.FIT",
			index: 7,
			want:  "SH2-142_LIGHT_H_300s_BIN1_-10C_007_20260812_001427_410_GA_100_OF_50_PA97_W.FIT",
		},
		{
			name:  "IC1805_LIGHT_H_900s_BIN1_-20C_001_20201024_204428_998_W.FIT",
			index: 12,
			want:  "IC1805_LIGHT_H_900s_BIN1_-20C_012_20201024_204428_998_W.FIT",
		},
		{
			// C3 has no index slot: the number goes before its gain token.
			name:  "LBN527_LIGHT_L_300s_BIN1_-10C_GA0_20260913_220057_864_PA310_W.FIT",
			index: 3,
			want:  "LBN527_LIGHT_L_300s_BIN1_-10C_003_GA0_20260913_220057_864_PA310_W.FIT",
		},
		{
			// No index and no gain token: insert in the same slot.
			name:  "X_LIGHT_H_60s_BIN1_-10C_20260101_000000_000_PA0_W.FIT",
			index: 5,
			want:  "X_LIGHT_H_60s_BIN1_-10C_005_20260101_000000_000_PA0_W.FIT",
		},
	} {
		got, ok := reindexedName(tc.name, tc.index)
		if !ok {
			t.Errorf("reindexedName(%q): no slot", tc.name)
			continue
		}
		if got != tc.want {
			t.Errorf("reindexedName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}

	for _, name := range []string{
		"FLAT_H_41.4s_BIN1_-10C_GA2750_20260910_070000_000_PA310.FIT",
		"not-a-frame.FIT",
		"X_LIGHT_H_60s.FIT",
	} {
		if _, ok := reindexedName(name, 1); ok {
			t.Errorf("reindexedName(%q) found a slot in a name that has none", name)
		}
	}
}

func asiIndexedName(target, filter, index, date, clock string) string {
	return fmt.Sprintf("%s_LIGHT_%s_300s_BIN1_-10C_%s_%s_%s_000_GA_100_OF_50_PA97_W.FIT",
		target, filter, index, date, clock)
}

// TestPrepareReindexesLightsPerFilter covers the whole scheme: per filter, by
// acquisition time, across nights, rejected included, with independent sequences.
func TestPrepareReindexesLightsPerFilter(t *testing.T) {
	const target = "NGC7000"
	root := t.TempDir()

	hOld := asiIndexedName(target, "H", "002", "20260910", "220000")
	hRejected := asiIndexedName(target, "H", "001", "20260910", "230000")
	hNext := asiIndexedName(target, "H", "001", "20260911", "220000")
	oOne := asiIndexedName(target, "O", "009", "20260910", "221000")
	oTwo := asiIndexedName(target, "O", "009", "20260910", "231000")

	prepareFixture{
		instrume: "ZWO ASI2600MM Pro",
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"H": {hOld, hRejected}, "O": {oOne, oTwo}},
				"2026-09-11": {"H": {hNext}},
			},
		},
	}.build(t, root)

	// Corrupt the middle H frame so it is rejected, which proves rejected frames
	// keep their ordinal in the sequence.
	for _, date := range []string{"2026-09-10"} {
		path := filepath.Join(root, lightsDir, target, date, "H", hRejected)
		if err := os.WriteFile(path, []byte("not a FITS file"), 0o644); err != nil {
			t.Fatalf("corrupt frame: %v", err)
		}
	}

	out := t.TempDir()
	opts := prepareOptionsForTest(target, root, out)
	opts.camera = "asi2600"
	opts.quiet = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	names := func(dir string) []string {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		out := make([]string, len(entries))
		for i, e := range entries {
			out[i] = e.Name()
		}
		return out
	}
	targetOut := filepath.Join(out, target)

	sessionOneH := names(filepath.Join(sessionOutDir(targetOut, "Session_01"), sessionLightsDir, "H"))
	if len(sessionOneH) != 1 || !strings.Contains(sessionOneH[0], "_001_20260910_220000_") {
		t.Errorf("Session_01 lights/H = %v, want the 22:00 frame as 001", sessionOneH)
	}
	sessionTwoH := names(filepath.Join(sessionOutDir(targetOut, "Session_02"), sessionLightsDir, "H"))
	if len(sessionTwoH) != 1 || !strings.Contains(sessionTwoH[0], "_003_20260911_220000_") {
		t.Errorf("Session_02 lights/H = %v, want the next night's frame as 003", sessionTwoH)
	}
	rejected := names(filepath.Join(targetOut, rejectedDir, "Session_01", "H"))
	if len(rejected) != 1 || !strings.Contains(rejected[0], "_002_20260910_230000_") {
		t.Errorf("rejected Session_01/H = %v, want the rejected frame as 002", rejected)
	}
	oNames := names(filepath.Join(sessionOutDir(targetOut, "Session_01"), sessionLightsDir, "O"))
	if len(oNames) != 2 ||
		!strings.Contains(oNames[0], "_001_20260910_221000_") ||
		!strings.Contains(oNames[1], "_002_20260910_231000_") {
		t.Errorf("Session_01 lights/O = %v, want an independent 001/002 sequence", oNames)
	}

	// The report keeps the source name and records what was copied.
	recs := readFramesCSV(t, targetOut)
	filename := columnIndex(t, recs[0], "filename")
	output := columnIndex(t, recs[0], "outputFilename")
	found := false
	for _, r := range recs[1:] {
		if r[filename] != hRejected {
			continue
		}
		found = true
		if !strings.Contains(r[output], "_002_20260910_230000_") {
			t.Errorf("outputFilename = %q, want the 002 name", r[output])
		}
	}
	if !found {
		t.Errorf("frames.csv has no row for %s", hRejected)
	}

	// A second run over the same set assigns the same names.
	before := names(filepath.Join(sessionOutDir(targetOut, "Session_01"), sessionLightsDir, "H"))
	if err := runPrepare(opts); err != nil {
		t.Fatalf("second run: %v", err)
	}
	after := names(filepath.Join(sessionOutDir(targetOut, "Session_01"), sessionLightsDir, "H"))
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Errorf("numbering changed between runs: %v then %v", before, after)
	}
}

// A light whose name has no index slot is copied unchanged and does not consume
// a number; the warning says so.
func TestPrepareLeavesUnrecognisedLightNameAlone(t *testing.T) {
	const target = "IC63"
	root := t.TempDir()

	odd := target + "_LIGHT_H_20260910_213000_PA97.FIT"
	normal := asiIndexedName(target, "H", "004", "20260910", "220000")
	prepareFixture{
		instrume: "ZWO ASI2600MM Pro",
		lights: map[string]map[string]map[string][]string{
			target: {"2026-09-10": {"H": {odd, normal}}},
		},
	}.build(t, root)

	out := t.TempDir()
	opts := prepareOptionsForTest(target, root, out)
	opts.camera = "asi2600"
	opts.minStars, opts.minSNR, opts.minScore = 0, 0, 0

	stderr := captureStderr(t, func() {
		if err := runPrepare(opts); err != nil {
			t.Fatalf("runPrepare: %v", err)
		}
	})
	if !strings.Contains(stderr, "no frame-index slot") {
		t.Errorf("no warning about the unrecognised name:\n%s", stderr)
	}

	lights := filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), sessionLightsDir, "H")
	if _, err := os.Stat(filepath.Join(lights, odd)); err != nil {
		t.Errorf("unrecognised light was renamed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lights, asiIndexedName(target, "H", "001", "20260910", "220000"))); err != nil {
		t.Errorf("recognised light did not take index 001: %v", err)
	}

	recs := readFramesCSV(t, filepath.Join(out, target))
	filename := columnIndex(t, recs[0], "filename")
	output := columnIndex(t, recs[0], "outputFilename")
	for _, r := range recs[1:] {
		if r[filename] == odd && r[output] != odd {
			t.Errorf("outputFilename = %q for the unrecognised frame, want it unchanged", r[output])
		}
	}
}
