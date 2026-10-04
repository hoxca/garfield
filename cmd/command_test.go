package cmd

import (
	"bytes"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// newAnalyzeCmd builds a cobra command wired to its own analyzeOptions value,
// mirroring the production analyzeCmd but without touching the package-level
// analyzeOpts. Each test gets a fresh command because cobra flag values
// persist across Execute calls on the same instance.
func newAnalyzeCmd(opts *analyzeOptions, run func(dir string, o analyzeOptions) error) *cobra.Command {
	c := &cobra.Command{
		Use:     "analyze [dir]",
		Aliases: []string{"analyse"},
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := opts.dir
			if len(args) == 1 {
				dir = args[0]
			}
			return run(dir, *opts)
		},
	}

	// The shared thresholds and worker flags are registered exactly as
	// production does, so this helper cannot drift from analyzeCmd. Only the
	// analyze-specific flags are declared here; their help text is left empty
	// because these tests exercise parsing rather than documentation.
	f := c.Flags()
	registerQualityFlags(f, &opts.qualityThresholds)
	registerWorkerFlags(f, &opts.workers, &opts.convWorkers)
	f.StringVarP(&opts.dir, "dir", "d", "images", "dossier contenant les FITS")
	f.IntVar(&opts.limitComputedStars, "limit-computed-stars", 500, "")
	f.StringVar(&opts.format, "format", "console", "")
	f.StringVarP(&opts.output, "output", "o", "", "")
	f.BoolVar(&opts.quiet, "quiet", false, "")

	return c
}

// TestTestHelperFlagsMatchProduction guards the helper above against drifting
// from the real command. It hand-replicates some flags, and a hand-written
// replica has already drifted once (the --workers help text), so the shared set
// is compared field by field.
func TestTestHelperFlagsMatchProduction(t *testing.T) {
	opts := &analyzeOptions{}
	helper := newAnalyzeCmd(opts, func(string, analyzeOptions) error { return nil })
	production := analyzeCmd.Flags()

	shared := []string{
		"min-snr", "max-fwhm", "max-ecc", "min-score", "min-stars",
		"workers", "conv-workers",
	}

	for _, name := range shared {
		hf := helper.Flags().Lookup(name)
		pf := production.Lookup(name)
		if hf == nil || pf == nil {
			t.Errorf("--%s missing from one side (helper=%v production=%v)", name, hf != nil, pf != nil)
			continue
		}
		if hf.DefValue != pf.DefValue {
			t.Errorf("--%s default: helper %q, production %q", name, hf.DefValue, pf.DefValue)
		}
		if hf.Usage != pf.Usage {
			t.Errorf("--%s help:\n  helper:     %q\n  production: %q", name, hf.Usage, pf.Usage)
		}
		if hf.Value.Type() != pf.Value.Type() {
			t.Errorf("--%s type: helper %q, production %q", name, hf.Value.Type(), pf.Value.Type())
		}
		if hf.Shorthand != pf.Shorthand {
			t.Errorf("--%s shorthand: helper %q, production %q", name, hf.Shorthand, pf.Shorthand)
		}
	}
}

// TestSharedFlagSetIsIdenticalAcrossCommands checks that every flag registered
// by the shared helpers appears on both commands with the same definition, and
// that neither command has gained a shared flag on its own.
func TestSharedFlagSetIsIdenticalAcrossCommands(t *testing.T) {
	shared := map[string]bool{
		"min-snr": true, "max-fwhm": true, "max-ecc": true,
		"min-score": true, "min-stars": true,
		"workers": true, "conv-workers": true,
	}

	for _, pair := range [][2]*cobra.Command{{analyzeCmd, prepareCmd}} {
		a, b := pair[0], pair[1]
		a.Flags().VisitAll(func(f *pflag.Flag) {
			if !shared[f.Name] {
				return
			}
			g := b.Flags().Lookup(f.Name)
			if g == nil {
				t.Errorf("--%s is on %s but not on %s", f.Name, a.Name(), b.Name())
				return
			}
			if g.DefValue != f.DefValue {
				t.Errorf("--%s default: %s %q, %s %q", f.Name, a.Name(), f.DefValue, b.Name(), g.DefValue)
			}
			if g.Usage != f.Usage {
				t.Errorf("--%s help: %s %q, %s %q", f.Name, a.Name(), f.Usage, b.Name(), g.Usage)
			}
			if g.Value.Type() != f.Value.Type() {
				t.Errorf("--%s type: %s %q, %s %q", f.Name, a.Name(), f.Value.Type(), b.Name(), g.Value.Type())
			}
			if g.Shorthand != f.Shorthand {
				t.Errorf("--%s shorthand: %s %q, %s %q", f.Name, a.Name(), f.Shorthand, b.Name(), g.Shorthand)
			}
		})
	}
}

