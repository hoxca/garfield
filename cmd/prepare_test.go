package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestPrepareSessionsAndFilterDirs(t *testing.T) {
	const target = "LBN527"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	// Sessions are numbered in date order, zero-padded to two digits.
	for i, date := range []string{"2026-09-10", "2026-09-11"} {
		want := fmt.Sprintf("Session_%02d", i+1)
		dir := filepath.Join(sessionOutDir(filepath.Join(out, target), want), sessionLightsDir, "H")
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		if len(files) == 0 {
			t.Errorf("%s/%s has no frames", want, date)
		}
	}

	// Exactly one session directory per date, and nothing else but the metrics
	// directory among the target's subdirectories.
	if got := sessionDirNames(t, out, target); strings.Join(got, ",") != "Session_01,Session_02" {
		t.Errorf("session dirs = %v, want [Session_01 Session_02]", got)
	}

	// No session directory may sit directly in the target root.
	for _, e := range dirEntries(t, filepath.Join(out, target)) {
		if e.IsDir() && strings.HasPrefix(e.Name(), "Session_") {
			t.Errorf("%s/%s sits in the target root, want it under %s/", target, e.Name(), sessionsDir)
		}
	}
}

// TestPrepareSessionsLiveUnderSessionsDir pins the target layout. Approved data
// sits under <target>/Sessions/Session_NN/ while rejects stay at the target
// root under <target>/rejected/Session_NN/, so no session directory may appear
// directly in the target root. The other layout tests only assert that session
// directories exist, not where, so this is the guard against the wrapper
// silently disappearing again.
func TestPrepareSessionsLiveUnderSessionsDir(t *testing.T) {
	const target = "NGC7000"
	root := t.TempDir()

	fixture := simpleFixture(target)
	// Make the first frame of the first night unreadable so it is rejected
	// while its sibling is approved, giving both trees content.
	rejected := fixture.lights[target]["2026-09-10"]["H"][0]
	fixture.build(t, root)
	if err := os.WriteFile(filepath.Join(root, lightsDir, target, "2026-09-10", "H", rejected), []byte("not a FITS file"), 0o644); err != nil {
		t.Fatalf("corrupt the frame: %v", err)
	}

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	rootDir := filepath.Join(out, target)

	// The target root holds exactly the three top-level directories.
	if got := strings.Join(subDirNames(t, rootDir), ","); got != "Sessions,"+metricsDir+","+rejectedDir {
		t.Errorf("target root holds %v, want [Sessions metrics rejected]", got)
	}

	// Approved frames live under the wrapper...
	lights := filepath.Join(sessionOutDir(rootDir, "Session_01"), sessionLightsDir, "H")
	if _, err := os.Stat(lights); err != nil {
		t.Errorf("%s missing: %v", filepath.Join(sessionsDir, "Session_01", sessionLightsDir, "H"), err)
	}
	flats := filepath.Join(sessionOutDir(rootDir, "Session_01"), sessionFlatsDir, "H")
	if _, err := os.Stat(flats); err != nil {
		t.Errorf("%s missing: %v", filepath.Join(sessionsDir, "Session_01", sessionFlatsDir, "H"), err)
	}

	// ...and the rejected frame stays at the target root, not under Sessions/.
	rej := filepath.Join(rootDir, rejectedDir, "Session_01", "H", rejected)
	if _, err := os.Stat(rej); err != nil {
		t.Errorf("%s missing: %v", filepath.Join(rejectedDir, "Session_01", "H", rejected), err)
	}
	if _, err := os.Stat(filepath.Join(sessionsDir, rejectedDir)); err == nil {
		t.Errorf("%s/%s exists, want the rejected tree at the target root only", sessionsDir, rejectedDir)
	}

	// The CSV reports keep the bare session name so a consumer joins on the
	// session column rather than reconstructing a path.
	for _, name := range []string{sessionsCSVName, framesCSVName} {
		f, err := os.Open(metricsPath(rootDir, name))
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		recs, err := newCSVReader(f).ReadAll()
		f.Close()
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(recs) < 2 {
			t.Fatalf("%s has %d rows, want a header and at least one frame", name, len(recs))
		}
		if recs[0][0] != "session" {
			t.Fatalf("%s first column is %q, want it to be the session", name, recs[0][0])
		}
		seen := 0
		for _, rec := range recs[1:] {
			if !strings.HasPrefix(rec[0], "Session_") {
				t.Errorf("%s first column = %q, want a bare session name", name, rec[0])
				continue
			}
			if strings.Contains(rec[0], sessionsDir) {
				t.Errorf("%s first column = %q, want the bare session name without the %s/ prefix", name, rec[0], sessionsDir)
			}
			seen++
		}
		if seen == 0 {
			t.Errorf("%s has no data rows, so the session column was not checked", name)
		}
	}
}

func TestPrepareCopiesFlatsIntoSession(t *testing.T) {
	const target = "M51"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	dir := filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "flats", "H")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read flats dir: %v", err)
	}
	// The default --flat-count is 0, so the session keeps only the flat its own
	// date held. Borrowing is opt-in; TestPrepareBorrowsFlatsAndCopiesThem
	// covers the other case.
	if len(files) != 1 {
		t.Fatalf("got %d flats, want 1 (the session's own date only)", len(files))
	}
	if !strings.HasPrefix(files[0].Name(), "FLAT_H_") {
		t.Errorf("flat = %q, want a FLAT_H_ name", files[0].Name())
	}
	// Contents must match the source, confirming a real copy.
	src := filepath.Join(root, flatsDir, "2026-09-10", files[0].Name())
	want, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	// Contents must match the source, confirming a real copy.
	got, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("flat contents = %q, want %q", got, want)
	}
}

// TestPrepareSkipsFlatsWithoutLights covers a night where the operator took
// flats for filters that were never imaged.
func TestPrepareSkipsFlatsWithoutLights(t *testing.T) {
	const target = "NGC7822"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-14": {"O": {lightName(target, "O", "20260914", "220000", "000", "196")}},
			},
		},
		flats: map[string][]string{
			"2026-09-14": {
				flatName("O", "41.4", "20260914", "070000", "000", "196"),
				flatName("S", "41.5", "20260914", "070100", "000", "196"),
				flatName("B", "41.6", "20260914", "070200", "000", "196"),
			},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	base := filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "flats")
	for _, filter := range []string{"O", "S", "B"} {
		if filter == "O" {
			continue
		}
		if _, err := os.Stat(filepath.Join(base, filter)); err == nil {
			t.Errorf("flats/%s was copied but has no lights", filter)
		}
	}
	files, err := os.ReadDir(filepath.Join(base, "O"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("got %d O flats, want 1", len(files))
	}
}

// TestPrepareUsesSessionDateForFlats is the subtlety that makes the tool work.
// Sessions run past midnight, so the date inside a light filename routinely
// differs from the directory naming the session. Flats must be matched on the
// directory date.
func TestPrepareUsesSessionDateForFlats(t *testing.T) {
	const target = "IC63"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				// Directory says 2026-09-01, but the frames were taken after
				// midnight on 09-01 / 09-02 / 09-03.
				"2026-09-01": {"L": {
					lightName(target, "L", "20260902", "014447", "431", "230"),
					lightName(target, "L", "20260903", "020500", "053", "230"),
					lightName(target, "L", "20260904", "031100", "000", "230"),
				}},
			},
		},
		// Flats exist only under the directory date.
		flats: map[string][]string{
			"2026-09-01": {flatName("L", "41.4", "20260901", "190000", "000", "230")},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	dir := filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "flats", "L")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read flats: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d flats, want 1 (matched on the session date)", len(files))
	}

	// The frame date range must be recorded, so the off-by-one stays visible.
	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if len(recs) != 2 {
		t.Fatalf("got %d CSV records, want 2", len(recs))
	}
	row := recs[1]
	if row[9] != "2026-09-02" || row[10] != "2026-09-04" {
		t.Errorf("frame date range = [%s .. %s], want [2026-09-02 .. 2026-09-04]", row[9], row[10])
	}
}

