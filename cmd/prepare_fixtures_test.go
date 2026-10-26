package cmd

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/astrogo/fitsio"
)

// Shared fixtures for the prepare tests.
//
// Both cameras' filename builders live here so a fixture can pick a convention
// in one place. The C3 and G4 builders are peers, not a general case and a
// special case: each writes the grammar its camera actually produces.
//
// Nothing here asserts behaviour. The tests live in prepare_test.go for the
// camera-neutral suite, prepare_c3_test.go and prepare_g4_test.go for the
// per-camera rules, and prepare_dark_test.go / prepare_bias_test.go for the
// calibration stages.

// prepareFixture describes a synthetic acquisition tree.
type prepareFixture struct {
	// lights maps target -> date -> filter -> filenames.
	lights map[string]map[string]map[string][]string
	// flats maps date -> filenames.
	flats map[string][]string
	// biases maps gain token -> master filename. Empty means the fixture writes
	// no Bias/Masters directory at all.
	biases map[string]string
	// darks maps a darkKey rendering, "GA2750/600s/-10C", to a master filename.
	// The file is written under a per-gain subdirectory, mirroring the
	// acquisition, unless camera is "g4": that camera has one gain setting, so
	// its masters sit directly in Darks/Masters. Empty means no directory.
	darks map[string]string
	// camera selects the filename convention. Empty means c3.
	camera string
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
	if len(f.biases) > 0 {
		dir := filepath.Join(root, biasDir, biasMastersDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		for _, n := range f.biases {
			writeStub(t, filepath.Join(dir, n))
		}
	}
	if len(f.darks) > 0 {
		dir := filepath.Join(root, darksDir, darksMastersDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		for key, n := range f.darks {
			if f.cameraName() == "g4" {
				// A CCD has one gain setting, so the acquisition writes no
				// per-gain subdirectory at all.
				writeStub(t, filepath.Join(dir, n))
				continue
			}
			// The acquisition splits darks by gain into subdirectories named
			// "G0" and "G2750" whose spelling disagrees with the GA0/GA2750
			// inside the filenames. Only the leading gain is needed here; the
			// subdirectory carries no setpoint.
			gain, _, found := strings.Cut(key, "/")
			if !found {
				t.Fatalf("dark key %q is not <gain>/<exposure>/<temp>", key)
			}
			subdir := filepath.Join(dir, "G"+strings.TrimPrefix(gain, "GA"))
			if err := os.MkdirAll(subdir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", subdir, err)
			}
			writeStub(t, filepath.Join(subdir, n))
		}
	}
}

// cameraName returns the profile name this fixture builds for.
func (f prepareFixture) cameraName() string {
	if f.camera == "" {
		return "c3"
	}
	return f.camera
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

// lightName builds a science frame filename in the acquisition convention, at
// the GA0 gain setting and 300s.
func lightName(target, filter, date, time, ms, pa string) string {
	return lightNameGainExp(target, filter, date, time, ms, pa, "GA0", "300s")
}

// lightNameGain builds a science frame filename at an explicit gain setting,
// which is what lets a fixture mix filters taken at two different gains.
func lightNameGain(target, filter, date, time, ms, pa, gain string) string {
	return lightNameGainExp(target, filter, date, time, ms, pa, gain, "300s")
}

// lightNameGainExp builds a science frame filename at an explicit gain and
// exposure, which is what lets a fixture need two different master darks in one
// session. exposure carries its own "s" suffix, as the acquisition spells it.
func lightNameGainExp(target, filter, date, time, ms, pa, gain, exposure string) string {
	return lightNameFull(target, filter, date, time, ms, pa, gain, exposure, "-10C")
}

// lightNameFull is the only builder that writes the sensor temperature setpoint
// token. The ones above fix it at -10C because that is the temperature the
// acquisition actually uses; this one exists so a fixture can put two setpoints
// in one target, which is the case the setpoint-in-the-key change is about.
func lightNameFull(target, filter, date, time, ms, pa, gain, exposure, temp string) string {
	return fmt.Sprintf("%s_LIGHT_%s_%s_BIN1_%s_%s_%s_%s_%s_PA%s_E.FIT",
		target, filter, exposure, temp, gain, date, time, ms, pa)
}

// flatName builds a flat frame filename.
func flatName(filter, dur, date, time, ms, pa string) string {
	return flatNameTemp(filter, dur, date, time, ms, pa, "-10C")
}

// flatNameTemp builds a flat at an explicit sensor temperature setpoint. The
// setpoint is not part of how a flat is matched -- only filter and angle are --
// so this exists to prove that: a fixture can put a 0C flat under a -10C session
// and the flat is still collected.
func flatNameTemp(filter, dur, date, time, ms, pa, temp string) string {
	return fmt.Sprintf("FLAT_%s_%ss_BIN1_%s_GA2750_%s_%s_%s_PA%s.FIT",
		filter, dur, temp, date, time, ms, pa)
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

// prepareOptionsForTest returns options carrying the shared default
// thresholds, so the synthetic frames in these fixtures are approved unless a
// test deliberately moves them.
func prepareOptionsForTest(target, root, out string) prepareOptions {
	return prepareOptions{
		target: target, input: root, output: out, quiet: true,
		// The flag defaults to c3, so a test that does not care about the
		// camera behaves the way a plain `garfield prepare` does.
		camera:            "c3",
		qualityThresholds: defaultThresholds,
	}
}

// dirEntries reads dir, failing the test if it cannot be listed.
func dirEntries(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	return entries
}

// subDirNames returns the sorted names of the directories directly inside dir.
func subDirNames(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	for _, e := range dirEntries(t, dir) {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// fileNames returns the sorted names of the files directly inside dir, skipping
// the dotfiles macOS leaves behind on exFAT volumes.
func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	for _, e := range dirEntries(t, dir) {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// sessionDirNames lists the prepared session directories of a target. It
// descends into sessionsDir, which is what distinguishes the Sessions/ wrapper
// from the older layout where sessions sat in the target root.
//
// The Session_ prefix filter matters now that bias/ shares the directory: a
// plain listing would return the master bias directory alongside the sessions,
// and any consumer doing the same -- globbing Sessions/*/ -- sees it too.
func sessionDirNames(t *testing.T, out, target string) []string {
	t.Helper()
	var names []string
	for _, e := range dirEntries(t, filepath.Join(out, target, sessionsDir)) {
		if e.IsDir() && strings.HasPrefix(e.Name(), "Session_") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
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

// flatSet builds n flat names for one filter and angle on a date.
func flatSet(filter, date string, pa string, n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, flatName(filter, "41.4", strings.ReplaceAll(date, "-", ""),
			fmt.Sprintf("07%04d", i), "000", pa))
	}
	return out
}

// buildFlatDirs writes flat sets keyed by date, for the collectFlats tests.
func buildFlatDirs(t *testing.T, root string, byDate map[string][]string) {
	t.Helper()
	for date, names := range byDate {
		dir := filepath.Join(root, flatsDir, date)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			writeStub(t, filepath.Join(dir, n))
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

func namesOf(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// The Moravian G4 is the CCD camera that preceded the C3. It has no rotator, so
// its filenames carry no position angle, and no gain setting. Its masters are
// keyed on the sensor temperature setpoint instead. These builders keep the
// fixtures honest about that convention.
//
// A real G4 light:
//
//	IC1805HA_LIGHT_H_900s_BIN1_-20C_001_20201024_204428_998_W.FIT
//
// The prefix is the target with the filter appended, which the acquisition used
// but garfield never reads: the Lights/<target> directory is authoritative.
func g4LightName(prefix, filter, exposure, temp, date, time, ms string) string {
	return prefix + "_LIGHT_" + filter + "_" + exposure + "_BIN1_" + temp + "_001_" +
		date + "_" + time + "_" + ms + "_W.FIT"
}

// g4FlatName builds a G4 flat. Unlike a C3 flat it has an unused prefix before
// FLAT_, no exposure, an index after the setpoint, and no position angle.
func g4FlatName(prefix, filter, temp, date, time, ms string) string {
	return prefix + "_FLAT_" + filter + "_BIN1_" + temp + "_001_" +
		date + "_" + time + "_" + ms + "_W.FIT"
}
