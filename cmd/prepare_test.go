package cmd

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/astrogo/fitsio"
)

// prepareFixture describes a synthetic acquisition tree.
type prepareFixture struct {
	// lights maps target -> date -> filter -> filenames.
	lights map[string]map[string]map[string][]string
	// flats maps date -> filenames.
	flats map[string][]string
}

// build writes the fixture under root and returns it.
func (f prepareFixture) build(t *testing.T, root string) {
	t.Helper()

	for target, dates := range f.lights {
		for date, filters := range dates {
			for filter, names := range filters {
				dir := filepath.Join(root, lightsDir, target, date, filter)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", dir, err)
				}
				for _, n := range names {
					writeFixtureFrame(t, dir, n)
				}
			}
		}
	}
	for date, names := range f.flats {
		dir := filepath.Join(root, flatsDir, date)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		for _, n := range names {
			writeStub(t, filepath.Join(dir, n))
		}
	}
}

// writeStub creates a small file standing in for a 120 MB frame. Contents are
// unique per name so a copy that mixed up sources would be detectable.
func writeStub(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeFixtureFrame writes a real FITS frame when the name looks like a science
// light, so the quality pass can actually read it, and a stub otherwise.
//
// Light frames carry a dense grid of Gaussian stars so processImage approves
// them under the default thresholds. Flat frames are never analysed, so they
// stay cheap stubs.
func writeFixtureFrame(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if !strings.Contains(name, "_LIGHT_") {
		writeStub(t, path)
		return path
	}
	writeStarFrame(t, path, 900, 900, 1000, 5, 3000, 1.5, 25, 11)
	return path
}

// writeStarFrame stamps a synthetic sky of Gaussian stars into a real FITS file.
//
// The dimensions matter: minStars defaults to 680, so a field must detect more
// than that to be approved. At a 25px spacing a 900x900 frame yields roughly
// 1200 detections, comfortably over the bar, while still analysing in a few
// milliseconds.
func writeStarFrame(t *testing.T, path string, w, h int, level, noise, amp, sigma float64, spacing int, seed int64) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	rng := rand.New(rand.NewSource(seed))
	pix := make([]float32, w*h)
	for i := range pix {
		pix[i] = float32(level + rng.NormFloat64()*noise)
	}
	for cy := spacing + 5; cy < h-spacing-5; cy += spacing {
		for cx := spacing + 5; cx < w-spacing-5; cx += spacing {
			for dy := -8; dy <= 8; dy++ {
				for dx := -8; dx <= 8; dx++ {
					r2 := float64(dx*dx+dy*dy) / (2 * sigma * sigma)
					pix[(cy+dy)*w+cx+dx] += float32(amp * math.Exp(-r2))
				}
			}
		}
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()

	tf, err := fitsio.Create(f)
	if err != nil {
		t.Fatalf("fitsio.Create: %v", err)
	}
	defer tf.Close()

	img := fitsio.NewImage(-32, []int{w, h})
	if err := img.Write(&pix); err != nil {
		t.Fatalf("write pixels to %s: %v", path, err)
	}
	if err := tf.Write(img); err != nil {
		t.Fatalf("write HDU: %v", err)
	}
}

// lightName builds a science frame filename in the acquisition convention.
func lightName(target, filter, date, time, ms, pa string) string {
	return fmt.Sprintf("%s_LIGHT_%s_300s_BIN1_-10C_GA0_%s_%s_%s_PA%s_E.FIT",
		target, filter, date, time, ms, pa)
}

// flatName builds a flat frame filename.
func flatName(filter, dur, date, time, ms, pa string) string {
	return fmt.Sprintf("FLAT_%s_%ss_BIN1_-10C_GA2750_%s_%s_%s_PA%s.FIT",
		filter, dur, date, time, ms, pa)
}

// simpleFixture is two nights, one filter, matching flats.
func simpleFixture(target string) prepareFixture {
	return prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"H": {
					lightName(target, "H", "20260910", "220000", "000", "310"),
					lightName(target, "H", "20260910", "223000", "000", "310"),
				}},
				"2026-09-11": {"H": {
					lightName(target, "H", "20260911", "220000", "000", "310"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {flatName("H", "41.4", "20260910", "070000", "000", "310")},
			"2026-09-11": {flatName("H", "41.5", "20260911", "070000", "000", "310")},
		},
	}
}

// prepareOptionsForTest returns options carrying analyze's default thresholds,
// so the synthetic frames in these fixtures are approved unless a test
// deliberately moves them.
func prepareOptionsForTest(target, root, out string) prepareOptions {
	return prepareOptions{
		target: target, input: root, output: out, quiet: true,
		minSNR: 11, maxFWHM: 5, maxEcc: 0.54, minScore: 2, minStars: 680,
	}
}

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
		want := fmt.Sprintf("Session-%02d", i+1)
		dir := filepath.Join(out, target, want, sessionLightsDir, "H")
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
	entries, err := os.ReadDir(filepath.Join(out, target))
	if err != nil {
		t.Fatal(err)
	}
	var sessions []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != metricsDir {
			sessions = append(sessions, e.Name())
		}
	}
	sort.Strings(sessions)
	want := []string{"Session-01", "Session-02"}
	if strings.Join(sessions, ",") != strings.Join(want, ",") {
		t.Errorf("session dirs = %v, want %v", sessions, want)
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

	dir := filepath.Join(out, target, "Session-01", "flats", "H")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read flats dir: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d flats, want 1", len(files))
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
	got, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("flat contents = %q, want %q", got, want)
	}
}

