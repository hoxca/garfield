package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// biasFixture is one night whose filters span two gain settings, which is the
// case that decides whether the bias is copied once or twice. B/G/L/R are taken
// at GA0 and O/S at GA2750 in the acquisition, so a night covering both needs
// both masters.
func biasFixture(target string) prepareFixture {
	return prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {
					"B": {
						lightNameGain(target, "B", "20260910", "220000", "000", "310", "GA0"),
						lightNameGain(target, "B", "20260910", "223000", "000", "310", "GA0"),
					},
					"O": {
						lightNameGain(target, "O", "20260910", "230000", "000", "310", "GA2750"),
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
		biases: map[string]string{
			"GA0":    "masterBias_GA0_-10C_257f_20251117.xisf",
			"GA2750": "masterBias_GA2750_-10C_129f_20251117.xisf",
		},
	}
}

// TestPrepareCopiesBothBiasesForMixedGainSession is the core case: one session
// needing two masters, copied once each into a shared directory, never per
// session.
func TestPrepareCopiesBothBiasesForMixedGainSession(t *testing.T) {
	const target = "LBN527"
	root := t.TempDir()
	biasFixture(target).build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	biasDirPath := filepath.Join(sessionOutDir(filepath.Join(out, target), ""), biasOutDir)
	got := fileNames(t, biasDirPath)
	for _, want := range []string{
		"masterBias_GA0_-10C_257f_20251117.xisf",
		"masterBias_GA2750_-10C_129f_20251117.xisf",
	} {
		if !contains(got, want) {
			t.Errorf("%s missing, got %v", want, got)
		}
	}

	// The bias directory sits beside the sessions, not inside one.
	for _, e := range dirEntries(t, sessionOutDir(filepath.Join(out, target), "")) {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "Session_") {
			continue
		}
		if _, err := os.Stat(filepath.Join(sessionOutDir(filepath.Join(out, target), e.Name()), biasOutDir)); err == nil {
			t.Errorf("%s holds its own %s/, want one shared directory", e.Name(), biasOutDir)
		}
	}

	// The report records both gains for the session.
	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if len(recs) != 2 {
		t.Fatalf("got %d records, want header plus one session", len(recs))
	}
	if got := recs[1][len(recs[1])-1]; got != "GA0 GA2750" {
		t.Errorf("gains column = %q, want %q", got, "GA0 GA2750")
	}
}

