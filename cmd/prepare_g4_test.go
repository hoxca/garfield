package cmd

// Tests for behaviour that exists only on the Moravian G4.
//
// The G4 is the CCD that preceded the C3: no rotator, so no position angle, and
// no gain setting. Its master calibration is keyed on the sensor temperature
// setpoint. Anything here would be meaningless under --camera c3, which is why
// it is separated from the camera-neutral suite in prepare_test.go.
//
// A real G4 light:
//
//	IC1805HA_LIGHT_H_900s_BIN1_-20C_001_20201024_204428_998_W.FIT
//
// and a real G4 flat:
//
//	H_AutoFlat_FLAT_H_BIN1_-10C_001_20220822_071017_086_E.FIT

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	g4Bias = "masterBias_-20C_257f_20201024.xisf"
	g4Dark = "masterDark_900s_-20C_20201024.xisf"
)

// TestG4PatternsExtractKeys pins the G4 regexes on real names.
func TestG4PatternsExtractKeys(t *testing.T) {
	light := g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "204428", "998")
	key, ok := g4Camera.lightDark(light)
	if !ok {
		t.Fatalf("g4Camera.lightDark(%q) = false", light)
	}
	if key.exposure != 900 || key.temp != "-20C" || key.gain != "" {
		t.Errorf("light key = %+v, want exposure 900, temp -20C, no gain", key)
	}
	// The rendering has to make an absent gain read as absent, not as an empty
	// field.
	if got := key.String(); got != "900s/-20C" {
		t.Errorf("key.String() = %q, want %q", got, "900s/-20C")
	}

	// A real frame from the 2022 TOA-150 set, which is G4 format: the token
	// where a C3 carries a gain is the frame index (010), and there is no PA.
	real := "VDB149_LIGHT_L_600s_BIN1_-10C_010_20220822_014556_614_W.FIT"
	rkey, ok := g4Camera.lightDark(real)
	if !ok {
		t.Fatalf("g4Camera.lightDark(%q) = false", real)
	}
	if rkey.exposure != 600 || rkey.temp != "-10C" || rkey.gain != "" {
		t.Errorf("real light key = %+v, want 600s/-10C with no gain", rkey)
	}

	dkey, ok := g4Camera.darkMaster(g4Dark)
	if !ok {
		t.Fatalf("g4Camera.darkMaster(%q) = false", g4Dark)
	}
	if dkey != key {
		t.Errorf("dark %v does not match light %v", dkey, key)
	}

	bkey, ok := g4Camera.biasKey(g4Bias)
	if !ok {
		t.Fatalf("g4Camera.biasKey(%q) = false", g4Bias)
	}
	if bkey != "-20C" {
		t.Errorf("bias key = %q, want %q", bkey, "-20C")
	}

	f := g4FlatName("IC1805", "S", "-20C", "20201024", "071758", "808")
	mf := g4Camera.flatFilterRe.FindStringSubmatch(f)
	if mf == nil || mf[1] != "S" {
		t.Errorf("g4 flat filter from %q = %v, want S", f, mf)
	}

	// A real flat from the 2022 set: the prefix is the filter plus a
	// descriptive suffix, there is no exposure, and the index follows the
	// setpoint.
	realFlat := "H_AutoFlat_FLAT_H_BIN1_-10C_001_20220822_071017_086_E.FIT"
	mf = g4Camera.flatFilterRe.FindStringSubmatch(realFlat)
	if mf == nil || mf[1] != "H" {
		t.Errorf("real g4 flat filter from %q = %v, want H", realFlat, mf)
	}
}