// TestPrepareFiltersFlatsByAngle is the load-bearing case: Flats/<date> can
// hold frames at several parallactic angles, and only the one matching the
// session's lights is valid calibration.
func TestPrepareFiltersFlatsByAngle(t *testing.T) {
	const target = "SH2-101"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-21": {"O": {
					lightName(target, "O", "20260921", "220000", "000", "240"),
				}},
			},
		},
		flats: map[string][]string{
			// Two angles on the same night; only PA240 matches.
			"2026-09-21": {
				flatName("O", "41.4", "20260921", "070000", "000", "240"),
				flatName("O", "41.5", "20260921", "070100", "000", "196"),
				flatName("O", "41.6", "20260921", "070200", "000", "310"),
				// Right filter, right angle not required: a different filter
				// at the matching angle must still be excluded.
				flatName("H", "41.7", "20260921", "070300", "000", "240"),
			},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	dir := filepath.Join(out, target, "Session-01", "flats", "O")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read flats: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d flats, want 1 (only the PA240 O frame)", len(files))
	}
	if !strings.Contains(files[0].Name(), "PA240") {
		t.Errorf("flat = %q, want the PA240 frame", files[0].Name())
	}
	// The other filter's directory must not exist.
	if _, err := os.Stat(filepath.Join(out, target, "Session-01", "flats", "H")); err == nil {
		t.Error("flats/H was created but no H lights exist")
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

	base := filepath.Join(out, target, "Session-01", "flats")
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

	dir := filepath.Join(out, target, "Session-01", "flats", "L")
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
		dir := filepath.Join(out, target, "Session-01", "lights", filter)
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("lights for %s were not copied: %v", filter, err)
		}
		if len(files) != 1 {
			t.Errorf("%s: got %d lights, want 1", filter, len(files))
		}
	}

	// No flats directory is created at all.
	if _, err := os.Stat(filepath.Join(out, target, "Session-01", "flats")); err == nil {
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
	sessions, _, err := planSessions(lightsRoot, filepath.Join(root, flatsDir))
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
	victim := filepath.Join(out, target, "Session-01", "lights", "H")
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

	dir := filepath.Join(out, target, "Session-01", "lights", "H")
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

	entries, err := os.ReadDir(filepath.Join(out, target))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != metricsDir {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	want := []string{"Session-01", "Session-02"}
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
		err := runPrepare(prepareOptions{target: "Nope", input: root, output: t.TempDir(), quiet: true})
		if err == nil || !strings.Contains(err.Error(), "introuvable") {
			t.Errorf("error = %v, want a target-not-found failure", err)
		}
	})

	t.Run("empty target", func(t *testing.T) {
		root := t.TempDir()
		err := runPrepare(prepareOptionsForTest("", root, t.TempDir()))
		if err == nil || !strings.Contains(err.Error(), "--target est requis") {
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
		err := runPrepare(prepareOptions{target: "Empty", input: root, output: t.TempDir(), quiet: true})
		if err == nil || !strings.Contains(err.Error(), "aucune session") {
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
		err := runPrepare(prepareOptions{target: "File", input: root, output: t.TempDir(), quiet: true})
		if err == nil || !strings.Contains(err.Error(), "n'est pas un dossier") {
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

		err := runPrepare(prepareOptions{target: target, input: root, output: blocked, quiet: true})
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

	wantHeader := []string{
		"session", "date", "pa", "filters", "lightCount",
		"approvedCount", "rejectedCount", "flatCount",
		"missingFlats", "frameDateMin", "frameDateMax",
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
	if row[0] != "Session-01" || row[1] != "2026-09-10" {
		t.Errorf("row = %v, want Session-01 / 2026-09-10", row[:2])
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

	// Session 02 has two lights but no flats that night.
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
		if _, err := os.Stat(filepath.Join(out, target, "Session-01", "lights", filter)); err != nil {
			t.Errorf("lights/%s missing: %v", filter, err)
		}
		if _, err := os.Stat(filepath.Join(out, target, "Session-01", "flats", filter)); err != nil {
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
// sits directly under Session-NN.
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

	session := filepath.Join(out, target, "Session-01")

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
	lights, err := os.ReadDir(filepath.Join(out, target, "Session-01", sessionLightsDir, "H"))
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
	lightsDirPath := filepath.Join(out, target, "Session-01", sessionLightsDir)
	if entries, err := os.ReadDir(lightsDirPath); err == nil {
		for _, e := range entries {
			files, _ := os.ReadDir(filepath.Join(lightsDirPath, e.Name()))
			if len(files) != 0 {
				t.Errorf("lights/%s holds %d files, want none", e.Name(), len(files))
			}
		}
	}

	// Every frame is under rejected/, keyed by session then filter.
	rejectedRoot := filepath.Join(out, target, rejectedDir, "Session-01", "H")
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

	if _, err := os.Stat(filepath.Join(out, target, "Session-01", sessionLightsDir, "H")); err == nil {
		t.Error("an unreadable frame was placed in lights/")
	}
	files, err := os.ReadDir(filepath.Join(out, target, rejectedDir, "Session-01", "H"))
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
	for _, s := range []string{"Session-01", "Session-02"} {
		entries, err := os.ReadDir(filepath.Join(out, target, s))
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
	for _, s := range []string{"Session-01", "Session-02"} {
		if _, err := os.Stat(filepath.Join(out, target, rejectedDir, s, "H")); err != nil {
			t.Errorf("rejected/%s/H missing: %v", s, err)
		}
	}
}

// readFramesCSV parses the per-frame report.
func readFramesCSV(t *testing.T, targetOut string) [][]string {
	t.Helper()

	b, err := os.ReadFile(metricsPath(targetOut, framesCSVName))
	if err != nil {
		t.Fatalf("read %s: %v", framesCSVName, err)
	}
	recs, err := newCSVReader(strings.NewReader(string(b))).ReadAll()
	if err != nil {
		t.Fatalf("parse %s: %v\n%s", framesCSVName, err, b)
	}
	for _, r := range recs {
		if len(r) != len(recs[0]) {
			t.Errorf("ragged row %v, want %d fields", r, len(recs[0]))
		}
	}
	return recs
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
		"decision", "error",
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
	if recs[1][0] != "Session-01" || recs[3][0] != "Session-02" {
		t.Errorf("sessions in row order = %q, %q, %q; want Session-01, Session-01, Session-02",
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

	// The error column is populated when there is one.
	errRow := imageResultRow(ImageResult{Filename: "b.FIT", Error: errFake{}})
	if errRow[len(errRow)-1] != "fake failure" {
		t.Errorf("error column = %q, want %q", errRow[len(errRow)-1], "fake failure")
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

	// The target root holds only data directories.
	entries, err = os.ReadDir(targetOut)
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
	wantDirs := []string{"Session-01", "Session-02", metricsDir}
	if strings.Join(dirs, ",") != strings.Join(wantDirs, ",") {
		t.Errorf("target root holds %v, want %v", dirs, wantDirs)
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
	}
}

func TestPrepareParsesLightNames(t *testing.T) {
	// The filename conventions the planner relies on.
	light := lightName("LBN527", "H", "20260910", "025946", "489", "310")
	if !frameDateRe.MatchString(light) {
		t.Errorf("frameDateRe does not match %q", light)
	}
	if m := paRe.FindStringSubmatch(light); m == nil || m[1] != "310" {
		t.Errorf("paRe on %q = %v, want 310", light, m)
	}

	flat := flatName("S", "67.02", "20260916", "071758", "808", "310")
	if m := flatFilterRe.FindStringSubmatch(flat); m == nil || m[1] != "S" {
		t.Errorf("flatFilterRe on %q = %v, want S", flat, m)
	}
	if m := paRe.FindStringSubmatch(flat); m == nil || m[1] != "310" {
		t.Errorf("paRe on %q = %v, want 310", flat, m)
	}

	// The master-flat style seen in the repository must not match. The filter
	// pattern is anchored so a frame that merely contains "FLAT_" somewhere in
	// its name cannot be picked up as a flat.
	for _, name := range []string{
		"masterFlat_BIN-1_9576x6388_FILTER-S_mono_SESSION-1.fit",
		"LBN527_LIGHT_H_600s_BIN1_-10C_GA2750_20260916_062721_275_PA310_E.FIT",
		"FLATL_H_41s_PA310.FIT",
		"NOT_A_FLAT_H_41s_PA310.FIT",
	} {
		if flatFilterRe.MatchString(name) {
			t.Errorf("flatFilterRe matched %q", name)
		}
	}

	// A flat is only usable when both the filter and the angle parse; the
	// indexer drops anything missing either.
	for _, name := range []string{
		"FLAT_H_41.4s_BIN1_PA310.FIT",
	} {
		if !flatFilterRe.MatchString(name) {
			t.Errorf("flatFilterRe did not match %q", name)
		}
	}
	if paRe.MatchString("FLAT_H_41.4s_BIN1.FIT") {
		t.Error("paRe matched a flat with no angle")
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

// readSessionsCSV parses a generated sessions.csv.
func readSessionsCSV(t *testing.T, path string) [][]string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	recs, err := newCSVReader(strings.NewReader(string(b))).ReadAll()
	if err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, b)
	}
	for _, r := range recs {
		if len(r) != len(recs[0]) {
			t.Errorf("ragged CSV row %v, want %d fields", r, len(recs[0]))
		}
	}
	return recs
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

// TestPrepareMixedAnglesWarns covers a session whose lights somehow span two
// parallactic angles, which the acquisition software should prevent.
func TestPrepareMixedAnglesWarns(t *testing.T) {
	const target = "TEST"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"H": {
					lightName(target, "H", "20260910", "220000", "000", "310"),
					lightName(target, "H", "20260910", "230000", "000", "240"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {
				flatName("H", "41.4", "20260910", "070000", "000", "240"),
				flatName("H", "41.5", "20260910", "070100", "000", "310"),
			},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	// The lowest angle wins deterministically and is what gets recorded.
	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if recs[1][2] != "PA240" {
		t.Errorf("pa = %q, want PA240 (lowest angle, deterministically)", recs[1][2])
	}
	files, err := os.ReadDir(filepath.Join(out, target, "Session-01", "flats", "H"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !strings.Contains(files[0].Name(), "PA240") {
		t.Errorf("flats = %v, want the single PA240 frame", namesOf(files))
	}
}

func namesOf(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// TestPrepareSessionDirPadding checks the two-digit format holds past nine
// sessions, which is where a naive %d would produce Session-10 correctly but
// Session-1 for the first.
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

	entries, err := os.ReadDir(filepath.Join(out, target))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != metricsDir {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	if len(dirs) != len(dates) {
		t.Fatalf("got %d session dirs, want %d", len(dirs), len(dates))
	}
	for i, d := range dirs {
		want := fmt.Sprintf("Session-%02d", i+1)
		if d != want {
			t.Errorf("session dir[%d] = %q, want %q", i, d, want)
		}
	}
}
