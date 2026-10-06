package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// darkFixture is one night needing two master darks: a B frame at GA0/300s and
// an O frame at GA2750/600s. SH2-54 on 2026-07-16 has exactly this shape in the
// real acquisition.
func darkFixture(target string) prepareFixture {
	return prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {
					"B": {
						lightNameGainExp(target, "B", "20260910", "220000", "000", "310", "GA0", "300s"),
					},
					"O": {
						lightNameGainExp(target, "O", "20260910", "230000", "000", "310", "GA2750", "600s"),
					},
				},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {
				flatName("B", "41.4", "20260910", "070000", "000", "310"),
				flatName("O", "41.5", "20260910", "071000", "000", "310"),
			},
		},
		darks: map[string]string{
			"GA0/300s":    "masterDark_300s_GA0_-10C_20251118.xisf",
			"GA2750/600s": "masterDark_600s_GA2750_-10C_20251120.xisf",
		},
	}
}

func darkOutDir(out, target string) string {
	return filepath.Join(sessionOutDir(filepath.Join(out, target), ""), darksOutDir)
}

// TestPrepareCopiesBothDarksForMixedExposureSession is the core case: one
// session needing two masters, keyed on gain and exposure together.
func TestPrepareCopiesBothDarksForMixedExposureSession(t *testing.T) {
	const target = "SH2-54"
	root := t.TempDir()
	darkFixture(target).build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	got := fileNames(t, darkOutDir(out, target))
	for _, want := range []string{
		"masterDark_300s_GA0_-10C_20251118.xisf",
		"masterDark_600s_GA2750_-10C_20251120.xisf",
	} {
		if !contains(got, want) {
			t.Errorf("%s missing, got %v", want, got)
		}
	}

	// Flat: no per-gain subdirectory, so the acquisition's G0-versus-GA0
	// spelling mismatch cannot propagate into the destination.
	for _, e := range dirEntries(t, darkOutDir(out, target)) {
		if e.IsDir() {
			t.Errorf("%s/ is a directory, want the masters flat", e.Name())
		}
	}

	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if got := recs[1][12]; got != "GA0/300s GA2750/600s" {
		t.Errorf("darks column = %q, want %q", got, "GA0/300s GA2750/600s")
	}
}

// TestIndexDarkMastersKeysOnFilenameNotDirectory is the silent-failure case. The
// acquisition holds these in "G2750/" while the files inside are named GA2750,
// so a key taken from the directory would match nothing and copy nothing.
func TestIndexDarkMastersKeysOnFilenameNotDirectory(t *testing.T) {
	dir := t.TempDir()

	for gain, sub := range map[string]string{"GA0": "G0", "GA2750": "G2750"} {
		subdir := filepath.Join(dir, sub)
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeStub(t, filepath.Join(subdir, "masterDark_300s_"+gain+"_-10C_20251118.xisf"))
	}

	masters, warnings, err := indexDarkMasters(dir)
	if err != nil {
		t.Fatalf("indexDarkMasters: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(masters) != 2 {
		t.Fatalf("got %d masters, want 2 (one per gain): %v", len(masters), masters)
	}
	for _, key := range []darkKey{
		{gain: "GA0", exposure: 300},
		{gain: "GA2750", exposure: 300},
	} {
		m, ok := masters[key]
		if !ok {
			t.Errorf("%s not indexed; the G0/G2750 directory names must not become keys", key)
			continue
		}
		// The source path keeps the subdirectory, but the key does not.
		if !strings.Contains(m.src, filepath.Join("G0", "masterDark_300s_GA0")) &&
			!strings.Contains(m.src, filepath.Join("G2750", "masterDark_300s_GA2750")) {
			t.Errorf("%s resolved to %q", key, m.src)
		}
		if strings.HasPrefix(filepath.Base(m.src), "G") && strings.Contains(filepath.Base(m.src), "master") {
			t.Errorf("%s src basename looks like a directory name", key)
		}
	}
}

// TestIndexDarkMastersPrefersTheLatestDate pins the collision rule: the master
// carrying the later date wins, because it supersedes rather than competes.
func TestIndexDarkMastersPrefersTheLatestDate(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "G0")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{
		"masterDark_300s_GA0_-10C_20251118.xisf",
		"masterDark_300s_GA0_-10C_20260901.xisf",
		"masterDark_300s_GA0_-10C_20260315.xisf",
	} {
		writeStub(t, filepath.Join(sub, n))
	}

	masters, warnings, err := indexDarkMasters(dir)
	if err != nil {
		t.Fatalf("indexDarkMasters: %v", err)
	}
	key := darkKey{gain: "GA0", exposure: 300}
	if got := masters[key].name; got != "masterDark_300s_GA0_-10C_20260901.xisf" {
		t.Errorf("resolved to %q, want the latest date 20260901", got)
	}
	if masters[key].date != "20260901" {
		t.Errorf("date = %q, want 20260901", masters[key].date)
	}
	if len(warnings) != 2 {
		t.Errorf("got %d warnings (%v), want both collisions reported", len(warnings), warnings)
	}
}