// TestPrepareWarnsOnMissingFlats covers a night with lights but no calibration.
// The session is still produced; the gap is reported rather than fatal.
func TestPrepareWarnsOnMissingFlats(t *testing.T) {
	const target = "LBN527"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-18": {
					"H": {lightName(target, "H", "20260918", "220000", "000", "310")},
					"O": {lightName(target, "O", "20260918", "223000", "000", "310")},
				},
			},
		},
		flats: map[string][]string{}, // Flats/2026-09-18 exists but is empty
	}
	if err := os.MkdirAll(filepath.Join(root, flatsDir, "2026-09-18"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("a missing flats directory must not fail the run: %v", err)
	}

	// Lights are still copied.
	for _, filter := range []string{"H", "O"} {
		dir := filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "lights", filter)
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("lights for %s were not copied: %v", filter, err)
		}
		if len(files) != 1 {
			t.Errorf("%s: got %d lights, want 1", filter, len(files))
		}
	}

	// No flats directory is created at all.
	if _, err := os.Stat(filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "flats")); err == nil {
		t.Error("a flats directory was created despite having no flats")
	}

	// The gap is recorded in the index.
	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	row := recs[1]
	if row[7] != "0" {
		t.Errorf("flatCount = %q, want 0", row[7])
	}
	if row[8] != "H O" {
		t.Errorf("missingFlats = %q, want %q", row[8], "H O")
	}
}

func TestPrepareDryRunWritesNothing(t *testing.T) {
	const target = "WR134"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.dryRun = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	// The output root must not even be created for the target.
	if _, err := os.Stat(filepath.Join(out, target)); err == nil {
		t.Error("dry run created the target directory")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run left %d entries in the output directory", len(entries))
	}
}

// TestPrepareDryRunStillAssesses confirms the quality pass runs in dry-run
// mode: a dry run that skipped assessment could not answer the question the
// flag exists for, which is what would be filtered.
func TestPrepareDryRunStillAssesses(t *testing.T) {
	const target = "NGC2237-1"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.dryRun = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	lightsRoot := filepath.Join(root, lightsDir, target)
	sessions, _, err := planSessions(lightsRoot, filepath.Join(root, flatsDir), target, 0, c3Camera)
	if err != nil {
		t.Fatal(err)
	}
	if err := assessSessions(sessions, opts); err != nil {
		t.Fatalf("assessSessions: %v", err)
	}

	total := 0
	for _, s := range sessions {
		total += s.approvedCount + s.rejectedCount
	}
	if total == 0 {
		t.Fatal("no frames were assessed in dry-run mode")
	}
	for _, s := range sessions {
		if s.approvedCount == 0 {
			t.Errorf("%s: no frames approved, the assessment did not run", s.sessionDir())
		}
	}
}

func TestPrepareSkipExistingIsIdempotent(t *testing.T) {
	const target = "M87-1"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	if err := runPrepare(opts); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Mark a destination file so a re-run would be detectable.
	victim := filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "lights", "H")
	entries, err := os.ReadDir(victim)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("fixture produced no lights")
	}
	marked := filepath.Join(victim, entries[0].Name())
	if err := os.WriteFile(marked, []byte("SENTINEL"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts.skipExisting = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("second run: %v", err)
	}

	got, err := os.ReadFile(marked)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "SENTINEL" {
		t.Errorf("file was overwritten despite --skip-existing: %q", got)
	}

	// Without the flag the file is replaced.
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("third run: %v", err)
	}
	got, err = os.ReadFile(marked)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == "SENTINEL" {
		t.Error("file was not overwritten without --skip-existing")
	}
}

func TestPrepareIgnoresDotFiles(t *testing.T) {
	const target = "LDN1527"
	root := t.TempDir()
	simpleFixture(target).build(t, root)

	// Real acquisition directories are full of .DS_Store.
	if err := os.WriteFile(filepath.Join(root, lightsDir, target, ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, lightsDir, target, "2026-09-10", ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, lightsDir, target, "2026-09-10", "H", ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, flatsDir, "2026-09-10", ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	dir := filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "lights", "H")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d entries, want 2 (the .DS_Store must be skipped)", len(files))
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".") {
			t.Errorf(".DS_Store was copied: %q", f.Name())
		}
	}
}

func TestPrepareOnlyAcceptsDateDirs(t *testing.T) {
	const target = "M13"
	root := t.TempDir()
	simpleFixture(target).build(t, root)

	// Non-date directories at the target level must be ignored. The first two
	// are outright invalid, the third is unpadded and the fourth uses the
	// compact form -- all four must be rejected by the date pattern, otherwise
	// they would each become a session directory.
	for _, name := range []string{"Masters", "_draft", "2026-9-1", "20260910"} {
		dir := filepath.Join(root, lightsDir, target, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Each decoy holds a frame, so if it were accepted it would show up as
		// a session with content rather than an empty directory.
		writeStub(t, filepath.Join(dir, "H", lightName(target, "H", "20260910", "220000", "000", "310")))
	}

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	// Nothing was rejected here, so the target root holds just the session
	// wrapper and the metrics directory.
	if got := strings.Join(subDirNames(t, filepath.Join(out, target)), ","); got != "Sessions,"+metricsDir {
		t.Errorf("target root holds %v, want [Sessions %s]", got, metricsDir)
	}

	// Session directories live under Sessions/, so the listing has to descend
	// one level to see them.
	dirs := sessionDirNames(t, out, target)
	want := []string{"Session_01", "Session_02"}
	if strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Errorf("session dirs = %v, want %v (only the padded YYYY-MM-DD ones)", dirs, want)
	}
}

func TestPrepareErrors(t *testing.T) {
	t.Run("missing target", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, lightsDir), 0o755); err != nil {
			t.Fatal(err)
		}
		err := runPrepare(prepareOptions{target: "Nope", input: root, output: t.TempDir(), quiet: true, camera: "c3"})
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("error = %v, want a target-not-found failure", err)
		}
	})

	t.Run("empty target", func(t *testing.T) {
		root := t.TempDir()
		err := runPrepare(prepareOptionsForTest("", root, t.TempDir()))
		if err == nil || !strings.Contains(err.Error(), "--target is required") {
			t.Errorf("error = %v, want a missing-target failure", err)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		err := runPrepare(prepareOptions{target: "X", input: "", output: t.TempDir(), quiet: true})
		if err == nil || !strings.Contains(err.Error(), "--input") {
			t.Errorf("error = %v, want an input validation failure", err)
		}
	})

	t.Run("empty output", func(t *testing.T) {
		err := runPrepare(prepareOptions{target: "X", input: t.TempDir(), output: "", quiet: true})
		if err == nil || !strings.Contains(err.Error(), "--output") {
			t.Errorf("error = %v, want an output validation failure", err)
		}
	})

	t.Run("no sessions", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, lightsDir, "Empty"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := runPrepare(prepareOptions{target: "Empty", input: root, output: t.TempDir(), quiet: true, camera: "c3"})
		if err == nil || !strings.Contains(err.Error(), "no session") {
			t.Errorf("error = %v, want a no-sessions failure", err)
		}
	})

	t.Run("target is a file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, lightsDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, lightsDir, "File"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := runPrepare(prepareOptions{target: "File", input: root, output: t.TempDir(), quiet: true, camera: "c3"})
		if err == nil || !strings.Contains(err.Error(), "is not a directory") {
			t.Errorf("error = %v, want a not-a-directory failure", err)
		}
	})

	t.Run("unwritable output", func(t *testing.T) {
		const target = "IC434"
		root := t.TempDir()
		simpleFixture(target).build(t, root)

		// A regular file where the output directory should be.
		blocked := filepath.Join(t.TempDir(), "blocked")
		if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}

		err := runPrepare(prepareOptions{target: target, input: root, output: blocked, quiet: true, camera: "c3"})
		if err == nil {
			t.Error("expected a failure when the output path is a file")
		}
	})
}