// TestCameraProfilesAreDisjoint is the property that lets both cameras share one
// acquisition tree: a name belonging to one camera must not parse as the other,
// so the inactive profile leaves it alone instead of mis-indexing it.
func TestCameraProfilesAreDisjoint(t *testing.T) {
	c3Light := lightName("LBN527", "S", "20260910", "220000", "000", "310")
	c3Flat := flatName("S", "67.02", "20260910", "071758", "808", "310")
	c3Dark := "masterDark_300s_GA0_-10C_20251118.xisf"
	c3Bias := "masterBias_GA0_-10C_257f_20251117.xisf"

	g4Light := g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "204428", "998")
	g4Flat := g4FlatName("IC1805", "S", "-20C", "20201024", "071758", "808")

	t.Run("c3 names do not parse as g4", func(t *testing.T) {
		if _, ok := g4Camera.lightDark(c3Light); ok {
			t.Error("g4Camera accepted a C3 light")
		}
		if _, ok := g4Camera.darkMaster(c3Dark); ok {
			t.Error("g4Camera accepted a C3 dark")
		}
		if _, ok := g4Camera.biasKey(c3Bias); ok {
			t.Error("g4Camera accepted a C3 bias")
		}
		if g4Camera.flatFilterRe.MatchString(c3Flat) {
			t.Error("g4Camera accepted a C3 flat")
		}
		if g4Camera.isFlat(c3Flat) {
			t.Error("g4Camera claims a C3 flat as its own, so it would be reported")
		}
	})

	t.Run("g4 names do not parse as c3", func(t *testing.T) {
		if _, ok := c3Camera.lightDark(g4Light); ok {
			t.Error("c3Camera accepted a G4 light")
		}
		if _, ok := c3Camera.darkMaster(g4Dark); ok {
			t.Error("c3Camera accepted a G4 dark")
		}
		if _, ok := c3Camera.biasKey(g4Bias); ok {
			t.Error("c3Camera accepted a G4 bias")
		}
		if c3Camera.flatFilterRe.MatchString(g4Flat) {
			t.Error("c3Camera accepted a G4 flat")
		}
		if c3Camera.isFlat(g4Flat) {
			t.Error("c3Camera claims a G4 flat as its own, so it would be reported")
		}
	})
}