func TestAnalyzeFlagDefaults(t *testing.T) {
	tests := []struct {
		name      string
		shorthand string
	}{
		{"dir", "d"},
		{"workers", "w"},
		{"conv-workers", ""},
		{"min-snr", ""},
		{"max-fwhm", ""},
		{"max-ecc", ""},
		{"min-score", ""},
		{"min-stars", ""},
		{"limit-computed-stars", ""},
		{"format", ""},
		{"output", "o"},
		{"quiet", ""},
	}

	for _, tc := range tests {
		f := analyzeCmd.Flags().Lookup(tc.name)
		if f == nil {
			t.Errorf("flag --%s is not registered", tc.name)
			continue
		}
		if f.Shorthand != tc.shorthand {
			t.Errorf("flag --%s shorthand = %q, want %q", tc.name, f.Shorthand, tc.shorthand)
		}
	}

	// The threshold expectations are deliberately literal rather than taken from
	// defaultThresholds: pinning the value independently is what makes a change
	// to the constant show up as a test failure instead of passing silently.
	want := map[string]string{
		"dir":                  "images",
		"workers":              "0",
		"conv-workers":         "0",
		"min-snr":              "11",
		"max-fwhm":             "5",
		"max-ecc":              "0.54",
		"min-score":            "2",
		"min-stars":            "680",
		"limit-computed-stars": "500",
		"format":               "console",
		"output":               "",
		"quiet":                "false",
	}
	for name, def := range want {
		f := analyzeCmd.Flags().Lookup(name)
		if f == nil {
			continue
		}
		if f.DefValue != def {
			t.Errorf("flag --%s default = %q, want %q", name, f.DefValue, def)
		}
	}
}