// TestIndexDarkMastersRejectsNonMasters checks the raw calibration frames and
// the syncthing partials stay out. Darks/-10C/G0/30s/ holds ~100 .FIT files
// that are master inputs, not masters.
//
// Each case gets its own directory holding only that kind of file, so a file is
// not merely surviving on the strength of the other guard: a .tmp file is
// already excluded by the extension check, and an AppleDouble sidecar already
// passes it, so mixing them hides both.
func TestIndexDarkMastersRejectsNonMasters(t *testing.T) {
	tests := []struct {
		name  string
		files []string
	}{
		{
			// The extension is .tmp, so only the extension guard stops these.
			name: "syncthing partials",
			files: []string{
				".syncthing.masterDark_300s_GA0_-10C_20251118.xisf.tmp",
				"masterDark_300s_GA0_-10C_20251118.xisf.tmp",
			},
		},
		{
			// These carry a .xisf extension, so only the dotfile guard stops
			// them. macOS writes these on exFAT volumes, where the destination
			// lives.
			name: "AppleDouble sidecars",
			files: []string{
				"._masterDark_300s_GA0_-10C_20251118.xisf",
				"._masterDark_600s_GA2750_-10C_20251120.xisf",
			},
		},
		{
			name: "raw calibration frames",
			files: []string{
				"Dark_G0_300s_DARK_S_20251118_022843_260_PA274_E.FIT",
				"masterDark_300s_GA0_-10C_20251118.FIT",
			},
		},
		{
			name:  "dotfiles generally",
			files: []string{".DS_Store", ".stignore"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sub := filepath.Join(dir, "G0")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, n := range tc.files {
				if err := os.WriteFile(filepath.Join(sub, n), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			masters, _, err := indexDarkMasters(dir)
			if err != nil {
				t.Fatalf("indexDarkMasters: %v", err)
			}
			if len(masters) != 0 {
				t.Errorf("indexed %d masters (%v), want none", len(masters), masters)
			}
		})
	}

	t.Run("the real master is indexed alongside junk", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "G0")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		writeStub(t, filepath.Join(sub, "masterDark_300s_GA0_-10C_20251118.xisf"))
		for _, n := range []string{
			"Dark_G0_300s_DARK_S_20251118_022843_260_PA274_E.FIT",
			"masterBias_GA0_-10C_257f_20251117.xisf",
			"masterDark_300s_G0_-10C_20251118.xisf", // legacy spelling, warned about
		} {
			if err := os.WriteFile(filepath.Join(sub, n), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// A directory at the top level, and one named like a master inside it.
		if err := os.MkdirAll(filepath.Join(dir, "stale"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(sub, "masterDark_60s_GA0_-10C_20251119.xisf"), 0o755); err != nil {
			t.Fatal(err)
		}

		masters, warnings, err := indexDarkMasters(dir)
		if err != nil {
			t.Fatalf("indexDarkMasters: %v", err)
		}
		if len(masters) != 1 {
			t.Errorf("got %d masters (%v), want only the one real dark", len(masters), masters)
		}
		if _, ok := masters[darkKey{gain: "GA0", exposure: 300}]; !ok {
			t.Error("the real master was not indexed")
		}
		// A .xisf carrying no recognisable pair is reported rather than dropped
		// in silence.
		if len(warnings) == 0 {
			t.Error("no warning for the files carrying no exposure/gain pair")
		}
	})
}

// TestPrepareDarksOnlyForApprovedFrames is the rule that separates darks from
// the biases: a dark is wanted only for frames that reach lights/. A frame that
// cannot be read is rejected, which is how a single frame is isolated here.
func TestPrepareDarksOnlyForApprovedFrames(t *testing.T) {
	t.Run("a rejected frame does not drag its dark along", func(t *testing.T) {
		const target = "M51"
		root := t.TempDir()
		f := darkFixture(target)
		f.build(t, root)

		// The night holds one B at GA0/300s and one O at GA2750/600s. Corrupting
		// the B leaves the O approved, so only the 600s dark should be copied.
		corrupt := filepath.Join(root, lightsDir, target, "2026-09-10", "B",
			lightNameGainExp(target, "B", "20260910", "220000", "000", "310", "GA0", "300s"))
		if err := os.WriteFile(corrupt, []byte("not FITS"), 0o644); err != nil {
			t.Fatal(err)
		}

		out := t.TempDir()
		if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
			t.Fatalf("runPrepare: %v", err)
		}

		got := fileNames(t, darkOutDir(out, target))
		for _, n := range got {
			if strings.Contains(n, "_GA0_") {
				t.Errorf("%s copied although its only light frame was rejected", n)
			}
		}
		if len(got) != 1 || !strings.Contains(got[0], "_GA2750_") {
			t.Errorf("darks dir holds %v, want only the GA2750/600s master", got)
		}
	})

	t.Run("one approved sibling is enough", func(t *testing.T) {
		const target = "M51"
		root := t.TempDir()

		f := prepareFixture{
			lights: map[string]map[string]map[string][]string{
				target: {"2026-09-10": {"B": {
					lightNameGainExp(target, "B", "20260910", "220000", "000", "310", "GA0", "300s"),
					lightNameGainExp(target, "B", "20260910", "223000", "000", "310", "GA0", "300s"),
				}}},
			},
			flats: map[string][]string{"2026-09-10": {flatName("B", "41.4", "20260910", "070000", "000", "310")}},
			darks: map[string]string{"GA0/300s": "masterDark_300s_GA0_-10C_20251118.xisf"},
		}
		f.build(t, root)

		corrupt := filepath.Join(root, lightsDir, target, "2026-09-10", "B",
			lightNameGainExp(target, "B", "20260910", "220000", "000", "310", "GA0", "300s"))
		if err := os.WriteFile(corrupt, []byte("not FITS"), 0o644); err != nil {
			t.Fatal(err)
		}

		out := t.TempDir()
		if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
			t.Fatalf("runPrepare: %v", err)
		}

		if got := fileNames(t, darkOutDir(out, target)); len(got) != 1 {
			t.Errorf("darks dir holds %v, want the master kept for the approved sibling", got)
		}
	})
}

// TestPrepareDarksSharedAcrossSessions checks N nights still yield one file per
// combination. For LBN527 across 20 sessions that is 467 MB rather than 9.3 GB.
func TestPrepareDarksSharedAcrossSessions(t *testing.T) {
	const target = "M31"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{},
		flats:  map[string][]string{},
		darks: map[string]string{
			"GA0/300s":    "masterDark_300s_GA0_-10C_20251118.xisf",
			"GA2750/600s": "masterDark_600s_GA2750_-10C_20251120.xisf",
		},
	}
	const nights = 5
	f.lights[target] = map[string]map[string][]string{}
	for i := range nights {
		date := "2026-09-0" + string(rune('1'+i))
		ms := "00" + string(rune('0'+i))
		f.lights[target][date] = map[string][]string{
			"B": {lightNameGainExp(target, "B", "20260910", "220000", ms, "310", "GA0", "300s")},
			"O": {lightNameGainExp(target, "O", "20260910", "230000", ms, "310", "GA2750", "600s")},
		}
		f.flats[date] = []string{
			flatName("B", "41.4", "20260910", "070000", "000", "310"),
			flatName("O", "41.5", "20260910", "071000", "000", "310"),
		}
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	sessions := sessionDirNames(t, out, target)
	if len(sessions) != nights {
		t.Fatalf("got %d sessions, want %d", len(sessions), nights)
	}
	if got := len(fileNames(t, darkOutDir(out, target))); got != 2 {
		t.Errorf("got %d dark files for %d sessions, want 2", got, len(sessions))
	}
}

// TestPrepareWarnsOnMissingDark checks a combination with no master is reported
// against the sessions it affects and the run still succeeds.
func TestPrepareWarnsOnMissingDark(t *testing.T) {
	const target = "NGC7000"
	root := t.TempDir()

	f := darkFixture(target)
	f.darks = map[string]string{"GA0/300s": "masterDark_300s_GA0_-10C_20251118.xisf"}
	f.build(t, root)
	out := t.TempDir()

	stderr := captureStderr(t, func() {
		if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
			t.Errorf("runPrepare returned an error for a missing dark: %v", err)
		}
	})

	// Naming the combination alone is not enough to act on; the sessions are.
	for _, want := range []string{"GA2750/600s", "Session_01"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q does not mention %s", stderr, want)
		}
	}

	got := fileNames(t, darkOutDir(out, target))
	if len(got) != 1 || !strings.Contains(got[0], "_GA0_") {
		t.Errorf("darks dir holds %v, want the available master copied anyway", got)
	}
}