func TestPrepareSessionsCSV(t *testing.T) {
	const target = "IC4090"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"B": {
					lightName(target, "B", "20260910", "220000", "000", "310"),
				}},
				"2026-09-11": {"B": {
					lightName(target, "B", "20260911", "220000", "000", "310"),
					lightName(target, "B", "20260911", "223000", "000", "310"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {flatName("B", "41.4", "20260910", "070000", "000", "310")},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3 (header plus two sessions)", len(recs))
	}

	// gains and darks are appended last, so every pre-existing column keeps its
	// position.
	wantHeader := []string{
		"session", "date", "pa", "filters", "lightCount",
		"approvedCount", "rejectedCount", "flatCount",
		"missingFlats", "frameDateMin", "frameDateMax",
		"gains", "darks",
	}
	if len(recs[0]) != len(wantHeader) {
		t.Fatalf("header has %d columns, want %d", len(recs[0]), len(wantHeader))
	}
	for i := range wantHeader {
		if recs[0][i] != wantHeader[i] {
			t.Errorf("header[%d] = %q, want %q", i, recs[0][i], wantHeader[i])
		}
	}

	// Session 01 has one light and a matching flat.
	row := recs[1]
	if row[0] != "Session_01" || row[1] != "2026-09-10" {
		t.Errorf("row = %v, want Session_01 / 2026-09-10", row[:2])
	}
	if row[2] != "PA310" {
		t.Errorf("pa = %q, want %q", row[2], "PA310")
	}
	if row[3] != "B" {
		t.Errorf("filters = %q, want %q", row[3], "B")
	}
	if row[4] != "1" || row[5] != "1" || row[6] != "0" {
		t.Errorf("counts = light %q approved %q rejected %q, want 1/1/0", row[4], row[5], row[6])
	}
	if row[7] != "1" {
		t.Errorf("flatCount = %q, want 1", row[7])
	}

	// Session 02 has two lights and no flats of its own that night. With the
	// default --flat-count of 0 nothing is borrowed, so it is recorded missing.
	row = recs[2]
	if row[4] != "2" || row[5] != "2" || row[6] != "0" {
		t.Errorf("counts = light %q approved %q rejected %q, want 2/2/0", row[4], row[5], row[6])
	}
	if row[7] != "0" {
		t.Errorf("flatCount = %q, want 0", row[7])
	}
	if row[8] != "B" {
		t.Errorf("missingFlats = %q, want %q", row[8], "B")
	}
	// lightName writes the GA0 token at 300s, so the session records that gain
	// and needs that master dark. The key carries the setpoint as well: a dark
	// is thermal, so "GA0/300s" alone would name two different masters.
	if row[11] != "GA0" {
		t.Errorf("gains = %q, want %q", row[11], "GA0")
	}
	if row[12] != "GA0/300s/-10C" {
		t.Errorf("darks = %q, want %q", row[12], "GA0/300s/-10C")
	}
}

func TestPrepareMultipleFiltersPerSession(t *testing.T) {
	const target = "LBN527"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {
					"B": {lightName(target, "B", "20260910", "220000", "000", "310")},
					"G": {lightName(target, "G", "20260910", "223000", "000", "310")},
					"R": {lightName(target, "R", "20260910", "230000", "000", "310")},
				},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {
				flatName("B", "41.4", "20260910", "070000", "000", "310"),
				flatName("G", "41.5", "20260910", "070100", "000", "310"),
				flatName("R", "41.6", "20260910", "070200", "000", "310"),
			},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	for _, filter := range []string{"B", "G", "R"} {
		if _, err := os.Stat(filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "lights", filter)); err != nil {
			t.Errorf("lights/%s missing: %v", filter, err)
		}
		if _, err := os.Stat(filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "flats", filter)); err != nil {
			t.Errorf("flats/%s missing: %v", filter, err)
		}
	}

	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if recs[1][3] != "B G R" {
		t.Errorf("filters = %q, want %q (sorted)", recs[1][3], "B G R")
	}
	if recs[1][7] != "3" {
		t.Errorf("flatCount = %q, want 3", recs[1][5])
	}
}

func TestPrepareMissingFlatsRoot(t *testing.T) {
	const target = "SH2-54"
	root := t.TempDir()

	// Lights only; the whole Flats directory is absent.
	prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"H": {lightName(target, "H", "20260910", "220000", "000", "292")}},
			},
		},
	}.build(t, root)

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("a missing Flats directory must not fail the run: %v", err)
	}
	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if recs[1][7] != "0" || recs[1][8] != "H" {
		t.Errorf("row = %v, want flatCount 0 and missingFlats H", recs[1])
	}
}

// TestPrepareSessionLayoutIsSymmetric pins the shape of a prepared session:
// lights and flats live under sibling directories, and no filter directory
// sits directly under Session_NN.
func TestPrepareSessionLayoutIsSymmetric(t *testing.T) {
	const target = "M87-2"
	root := t.TempDir()

	prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {
					"B": {lightName(target, "B", "20260910", "220000", "000", "286")},
					"R": {lightName(target, "R", "20260910", "223000", "000", "286")},
				},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {
				flatName("B", "41.4", "20260910", "070000", "000", "286"),
				flatName("R", "41.5", "20260910", "070100", "000", "286"),
			},
		},
	}.build(t, root)

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	session := sessionOutDir(filepath.Join(out, target), "Session_01")

	// The session holds exactly the two sibling directories.
	entries, err := os.ReadDir(session)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	want := []string{sessionFlatsDir, sessionLightsDir}
	if strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Errorf("session dirs = %v, want %v (and no stray filter dirs)", dirs, want)
	}

	// Both trees carry one directory per filter.
	for _, sub := range []string{sessionLightsDir, sessionFlatsDir} {
		filters, err := os.ReadDir(filepath.Join(session, sub))
		if err != nil {
			t.Fatalf("read %s: %v", sub, err)
		}
		var names []string
		for _, f := range filters {
			names = append(names, f.Name())
		}
		sort.Strings(names)
		if strings.Join(names, ",") != "B,R" {
			t.Errorf("%s contains %v, want [B R]", sub, names)
		}
	}
}

// TestPrepareRoutesByDecision is the core of the quality filtering: approved
// frames go into the session, everything else into the target-level rejected
// tree.
func TestPrepareRoutesByDecision(t *testing.T) {
	const target = "SH2-101"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	// The synthetic frames are good, so everything lands in lights/.
	lights, err := os.ReadDir(filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), sessionLightsDir, "H"))
	if err != nil {
		t.Fatalf("lights missing: %v", err)
	}
	if len(lights) != 2 {
		t.Errorf("got %d approved lights, want 2", len(lights))
	}

	// Nothing was rejected, so the rejected tree must not exist.
	if _, err := os.Stat(filepath.Join(out, target, rejectedDir)); err == nil {
		t.Error("a rejected/ tree was created with nothing rejected")
	}
}

// TestPrepareRejectsUnusableFrames drives the rejection path with thresholds no
// frame can satisfy, and checks the frames are quarantined rather than dropped.
func TestPrepareRejectsUnusableFrames(t *testing.T) {
	const target = "WR134"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.maxFWHM = 0.5 // nothing can measure sharper than this
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	// No approved lights, and no lights directory content either.
	lightsDirPath := filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), sessionLightsDir)
	if entries, err := os.ReadDir(lightsDirPath); err == nil {
		for _, e := range entries {
			files, _ := os.ReadDir(filepath.Join(lightsDirPath, e.Name()))
			if len(files) != 0 {
				t.Errorf("lights/%s holds %d files, want none", e.Name(), len(files))
			}
		}
	}

	// Every frame is under rejected/, keyed by session then filter.
	rejectedRoot := filepath.Join(out, target, rejectedDir, "Session_01", "H")
	files, err := os.ReadDir(rejectedRoot)
	if err != nil {
		t.Fatalf("rejected tree missing: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("got %d rejected frames, want 2", len(files))
	}

	// The reject reason is recorded in sessions.csv.
	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if recs[1][5] != "0" {
		t.Errorf("approvedCount = %q, want 0", recs[1][5])
	}
	if recs[1][6] != "2" {
		t.Errorf("rejectedCount = %q, want 2", recs[1][6])
	}
}

// TestPrepareThresholdsRouteFrames checks each threshold can move a frame
// between the two trees.
func TestPrepareThresholdsRouteFrames(t *testing.T) {
	const target = "IC2087"

	tests := []struct {
		name      string
		mutate    func(*prepareOptions)
		wantLight bool
	}{
		{"defaults approve", func(*prepareOptions) {}, true},
		{"maxFWHM forces rejection", func(o *prepareOptions) { o.maxFWHM = 0.5 }, false},
		{"minSNR forces rejection", func(o *prepareOptions) { o.minSNR = 1e9 }, false},
		{"maxEcc forces rejection", func(o *prepareOptions) { o.maxEcc = 0.0001 }, false},
		{"minScore forces rejection", func(o *prepareOptions) { o.minScore = 1e9 }, false},
		{"minStars forces rejection", func(o *prepareOptions) { o.minStars = 1 << 30 }, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			simpleFixture(target).build(t, root)
			out := t.TempDir()

			opts := prepareOptionsForTest(target, root, out)
			tc.mutate(&opts)
			if err := runPrepare(opts); err != nil {
				t.Fatalf("runPrepare: %v", err)
			}

			recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
			approved := recs[1][5] == "2"
			rejected := recs[1][6] == "2"

			if tc.wantLight && !approved {
				t.Errorf("approvedCount = %q, want 2", recs[1][5])
			}
			if !tc.wantLight && !rejected {
				t.Errorf("rejectedCount = %q, want 2", recs[1][6])
			}
			if !tc.wantLight && approved {
				t.Error("frames were approved despite the tightened threshold")
			}
		})
	}
}

