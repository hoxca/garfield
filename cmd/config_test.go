package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// writeConfig drops a config file into a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "garfield.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// configTestAnalyzeCmd mirrors analyzeCmd's flag registration but keeps its own
// values, so applying a config cannot leak into the package-level analyzeOpts
// the rest of the suite depends on.
func configTestAnalyzeCmd() (*cobra.Command, *analyzeOptions) {
	opts := &analyzeOptions{}
	c := &cobra.Command{Use: "analyze"}
	f := c.Flags()
	registerQualityFlags(f, &opts.qualityThresholds)
	registerWorkerFlags(f, &opts.workers, &opts.convWorkers)
	f.StringVarP(&opts.dir, "dir", "d", "images", "dossier contenant les FITS")
	f.StringVarP(&opts.output, "output", "o", "", "fichier CSV de sortie")
	setConfigSections(c, sectionThresholds, sectionConcurrency, sectionAnalyze)
	return c, opts
}

// configTestPrepareCmd mirrors prepareCmd the same way.
func configTestPrepareCmd() (*cobra.Command, *prepareOptions) {
	opts := &prepareOptions{}
	c := &cobra.Command{Use: "prepare"}
	f := c.Flags()
	registerQualityFlags(f, &opts.qualityThresholds)
	registerWorkerFlags(f, &opts.workers, &opts.convWorkers)
	f.StringVarP(&opts.input, "input", "i", defaultPrepareInput, "racine des données d'acquisition")
	f.StringVarP(&opts.output, "output", "o", defaultPrepareOutput, "racine de destination")
	setConfigSections(c, sectionThresholds, sectionConcurrency, sectionPrepare)
	return c, opts
}

// TestConfigOverridesFlagDefaults checks a config file supplies the defaults for
// both commands, which is the whole point of the file.
func TestConfigOverridesFlagDefaults(t *testing.T) {
	path := writeConfig(t, `
thresholds:
  min-snr: 7.5
  max-fwhm: 3.2
  max-ecc: 0.5
  min-score: 1.5
  min-stars: 700
concurrency:
  workers: 4
  conv-workers: 2
`)
	v := mustReadConfig(t, path)

	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
	}{{"analyze", nil}, {"prepare", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			var cmd *cobra.Command
			if tc.name == "analyze" {
				cmd, _ = configTestAnalyzeCmd()
			} else {
				cmd, _ = configTestPrepareCmd()
			}
			if err := applySections(cmd, v, path); err != nil {
				t.Fatalf("applySections: %v", err)
			}
			for flag, want := range map[string]string{
				"min-snr": "7.5", "max-fwhm": "3.2", "max-ecc": "0.5",
				"min-score": "1.5", "min-stars": "700",
				"workers": "4", "conv-workers": "2",
			} {
				f := cmd.Flags().Lookup(flag)
				if f == nil {
					t.Errorf("--%s is not registered", flag)
					continue
				}
				if got := f.Value.String(); got != want {
					t.Errorf("--%s = %s, want %s", flag, got, want)
				}
				// The help must advertise the value actually in force.
				if f.DefValue != want {
					t.Errorf("--%s DefValue = %s, want %s so --help is truthful", flag, f.DefValue, want)
				}
				if !strings.Contains(f.Usage, configMarker) {
					t.Errorf("--%s usage %q does not say the default came from a config file", flag, f.Usage)
				}
			}
		})
	}
}

// TestExplicitFlagBeatsConfig pins the precedence order: command line first.
func TestExplicitFlagBeatsConfig(t *testing.T) {
	path := writeConfig(t, "thresholds:\n  max-ecc: 0.5\n  min-stars: 700\n")
	v := mustReadConfig(t, path)

	cmd, opts := configTestAnalyzeCmd()
	if err := cmd.Flags().Set("max-ecc", "0.42"); err != nil {
		t.Fatal(err)
	}
	if err := applySections(cmd, v, path); err != nil {
		t.Fatalf("applySections: %v", err)
	}

	if opts.maxEcc != 0.42 {
		t.Errorf("maxEcc = %v, want the explicit 0.42", opts.maxEcc)
	}
	if opts.minStars != 700 {
		t.Errorf("minStars = %v, want the configured 700", opts.minStars)
	}
}