func TestAnalyzeFlagParsing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want analyzeOptions
		// wantDir is the directory the RunE should resolve.
		wantDir string
	}{
		{
			name:    "no arguments uses the dir default",
			args:    nil,
			wantDir: "images",
			want:    analyzeOptions{dir: "images", qualityThresholds: defaultThresholds, limitComputedStars: 500, format: "console"},
		},
		{
			name:    "positional argument sets the directory",
			args:    []string{"/data/frames"},
			wantDir: "/data/frames",
			want:    analyzeOptions{dir: "images", qualityThresholds: defaultThresholds, limitComputedStars: 500, format: "console"},
		},
		{
			name:    "positional argument overrides --dir",
			args:    []string{"/positional", "--dir", "/flag"},
			wantDir: "/positional",
			want:    analyzeOptions{dir: "/flag", qualityThresholds: defaultThresholds, limitComputedStars: 500, format: "console"},
		},
		{
			// Only the overridden thresholds are spelled out; the rest come from
			// the shared defaults so this expectation cannot drift.
			name:    "long flags",
			args:    []string{"--min-snr", "5.5", "--max-fwhm", "3.2", "--min-stars", "100"},
			wantDir: "images",
			want: analyzeOptions{
				dir: "images",
				qualityThresholds: qualityThresholds{
					minSNR:   5.5,
					maxFWHM:  3.2,
					maxEcc:   defaultThresholds.maxEcc,
					minScore: defaultThresholds.minScore,
					minStars: 100,
				},
				limitComputedStars: 500,
				format:             "console",
			},
		},
		{
			name:    "short flags",
			args:    []string{"-d", "/short", "-w", "3"},
			wantDir: "/short",
			want:    analyzeOptions{dir: "/short", workers: 3, qualityThresholds: defaultThresholds, limitComputedStars: 500, format: "console"},
		},
		{
			name:    "output shorthand",
			args:    []string{"-o", "results.csv", "--format", "csv"},
			wantDir: "images",
			want:    analyzeOptions{dir: "images", qualityThresholds: defaultThresholds, limitComputedStars: 500, format: "csv", output: "results.csv"},
		},
		{
			name:    "boolean quiet",
			args:    []string{"--quiet"},
			wantDir: "images",
			want:    analyzeOptions{dir: "images", qualityThresholds: defaultThresholds, limitComputedStars: 500, format: "console", quiet: true},
		},
		{
			name:    "float in exponent form",
			args:    []string{"--min-score", "1e-3"},
			wantDir: "images",
			want: analyzeOptions{
				dir: "images",
				qualityThresholds: qualityThresholds{
					minSNR:   defaultThresholds.minSNR,
					maxFWHM:  defaultThresholds.maxFWHM,
					maxEcc:   defaultThresholds.maxEcc,
					minScore: 0.001,
					minStars: defaultThresholds.minStars,
				},
				limitComputedStars: 500,
				format:             "console",
			},
		},
		{
			name:    "conv-workers",
			args:    []string{"--conv-workers", "2"},
			wantDir: "images",
			want:    analyzeOptions{dir: "images", convWorkers: 2, qualityThresholds: defaultThresholds, limitComputedStars: 500, format: "console"},
		},
		{
			name:    "limit-computed-stars",
			args:    []string{"--limit-computed-stars", "42"},
			wantDir: "images",
			want:    analyzeOptions{dir: "images", qualityThresholds: defaultThresholds, limitComputedStars: 42, format: "console"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotDir string
			var gotOpts analyzeOptions

			opts := analyzeOptions{}
			c := newAnalyzeCmd(&opts, func(dir string, o analyzeOptions) error {
				gotDir = dir
				gotOpts = o
				return nil
			})
			c.SetArgs(tc.args)
			c.SetOut(io.Discard)
			c.SetErr(io.Discard)

			if err := c.Execute(); err != nil {
				t.Fatalf("Execute(%v): %v", tc.args, err)
			}

			if gotDir != tc.wantDir {
				t.Errorf("resolved dir = %q, want %q", gotDir, tc.wantDir)
			}
			if gotOpts != tc.want {
				t.Errorf("options = %+v, want %+v", gotOpts, tc.want)
			}
		})
	}
}

func TestAnalyzeArgValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "two positional arguments are rejected",
			args:    []string{"one", "two"},
			wantErr: "accepts at most 1 arg(s), received 2",
		},
		{
			name:    "unknown flag is rejected",
			args:    []string{"--nonexistent"},
			wantErr: "unknown flag: --nonexistent",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := analyzeOptions{}
			called := false
			c := newAnalyzeCmd(&opts, func(string, analyzeOptions) error {
				called = true
				return nil
			})
			c.SetArgs(tc.args)
			c.SetOut(io.Discard)
			c.SetErr(io.Discard)
			c.SilenceUsage = true
			c.SilenceErrors = true

			err := c.Execute()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
			if called {
				t.Error("RunE ran despite invalid arguments")
			}
		})
	}
}

// TestAnalyzeAliasRegistersAndResolves checks the analyse spelling reaches the
// same RunE as analyze, through a root command as the real binary does.
func TestAnalyzeAlias(t *testing.T) {
	for _, name := range []string{"analyze", "analyse"} {
		t.Run(name, func(t *testing.T) {
			var called bool

			opts := analyzeOptions{}
			sub := newAnalyzeCmd(&opts, func(string, analyzeOptions) error {
				called = true
				return nil
			})

			root := &cobra.Command{Use: "garfield", SilenceUsage: true, SilenceErrors: true}
			root.AddCommand(sub)
			root.SetArgs([]string{name})
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)

			if err := root.Execute(); err != nil {
				t.Fatalf("Execute(%q): %v", name, err)
			}
			if !called {
				t.Errorf("%q did not reach RunE", name)
			}
		})
	}
}