// TestPrepareWithoutDarksMastersIsNotAnError checks a target with no Darks/
// directory prepares normally.
func TestPrepareWithoutDarksMastersIsNotAnError(t *testing.T) {
	const target = "WR134"
	root := t.TempDir()
	simpleFixture(target).build(t, root) // no darks written
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}
	if _, err := os.Stat(darkOutDir(out, target)); err == nil {
		t.Error("a darks/ directory was created with no masters to copy")
	}
	if got := len(sessionDirNames(t, out, target)); got != 2 {
		t.Errorf("got %d sessions, want 2", got)
	}
}

// TestPrepareReportsCopiedDarks checks the console line names the combinations
// it actually copied. When a master is missing the copied and wanted sets
// differ, and a line reporting the wrong one would claim a copy that never
// happened.
func TestPrepareReportsCopiedDarks(t *testing.T) {
	const target = "M51"
	root := t.TempDir()
	darkFixture(target).build(t, root)

	stdout := captureStdout(t, func() {
		opts := prepareOptionsForTest(target, root, t.TempDir())
		opts.quiet = false
		if err := runPrepare(opts); err != nil {
			t.Fatalf("runPrepare: %v", err)
		}
	})

	var line string
	for _, l := range strings.Split(stdout, "\n") {
		if strings.Contains(l, "master dark") && strings.Contains(l, "pour") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no dark line naming the combinations:\n%s", stdout)
	}
	if !strings.Contains(line, "2 master dark") {
		t.Errorf("line %q does not report a count of 2", line)
	}
	for _, want := range []string{"GA0/300s", "GA2750/600s"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q does not name %s", line, want)
		}
	}
}