// TestG4PlanHasNoAngleAndMatchesFlatsByFilter is the core of the G4 behaviour:
// no position angle is recorded, and flats match on filter alone.
func TestG4PlanHasNoAngleAndMatchesFlatsByFilter(t *testing.T) {
	const target = "IC1805"
	root := t.TempDir()

	f := prepareFixture{
		camera: "g4",
		lights: map[string]map[string]map[string][]string{
			target: {
				"2020-10-24": {"H": {
					g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "204428", "998"),
					g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "205500", "123"),
				}},
			},
		},
		flats: map[string][]string{
			"2020-10-24": {g4FlatName("IC1805", "H", "-20C", "20201024", "071758", "808")},
		},
	}
	f.build(t, root)

	sessions, warnings, err := planSessions(
		filepath.Join(root, lightsDir, target), filepath.Join(root, flatsDir), target, 0, g4Camera)
	if err != nil {
		t.Fatalf("planSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	s := sessions[0]

	if s.pa != "" {
		t.Errorf("pa = %q, want empty: the G4 has no rotator", s.pa)
	}
	if got := len(s.flats["H"]); got != 1 {
		t.Errorf("matched %d flats, want 1 (matched on filter, not angle)", got)
	}
	if len(s.missingFlats) != 0 {
		t.Errorf("missingFlats = %v, want none", s.missingFlats)
	}
	// The bias key set is the setpoint, not a gain.
	if got := strings.Join(s.biasKeys, " "); got != "-20C" {
		t.Errorf("biasKeys = %q, want %q", got, "-20C")
	}
	if len(s.gains) != 0 {
		t.Errorf("gains = %v, want empty: the G4 has no gain setting", s.gains)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

// TestG4IgnoresC3FlatsInTheSameDirectory covers the shared Flats tree. A C3 flat
// shot the same night must not be indexed under the angle-less key the G4 uses,
// or it would be copied into a G4 session.
func TestG4IgnoresC3FlatsInTheSameDirectory(t *testing.T) {
	const target = "IC1805"
	root := t.TempDir()

	f := prepareFixture{
		camera: "g4",
		lights: map[string]map[string]map[string][]string{
			target: {
				"2020-10-24": {"H": {
					g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "204428", "998"),
				}},
			},
		},
		flats: map[string][]string{
			"2020-10-24": {
				g4FlatName("IC1805", "H", "-20C", "20201024", "071758", "808"),
				flatName("H", "41.4", "20201024", "070000", "000", "310"), // C3
			},
		},
	}
	f.build(t, root)

	sessions, warnings, err := planSessions(
		filepath.Join(root, lightsDir, target), filepath.Join(root, flatsDir), target, 0, g4Camera)
	if err != nil {
		t.Fatalf("planSessions: %v", err)
	}
	if got := len(sessions[0].flats["H"]); got != 1 {
		t.Fatalf("matched %d flats, want only the G4 one", got)
	}
	if name := sessions[0].flats["H"][0].name; !strings.Contains(name, "_FLAT_H_BIN1_") {
		t.Errorf("matched %q, want the G4 flat", name)
	}
	// The C3 flat is another camera's valid data, so it is left alone rather
	// than reported as a broken flat.
	for _, w := range warnings {
		if strings.Contains(w, "position angle") || strings.Contains(w, "no known filter") {
			t.Errorf("warning %q treats the other camera's flat as malformed", w)
		}
	}
}

// TestG4PrepareCopiesMastersKeyedOnSetpoint drives a whole run: bias keyed on
// the setpoint, dark on exposure plus setpoint, neither on gain.
func TestG4PrepareCopiesMastersKeyedOnSetpoint(t *testing.T) {
	const target = "IC1805"
	root := t.TempDir()

	f := prepareFixture{
		camera: "g4",
		lights: map[string]map[string]map[string][]string{
			target: {
				"2020-10-24": {"H": {
					g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "204428", "998"),
					g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "205500", "123"),
				}},
			},
		},
		flats: map[string][]string{
			"2020-10-24": {g4FlatName("IC1805", "H", "-20C", "20201024", "071758", "808")},
		},
		biases: map[string]string{"-20C": g4Bias},
		darks:  map[string]string{"900s/-20C": g4Dark},
	}
	f.build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.camera = "g4"
	opts.minStars = 0
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	if got := fileNames(t, filepath.Join(out, target, sessionsDir, biasOutDir)); len(got) != 1 {
		t.Errorf("bias dir holds %v, want the one master", got)
	}
	if got := fileNames(t, filepath.Join(out, target, sessionsDir, darksOutDir)); len(got) != 1 {
		t.Errorf("darks dir holds %v, want the one master", got)
	}

	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if len(recs) != 2 {
		t.Fatalf("got %d records, want header plus one session", len(recs))
	}
	row := recs[1]
	if row[2] != "" {
		t.Errorf("pa = %q, want empty on a camera with no rotator", row[2])
	}
	if row[11] != "" {
		t.Errorf("gains = %q, want empty on a CCD", row[11])
	}
	if row[12] != "900s/-20C" {
		t.Errorf("darks = %q, want %q", row[12], "900s/-20C")
	}
}