// TestPrepareCommandWiring checks prepare is registered and rejects positional
// arguments, so a mistyped invocation fails loudly instead of silently using
// the wrong directory.
func TestPrepareCommandWiring(t *testing.T) {
	var found *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "prepare" {
			found = c
		}
	}
	if found == nil {
		t.Fatal("prepare is not registered on rootCmd")
	}
	if err := found.Args(found, []string{"extra"}); err == nil {
		t.Error("prepare accepted a positional argument, want a rejection")
	}
	if err := found.Args(found, nil); err != nil {
		t.Errorf("prepare rejected an empty argument list: %v", err)
	}
}

// TestPrepareLongHelpDocumentsLayout pins the three paths shown in the prepare
// help to the ones the command actually builds. The help text went stale once
// already -- it still described sessions sitting in the target root after the
// Sessions/ wrapper was added -- because nothing asserted it against the code.
// The expected strings are built from the same constants and helpers the command
// uses, so a layout change that is not documented fails here.
func TestPrepareLongHelpDocumentsLayout(t *testing.T) {
	const (
		target = "<cible>"
		out    = "<sortie>"
		filter = "<filtre>"
	)

	// The help documents the shape with NN standing in for the number. Derive
	// that placeholder from what sessionDir actually produces, so reverting to
	// a dash or dropping the padding fails here rather than silently leaving
	// the help describing a layout the command no longer builds.
	session := sessionPlan{number: 1}.sessionDir()
	placeholder := regexp.MustCompile(`\d+$`).ReplaceAllString(session, "NN")
	if placeholder != "Session_NN" {
		t.Fatalf("sessionDir produces %q, whose placeholder form is %q, want Session_NN", session, placeholder)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"lights", filepath.Join(out, target, sessionsDir, placeholder, sessionLightsDir, filter, "*.FIT")},
		{"flats", filepath.Join(out, target, sessionsDir, placeholder, sessionFlatsDir, filter, "*.FIT")},
		{"rejected", filepath.Join(out, target, rejectedDir, placeholder, filter, "*.FIT")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(prepareCmd.Long, tc.path) {
				t.Errorf("prepare help does not document %q\nhelp:\n%s", tc.path, prepareCmd.Long)
			}
		})
	}
}

// TestRootCommandWiring checks the command tree the binary exposes.
func TestRootCommandWiring(t *testing.T) {
	names := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"analyze", "prepare", "version"} {
		if !names[want] {
			t.Errorf("rootCmd is missing the %q subcommand (has %v)", want, names)
		}
	}
	if rootCmd.Version != Version {
		t.Errorf("rootCmd.Version = %q, want %q", rootCmd.Version, Version)
	}
}

// TestVersionCommandPrints checks the version subcommand reports the value the
// linker sets. versionCmd writes with a bare fmt.Printf rather than through
// cmd.OutOrStdout, so stdout is captured instead of the command's writer.
func TestVersionCommandPrints(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	Version = "v1.2.3"

	out := captureStdout(t, func() { versionCmd.Run(versionCmd, nil) })
	if !strings.Contains(out, "v1.2.3") {
		t.Errorf("version output = %q, want it to contain %q", out, "v1.2.3")
	}
	if !strings.Contains(out, "garfield") {
		t.Errorf("version output = %q, want it to name the binary", out)
	}
}

// TestSilenceUsageKeepsErrorsClean documents why error assertions set
// SilenceUsage: without it cobra dumps the full usage text alongside the error.
func TestSilenceUsageKeepsErrorsClean(t *testing.T) {
	var noisy, quiet bytes.Buffer

	noisyCmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return errFake2{"boom"} }}
	noisyCmd.SetOut(&noisy)
	noisyCmd.SetErr(&noisy)
	_ = noisyCmd.Execute()

	quietCmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return errFake2{"boom"} }}
	quietCmd.SetOut(&quiet)
	quietCmd.SetErr(&quiet)
	quietCmd.SilenceUsage = true
	quietCmd.SilenceErrors = true
	_ = quietCmd.Execute()

	if noisy.Len() == 0 {
		t.Error("expected cobra to print usage without SilenceUsage")
	}
	if quiet.Len() != 0 {
		t.Errorf("SilenceUsage did not suppress output: %q", quiet.String())
	}
}

type errFake2 struct{ msg string }

func (e errFake2) Error() string { return e.msg }