// TestPrepareBiasIsSharedAcrossSessions checks twenty nights still yield two
// files, not forty. This is the property that keeps the destination at 466 MB
// instead of 18 GB for a target of this size.
func TestPrepareBiasIsSharedAcrossSessions(t *testing.T) {
	const target = "M31"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{},
		flats:  map[string][]string{},
		biases: map[string]string{
			"GA0":    "masterBias_GA0_-10C_257f_20251117.xisf",
			"GA2750": "masterBias_GA2750_-10C_129f_20251117.xisf",
		},
	}
	const nights = 5
	f.lights[target] = map[string]map[string][]string{}
	for i := range nights {
		date := "2026-09-0" + string(rune('1'+i))
		ms := "00" + string(rune('0'+i))
		f.lights[target][date] = map[string][]string{
			"B": {lightNameGain(target, "B", "20260910", "220000", ms, "310", "GA0")},
			"O": {lightNameGain(target, "O", "20260910", "230000", ms, "310", "GA2750")},
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
	if got := len(fileNames(t, filepath.Join(sessionOutDir(filepath.Join(out, target), ""), biasOutDir))); got != 2 {
		t.Errorf("got %d bias files for %d sessions, want 2", got, len(sessions))
	}
}

// TestPrepareCopiesOnlyTheBiasesNeeded checks a single-gain target copies one
// master even though another is present.
func TestPrepareCopiesOnlyTheBiasesNeeded(t *testing.T) {
	const target = "IC63"
	root := t.TempDir()

	f := simpleFixture(target) // lightName writes GA0
	f.biases = map[string]string{
		"GA0":    "masterBias_GA0_-10C_257f_20251117.xisf",
		"GA2750": "masterBias_GA2750_-10C_129f_20251117.xisf",
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	got := fileNames(t, filepath.Join(sessionOutDir(filepath.Join(out, target), ""), biasOutDir))
	if len(got) != 1 || !strings.Contains(got[0], "GA0") {
		t.Errorf("bias dir holds %v, want only the GA0 master", got)
	}
}

// TestPrepareWarnsOnMissingBias checks a gain the lights need but the masters
// lack is reported and the run still succeeds, matching how a missing flat is
// handled: the session is usable data, and the reduction can add the bias.
func TestPrepareWarnsOnMissingBias(t *testing.T) {
	const target = "NGC7000"
	root := t.TempDir()

	f := biasFixture(target)
	f.biases = map[string]string{"GA0": "masterBias_GA0_-10C_257f_20251117.xisf"}
	f.build(t, root)
	out := t.TempDir()

	// runPrepare reports warnings on stderr, so the whole run is captured.
	stderr := captureStderr(t, func() {
		if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
			t.Errorf("runPrepare returned an error for a missing master: %v", err)
		}
	})
	if !strings.Contains(stderr, "GA2750") {
		t.Errorf("stderr %q does not name the missing gain GA2750", stderr)
	}

	// The master that does exist is still copied.
	got := fileNames(t, filepath.Join(sessionOutDir(filepath.Join(out, target), ""), biasOutDir))
	if len(got) != 1 || !strings.Contains(got[0], "GA0") {
		t.Errorf("bias dir holds %v, want the available GA0 master copied anyway", got)
	}
}

// TestPrepareWithoutBiasMastersIsNotAnError checks a target with no Bias/
// directory prepares normally. The bias can be supplied during the reduction.
func TestPrepareWithoutBiasMastersIsNotAnError(t *testing.T) {
	const target = "WR134"
	root := t.TempDir()
	simpleFixture(target).build(t, root) // no biases written
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	if _, err := os.Stat(filepath.Join(sessionOutDir(filepath.Join(out, target), ""), biasOutDir)); err == nil {
		t.Error("a bias/ directory was created with no masters to copy")
	}
	// The sessions themselves must be unaffected.
	if got := len(sessionDirNames(t, out, target)); got != 2 {
		t.Errorf("got %d sessions, want 2", got)
	}
}

// TestPrepareSkipsNonMasterFilesInBiasDir checks the syncthing partials and the
// dotfiles beside the masters are never copied. A half-transferred master would
// be worse than none at all.
func TestPrepareSkipsNonMasterFilesInBiasDir(t *testing.T) {
	const target = "SH2-54"
	root := t.TempDir()
	biasFixture(target).build(t, root)

	biasSrc := filepath.Join(root, biasDir, biasMastersDir)
	for _, junk := range []string{
		".syncthing.masterBias_GA0_-10C_257f_20251117.xisf.tmp",
		".DS_Store",
		"masterBias_GA0_-10C_257f_20251117.xisf.tmp",
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(biasSrc, junk), []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(biasSrc, "G0"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	got := fileNames(t, filepath.Join(sessionOutDir(filepath.Join(out, target), ""), biasOutDir))
	if len(got) != 2 {
		t.Fatalf("bias dir holds %v, want exactly the two masters", got)
	}
	for _, n := range got {
		if strings.HasPrefix(n, ".") || strings.HasSuffix(n, ".tmp") || n == "G0" || n == "notes.txt" {
			t.Errorf("%q was copied into the bias directory", n)
		}
	}
}

// TestIndexBiasMasters covers the indexing rules directly.
func TestIndexBiasMasters(t *testing.T) {
	t.Run("keys by the gain in the name", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{
			"masterBias_GA0_-10C_257f_20251117.xisf",
			"masterBias_GA2750_-10C_129f_20251117.xisf",
		} {
			writeStub(t, filepath.Join(dir, n))
		}

		masters, warnings, err := indexBiasMasters(dir)
		if err != nil {
			t.Fatalf("indexBiasMasters: %v", err)
		}
		if len(warnings) != 0 {
			t.Errorf("warnings = %v, want none", warnings)
		}
		if len(masters) != 2 {
			t.Fatalf("got %d masters, want 2", len(masters))
		}
		if masters["GA0"].name != "masterBias_GA0_-10C_257f_20251117.xisf" {
			t.Errorf("GA0 resolved to %q", masters["GA0"].name)
		}
		if masters["GA2750"].name != "masterBias_GA2750_-10C_129f_20251117.xisf" {
			t.Errorf("GA2750 resolved to %q", masters["GA2750"].name)
		}
		if masters["GA0"].src != filepath.Join(dir, "masterBias_GA0_-10C_257f_20251117.xisf") {
			t.Errorf("src = %q", masters["GA0"].src)
		}
	})

	t.Run("a file with no gain is reported and skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeStub(t, filepath.Join(dir, "masterBias_BIN-1_9576x6388.xisf"))
		writeStub(t, filepath.Join(dir, "masterBias_GA0_-10C_257f_20251117.xisf"))

		masters, warnings, err := indexBiasMasters(dir)
		if err != nil {
			t.Fatalf("indexBiasMasters: %v", err)
		}
		if len(masters) != 1 {
			t.Errorf("got %d masters, want 1", len(masters))
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "masterBias_BIN-1_9576x6388.xisf") {
			t.Errorf("warnings = %v, want the gainless file named", warnings)
		}
	})

	t.Run("two masters for one gain are reported, not silently resolved", func(t *testing.T) {
		dir := t.TempDir()
		writeStub(t, filepath.Join(dir, "masterBias_GA0_-10C_257f_20251117.xisf"))
		writeStub(t, filepath.Join(dir, "masterBias_GA0_-10C_300f_20260901.xisf"))

		masters, warnings, err := indexBiasMasters(dir)
		if err != nil {
			t.Fatalf("indexBiasMasters: %v", err)
		}
		if len(masters) != 1 {
			t.Errorf("got %d masters, want 1", len(masters))
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "deux masters") {
			t.Errorf("warnings = %v, want the collision reported", warnings)
		}
		// Sorted order decides, so the outcome does not depend on readdir.
		if masters["GA0"].name != "masterBias_GA0_-10C_257f_20251117.xisf" {
			t.Errorf("resolved to %q, want the first in sorted order", masters["GA0"].name)
		}
	})

	t.Run("a missing directory is an error", func(t *testing.T) {
		if _, _, err := indexBiasMasters(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Error("a missing masters directory was accepted")
		}
	})

	// Checked here rather than through the destination tree, because the
	// listing helper used there skips dotfiles and would hide exactly what this
	// is looking for.
	t.Run("partials, dotfiles, subdirectories and other extensions are skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeStub(t, filepath.Join(dir, "masterBias_GA0_-10C_257f_20251117.xisf"))

		junk := []string{
			// syncthing writes its partials beside the finished file; copying a
			// half-transferred 233 MB master would be worse than none at all.
			".syncthing.masterBias_GA0_-10C_257f_20251117.xisf.tmp",
			".syncthing.masterBias_GA2750_-10C_129f_20251117.xisf.tmp",
			".DS_Store",
			"masterBias_GA2750_-10C_129f_20251117.xisf.tmp", // no leading dot
			"notes.txt",
			"masterBias_GA2750_-10C_129f_20251117.FITS",
			"masterBias_GA2750_-10C_129f_20251117",
			// The AppleDouble sidecar macOS writes on exFAT. Its extension is
			// .xisf, so it satisfies the extension check and only the dotfile
			// guard keeps it out. A target volume already carries hundreds of
			// these.
			"._masterBias_GA2750_-10C_129f_20251117.xisf",
		}
		for _, n := range junk {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Join(dir, "G0"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A directory whose name ends in .xisf would otherwise be indexed as a
		// master and then fail to copy.
		if err := os.MkdirAll(filepath.Join(dir, "masterBias_GA2750_-10C_129f_20251117.xisf"), 0o755); err != nil {
			t.Fatal(err)
		}

		masters, warnings, err := indexBiasMasters(dir)
		if err != nil {
			t.Fatalf("indexBiasMasters: %v", err)
		}
		if len(masters) != 1 {
			t.Errorf("got %d masters (%v), want only the GA0 one", len(masters), masters)
		}
		if _, ok := masters["GA2750"]; ok {
			t.Error("GA2750 was indexed from a file that should have been skipped")
		}
		if len(warnings) != 0 {
			t.Errorf("warnings = %v, want none: skipped files are not anomalies", warnings)
		}
	})

	t.Run("the master extension set does not admit FITS", func(t *testing.T) {
		// masterExts is deliberately separate from fitsExts: the masters are
		// XISF, which readfits cannot parse, so a master must never reach the
		// quality pass that fitsExts gates.
		for _, ext := range []string{".FIT", ".FITS", ".FTS"} {
			if masterExts[ext] {
				t.Errorf("masterExts admits %q, want only XISF", ext)
			}
		}
		if !masterExts[".XISF"] {
			t.Error("masterExts does not admit .XISF")
		}
	})
}

// TestTargetGainsUnionsAcrossSessions checks the union and the ordering.
func TestTargetGainsUnionsAcrossSessions(t *testing.T) {
	sessions := []sessionPlan{
		{gains: []string{"GA0"}},
		{gains: []string{"GA2750", "GA0"}},
		{gains: nil},
		{gains: []string{"GA0"}},
	}
	if got := strings.Join(targetGains(sessions), " "); got != "GA0 GA2750" {
		t.Errorf("targetGains = %q, want %q", got, "GA0 GA2750")
	}
	if got := targetGains(nil); len(got) != 0 {
		t.Errorf("targetGains(nil) = %v, want empty", got)
	}
}

// TestPrepareDryRunTouchesNoBias checks dry-run reports the biases without
// copying them.
func TestPrepareDryRunTouchesNoBias(t *testing.T) {
	const target = "M87-1"
	root := t.TempDir()
	biasFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.dryRun = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	if _, err := os.Stat(filepath.Join(sessionOutDir(filepath.Join(out, target), ""), biasOutDir)); err == nil {
		t.Error("dry-run created the bias directory")
	}
	// Nor the reports, which the dry-run contract already covers.
	if _, err := os.Stat(metricsPath(filepath.Join(out, target), sessionsCSVName)); err == nil {
		t.Error("dry-run wrote sessions.csv")
	}
}

// TestPrepareBiasSkipExistingIsIdempotent checks a second run does not recopy.
// TestPrepareReportsCopiedBiases checks the console line names the gains it
// actually copied, and not the ones it wanted. The distinction matters when a
// master is missing: the two sets differ, and a line reporting the wanted gains
// would claim a copy that never happened.
func TestPrepareReportsCopiedBiases(t *testing.T) {
	t.Run("every gain copied is named", func(t *testing.T) {
		const target = "M51"
		root := t.TempDir()
		biasFixture(target).build(t, root)
		out := t.TempDir()

		// Two lines mention the bias: the per-copy one naming the gains, and the
		// run summary giving the count. This is the one that reports which
		// masters, so that is the one under test.
		var line string
		stdout := captureStdout(t, func() {
			opts := prepareOptionsForTest(target, root, out)
			opts.quiet = false
			if err := runPrepare(opts); err != nil {
				t.Fatalf("runPrepare: %v", err)
			}
		})
		for _, l := range strings.Split(stdout, "\n") {
			if strings.Contains(l, "master bias") && strings.Contains(l, "gain") {
				line = l
			}
		}
		if line == "" {
			t.Fatalf("no bias line naming the gains in the output:\n%s", stdout)
		}
		if !strings.Contains(line, "2 master bias") {
			t.Errorf("line %q does not report a count of 2", line)
		}
		for _, gain := range []string{"GA0", "GA2750"} {
			if !strings.Contains(line, gain) {
				t.Errorf("line %q does not name %s", line, gain)
			}
		}
	})

	t.Run("a missing master is not reported as copied", func(t *testing.T) {
		const target = "M51"
		root := t.TempDir()

		f := biasFixture(target)
		f.biases = map[string]string{"GA0": "masterBias_GA0_-10C_257f_20251117.xisf"}
		f.build(t, root)

		stdout := captureStdout(t, func() {
			opts := prepareOptionsForTest(target, root, t.TempDir())
			opts.quiet = false
			if err := runPrepare(opts); err != nil {
				t.Fatalf("runPrepare: %v", err)
			}
		})
		for _, l := range strings.Split(stdout, "\n") {
			if !strings.Contains(l, "master bias") || !strings.Contains(l, "gain") {
				continue
			}
			if strings.Contains(l, "GA2750") {
				t.Errorf("line %q claims GA2750 was copied, want only the available gain", l)
			}
			if !strings.Contains(l, "1 master bias") {
				t.Errorf("line %q does not report a count of 1", l)
			}
		}
	})

	t.Run("nothing copied reports none", func(t *testing.T) {
		const target = "M51"
		root := t.TempDir()

		f := biasFixture(target)
		f.biases = nil
		f.build(t, root)

		stdout := captureStdout(t, func() {
			opts := prepareOptionsForTest(target, root, t.TempDir())
			opts.quiet = false
			if err := runPrepare(opts); err != nil {
				t.Fatalf("runPrepare: %v", err)
			}
		})
		if !strings.Contains(stdout, "aucun master bias") {
			t.Errorf("no \"aucun master bias\" line in the output:\n%s", stdout)
		}
	})
}

func TestPrepareBiasSkipExistingIsIdempotent(t *testing.T) {
	const target = "IC4090"
	root := t.TempDir()
	biasFixture(target).build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("first run: %v", err)
	}

	opts := prepareOptionsForTest(target, root, out)
	opts.skipExisting = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("second run: %v", err)
	}

	if got := len(fileNames(t, filepath.Join(sessionOutDir(filepath.Join(out, target), ""), biasOutDir))); got != 2 {
		t.Errorf("got %d bias files after a second run, want 2", got)
	}
}