// TestPrepareDarksDryRunTouchesNothing checks dry-run reports without copying.
func TestPrepareDarksDryRunTouchesNothing(t *testing.T) {
	const target = "M87-1"
	root := t.TempDir()
	darkFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.dryRun = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}
	if _, err := os.Stat(darkOutDir(out, target)); err == nil {
		t.Error("dry-run created the darks directory")
	}
}

// TestPrepareDarksSkipExistingIsIdempotent checks a second run does not recopy.
func TestPrepareDarksSkipExistingIsIdempotent(t *testing.T) {
	const target = "IC4090"
	root := t.TempDir()
	darkFixture(target).build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	opts := prepareOptionsForTest(target, root, out)
	opts.skipExisting = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := len(fileNames(t, darkOutDir(out, target))); got != 2 {
		t.Errorf("got %d dark files after a second run, want 2", got)
	}
}

// TestFixtureLightNamesAreParseable guards the filename builders themselves.
// A doubled suffix once produced "300ss_BIN1_", which every extraction silently
// declined to match -- so the fixtures built frames no master dark could ever be
// found for, and the tests failed for the wrong reason. Nothing caught it
// because the tests that pinned the patterns used real filenames instead.
func TestFixtureLightNamesAreParseable(t *testing.T) {
	const target = "M31"
	for _, tc := range []struct {
		name string
		got  string
		want darkKey
	}{
		{"lightName", lightName(target, "B", "20260910", "220000", "000", "310"),
			darkKey{gain: "GA0", exposure: 300}},
		{"lightNameGain", lightNameGain(target, "B", "20260910", "220000", "000", "310", "GA2750"),
			darkKey{gain: "GA2750", exposure: 300}},
		{"lightNameGainExp 60s", lightNameGainExp(target, "B", "20260910", "220000", "000", "310", "GA0", "60s"),
			darkKey{gain: "GA0", exposure: 60}},
	} {
		key, ok := lightDarkKey(tc.got)
		if !ok {
			t.Errorf("%s built %q, which yields no master dark key", tc.name, tc.got)
			continue
		}
		if key != tc.want {
			t.Errorf("%s built %q -> %s, want %s", tc.name, tc.got, key, tc.want)
		}
		if strings.Contains(tc.got, "ss") {
			t.Errorf("%s built %q with a doubled suffix", tc.name, tc.got)
		}
	}
}