// TestG4MixedSetpointsDrawTwoMasters is the reason the setpoint is in the G4
// bias key as well as the dark key.
func TestG4MixedSetpointsDrawTwoMasters(t *testing.T) {
	const target = "IC1805"
	root := t.TempDir()

	f := prepareFixture{
		camera: "g4",
		lights: map[string]map[string]map[string][]string{
			target: {
				"2020-10-24": {"H": {
					g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "204428", "998"),
				}},
				"2020-11-02": {"H": {
					g4LightName("IC1805HA", "H", "900s", "-10C", "20201102", "210000", "000"),
				}},
			},
		},
		flats: map[string][]string{
			"2020-10-24": {g4FlatName("IC1805", "H", "-20C", "20201024", "071758", "808")},
			"2020-11-02": {g4FlatName("IC1805", "H", "-10C", "20201102", "070000", "000")},
		},
		biases: map[string]string{
			"-20C": "masterBias_-20C_257f_20201024.xisf",
			"-10C": "masterBias_-10C_257f_20201102.xisf",
		},
		darks: map[string]string{
			"900s/-20C": "masterDark_900s_-20C_20201024.xisf",
			"900s/-10C": "masterDark_900s_-10C_20201102.xisf",
		},
	}
	f.build(t, root)
	out := t.TempDir()

	opts := prepareOptionsForTest(target, root, out)
	opts.camera = "g4"
	if err := runPrepare(opts); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	if got := fileNames(t, filepath.Join(out, target, sessionsDir, biasOutDir)); len(got) != 2 {
		t.Errorf("bias dir holds %v, want one per setpoint", got)
	}
	if got := fileNames(t, filepath.Join(out, target, sessionsDir, darksOutDir)); len(got) != 2 {
		t.Errorf("darks dir holds %v, want one per setpoint", got)
	}
}

// TestG4DoesNotFailOnMissingAngle pins that the C3 mixed-angle rules do not
// reach a camera that has no rotator.
func TestG4DoesNotFailOnMissingAngle(t *testing.T) {
	const target = "IC1805"
	root := t.TempDir()

	f := prepareFixture{
		camera: "g4",
		lights: map[string]map[string]map[string][]string{
			target: {
				"2020-10-24": {"H": {
					g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "204428", "998"),
				}},
				"2020-11-02": {"H": {
					g4LightName("IC1805HA", "H", "900s", "-20C", "20201102", "210000", "000"),
				}},
			},
		},
	}
	f.build(t, root)

	if _, _, err := planSessions(
		filepath.Join(root, lightsDir, target), filepath.Join(root, flatsDir), target, 0, g4Camera); err != nil {
		t.Fatalf("planSessions rejected a G4 target for having no angle: %v", err)
	}
}

// TestCameraFlagValidation checks an unknown camera is refused rather than
// defaulted: the matching rules are not interchangeable.
func TestCameraFlagValidation(t *testing.T) {
	if _, err := cameraProfileFor("c3"); err != nil {
		t.Errorf("c3 rejected: %v", err)
	}
	if _, err := cameraProfileFor("g4"); err != nil {
		t.Errorf("g4 rejected: %v", err)
	}
	for _, bad := range []string{"", "C3", "c4", "ccd"} {
		if _, err := cameraProfileFor(bad); err == nil {
			t.Errorf("cameraProfileFor(%q) accepted, want an error", bad)
		}
	}
}

// TestCameraMismatchWarning covers the one-line diagnosis when the switch does
// not match the data. Without it the run still fails, but only with a pile of
// per-filter and per-master warnings that never name the cause.
func TestCameraMismatchWarning(t *testing.T) {
	c3Light := lightName("LBN527", "S", "20260910", "220000", "000", "310")
	g4Light := g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "204428", "998")

	c3Session := sessionPlan{filters: map[string][]string{"S": {c3Light}}}
	g4Session := sessionPlan{filters: map[string][]string{"H": {g4Light}}}

	if w := cameraMismatchWarning([]sessionPlan{c3Session}, c3Camera); w != "" {
		t.Errorf("c3 data under c3 warned: %q", w)
	}
	if w := cameraMismatchWarning([]sessionPlan{g4Session}, g4Camera); w != "" {
		t.Errorf("g4 data under g4 warned: %q", w)
	}
	if w := cameraMismatchWarning([]sessionPlan{g4Session}, c3Camera); !strings.Contains(w, "is this G4 data") {
		t.Errorf("c3 profile on G4 data warned %q, want a G4 hint", w)
	}
	if w := cameraMismatchWarning([]sessionPlan{c3Session}, g4Camera); !strings.Contains(w, "is this C3 data") {
		t.Errorf("g4 profile on C3 data warned %q, want a C3 hint", w)
	}

	// A per-session mixture is a contradiction, not a camera; no hint is issued
	// because neither answer would be right.
	mixed := sessionPlan{filters: map[string][]string{"S": {c3Light, g4Light}}}
	if w := cameraMismatchWarning([]sessionPlan{mixed}, c3Camera); w != "" {
		t.Errorf("mixed field warned %q, want silence", w)
	}
}

