package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

	f := c.Flags()
	f.StringVarP(&opts.dir, "dir", "d", "images", "dossier contenant les FITS")
	f.IntVarP(&opts.workers, "workers", "w", 0, "workers externes")
	f.IntVar(&opts.convWorkers, "conv-workers", 0, "workers de convolution internes")
	f.Float64Var(&opts.minSNR, "min-snr", 11.0, "")
	f.Float64Var(&opts.maxFWHM, "max-fwhm", 5.0, "")
	f.Float64Var(&opts.maxEcc, "max-ecc", 0.54, "")
	f.Float64Var(&opts.minScore, "min-score", 2.0, "")
	f.IntVar(&opts.minStars, "min-stars", 680, "")
	f.IntVar(&opts.limitComputedStars, "limit-computed-stars", 500, "")
	f.StringVar(&opts.format, "format", "console", "")
	f.StringVarP(&opts.output, "output", "o", "", "")
	f.BoolVar(&opts.quiet, "quiet", false, "")

	return c
}

// TestAnalyzeFlagDefaults reads the defaults off the production command so a
// change to any threshold is visible here rather than silently altering every
// frame's verdict.
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
			name:    "long flags",
			args:    []string{"--min-snr", "5.5", "--max-fwhm", "3.2", "--min-stars", "100"},
			wantDir: "images",
			want:    analyzeOptions{dir: "images", qualityThresholds: qualityThresholds{minSNR: 5.5, maxFWHM: 3.2, maxEcc: 0.54, minScore: 2, minStars: 100}, limitComputedStars: 500, format: "console"},
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
			want:    analyzeOptions{dir: "images", qualityThresholds: qualityThresholds{minSNR: 11, maxFWHM: 5, maxEcc: 0.54, minScore: 0.001, minStars: 680}, limitComputedStars: 500, format: "console"},
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

// TestAnalyzeRunEPropagatesErrors confirms a failure from runAnalyze reaches the
// caller, which is what makes Execute exit non-zero.
func TestAnalyzeRunEPropagatesErrors(t *testing.T) {
	wantErr := "boom"
	opts := analyzeOptions{}
	c := newAnalyzeCmd(&opts, func(string, analyzeOptions) error {
		return errFake2{wantErr}
	})
	c.SetArgs([]string{"/data"})
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	c.SilenceUsage = true
	c.SilenceErrors = true

	err := c.Execute()
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != wantErr {
		t.Errorf("error = %q, want %q", err.Error(), wantErr)
	}
}

// TestAnalyzeEndToEndThroughCommand runs the full command over a real directory
// of synthetic frames, confirming the flag plumbing reaches runAnalyze intact.
func TestAnalyzeEndToEndThroughCommand(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, frameName("B", 0), goodFrameOpts())
	writeFrame(t, dir, frameName("G", 1), goodFrameOpts())

	out := filepath.Join(dir, "result.csv")
	opts := analyzeOptions{}
	c := newAnalyzeCmd(&opts, func(d string, o analyzeOptions) error {
		return runAnalyze(d, o)
	})
	c.SetArgs([]string{
		dir,
		"--format", "csv",
		"--output", out,
		"--min-stars", "1",
		"--quiet",
	})
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	c.SilenceUsage = true
	c.SilenceErrors = true

	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	recs := readCSVFile(t, out)
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3 (header plus two frames)", len(recs))
	}
	for _, r := range recs[1:] {
		if r[11] != "" {
			t.Errorf("%s reported an error: %s", r[0], r[11])
		}
		if r[10] != decisionApproved {
			t.Errorf("%s decision = %q, want %q", r[0], r[10], decisionApproved)
		}
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

var _ = os.Stdout