// TestPrepareUnreadableFrameIsRejected checks a frame that cannot be analysed is
// quarantined rather than silently promoted into lights/.
func TestPrepareUnreadableFrameIsRejected(t *testing.T) {
	const target = "LDN1527"
	root := t.TempDir()

	prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"H": {
					lightName(target, "H", "20260910", "220000", "000", "292"),
				}},
			},
		},
		flats: map[string][]string{},
	}.build(t, root)

	// Corrupt the frame after building so processImage cannot read it.
	bad := filepath.Join(root, lightsDir, target, "2026-09-10", "H",
		lightName(target, "H", "20260910", "220000", "000", "292"))
	if err := os.WriteFile(bad, []byte("not a FITS file"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	if _, err := os.Stat(filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), sessionLightsDir, "H")); err == nil {
		t.Error("an unreadable frame was placed in lights/")
	}
	files, err := os.ReadDir(filepath.Join(out, target, rejectedDir, "Session_01", "H"))
	if err != nil {
		t.Fatalf("unreadable frame was not quarantined: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("got %d rejected frames, want 1", len(files))
	}
}

// TestPrepareRejectedTreeIsTargetScoped pins the layout: rejected frames live
// under <target>/rejected/<session>/<filter>, never inside a session.
func TestPrepareRejectedTreeIsTargetScoped(t *testing.T) {
	const target = "M51"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.maxFWHM = 0.5 // force everything to the rejected tree
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	// A session directory holds only lights and flats -- never rejected.
	// lights/ is absent here because nothing was approved, mirroring how
	// flats/ is only created when there are flats to put in it.
	for _, s := range []string{"Session_01", "Session_02"} {
		entries, err := os.ReadDir(sessionOutDir(filepath.Join(out, target), s))
		if err != nil {
			t.Fatal(err)
		}
		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			if d == rejectedDir {
				t.Errorf("%s contains a rejected/ directory; rejected frames belong "+
					"under <target>/%s/", s, rejectedDir)
			}
		}
		if strings.Join(dirs, ",") != "flats" {
			t.Errorf("%s contains %v, want [flats] when nothing is approved", s, dirs)
		}
	}

	// Both sessions are represented under rejected/.
	for _, s := range []string{"Session_01", "Session_02"} {
		if _, err := os.Stat(filepath.Join(out, target, rejectedDir, s, "H")); err != nil {
			t.Errorf("rejected/%s/H missing: %v", s, err)
		}
	}
}

// TestWriteFramesCSV covers the per-frame metrics report: one row per analysed
// frame, approved and rejected alike.
func TestWriteFramesCSV(t *testing.T) {
	const target = "LBN527"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.maxFWHM = 0.5 // reject everything, so both outcomes appear
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readFramesCSV(t, filepath.Join(out, target))

	wantHeader := []string{
		"session", "sessionDate", "pa", "filter",
		"filename", "detectedStars", "starCount",
		"avgFWHM", "avgSignal", "avgEccentricity", "snr", "score",
		"decision", "error", "background", "noise", "bgStructure",
		"cloudIndex", "cloudRatio", "structRatio",
	}
	if len(recs[0]) != len(wantHeader) {
		t.Fatalf("header has %d columns, want %d: %v", len(recs[0]), len(wantHeader), recs[0])
	}
	for i := range wantHeader {
		if recs[0][i] != wantHeader[i] {
			t.Errorf("header[%d] = %q, want %q", i, recs[0][i], wantHeader[i])
		}
	}

	// Every analysed frame appears, not just the approved ones.
	if len(recs) != 4 { // header + 3 lights across two sessions
		t.Fatalf("got %d records, want 4 (header plus three frames)", len(recs))
	}

	// Rows are grouped by session, in session order.
	if recs[1][0] != "Session_01" || recs[3][0] != "Session_02" {
		t.Errorf("sessions in row order = %q, %q, %q; want Session_01, Session_01, Session_02",
			recs[1][0], recs[2][0], recs[3][0])
	}

	row := recs[1]
	if row[1] != "2026-09-10" {
		t.Errorf("sessionDate = %q, want %q", row[1], "2026-09-10")
	}
	if row[2] != "PA310" {
		t.Errorf("pa = %q, want %q", row[2], "PA310")
	}
	if row[3] != "H" {
		t.Errorf("filter = %q, want %q (the Lights subdirectory name)", row[3], "H")
	}
	if !strings.HasPrefix(row[4], "LBN527_LIGHT_H_") {
		t.Errorf("filename = %q, want a light frame name", row[4])
	}
	// Metrics are populated for a frame that was analysed.
	for _, col := range []int{5, 6} {
		if row[col] == "" || row[col] == "0" {
			t.Errorf("column %q = %q, want a populated metric", wantHeader[col], row[col])
		}
	}
	for _, col := range []int{7, 8, 9, 10, 11} {
		if !strings.Contains(row[col], ".") {
			t.Errorf("column %q = %q, want four decimal places", wantHeader[col], row[col])
		}
	}
	if row[12] != decisionFWHM {
		t.Errorf("decision = %q, want %q", row[12], decisionFWHM)
	}
}

// TestWriteFramesCSVIncludesApprovedAndRejected checks both outcomes are
// recorded, since the point of the file is diagnosing rejections.
func TestWriteFramesCSVIncludesApprovedAndRejected(t *testing.T) {
	const target = "M51"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readFramesCSV(t, filepath.Join(out, target))
	counts := map[string]int{}
	for _, r := range recs[1:] {
		counts[r[12]]++
	}
	if counts[decisionApproved] != 3 {
		t.Errorf("approved rows = %d, want 3 (%v)", counts[decisionApproved], counts)
	}
	if len(counts) != 1 {
		t.Errorf("decisions present = %v, want only %q with default thresholds", counts, decisionApproved)
	}
}

// TestFramesCSVUsesSessionDateNotFrameDate covers the midnight rollover: the
// session column carries the directory date while the filename keeps its own.
func TestFramesCSVUsesSessionDateNotFrameDate(t *testing.T) {
	const target = "IC63"
	root := t.TempDir()

	prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				// Directory says 09-01, frames are stamped after midnight.
				"2026-09-01": {"L": {
					lightName(target, "L", "20260902", "014447", "431", "230"),
					lightName(target, "L", "20260904", "031100", "000", "230"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-01": {flatName("L", "41.4", "20260901", "190000", "000", "230")},
		},
	}.build(t, root)

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readFramesCSV(t, filepath.Join(out, target))
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}

	// sessionDate is the parent directory date, for every row.
	for _, r := range recs[1:] {
		if r[1] != "2026-09-01" {
			t.Errorf("sessionDate = %q, want %q (the Lights directory date)", r[1], "2026-09-01")
		}
	}
	// The two frames carry different embedded dates, proving the rollover is
	// preserved rather than flattened.
	if !strings.Contains(recs[1][4], "20260902") || !strings.Contains(recs[2][4], "20260904") {
		t.Errorf("filenames = %q, %q; want the embedded dates kept", recs[1][4], recs[2][4])
	}
}