// TestConfigKeyTouchesOnlyItsOwnSections checks the per-command path sections do
// not bleed across commands. This is the reason the sections exist: --output is
// a CSV file for analyze and a directory for prepare, and a shared key would
// hand the directory to the CSV flag.
func TestConfigKeyTouchesOnlyItsOwnSections(t *testing.T) {
	path := writeConfig(t, `
analyze:
  dir: /data/frames
prepare:
  input: /data/acq
  output: /data/work
`)
	v := mustReadConfig(t, path)

	analyzeCmd, analyzeOpts := configTestAnalyzeCmd()
	if err := applySections(analyzeCmd, v, path); err != nil {
		t.Fatalf("applySections: %v", err)
	}
	if analyzeOpts.dir != "/data/frames" {
		t.Errorf("analyze dir = %q, want /data/frames", analyzeOpts.dir)
	}
	if analyzeCmd.Flags().Lookup("input") != nil {
		t.Error("analyze grew an --input flag")
	}

	prepareCmd, prepareOpts := configTestPrepareCmd()
	if err := applySections(prepareCmd, v, path); err != nil {
		t.Fatalf("applySections: %v", err)
	}
	if prepareOpts.input != "/data/acq" {
		t.Errorf("prepare input = %q, want /data/acq", prepareOpts.input)
	}
	if prepareOpts.output != "/data/work" {
		t.Errorf("prepare output = %q, want /data/work", prepareOpts.output)
	}
	// The analyze section must not reach prepare's flags.
	if prepareCmd.Flags().Lookup("dir") != nil {
		t.Error("prepare grew a --dir flag, so analyze.dir could leak into it")
	}
}

// TestConfigSectionsAreWiredOnRealCommands checks the shipped commands declare
// the sections the design calls for.
func TestConfigSectionsAreWiredOnRealCommands(t *testing.T) {
	want := map[string][]string{
		"analyze": {sectionThresholds, sectionConcurrency, sectionAnalyze},
		"prepare": {sectionThresholds, sectionConcurrency, sectionPrepare},
	}
	got := map[string][]string{
		"analyze": configSections[analyzeCmd],
		"prepare": configSections[prepareCmd],
	}
	for name, sections := range want {
		if strings.Join(got[name], ",") != strings.Join(sections, ",") {
			t.Errorf("%s reads sections %v, want %v", name, got[name], sections)
		}
	}
}

// TestSharedConfigKeysCoverBothCommands checks every threshold and concurrency
// flag is reachable from the config on both commands, so the file can never
// describe only half of what decides a frame's verdict.
func TestSharedConfigKeysCoverBothCommands(t *testing.T) {
	keys := knownConfigKeys()

	for _, flag := range []string{"min-snr", "max-fwhm", "max-ecc", "min-score", "min-stars"} {
		if !keys[sectionThresholds+"."+flag] {
			t.Errorf("%s.%s is not a known config key", sectionThresholds, flag)
		}
	}
	for _, flag := range []string{"workers", "conv-workers"} {
		if !keys[sectionConcurrency+"."+flag] {
			t.Errorf("%s.%s is not a known config key", sectionConcurrency, flag)
		}
	}
	// The two --output flags live under different keys, and analyze's is
	// excluded outright, so no config value can reach a report path.
	if keys["output"] {
		t.Error("a bare output key is known, want each command namespaced under its own section")
	}
	if keys[sectionAnalyze+".output"] {
		t.Error("analyze.output is a known key, want analyze's --output excluded from the config")
	}
	if !keys[sectionPrepare+".output"] {
		t.Error("prepare.output is not a known key, want prepare's destination directory configurable")
	}
	if !keys[sectionAnalyze+".dir"] {
		t.Error("analyze.dir is not a known key")
	}
}

