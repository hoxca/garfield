package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astrogo/fitsio"
)

const (
	c3Instrume = "Moravian Instruments, C3-61000EC PRO"
	g4Instrume = "Gx/Cx USB"
)

func TestDetectCameraName(t *testing.T) {
	tests := []struct {
		name      string
		instrume  string
		want      string
		wantFound bool
	}{
		{"c3 exact", c3Instrume, "c3", true},
		{"g4 exact", g4Instrume, "g4", true},
		{"c3 case-insensitive", "MORAVIAN INSTRUMENTS, C3-61000EC PRO", "c3", true},
		{"g4 padded", "  Gx/Cx USB  ", "g4", true},
		{"c3 with firmware suffix", "Moravian Instruments, C3-61000EC PRO (fw 1.2)", "c3", true},
		{"asi2600 exact", "ZWO ASI2600MM Pro", "asi2600", true},
		{"asi2600 colour", "zwo asi2600mc pro", "asi2600", true},
		{"another camera", "QHY268M", "", false},
		{"empty", "", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, ok := detectCameraName(tc.instrume)
			if ok != tc.wantFound {
				t.Fatalf("detectCameraName(%q) found = %v, want %v", tc.instrume, ok, tc.wantFound)
			}
			if ok && name != tc.want {
				t.Errorf("detectCameraName(%q) = %q, want %q", tc.instrume, name, tc.want)
			}
		})
	}
}

func TestResolveCameraExplicitSkipsHeader(t *testing.T) {
	// The lights directory does not exist, so a header read would fail. An
	// explicit camera must not look at the filesystem at all.
	profile, err := resolveCamera("c3", filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("resolveCamera: %v", err)
	}
	if profile.name != "c3" {
		t.Errorf("profile = %q, want c3", profile.name)
	}
}

func TestCameraFromLightsUsesEarliestSession(t *testing.T) {
	const target = "TGT"
	root := t.TempDir()

	writeLight := func(date, filter, instrume string) {
		dir := filepath.Join(root, lightsDir, target, date, filter)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		name := lightName(target, filter, strings.ReplaceAll(date, "-", ""), "220000", "000", "310")
		writeStarFrame(t, filepath.Join(dir, name), 200, 200, 1000, 5, 3000, 1.3, 25, 1,
			fitsio.Card{Name: "INSTRUME", Value: instrume})
	}
	writeLight("2026-09-11", "S", g4Instrume)
	writeLight("2026-09-10", "H", c3Instrume)

	name, err := cameraNameFromLights(filepath.Join(root, lightsDir, target))
	if err != nil {
		t.Fatalf("cameraNameFromLights: %v", err)
	}
	if name != "c3" {
		t.Errorf("camera = %q, want c3 from the earliest session", name)
	}
}

func TestCameraFromLightsSkipsUnreadableFrames(t *testing.T) {
	const target = "TGT"
	root := t.TempDir()

	badDir := filepath.Join(root, lightsDir, target, "2026-09-10", "H")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bad := filepath.Join(badDir, lightName(target, "H", "20260910", "220000", "000", "310"))
	if err := os.WriteFile(bad, []byte("not a FITS file"), 0o644); err != nil {
		t.Fatalf("write bad frame: %v", err)
	}

	goodDir := filepath.Join(root, lightsDir, target, "2026-09-11", "S")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	good := filepath.Join(goodDir, lightName(target, "S", "20260911", "220000", "000", "310"))
	writeStarFrame(t, good, 200, 200, 1000, 5, 3000, 1.3, 25, 1,
		fitsio.Card{Name: "INSTRUME", Value: g4Instrume})

	name, err := cameraNameFromLights(filepath.Join(root, lightsDir, target))
	if err != nil {
		t.Fatalf("cameraNameFromLights: %v", err)
	}
	if name != "g4" {
		t.Errorf("camera = %q, want g4 after skipping the unreadable frame", name)
	}
}

func TestCameraFromLightsUnknownInstrumeIsFatalWithHint(t *testing.T) {
	const target = "TGT"
	root := t.TempDir()

	f := prepareFixture{
		instrume: "QHY268M",
		lights: map[string]map[string]map[string][]string{
			target: {"2026-09-10": {"H": {lightName(target, "H", "20260910", "220000", "000", "310")}}},
		},
	}
	f.build(t, root)

	_, err := cameraNameFromLights(filepath.Join(root, lightsDir, target))
	if err == nil {
		t.Fatal("cameraNameFromLights accepted an unknown INSTRUME")
	}
	for _, want := range []string{"QHY268M", "--camera c3", "--camera g4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestPrepareAutoDetectsC3AndLogsIt(t *testing.T) {
	const target = "M51"
	root := t.TempDir()
	f := simpleFixture(target)
	f.instrume = c3Instrume
	f.build(t, root)

	out := t.TempDir()
	opts := prepareOptionsForTest(target, root, out)
	opts.camera = cameraAuto
	opts.quiet = false

	stdout := captureStdout(t, func() {
		if err := runPrepare(opts); err != nil {
			t.Errorf("runPrepare: %v", err)
		}
	})
	if !strings.Contains(stdout, "camera: c3 detected from FITS header") {
		t.Errorf("console does not report the detected camera:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(out, target, sessionsDir)); err != nil {
		t.Errorf("no session tree written: %v", err)
	}
}

func TestPrepareAutoDetectsG4AndLogsIt(t *testing.T) {
	const target = "IC1805"
	root := t.TempDir()

	f := prepareFixture{
		camera:   "g4",
		instrume: g4Instrume,
		lights: map[string]map[string]map[string][]string{
			target: {
				"2020-10-24": {"H": {
					g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "204428", "998"),
				}},
			},
		},
		flats: map[string][]string{
			"2020-10-24": {g4FlatName("IC1805", "H", "-20C", "20201024", "071758", "808")},
		},
	}
	f.build(t, root)

	out := t.TempDir()
	opts := prepareOptionsForTest(target, root, out)
	opts.camera = cameraAuto
	opts.quiet = false

	stdout := captureStdout(t, func() {
		if err := runPrepare(opts); err != nil {
			t.Errorf("runPrepare: %v", err)
		}
	})
	if !strings.Contains(stdout, "camera: g4 detected from FITS header") {
		t.Errorf("console does not report the detected camera:\n%s", stdout)
	}
}

func TestPrepareAutoMissingInstrumeIsFatalAndWritesNothing(t *testing.T) {
	const target = "M51"
	root := t.TempDir()
	simpleFixture(target).build(t, root) // no INSTRUME card

	out := t.TempDir()
	opts := prepareOptionsForTest(target, root, out)
	opts.camera = cameraAuto

	err := runPrepare(opts)
	if err == nil {
		t.Fatal("runPrepare succeeded without an INSTRUME header in auto mode")
	}
	for _, want := range []string{"INSTRUME", "--camera c3", "--camera g4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(out, target)); !os.IsNotExist(statErr) {
		t.Errorf("output was written despite detection failing (stat err: %v)", statErr)
	}
}

func TestPrepareExplicitCameraDoesNotNeedInstrume(t *testing.T) {
	const target = "M51"
	root := t.TempDir()
	simpleFixture(target).build(t, root) // no INSTRUME card

	out := t.TempDir()
	opts := prepareOptionsForTest(target, root, out)
	opts.camera = "c3"

	if err := runPrepare(opts); err != nil {
		t.Fatalf("an explicit --camera must not require INSTRUME: %v", err)
	}
}