// TestDarkKeyString pins the rendering, which is what the console line and the
// sessions.csv column both use.
func TestDarkKeyString(t *testing.T) {
	tests := map[darkKey]string{
		{gain: "GA0", exposure: 30}:     "GA0/30s",
		{gain: "GA0", exposure: 300}:    "GA0/300s",
		{gain: "GA2750", exposure: 600}: "GA2750/600s",
	}
	for key, want := range tests {
		if got := key.String(); got != want {
			t.Errorf("%v renders as %q, want %q", key, got, want)
		}
	}
}

// TestLightDarkKeyRe pins the extraction on real acquisition filename shapes,
// including the exposure values that appear across the whole Lights tree.
func TestLightDarkKeyRe(t *testing.T) {
	tests := []struct {
		name string
		gain string
		exp  int
	}{
		{"LBN527_LIGHT_H_600s_BIN1_-10C_GA2750_20260902_014447_431_PA310_W.FIT", "GA2750", 600},
		{"IC63_LIGHT_H_300s_BIN1_-10C_GA2750_20251108_224543_735_PA230_W.FIT", "GA2750", 300},
		{"LBN527_LIGHT_B_300s_BIN1_-10C_GA0_20260914_045755_638_PA310_E.FIT", "GA0", 300},
		{"M87_LIGHT_H_60s_BIN1_-10C_GA0_20260101_010101_000_PA000_W.FIT", "GA0", 60},
		{"M87_LIGHT_H_30s_BIN1_-10C_GA0_20260101_010101_000_PA000_W.FIT", "GA0", 30},
	}
	for _, tc := range tests {
		key, ok := lightDarkKey(tc.name)
		if !ok {
			t.Errorf("no combination extracted from %q", tc.name)
			continue
		}
		if key.gain != tc.gain || key.exposure != tc.exp {
			t.Errorf("%q -> %s, want %s/%ds", tc.name, key, tc.gain, tc.exp)
		}
	}

	// A flat frame and a non-light file must yield nothing rather than a
	// plausible-looking wrong key.
	for _, name := range []string{
		"FLAT_B_41.4s_BIN1_-10C_GA2750_20260910_070000_000_PA310.FIT",
		"LBN527_LIGHT_B_BIN1_-10C_GA0_20260914_045755_638_PA310_E.FIT", // no exposure
	} {
		if _, ok := lightDarkKey(name); ok {
			t.Errorf("lightDarkKey accepted %q", name)
		}
	}
}

// TestDarkComboRe pins the master-side pattern on the real filenames.
func TestDarkComboRe(t *testing.T) {
	for _, tc := range []struct {
		name string
		gain string
		exp  int
	}{
		{"masterDark_300s_GA0_-10C_20251118.xisf", "GA0", 300},
		{"masterDark_600s_GA2750_-10C_20251120.xisf", "GA2750", 600},
		{"masterDark_30s_GA0_-10C_20260901.xisf", "GA0", 30},
	} {
		m := darkComboRe.FindStringSubmatch(tc.name)
		if m == nil {
			t.Errorf("darkComboRe does not match %q", tc.name)
			continue
		}
		if m[2] != tc.gain {
			t.Errorf("%q gain = %q, want %q", tc.name, m[2], tc.gain)
		}
	}
}