// TestExcludedFlagsAreIgnored checks analyze's --output keeps its compiled
// default no matter what the config says, and that the exclusion is not reported
// as an unknown key.
func TestExcludedFlagsAreIgnored(t *testing.T) {
	path := writeConfig(t, "analyze:\n  output: /data/work\n  dir: /data/frames\n")
	v := mustReadConfig(t, path)

	cmd, opts := configTestAnalyzeCmd()
	if err := applySections(cmd, v, path); err != nil {
		t.Fatalf("applySections: %v", err)
	}
	if opts.output != "" {
		t.Errorf("analyze output = %q, want the flag untouched by the config", opts.output)
	}
	if opts.dir != "/data/frames" {
		t.Errorf("analyze dir = %q, want /data/frames from the same section", opts.dir)
	}
	// Silently dropping it would be the trap this guards against: the file says
	// set it, so saying so is better than pretending it took effect.
	out := captureStderr(t, func() { warnUnknownKeys(v, path) })
	if !strings.Contains(out, "volontairement non configurables") || !strings.Contains(out, "analyze.output") {
		t.Errorf("warning %q, want it to name analyze.output as deliberately refused", out)
	}
}

// TestResolveConfigPath covers the search order and the two error cases.
func TestResolveConfigPath(t *testing.T) {
	t.Run("absent config is not an error", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
		t.Setenv("HOME", t.TempDir())
		chdir(t, t.TempDir())

		path, err := resolveConfigPath("")
		if err != nil {
			t.Fatalf("resolveConfigPath: %v", err)
		}
		if path != "" {
			t.Errorf("path = %q, want empty so the built-in defaults apply", path)
		}
	})

	t.Run("working-directory config is found", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
		t.Setenv("HOME", filepath.Join(root, "home"))
		if err := os.MkdirAll(filepath.Join(root, configRelPath), 0o755); err != nil {
			t.Fatal(err)
		}
		chdir(t, root)

		path, err := resolveConfigPath("")
		if err != nil {
			t.Fatalf("resolveConfigPath: %v", err)
		}
		if path != configRelPath {
			t.Errorf("path = %q, want %q", path, configRelPath)
		}
	})

	t.Run("xdg config is found when no local one exists", func(t *testing.T) {
		work, xdg := t.TempDir(), t.TempDir()
		if err := os.MkdirAll(filepath.Join(xdg, "garfield"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(xdg, "garfield", "garfield.yaml"), []byte("thresholds:\n  max-ecc: 0.5\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", xdg)
		t.Setenv("HOME", filepath.Join(work, "home"))
		chdir(t, work)

		path, err := resolveConfigPath("")
		if err != nil {
			t.Fatalf("resolveConfigPath: %v", err)
		}
		if want := filepath.Join(xdg, "garfield", "garfield.yaml"); path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
	})

	t.Run("explicit path wins", func(t *testing.T) {
		path := writeConfig(t, "thresholds:\n  max-ecc: 0.5\n")
		got, err := resolveConfigPath(path)
		if err != nil {
			t.Fatalf("resolveConfigPath: %v", err)
		}
		if got != path {
			t.Errorf("path = %q, want %q", got, path)
		}
	})

	t.Run("explicit missing path is an error", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "absent.yaml")
		if _, err := resolveConfigPath(missing); err == nil {
			t.Error("a missing --config was accepted, want an error rather than a silent fallback")
		}
	})
}

// TestMalformedConfigIsFatal checks a broken file stops the run. Falling back to
// the built-in thresholds here would silently judge frames by different
// criteria than the file describes.
func TestMalformedConfigIsFatal(t *testing.T) {
	path := writeConfig(t, "thresholds:\n  max-ecc: [unclosed\n")
	if _, err := readConfig(path); err == nil {
		t.Error("a malformed config was accepted")
	}
}