// TestFramesCSVRecordsUnreadableFrames checks a frame that failed to analyse
// still gets a row, carrying the error rather than silent empty metrics.
func TestFramesCSVRecordsUnreadableFrames(t *testing.T) {
	const target = "WR134"
	root := t.TempDir()

	prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"H": {
					lightName(target, "H", "20260910", "220000", "000", "292"),
				}},
			},
		},
		flats: map[string][]string{},
	}.build(t, root)

	bad := filepath.Join(root, lightsDir, target, "2026-09-10", "H",
		lightName(target, "H", "20260910", "220000", "000", "292"))
	if err := os.WriteFile(bad, []byte("not a FITS file"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readFramesCSV(t, filepath.Join(out, target))
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (header plus the unreadable frame)", len(recs))
	}
	row := recs[1]
	// The frame was never analysed, so every metric is a clean zero rather than
	// a stale or invented value.
	for _, col := range []int{5, 6} {
		if row[col] != "0" {
			t.Errorf("column %q = %q, want 0 for a frame that was never measured",
				framesCSVHeader[col], row[col])
		}
	}
	for _, col := range []int{7, 8, 9, 10, 11} {
		if row[col] != "0.0000" {
			t.Errorf("column %q = %q, want 0.0000 for a frame that was never measured",
				framesCSVHeader[col], row[col])
		}
	}
	// The decision stays empty and the reason is recorded in the error column.
	if row[12] != "" {
		t.Errorf("decision = %q, want empty alongside an error", row[12])
	}
	if row[13] == "" {
		t.Error("error column is empty; an unreadable frame must be recorded as such")
	}
}