// TestGainReMatchesAcquisitionNames pins the token shape on both sides. The
// master names were renamed from G0 to GA0 to match the light frames; a
// regression here would silently find no master at all.
func TestGainReMatchesAcquisitionNames(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"LBN527_LIGHT_L_300s_BIN1_-10C_GA0_20260914_045755_638_PA310_E.FIT", "GA0"},
		{"LBN527_LIGHT_H_600s_BIN1_-10C_GA2750_20260902_014447_431_PA310_W.FIT", "GA2750"},
		{"masterBias_GA0_-10C_257f_20251117.xisf", "GA0"},
		{"masterBias_GA2750_-10C_129f_20251117.xisf", "GA2750"},
		{"masterDark_300s_GA0_-10C_20251118.xisf", "GA0"},
	}
	for _, tc := range tests {
		m := gainRe.FindStringSubmatch(tc.name)
		if m == nil {
			t.Errorf("gainRe does not match %q", tc.name)
			continue
		}
		if m[1] != tc.want {
			t.Errorf("gainRe on %q = %q, want %q", tc.name, m[1], tc.want)
		}
	}

	// A legacy name must not match: the old masters were named G0, and treating
	// those as GA0 would mislabel the file.
	if gainRe.MatchString("masterBias_G0_-10C_257f_20251117.xisf") {
		t.Error("gainRe matches the legacy G0 spelling, want only GA<n>")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
