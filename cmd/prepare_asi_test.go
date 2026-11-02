package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ASI2600 fixture builders. The camera writes the gain and offset as separate
// tokens ("GA_100_OF_50"), which is the one structural difference from the C3
// light name.

func asiLightName(target, filter, date, time, ms, pa string) string {
	return fmt.Sprintf("%s_LIGHT_%s_300s_BIN1_-10C_001_%s_%s_%s_GA_100_OF_50_PA%s_E.FIT",
		target, filter, date, time, ms, pa)
}

func asiFlatName(prefix, filter, date, time, ms, pa string) string {
	return fmt.Sprintf("%s_FLAT_%s_BIN1_-10C_001_%s_%s_%s_GA_100_OF_50_PA%s_E.FIT",
		prefix, filter, date, time, ms, pa)
}

func TestASI2600LightDarkKey(t *testing.T) {
	tests := []struct {
		name string
		file string
		want darkKey
	}{
		{
			name: "gain 100",
			file: "SH2-142_LIGHT_H_300s_BIN1_-10C_001_20260812_001427_410_GA_100_OF_50_PA97_W.FIT",
			want: darkKey{gain: "GA100", exposure: 300, temp: "-10C"},
		},
		{
			name: "gain 0 at zero degrees",
			file: "M42_LIGHT_B_15s_BIN1_0C_002_20251220_032012_924_GA_0_OF_50_PA120_E.FIT",
			want: darkKey{gain: "GA0", exposure: 15, temp: "0C"},
		},
		{
			name: "negative setpoint",
			file: "M51_LIGHT_L_300s_BIN1_-5C_010_20260318_221500_123_GA_100_OF_50_PA90_E.FIT",
			want: darkKey{gain: "GA100", exposure: 300, temp: "-5C"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := asi2600Camera.lightDark(tc.file)
			if !ok {
				t.Fatalf("lightDark rejected %q", tc.file)
			}
			if got != tc.want {
				t.Errorf("lightDark(%q) = %+v, want %+v", tc.file, got, tc.want)
			}
		})
	}
}

func TestASI2600LightDarkRejectsOtherCameras(t *testing.T) {
	for _, name := range []string{
		"LBN527_LIGHT_B_300s_BIN1_-10C_GA0_20260910_025946_489_PA310_E.FIT", // C3
		"IC1805HA_LIGHT_H_900s_BIN1_-20C_001_20201024_204428_998_W.FIT",     // G4
		"M42_LIGHT_B_15s_BIN1_0C_002_20251220_032012_924_PA120_E.FIT",       // no gain
		"unrelated.FIT",
	} {
		if _, ok := asi2600Camera.lightDark(name); ok {
			t.Errorf("ASI lightDark accepted %q", name)
		}
	}
}

func TestASI2600FlatFilterAndAngle(t *testing.T) {
	tests := []struct {
		prefix string
		filter string
	}{
		{"FlatRGB", "B"},
		{"FlatRGB", "G"},
		{"FlatRGB", "R"},
		{"FlatL", "L"},
		{"FlatHA3", "H"},
		{"FlatS3", "S"},
		{"FlatO3", "O"},
	}

	for _, tc := range tests {
		name := asiFlatName(tc.prefix, tc.filter, "20260318", "073630", "129", "120")
		m := asi2600Camera.flatFilterRe.FindStringSubmatch(name)
		if m == nil {
			t.Fatalf("flat filter pattern rejected %q", name)
		}
		if m[1] != tc.filter {
			t.Errorf("%q filter = %q, want %q", name, m[1], tc.filter)
		}
		if pa := paRe.FindStringSubmatch(name); pa == nil || pa[1] != "120" {
			t.Errorf("%q angle not parsed: %v", name, pa)
		}
	}

	// A C3 flat has no prefix before FLAT_, so the ASI pattern must not claim it.
	c3Flat := "FLAT_S_67.02s_BIN1_-10C_GA2750_20260916_071758_808_PA310.FIT"
	if m := asi2600Camera.flatFilterRe.FindStringSubmatch(c3Flat); m != nil {
		t.Errorf("ASI flat pattern claimed the C3 flat %q", c3Flat)
	}
}

func TestASI2600MasterKeys(t *testing.T) {
	bias := "masterBias_GA100_-10C_257f_20260901.xisf"
	if key, ok := asi2600Camera.biasKey(bias); !ok || key != "GA100" {
		t.Errorf("biasKey(%q) = %q, %v, want GA100", bias, key, ok)
	}

	dark := "masterDark_300s_GA100_-10C_20260901.xisf"
	if key, ok := asi2600Camera.darkMaster(dark); !ok || key != (darkKey{gain: "GA100", exposure: 300, temp: "-10C"}) {
		t.Errorf("darkMaster(%q) = %+v, %v", dark, key, ok)
	}
}

func TestPrepareAutoDetectsASI2600(t *testing.T) {
	const target = "SH2-142"
	root := t.TempDir()

	f := prepareFixture{
		instrume: "ZWO ASI2600MM Pro",
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-08-11": {"H": {
					asiLightName(target, "H", "20260811", "220000", "000", "97"),
					asiLightName(target, "H", "20260811", "223000", "000", "97"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-08-11": {asiFlatName("FlatHA3", "H", "20260811", "070000", "000", "97")},
		},
		biases: map[string]string{
			"GA100": "masterBias_GA100_-10C_257f_20260901.xisf",
		},
		darks: map[string]string{
			"GA100/300s/-10C": "masterDark_300s_GA100_-10C_20260901.xisf",
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
	if !strings.Contains(stdout, "camera: asi2600 detected from FITS header") {
		t.Errorf("console does not report the detected camera:\n%s", stdout)
	}

	targetOut := filepath.Join(out, target)
	session := sessionOutDir(targetOut, "Session_01")

	for _, path := range []string{
		filepath.Join(session, sessionLightsDir, "H", asiLightName(target, "H", "20260811", "220000", "000", "97")),
		filepath.Join(session, sessionFlatsDir, "H", asiFlatName("FlatHA3", "H", "20260811", "070000", "000", "97")),
		filepath.Join(sessionOutDir(targetOut, ""), biasOutDir, "masterBias_GA100_-10C_257f_20260901.xisf"),
		filepath.Join(sessionOutDir(targetOut, ""), darksOutDir, "masterDark_300s_GA100_-10C_20260901.xisf"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing %s: %v", path, err)
		}
	}

	rows, err := os.ReadFile(metricsPath(targetOut, sessionsCSVName))
	if err != nil {
		t.Fatalf("read sessions.csv: %v", err)
	}
	if !strings.Contains(string(rows), "GA100/300s/-10C") {
		t.Errorf("sessions.csv does not record the ASI dark key:\n%s", rows)
	}
}