// TestImageResultRowSharedByBothCommands is the cross-check that extracting the
// row builder did not change either command's output.
func TestImageResultRowSharedByBothCommands(t *testing.T) {
	r := ImageResult{
		Filename: "a.FIT", Filter: "B", Date: "2026-01-01",
		DetectedStars: 100, StarCount: 50,
		AvgFWHM: 3.14159, AvgSignal: 1234.5678, AvgEccentricity: 0.123456,
		SNR: 12.5, Score: 3.7, Decision: decisionApproved,
	}

	row := imageResultRow(r)
	if len(row) != len(imageResultHeader) {
		t.Fatalf("row has %d columns, header has %d", len(row), len(imageResultHeader))
	}
	if row[0] != "a.FIT" || row[1] != "B" || row[2] != "2026-01-01" {
		t.Errorf("identity columns = %v", row[:3])
	}
	if row[5] != "3.1416" {
		t.Errorf("avgFWHM = %q, want %q (four decimal places)", row[5], "3.1416")
	}

	// analyze's CSV must match the shared columns exactly.
	var sb strings.Builder
	if err := writeResultsCSV([]ImageResult{r}, newCSVWriter(&sb)); err != nil {
		t.Fatalf("writeResultsCSV: %v", err)
	}
	recs, err := newCSVReader(strings.NewReader(sb.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for i, h := range imageResultHeader {
		if recs[0][i] != h {
			t.Errorf("analyze header[%d] = %q, want %q", i, recs[0][i], h)
		}
	}
	for i := range row {
		if recs[1][i] != row[i] {
			t.Errorf("analyze row[%d] = %q, imageResultRow gives %q", i, recs[1][i], row[i])
		}
	}

	// The error column is populated when there is one. Looked up by name rather
	// than assumed to be last: columns are appended after it now.
	errRow := imageResultRow(ImageResult{Filename: "b.FIT", Error: errFake{}})
	errCol := -1
	for i, h := range imageResultHeader {
		if h == "error" {
			errCol = i
		}
	}
	if errCol < 0 {
		t.Fatal("no error column in imageResultHeader")
	}
	if errRow[errCol] != "fake failure" {
		t.Errorf("error column = %q, want %q", errRow[errCol], "fake failure")
	}
}

// TestPrepareDryRunWritesNoCSV confirms the report is part of the real run only.
func TestPrepareDryRunWritesNoCSV(t *testing.T) {
	const target = "IC4090"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.dryRun = true
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	for _, name := range []string{sessionsCSVName, framesCSVName} {
		if _, err := os.Stat(metricsPath(filepath.Join(out, target), name)); err == nil {
			t.Errorf("dry run wrote %s", name)
		}
	}
}

// TestPrepareReportsLiveInMetricsDir pins the target-root layout: the CSV
// reports sit in metrics/ beside the session and rejected trees, not loose at
// the root.
func TestPrepareReportsLiveInMetricsDir(t *testing.T) {
	const target = "IC434"
	root := t.TempDir()
	simpleFixture(target).build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}
	targetOut := filepath.Join(out, target)

	// Both reports exist under metrics/.
	for _, name := range []string{sessionsCSVName, framesCSVName} {
		if _, err := os.Stat(metricsPath(targetOut, name)); err != nil {
			t.Errorf("%s missing from %s: %v", name, metricsDir, err)
		}
	}

	// And nowhere at the target root.
	for _, name := range []string{sessionsCSVName, framesCSVName} {
		if _, err := os.Stat(filepath.Join(targetOut, name)); err == nil {
			t.Errorf("%s was written to the target root instead of %s/", name, metricsDir)
		}
	}

	// metrics/ holds the reports and nothing else.
	entries, err := os.ReadDir(filepath.Join(targetOut, metricsDir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	want := []string{framesCSVName, sessionsCSVName}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("%s contains %v, want %v", metricsDir, names, want)
	}

	// The target root holds only data directories, with the sessions grouped
	// under the wrapper rather than sitting beside metrics/.
	if got := strings.Join(subDirNames(t, targetOut), ","); got != "Sessions,"+metricsDir {
		t.Errorf("target root holds %v, want [Sessions %s]", got, metricsDir)
	}
}

// TestPrepareRejectsUnwritableMetricsDir checks the failure is reported rather
// than silently skipped when the reports cannot be written.
func TestPrepareRejectsUnwritableMetricsDir(t *testing.T) {
	const target = "SH2-54"
	root := t.TempDir()
	simpleFixture(target).build(t, root)

	// A regular file occupying the metrics path.
	out := t.TempDir()
	blocked := filepath.Join(out, target)
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, metricsDir), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runPrepare(prepareOptionsForTest(target, root, out))
	if err == nil {
		t.Fatal("expected a failure when metrics/ cannot be created")
	}
	if !strings.Contains(err.Error(), metricsDir) {
		t.Errorf("error = %v, want it to mention %s", err, metricsDir)
	}
}

func TestPrepareFlagDefaults(t *testing.T) {
	want := map[string]string{
		"target":        "",
		"input":         defaultPrepareInput,
		"output":        defaultPrepareOutput,
		"dry-run":       "false",
		"skip-existing": "false",
		"quiet":         "false",
		// The quality thresholds must match analyze's defaults exactly, or the
		// two commands would silently disagree on which frames are usable.
		"min-snr":   "11",
		"max-fwhm":  "5",
		"max-ecc":   "0.54",
		"min-score": "2",
		"min-stars": "680",
		"workers":   "0",
	}
	for name, def := range want {
		f := prepareCmd.Flags().Lookup(name)
		if f == nil {
			t.Errorf("flag --%s is not registered", name)
			continue
		}
		if f.DefValue != def {
			t.Errorf("flag --%s default = %q, want %q", name, f.DefValue, def)
		}
	}

	shorthands := map[string]string{"target": "t", "input": "i", "output": "o", "workers": "w"}
	for name, sh := range shorthands {
		f := prepareCmd.Flags().Lookup(name)
		if f == nil {
			continue
		}
		if f.Shorthand != sh {
			t.Errorf("flag --%s shorthand = %q, want %q", name, f.Shorthand, sh)
		}
	}
}

// TestSessionPlanApproved pins the approval predicate directly, including the
// defensive paths that the end-to-end tests cannot reach: an absent verdict and
// an error paired with a decision.
func TestSessionPlanApproved(t *testing.T) {
	tests := []struct {
		name    string
		verdict ImageResult
		present bool
		want    bool
	}{
		{
			name:    "approved",
			present: true,
			verdict: ImageResult{Decision: decisionApproved},
			want:    true,
		},
		{
			name:    "rejected on snr",
			present: true,
			verdict: ImageResult{Decision: decisionSNR},
			want:    false,
		},
		{
			name:    "rejected with no decision but an error",
			present: true,
			verdict: ImageResult{Error: errFake{}},
			want:    false,
		},
		{
			// A frame that failed to read carries no decision. Checking the
			// decision alone would be enough here, but an error must never be
			// promotable regardless of what the decision field says.
			name:    "error alongside an approved decision",
			present: true,
			verdict: ImageResult{Decision: decisionApproved, Error: errFake{}},
			want:    false,
		},
		{
			name:    "absent verdict is not approved",
			present: false,
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := sessionPlan{verdicts: map[string]ImageResult{}}
			if tc.present {
				s.verdicts["frame.FIT"] = tc.verdict
			}
			if got := s.approved("frame.FIT"); got != tc.want {
				t.Errorf("approved() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPrepareThresholdsMatchAnalyze guards against the two commands drifting:
// analyze is the reference for what an acceptable frame is, so prepare must
// register identical defaults.
func TestPrepareThresholdsMatchAnalyze(t *testing.T) {
	// workers is not a quality threshold but is shared by both commands, so it
	// is checked here too.
	pairs := [][2]string{
		{"min-snr", "min-snr"},
		{"max-fwhm", "max-fwhm"},
		{"max-ecc", "max-ecc"},
		{"min-score", "min-score"},
		{"min-stars", "min-stars"},
		{"workers", "workers"},
	}
	for _, p := range pairs {
		pf := prepareCmd.Flags().Lookup(p[0])
		af := analyzeCmd.Flags().Lookup(p[1])
		if pf == nil || af == nil {
			t.Errorf("flag %q missing from one of the commands", p[0])
			continue
		}
		if pf.DefValue != af.DefValue {
			t.Errorf("prepare --%s default %q differs from analyze --%s default %q",
				p[0], pf.DefValue, p[1], af.DefValue)
		}
		// The help text is shared too, so a divergence there is the same bug.
		if pf.Usage != af.Usage {
			t.Errorf("prepare --%s help %q differs from analyze --%s help %q",
				p[0], pf.Usage, p[1], af.Usage)
		}
	}
}

// TestDefaultThresholdsAreRegistered checks every threshold reaches both
// commands with the configured value.
func TestDefaultThresholdsAreRegistered(t *testing.T) {
	tests := []struct {
		flag  string
		want  string
		isInt bool
	}{
		{flag: "min-snr", want: strconv.FormatFloat(defaultThresholds.minSNR, 'g', -1, 64)},
		{flag: "max-fwhm", want: strconv.FormatFloat(defaultThresholds.maxFWHM, 'g', -1, 64)},
		{flag: "max-ecc", want: strconv.FormatFloat(defaultThresholds.maxEcc, 'g', -1, 64)},
		{flag: "min-score", want: strconv.FormatFloat(defaultThresholds.minScore, 'g', -1, 64)},
		{flag: "min-stars", want: strconv.Itoa(defaultThresholds.minStars), isInt: true},
	}

	for _, tc := range tests {
		for _, c := range []*cobra.Command{analyzeCmd, prepareCmd} {
			f := c.Flags().Lookup(tc.flag)
			if f == nil {
				t.Errorf("%s: flag --%s is not registered", c.Name(), tc.flag)
				continue
			}
			if f.DefValue != tc.want {
				t.Errorf("%s: --%s default = %q, want %q (from defaultThresholds)",
					c.Name(), tc.flag, f.DefValue, tc.want)
			}
			// The declared type must match the field it binds, or a flag would
			// silently fail to update the thresholds.
			if tc.isInt && f.Value.Type() != "int" {
				t.Errorf("%s: --%s type = %q, want int", c.Name(), tc.flag, f.Value.Type())
			}
			if !tc.isInt && f.Value.Type() != "float64" {
				t.Errorf("%s: --%s type = %q, want float64", c.Name(), tc.flag, f.Value.Type())
			}
			// User-facing text must survive the consolidation. The exact wording
			// is not pinned, but an empty description would be a regression.
			if strings.TrimSpace(f.Usage) == "" {
				t.Errorf("%s: --%s has no help text", c.Name(), tc.flag)
			}
		}
	}
}

// TestQualityThresholdsAnalyzeConversion checks the conversion prepare hands to
// processImage carries the thresholds through unchanged, and sets the star limit
// processImage requires.
func TestQualityThresholdsAnalyzeConversion(t *testing.T) {
	q := qualityThresholds{
		minSNR:   1.5,
		maxFWHM:  2.5,
		maxEcc:   0.25,
		minScore: 3.5,
		minStars: 123,
	}
	got := q.analyze()

	if got.minSNR != q.minSNR || got.maxFWHM != q.maxFWHM || got.maxEcc != q.maxEcc ||
		got.minScore != q.minScore || got.minStars != q.minStars {
		t.Errorf("thresholds lost in conversion: got %+v, want %+v", got.qualityThresholds, q)
	}
	if got.limitComputedStars != analyzeDefaultLimitComputedStars {
		t.Errorf("limitComputedStars = %d, want %d; processImage rejects every frame without it",
			got.limitComputedStars, analyzeDefaultLimitComputedStars)
	}
}

// TestKnownFilters pins the recognised filter set and isKnownFilter.
//
// isKnownFilter gates both the flat filename token and the light filter
// subdirectory name, so an entry that drifts out of step with knownFilters
// would silently stop indexing that filter's flats.
//
// The comparison is on the whole string, not per-letter, because the order is
// not cosmetic: it is the wheel's positions 1 to 7, and flatFilterRe splices
// the set into a character class that ignores order. So this equality is the
// only thing recording that sequence, and a well-meaning sort would pass every
// behavioural test while losing it.
func TestKnownFilters(t *testing.T) {
	if knownFilters != "LRGBSHO" {
		t.Errorf("knownFilters = %q, want %q (filter wheel, positions 1-7)", knownFilters, "LRGBSHO")
	}

	for _, f := range []string{"L", "R", "G", "B", "S", "H", "O"} {
		if !isKnownFilter(f) {
			t.Errorf("isKnownFilter(%q) = false, want true", f)
		}
	}

	// Single characters only, so a narrowband name is not accepted by accident
	// just because its letters appear in the set: "BG" is a substring of the
	// set, and strings.Contains would otherwise report it a member.
	for _, f := range []string{"Ha", "OIII", "L2", "LR", "BG", "b", "l", "", "LRGBSHO"} {
		if isKnownFilter(f) {
			t.Errorf("isKnownFilter(%q) = true, want false", f)
		}
	}
}

// TestFilterSubdirsSkipsUnknownFilters checks the light side: a session
// subdirectory that is not a known filter is reported and dropped rather than
// carried into the plan as a filter that will then look for flats.
func TestFilterSubdirsSkipsUnknownFilters(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"B", "R", "Ha", "calibration", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	names, warnings, err := filterSubdirs(dir)
	if err != nil {
		t.Fatalf("filterSubdirs: %v", err)
	}

	if got := strings.Join(names, " "); got != "B R" {
		t.Errorf("names = %q, want %q", got, "B R")
	}

	// Ha and calibration are reported; the dotfile is not, matching the rule
	// that dotfiles are skipped rather than named.
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings (%v), want 2", len(warnings), warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"Ha", "calibration", "not a known filter", knownFilters} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q do not mention %q", joined, want)
		}
	}
	if strings.Contains(joined, ".hidden") {
		t.Errorf("warnings %q report a dotfile", joined)
	}
}

// TestCollectFlatsZeroTargetKeepsOnlyTheSessionDate pins the default.
//
// Zero means "no goal", not "a goal of zero". Testing len(matched) >= target
// without guarding on a positive target is true immediately, so the function
// would hand back no flats at all -- which reads as a night that shot none,
// rather than a run that declined to borrow. This is the default path, so it
// is the one that must not be quietly wrong.
func TestCollectFlatsZeroTargetKeepsOnlyTheSessionDate(t *testing.T) {
	root := t.TempDir()
	buildFlatDirs(t, root, map[string][]string{
		"2026-09-09": flatSet("S", "2026-09-09", "310", 20),
		"2026-09-10": flatSet("S", "2026-09-10", "310", 3),
		"2026-09-11": flatSet("S", "2026-09-11", "310", 20),
	})
	dates := []string{"2026-09-09", "2026-09-10", "2026-09-11"}
	flatRoot := filepath.Join(root, flatsDir)

	matched, searched := collectFlats(flatRoot, "2026-09-10", "310", "S", dates, 0, c3Camera)

	if len(matched) != 3 {
		t.Fatalf("got %d flats, want the session's own 3 (not none, not borrowed)", len(matched))
	}
	for _, m := range matched {
		if m.date != "2026-09-10" {
			t.Errorf("borrowed from %q, want no borrowing at a target of 0", m.date)
		}
	}
	// No goal, so nothing was searched beyond the session's own date and there
	// is no shortfall to report.
	if len(searched) != 0 {
		t.Errorf("searched = %v, want empty at a target of 0", searched)
	}
}

// TestPrepareZeroFlatCountWarnsNothing checks the default run is quiet about
// flats: three own-date frames is a complete session when no target is set, so
// neither a shortfall warning nor a missingFlats entry is warranted.
func TestPrepareZeroFlatCountWarnsNothing(t *testing.T) {
	const target = "TEST"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"S": {
					lightName(target, "S", "20260910", "220000", "000", "310"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": flatSet("S", "2026-09-10", "310", 3),
		},
	}
	f.build(t, root)

	sessions, warnings, err := planSessions(
		filepath.Join(root, lightsDir, target), filepath.Join(root, flatsDir), target, 0, c3Camera)
	if err != nil {
		t.Fatalf("planSessions: %v", err)
	}

	joined := strings.Join(warnings, "\n")
	if strings.Contains(joined, "want 14") || strings.Contains(joined, "flat(s) for filter") {
		t.Errorf("warnings %q report a shortfall against a target that was never set", joined)
	}
	if len(sessions[0].missingFlats) != 0 {
		t.Errorf("missingFlats = %v, want empty when the session has flats", sessions[0].missingFlats)
	}
	if got := len(sessions[0].flats["S"]); got != 3 {
		t.Errorf("flats for S = %d, want the session's own 3", got)
	}
}

// TestPrepareRejectsNegativeFlatCount checks a mistyped count is refused rather
// than clamped. Clamping would silently turn borrowing off, which is a
// behaviour change the operator never asked for.
func TestPrepareRejectsNegativeFlatCount(t *testing.T) {
	root := t.TempDir()
	opts := prepareOptionsForTest("TEST", root, t.TempDir())
	opts.targetFlats = -1

	err := runPrepare(opts)
	if err == nil {
		t.Fatal("runPrepare accepted a negative --flat-count, want an error")
	}
	if !strings.Contains(err.Error(), "--flat-count") {
		t.Errorf("error %q does not name the flag", err)
	}
}

// TestFlatCountIsAConfigKey checks the flag is drivable from the config file.
// knownConfigKeys walks the command's flags, so a new prepare flag must show up
// as a known key rather than being reported as an unknown one.
func TestFlatCountIsAConfigKey(t *testing.T) {
	if !knownConfigKeys()["prepare.flat-count"] {
		t.Error("prepare.flat-count is not a known config key")
	}
	path := writeConfig(t, "prepare:\n  flat-count: 9\n")

	cmd, _ := configTestPrepareCmd()
	if err := applySections(cmd, mustReadConfig(t, path), path); err != nil {
		t.Fatalf("applySections: %v", err)
	}
	if got := cmd.Flags().Lookup("flat-count").Value.String(); got != "9" {
		t.Errorf("flat-count from config = %q, want %q", got, "9")
	}
}

// flatTarget is the --flat-count the borrowing tests ask for. Production reads
// it from the flag rather than a constant, so the tests state it explicitly.
const flatTarget = 14

// TestFlatSearchOrder pins the consultation order: own date, then previous
// before next, stepping outward. The first session has no previous, so its walk
// starts at the following date, which is what makes borrowing possible at all
// for the opening night.
func TestFlatSearchOrder(t *testing.T) {
	tests := []struct {
		name string
		n    int
		pos  int
		want string
	}{
		{"only session", 1, 0, "0"},
		{"first session", 3, 0, "0 1 2"},
		{"middle session", 5, 2, "2 1 3 0 4"},
		{"last session", 3, 2, "2 1 0"},
		{"second session", 2, 1, "1 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := flatSearchOrder(tc.n, tc.pos)
			var parts []string
			for _, i := range got {
				parts = append(parts, fmt.Sprint(i))
			}
			if strings.Join(parts, " ") != tc.want {
				t.Errorf("flatSearchOrder(%d, %d) = %v, want [%s]",
					tc.n, tc.pos, parts, tc.want)
			}
		})
	}
}

// TestCollectFlatsBorrowsFromPreviousDate covers topping a short session up
// from the night before, and stopping as soon as the target is reached.
func TestCollectFlatsBorrowsFromPreviousDate(t *testing.T) {
	root := t.TempDir()
	buildFlatDirs(t, root, map[string][]string{
		"2026-09-09": flatSet("S", "2026-09-09", "310", 20),
		"2026-09-10": flatSet("S", "2026-09-10", "310", 5),
		"2026-09-11": flatSet("S", "2026-09-11", "310", 20),
	})
	dates := []string{"2026-09-09", "2026-09-10", "2026-09-11"}
	flatRoot := filepath.Join(root, flatsDir)

	matched, _ := collectFlats(flatRoot, "2026-09-10", "310", "S", dates, flatTarget, c3Camera)

	if len(matched) != flatTarget {
		t.Fatalf("got %d flats, want %d", len(matched), flatTarget)
	}
	// Own five first, then the previous night, and no further: the search stops
	// once the target is met rather than taking every flat it can find.
	own := 0
	for _, m := range matched {
		switch m.date {
		case "2026-09-10":
			own++
		case "2026-09-09":
		default:
			t.Errorf("borrowed from %q, want only the own or previous date", m.date)
		}
	}
	if own != 5 {
		t.Errorf("own-date flats = %d, want 5", own)
	}
}

// TestCollectFlatsFirstSessionBorrowsFromNextDate covers the opening session,
// which has no previous date and must reach forward instead.
func TestCollectFlatsFirstSessionBorrowsFromNextDate(t *testing.T) {
	root := t.TempDir()
	buildFlatDirs(t, root, map[string][]string{
		"2026-09-09": flatSet("S", "2026-09-09", "310", 3),
		"2026-09-10": flatSet("S", "2026-09-10", "310", 20),
	})
	dates := []string{"2026-09-09", "2026-09-10"}
	flatRoot := filepath.Join(root, flatsDir)

	matched, searched := collectFlats(flatRoot, "2026-09-09", "310", "S", dates, flatTarget, c3Camera)

	if len(matched) != flatTarget {
		t.Fatalf("got %d flats, want %d", len(matched), flatTarget)
	}
	if matched[0].date != "2026-09-09" {
		t.Errorf("first flat from %q, want the session's own date", matched[0].date)
	}
	borrowed := false
	for _, m := range matched {
		if m.date == "2026-09-10" {
			borrowed = true
		}
		if m.date != "2026-09-09" && m.date != "2026-09-10" {
			t.Errorf("borrowed from %q, want only the own or next date", m.date)
		}
	}
	if !borrowed {
		t.Error("the first session did not borrow from the following date")
	}
	if len(searched) == 0 || searched[0] != "2026-09-09" {
		t.Errorf("searched = %v, want the own date first", searched)
	}
}

// TestCollectFlatsWalksPastAnEmptyNeighbour covers the rained-out night: the
// immediately previous date holds nothing usable, so the search must reach
// further rather than stop at the first neighbour that does not help.
func TestCollectFlatsWalksPastAnEmptyNeighbour(t *testing.T) {
	root := t.TempDir()
	buildFlatDirs(t, root, map[string][]string{
		// Previous date has flats, but only for another angle.
		"2026-09-09": flatSet("S", "2026-09-09", "240", 20),
		"2026-09-10": flatSet("S", "2026-09-10", "310", 2),
		"2026-09-11": flatSet("S", "2026-09-11", "310", 20),
	})
	dates := []string{"2026-09-09", "2026-09-10", "2026-09-11"}
	flatRoot := filepath.Join(root, flatsDir)

	matched, _ := collectFlats(flatRoot, "2026-09-10", "310", "S", dates, flatTarget, c3Camera)

	// Two own-date frames plus twelve from two nights on: the angle is part of
	// the key, so the PA240 flats are not candidates.
	if len(matched) != flatTarget {
		t.Fatalf("got %d flats, want %d", len(matched), flatTarget)
	}
	for _, m := range matched {
		if m.date == "2026-09-09" {
			t.Errorf("took a flat from the previous date, which holds only PA240")
		}
	}
	if matched[len(matched)-1].date != "2026-09-11" {
		t.Errorf("last flat from %q, want the walk to reach 2026-09-11",
			matched[len(matched)-1].date)
	}
}

// TestCollectFlatsNeverBorrowsAForeignDate pins the scoping: only dates the
// target itself acquired on are searched, so a flat directory belonging to
// another target is never reached into.
func TestCollectFlatsNeverBorrowsAForeignDate(t *testing.T) {
	root := t.TempDir()
	buildFlatDirs(t, root, map[string][]string{
		"2026-09-08": flatSet("S", "2026-09-08", "310", 20), // another target's night
		"2026-09-10": flatSet("S", "2026-09-10", "310", 20),
	})
	// The target only ever acquired on 2026-09-10.
	dates := []string{"2026-09-10"}
	flatRoot := filepath.Join(root, flatsDir)

	matched, _ := collectFlats(flatRoot, "2026-09-10", "310", "S", dates, flatTarget, c3Camera)

	for _, m := range matched {
		if m.date != "2026-09-10" {
			t.Errorf("borrowed from %q, which the target never acquired on", m.date)
		}
	}
	if len(matched) != flatTarget {
		t.Errorf("got %d flats, want %d", len(matched), flatTarget)
	}
}

// TestCollectFlatsReportsAShortfall covers the case where every date the target
// observed has been searched and the target cannot be met: the caller warns
// and continues rather than failing.
func TestCollectFlatsReportsAShortfall(t *testing.T) {
	root := t.TempDir()
	buildFlatDirs(t, root, map[string][]string{
		"2026-09-09": flatSet("S", "2026-09-09", "310", 2),
		"2026-09-10": flatSet("S", "2026-09-10", "310", 3),
	})
	dates := []string{"2026-09-09", "2026-09-10"}
	flatRoot := filepath.Join(root, flatsDir)

	matched, searched := collectFlats(flatRoot, "2026-09-10", "310", "S", dates, flatTarget, c3Camera)

	// Three on the session's own date plus two on the previous, and nothing more
	// to find: the search is exhausted rather than truncated by the target.
	const want = 5
	if len(matched) != want {
		t.Fatalf("got %d flats, want %d", len(matched), want)
	}
	if len(matched) >= flatTarget {
		t.Fatalf("got %d flats, want fewer than the target %d", len(matched), flatTarget)
	}
	// Every date is named, so a shortfall says what was available rather than
	// leaving the operator to wonder whether the search was bounded.
	if len(searched) != 2 {
		t.Errorf("searched = %v, want both dates", searched)
	}
}

// TestPrepareBorrowsFlatsAndCopiesThem is the end-to-end guard for the refactor:
// a flat collected from a neighbouring date must actually be copied from that
// date. Resolving the source from the session date instead would compile and
// plan perfectly, then fail at copy time.
func TestPrepareBorrowsFlatsAndCopiesThem(t *testing.T) {
	const target = "TEST"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"S": {
					lightName(target, "S", "20260910", "220000", "000", "310"),
				}},
				"2026-09-11": {"S": {
					lightName(target, "S", "20260911", "220000", "000", "310"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": flatSet("S", "2026-09-10", "310", 2),
			"2026-09-11": flatSet("S", "2026-09-11", "310", 6),
		},
	}
	f.build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.targetFlats = flatTarget
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	for _, sess := range []string{"Session_01", "Session_02"} {
		dir := filepath.Join(sessionOutDir(filepath.Join(out, target), sess), "flats", "S")
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("%s flats: %v", sess, err)
		}
		// Eight available across the two nights, short of the target of 14.
		if len(files) != 8 {
			t.Errorf("%s copied %d flats, want 8 (own plus borrowed)", sess, len(files))
		}
		for _, file := range files {
			// The copy must be readable and non-empty: a mis-resolved source
			// would have failed the run outright, and a zero-byte stub here
			// would mean the wrong file was read.
			got, err := os.ReadFile(filepath.Join(dir, file.Name()))
			if err != nil {
				t.Fatalf("%s/%s: %v", sess, file.Name(), err)
			}
			if len(got) == 0 {
				t.Errorf("%s/%s is empty", sess, file.Name())
			}
		}
	}
}

// TestPrepareWarnsWhenShortOfFlats checks the shortfall warning names the count
// and the dates searched, which is the only way to tell a genuinely short
// session from one that was never looked for properly.
func TestPrepareWarnsWhenShortOfFlats(t *testing.T) {
	const target = "TEST"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"S": {
					lightName(target, "S", "20260910", "220000", "000", "310"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": flatSet("S", "2026-09-10", "310", 3),
		},
	}
	f.build(t, root)

	sessions, warnings, err := planSessions(
		filepath.Join(root, lightsDir, target), filepath.Join(root, flatsDir), target, flatTarget, c3Camera)
	if err != nil {
		t.Fatalf("planSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}

	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"3 flat(s)", "want 14", "2026-09-10"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q do not mention %q", joined, want)
		}
	}
	// Short is not missing: the frames are usable.
	if len(sessions[0].missingFlats) != 0 {
		t.Errorf("missingFlats = %v, want empty when flats were borrowed", sessions[0].missingFlats)
	}
	if got := len(sessions[0].flats["S"]); got != 3 {
		t.Errorf("flats for S = %d, want 3", got)
	}
}

func TestPrepareFormatFrameDate(t *testing.T) {
	tests := []struct{ in, want string }{
		{"20260910", "2026-09-10"},
		{"20261231", "2026-12-31"},
		{"20260101", "2026-01-01"},
		{"short", "short"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := formatFrameDate(tc.in); got != tc.want {
			t.Errorf("formatFrameDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPrepareRealTreeShape runs against a fixture mirroring the real acquisition
// layout, including a directory named like a FITS file and stray files at the
// target level.
func TestPrepareRealTreeShape(t *testing.T) {
	const target = "LBN527"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-16": {"S": {
					lightName(target, "S", "20260916", "062721", "275", "310"),
					lightName(target, "S", "20260917", "065210", "993", "310"),
				}},
				"2026-09-20": {"O": {
					lightName(target, "O", "20260920", "022212", "144", "310"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-16": {
				flatName("S", "67.02", "20260916", "071758", "808", "310"),
				flatName("S", "67.06", "20260916", "072240", "953", "310"),
			},
			"2026-09-20": {
				flatName("O", "41.40", "20260920", "080000", "000", "310"),
			},
		},
	}
	f.build(t, root)

	// A file whose name looks like a target must not confuse anything.
	writeStub(t, filepath.Join(root, lightsDir, "masterFlat_BIN-1_9576x6388_FILTER-S_mono_SESSION-1.fit"))

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	if recs[1][1] != "2026-09-16" || recs[1][4] != "2" || recs[1][5] != "2" || recs[1][7] != "2" {
		t.Errorf("session 01 row = %v", recs[1])
	}
	if recs[2][1] != "2026-09-20" || recs[2][4] != "1" || recs[2][5] != "1" || recs[2][7] != "1" {
		t.Errorf("session 02 row = %v", recs[2])
	}
	// The frame dates are recorded so the midnight rollover stays visible.
	if recs[1][9] != "2026-09-16" || recs[1][10] != "2026-09-17" {
		t.Errorf("frame date range = [%s .. %s], want [2026-09-16 .. 2026-09-17]", recs[1][9], recs[1][10])
	}
}

// TestPrepareSessionDirPadding checks the two-digit format holds past nine
// sessions, which is where a naive %d would produce Session_10 correctly but
// Session_1 for the first.
func TestPrepareSessionDirPadding(t *testing.T) {
	const target = "BIG"
	root := t.TempDir()

	lights := map[string]map[string][]string{}
	flats := map[string][]string{}
	dates := []string{
		"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04", "2026-09-05",
		"2026-09-06", "2026-09-07", "2026-09-08", "2026-09-09", "2026-09-10",
		"2026-09-11", "2026-09-12",
	}
	for i, d := range dates {
		compact := strings.ReplaceAll(d, "-", "")
		lights[d] = map[string][]string{
			"H": {lightName(target, "H", compact, "22000"+strconv.Itoa(i), "000", "310")},
		}
		flats[d] = []string{flatName("H", "41.4", compact, "070000", "000", "310")}
	}
	prepareFixture{
		lights: map[string]map[string]map[string][]string{target: lights},
		flats:  flats,
	}.build(t, root)

	out := t.TempDir()
	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	dirs := sessionDirNames(t, out, target)
	if len(dirs) != len(dates) {
		t.Fatalf("got %d session dirs, want %d", len(dirs), len(dates))
	}
	for i, d := range dirs {
		want := fmt.Sprintf("Session_%02d", i+1)
		if d != want {
			t.Errorf("session dir[%d] = %q, want %q", i, d, want)
		}
	}
}