// TestG4FlatFilterReStillRejectsBadFilters keeps the G4 pattern honest: the
// prefix is optional-looking but the filter set is not.
func TestG4FlatFilterReStillRejectsBadFilters(t *testing.T) {
	for _, name := range []string{
		"IC1805_FLAT_Ha_BIN1_-20C_001_20201024_071758_808_W.FIT",
		"IC1805_FLAT_OIII_BIN1_-20C_001_20201024_071758_808_W.FIT",
		"IC1805_FLATL_BIN1_-20C_001_20201024_071758_808_W.FIT",
	} {
		if g4Camera.flatFilterRe.MatchString(name) {
			t.Errorf("g4 flat pattern accepted %q", name)
		}
	}
	// The prefix test is looser on purpose, so an unusable G4 flat is reported
	// rather than silently dropped.
	if !g4Camera.isFlat("IC1805_FLAT_Ha_BIN1_-20C_001_20201024_071758_808_W.FIT") {
		t.Error("g4 prefix test did not recognise an unusable flat")
	}
}

// TestG4MastersInFlatDarksDirectory pins the layout difference: a CCD writes no
// per-gain subdirectory, so masters sit directly in Darks/Masters.
func TestG4MastersInFlatDarksDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, darksDir, darksMastersDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, filepath.Join(dir, g4Dark))
	writeStub(t, filepath.Join(dir, "masterDark_600s_-10C_20201102.xisf"))

	masters, warnings, err := indexDarkMasters(dir, g4Camera)
	if err != nil {
		t.Fatalf("indexDarkMasters: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if _, ok := masters[darkKey{exposure: 900, temp: "-20C"}]; !ok {
		t.Errorf("900s/-20C not indexed; masters = %v", masters)
	}
	if _, ok := masters[darkKey{exposure: 600, temp: "-10C"}]; !ok {
		t.Errorf("600s/-10C not indexed; masters = %v", masters)
	}
}

// TestAnalyzeParsesG4Names pins that analyze needs no camera switch: its
// positional parser happens to be correct for both G4 lights and G4 flats.
func TestAnalyzeParsesG4Names(t *testing.T) {
	light := g4LightName("IC1805HA", "H", "900s", "-20C", "20201024", "204428", "998")
	filter, date := parseFilename(light)
	if filter != "H" || date != "2020-10-24" {
		t.Errorf("parseFilename(%q) = %q/%q, want H/2020-10-24", light, filter, date)
	}

	// The flat grammar puts FLAT at index 1, so the filter is still index 2.
	flat := g4FlatName("IC1805", "S", "-20C", "20201024", "071758", "808")
	filter, date = parseFilename(flat)
	if filter != "S" {
		t.Errorf("parseFilename(%q) filter = %q, want S", flat, filter)
	}
	if date != "" {
		t.Errorf("parseFilename(%q) date = %q, want empty", flat, date)
	}
}

// TestCameraIsAConfigKey checks the switch is drivable from the config file.
// knownConfigKeys walks the command's flags, so a new prepare flag must appear
// as a known key rather than being reported as an unknown one.
func TestCameraIsAConfigKey(t *testing.T) {
	if !knownConfigKeys()["prepare.camera"] {
		t.Error("prepare.camera is not a known config key")
	}
	path := writeConfig(t, "prepare:\n  camera: g4\n")

	cmd, _ := configTestPrepareCmd()
	if err := applySections(cmd, mustReadConfig(t, path), path); err != nil {
		t.Fatalf("applySections: %v", err)
	}
	if got := cmd.Flags().Lookup("camera").Value.String(); got != "g4" {
		t.Errorf("camera from config = %q, want %q", got, "g4")
	}
}