// TestUnknownKeysAreWarned checks typos surface. A key like max_ecc is ignored
// by viper, so without this the built-in default stays in force while the file
// reads as if the threshold had changed.
func TestUnknownKeysAreWarned(t *testing.T) {
	path := writeConfig(t, "thresholds:\n  max_ecc: 0.5\nconcurrency:\n  wrkers: 4\n")
	v := mustReadConfig(t, path)

	out := captureStderr(t, func() { warnUnknownKeys(v, path) })
	for _, key := range []string{"thresholds.max_ecc", "concurrency.wrkers"} {
		if !strings.Contains(out, key) {
			t.Errorf("warning %q does not mention %q", out, key)
		}
	}
}

// TestKnownKeysAreNotWarned is the other half: a key belonging to another
// command, or every threshold key, must stay quiet.
func TestKnownKeysAreNotWarned(t *testing.T) {
	path := writeConfig(t, "thresholds:\n  max-ecc: 0.5\nprepare:\n  input: /acq\nanalyze:\n  dir: /frames\n")
	v := mustReadConfig(t, path)

	if out := captureStderr(t, func() { warnUnknownKeys(v, path) }); out != "" {
		t.Errorf("warning %q, want none: every key belongs to a command", out)
	}
}

// TestConfigMarkerIsNotDuplicated checks applying the config twice does not
// stack markers, which happens when help renders after the PreRun hook.
func TestConfigMarkerIsNotDuplicated(t *testing.T) {
	path := writeConfig(t, "thresholds:\n  max-ecc: 0.5\n")
	v := mustReadConfig(t, path)

	cmd, _ := configTestAnalyzeCmd()
	for i := 0; i < 3; i++ {
		if err := applySections(cmd, v, path); err != nil {
			t.Fatalf("applySections: %v", err)
		}
	}
	usage := cmd.Flags().Lookup("max-ecc").Usage
	if n := strings.Count(usage, configMarker); n != 1 {
		t.Errorf("usage %q carries the marker %d times, want 1", usage, n)
	}
}

// TestApplyConfigBadValueIsAnError checks a value that cannot be parsed is
// reported rather than leaving the flag at its default, and that every bad key
// is named so one pass fixes them all.
func TestApplyConfigBadValueIsAnError(t *testing.T) {
	path := writeConfig(t, "thresholds:\n  max-ecc: not-a-number\n  min-stars: many\nconcurrency:\n  workers: lots\n")
	v := mustReadConfig(t, path)

	cmd, _ := configTestAnalyzeCmd()
	err := applySections(cmd, v, path)
	if err == nil {
		t.Fatal("a non-numeric threshold was accepted, want an error")
	}
	for _, key := range []string{"thresholds.max-ecc", "thresholds.min-stars", "concurrency.workers"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not name %s", err, key)
		}
	}
}

// TestRepoConfigIsValid checks the shipped conf/garfield.yaml is complete and
// well-formed: every key the tool reads is present, nothing unrecognised is in
// there, and the values have the types the flags expect.
//
// The values themselves are deliberately NOT checked against
// defaultThresholds. Tuning a threshold is the whole point of shipping this
// file, and an earlier version of this test asserted equality with the compiled
// defaults, which made editing the config a test failure. The compiled defaults
// remain the fallback for when no config file exists; once one does, the file
// wins, and nothing should second-guess it.
func TestRepoConfigIsValid(t *testing.T) {
	v := mustReadConfig(t, "../"+configRelPath)

	for key, want := range map[string]string{
		"prepare.input":  defaultPrepareInput,
		"prepare.output": defaultPrepareOutput,
		"analyze.dir":    "images",
	} {
		if got := v.GetString(key); got != want {
			t.Errorf("%s = %q in %s, want %q", key, got, configRelPath, want)
		}
	}

	// Every key must be present, so deleting one cannot silently fall back to
	// the compiled default for a whole run.
	for _, key := range []string{
		"thresholds.min-snr", "thresholds.max-fwhm", "thresholds.max-ecc",
		"thresholds.min-score", "thresholds.min-stars",
		"concurrency.workers", "concurrency.conv-workers",
		"prepare.input", "prepare.output", "analyze.dir",
	} {
		if !v.IsSet(key) {
			t.Errorf("%s is missing from %s", key, configRelPath)
		}
	}

	// Types must line up with the flags, or the value is rejected at startup.
	for _, key := range []string{
		"thresholds.min-snr", "thresholds.max-fwhm", "thresholds.max-ecc", "thresholds.min-score",
	} {
		if _, ok := v.Get(key).(float64); !ok {
			t.Errorf("%s holds %T, want a number the float flags accept", key, v.Get(key))
		}
	}
	for _, key := range []string{"thresholds.min-stars", "concurrency.workers", "concurrency.conv-workers"} {
		if _, ok := v.Get(key).(int); !ok {
			t.Errorf("%s holds %T, want an int the integer flags accept", key, v.Get(key))
		}
	}

	if out := captureStderr(t, func() { warnUnknownKeys(v, "../"+configRelPath) }); out != "" {
		t.Errorf("the shipped config has unrecognised keys: %s", out)
	}
}

