package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"garfield/internal/mrs"

	"github.com/spf13/cobra"
)

type measureOptions struct {
	dir       string
	input     string
	target    string
	filter    string
	output    string
	recursive bool
	moffat    bool
	limit     int
	workers   int
	conv      int
	quiet     bool

	// explicit records the flags the operator typed, so --target and --dir are
	// refused together without treating a configured --dir as an explicit one.
	explicit map[string]bool
}

var measureOpts = measureOptions{}

var measureCmd = &cobra.Command{
	Use:   "measure [dir]",
	Short: "Measure FITS frames and store the metrics",
	Long: `Walks a directory or a target tree and computes every frame's metrics,
storing them in the measurement cache. No gate, threshold or verdict is applied:
that is the job of analyze. The same store is what analyze and prepare read, so
this step is paid once and the tuning loop that follows reads it.

With --target the frames are looked for under <input>/Lights/<target>, as prepare
does. Without a target, --dir (or a positional directory) selects an arbitrary
directory.

Example:
  garfield measure --target LBN527 --input /Volumes/Dyno/Kosmodrom --output /Volumes/T7/Workspace
  garfield measure --dir images/ --recursive`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := measureOpts
		if len(args) == 1 {
			opts.dir = args[0]
		}
		opts.explicit = explicitFlags(cmd)
		return runMeasure(opts)
	},
}

func init() {
	measureCmd.Flags().StringVarP(&measureOpts.dir, "dir", "d", "images", "directory containing the FITS files")
	measureCmd.Flags().StringVarP(&measureOpts.input, "input", "i", defaultPrepareInput,
		"root of the acquisition data, used with --target")
	measureCmd.Flags().StringVarP(&measureOpts.target, "target", "t", "",
		"measure <input>/Lights/<target>, the same tree prepare reads; implies --recursive")
	measureCmd.Flags().StringVarP(&measureOpts.output, "output", "o", "",
		"destination root for the measurement store")
	measureCmd.Flags().StringVar(&measureOpts.filter, "filter", "",
		"only measure frames of this filter (one of LRGBSHO)")
	measureCmd.Flags().BoolVarP(&measureOpts.recursive, "recursive", "r", false,
		fmt.Sprintf("also walk the subdirectories of --dir, %d levels deep", maxRecursiveDepth))
	measureCmd.Flags().IntVar(&measureOpts.limit, "limit-computed-stars", analyzeDefaultLimitComputedStars,
		"limit the number of bright stars measured")
	measureCmd.Flags().BoolVar(&measureOpts.moffat, "moffat", defaultThresholds.moffat,
		"fit a Moffat PSF profile instead of a Gaussian (slower, FWHM agrees with PixInsight)")
	measureCmd.Flags().IntVarP(&measureOpts.workers, "workers", "w", 0,
		"processing workers (0 = auto: NumCPU/2)")
	measureCmd.Flags().IntVar(&measureOpts.conv, "conv-workers", 0,
		"inner convolution workers (0 = auto)")
	measureCmd.Flags().BoolVar(&measureOpts.quiet, "quiet", false, "suppress console output")
}

func runMeasure(opts measureOptions) error {
	if opts.output == "" {
		return fmt.Errorf("--output is required: measurements are stored under %s", cachePath("<output>"))
	}

	dir := opts.dir
	if opts.target != "" {
		if opts.explicit["dir"] {
			return fmt.Errorf("--target and --dir name the data two ways: --target scans <input>/%s/%s, --dir takes a directory",
				lightsDir, opts.target)
		}
		if opts.input == "" {
			return fmt.Errorf("--target requires --input: frames are looked for under <input>/%s/<target>", lightsDir)
		}
		dir = filepath.Join(opts.input, lightsDir, opts.target)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return fmt.Errorf("no %s/%s under %q", lightsDir, opts.target, opts.input)
		}
		opts.recursive = true
	}

	filter, err := normaliseFilter(opts.filter)
	if err != nil {
		return err
	}

	fitsFiles, truncated, err := discoverFITSFiles(dir, opts.recursive)
	if err != nil {
		return err
	}
	fitsFiles = framesForFilter(fitsFiles, filter)
	sortFramesByTime(fitsFiles)

	if len(fitsFiles) == 0 {
		if opts.recursive {
			return fmt.Errorf("no FITS file found in %q within %d subdirectory levels", dir, maxRecursiveDepth)
		}
		return fmt.Errorf("no FITS file found in %q; use -r to walk the subdirectories too", dir)
	}
	if truncated {
		fmt.Fprintf(os.Stderr,
			"  note: recursive search capped at %d levels; deeper subdirectories were not walked\n",
			maxRecursiveDepth)
	}
	if opts.recursive {
		for _, w := range warnDuplicateBasenames(fitsFiles) {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
		}
	}

	storeDir := opts.output
	if opts.target != "" {
		storeDir = targetMetricsDir(opts.output, opts.target)
	}
	cache, err := openMeasureCache(storeDir)
	if err != nil {
		return err
	}
	defer func() {
		// ClosePruned is the point of measure: the store is left with one record
		// per live frame, others' filters included, so it never grows sideways.
		if err := cache.ClosePruned(); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: measurement cache not saved: %v\n", err)
		}
	}()

	plan := resolveWorkers(opts.workers, opts.conv, len(fitsFiles),
		frameWorkingSetFor(fitsFiles[0]), physicalMemory())
	mrs.ConvWorkers = plan.Conv
	if warn := plan.workerWarning(opts.workers); warn != "" {
		fmt.Fprintf(os.Stderr, "  warning: %s\n", warn)
	}

	if !opts.quiet {
		scope := ""
		if opts.target != "" {
			scope = " of target " + opts.target
		}
		if filter != "" {
			scope += " for filter " + filter
		}
		fmt.Printf("\nMeasuring %d FITS file(s)%s in %q (%d workers, conv=%d)...\n\n",
			len(fitsFiles), scope, dir, plan.Outer, plan.Conv)
	}

	aopts := analyzeOptions{
		qualityThresholds:  defaultThresholds,
		limitComputedStars: opts.limit,
		workers:            opts.workers,
		convWorkers:        opts.conv,
	}
	aopts.moffat = opts.moffat

	results := measurePaths(fitsFiles, aopts, cache, plan.Outer, func(done, total int) {
		if !opts.quiet && (done%10 == 0 || done == total) {
			fmt.Printf("\r  measure : %d/%d image(s)", done, total)
		}
	})
	if !opts.quiet {
		fmt.Println()
		fmt.Println()
	}

	if !opts.quiet {
		layout := newConsoleLayout(results)
		for i, r := range results {
			label, known := frameLabel(r.Filename)
			if !known {
				label = r.Filename
			}
			if r.Error != nil {
				fmt.Printf("  [%0*d/%d] %s ... ERROR: %v\n",
					layout.counterWidth, i+1, len(results), label, r.Error)
				continue
			}
			fmt.Println(layout.measureRow(i+1, len(results), label, r))
		}
	}

	measured, reused, failed := 0, 0, 0
	for _, r := range results {
		switch {
		case r.Error != nil:
			failed++
		case r.Cached:
			reused++
		default:
			measured++
		}
	}
	if !opts.quiet {
		fmt.Printf("  cache: %d measured, %d reused, %d error(s) from %q\n",
			measured, reused, failed, cachePath(storeDir))
	}
	return nil
}