// TestConfigDoesNotRegisterFlags guards the mapping rule: keys are
// "<section>.<flag>" and nothing is renamed on the way in.
func TestConfigDoesNotRegisterFlags(t *testing.T) {
	cmd, _ := configTestAnalyzeCmd()
	before := map[string]bool{}
	cmd.Flags().VisitAll(func(f *pflag.Flag) { before[f.Name] = true })

	path := writeConfig(t, "thresholds:\n  max-ecc: 0.5\n")
	if err := applySections(cmd, mustReadConfig(t, path), path); err != nil {
		t.Fatalf("applySections: %v", err)
	}

	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !before[f.Name] {
			t.Errorf("applying the config registered a new flag --%s", f.Name)
		}
	})
}

// TestHelpReflectsConfigFile renders help through the root command's help
// function, the way cobra does for --help. cobra returns on the help flag
// before any PreRun hook runs, so without the help hook installed the config
// would never load and the rendered defaults would be the compiled-in ones
// rather than the ones in force.
func TestHelpReflectsConfigFile(t *testing.T) {
	path := writeConfig(t, "thresholds:\n  max-ecc: 0.5\n")

	// The real command is used on purpose: the wiring under test is the root
	// command's help function, which no throwaway command can reach. Its flag
	// state is restored afterwards so the rest of the suite is unaffected.
	restore := snapshotFlags(t, prepareCmd)
	t.Cleanup(restore)
	setConfigPath(t, path)

	var out bytes.Buffer
	prepareCmd.SetOut(&out)
	t.Cleanup(func() { prepareCmd.SetOut(nil) })

	rootCmd.HelpFunc()(prepareCmd, nil)

	help := out.String()
	if !strings.Contains(help, "(default 0.5)") {
		t.Errorf("help does not show the configured max-ecc:\n%s", help)
	}
	if !strings.Contains(help, configMarker) {
		t.Errorf("help does not mark max-ecc as coming from the config file:\n%s", help)
	}
}

// snapshotFlags records every flag value, DefValue and usage of cmd so a test
// that must run against a real command can put it back exactly as it found it.
func snapshotFlags(t *testing.T, cmd *cobra.Command) func() {
	t.Helper()

	type state struct {
		value    string
		defValue string
		usage    string
		changed  bool
	}
	saved := map[string]state{}

	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		saved[f.Name] = state{f.Value.String(), f.DefValue, f.Usage, f.Changed}
	})

	return func() {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			s, ok := saved[f.Name]
			if !ok {
				return
			}
			_ = f.Value.Set(s.value)
			f.DefValue = s.defValue
			f.Usage = s.usage
			f.Changed = s.changed
		})
	}
}

// setConfigPath points the root command at a config file for one test.
func setConfigPath(t *testing.T, path string) {
	t.Helper()
	prev := configPath
	configPath = path
	t.Cleanup(func() { configPath = prev })
}

func mustReadConfig(t *testing.T, path string) *viper.Viper {
	t.Helper()
	v, err := readConfig(path)
	if err != nil {
		t.Fatalf("readConfig(%s): %v", path, err)
	}
	return v
}

// chdir switches to dir for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}
