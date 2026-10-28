package cmd

import (
	"encoding/csv"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"garfield/internal/mrs"
	"garfield/internal/readfits"
	"garfield/internal/starsmetrics"

	"github.com/spf13/cobra"
)

type analyzeOptions struct {
	qualityThresholds

	dir string
	// recursive extends the search to subdirectories of dir. Off by default, so
	// pointing analyze at a directory containing frames analyses exactly those.
	recursive          bool
	workers            int
	convWorkers        int
	limitComputedStars int
	format             string
	// output is a directory: the report is written inside it as
	// analyseReportName, and rejected frames are copied beneath it when
	// copyRejected is set. It is not a CSV path any more.
	output       string
	copyRejected bool
	quiet        bool
}

var analyzeOpts = analyzeOptions{}

var analyzeCmd = &cobra.Command{
	Use:     "analyze [dir]",
	Aliases: []string{"analyse"},
	Short:   "Analyse a directory of FITS images",
	Long: `Walks a directory, detects .fit/.fits/.fts files
and computes FWHM, eccentricity, SNR and score for each.

score = snr/fwhm*(1-ecc)

Example:
  garfield analyze images/
  garfield analyze --dir images/ --limit-computed-stars 500
  garfield analyze --dir images/ --format csv --output out/
  garfield analyze --dir images/ --format both --output out/ --quiet
  garfield analyze --dir images/ --copy-rejected --output out/`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := analyzeOpts.dir
		if len(args) == 1 {
			dir = args[0]
		}
		return runAnalyze(dir, analyzeOpts)
	},
}

func init() {
	analyzeCmd.Flags().StringVarP(&analyzeOpts.dir, "dir", "d", "images", "directory containing the FITS files")
	analyzeCmd.Flags().BoolVarP(&analyzeOpts.recursive, "recursive", "r", false,
		fmt.Sprintf("also walk the subdirectories of --dir, %d levels deep", maxRecursiveDepth))
	registerWorkerFlags(analyzeCmd.Flags(), &analyzeOpts.workers, &analyzeOpts.convWorkers)
	registerQualityFlags(analyzeCmd.Flags(), &analyzeOpts.qualityThresholds)
	analyzeCmd.Flags().IntVar(&analyzeOpts.limitComputedStars, "limit-computed-stars", 500, "limit the number of bright stars measured")
	analyzeCmd.Flags().StringVar(&analyzeOpts.format, "format", "console", "output format: console, csv or both")
	analyzeCmd.Flags().StringVarP(&analyzeOpts.output, "output", "o", "",
		"output directory for the report and copied rejected frames")
	analyzeCmd.Flags().BoolVar(&analyzeOpts.copyRejected, "copy-rejected", false,
		"copy rejected frames into <output>/rejected/<category>/")
	analyzeCmd.Flags().BoolVar(&analyzeOpts.quiet, "quiet", false, "suppress console output")
}

// ImageResult holds the quality metrics for one FITS file.
type ImageResult struct {
	Filename        string
	Filter          string
	Date            string
	DetectedStars   int
	StarCount       int
	AvgFWHM         float64
	AvgSignal       float64
	AvgEccentricity float64
	SNR             float64
	Score           float64
	// Background is the median sky level over the frame, in physical ADU:
	// readfits applies BSCALE/BZERO, so this is not offset the way raw stored
	// integers are. Noise is the per-pixel noise sigma the background/noise
	// estimator reports. BgStructure is the median absolute deviation of 64x64
	// block medians divided by Background: how much the sky varies across the
	// frame at scales far larger than a star. It is the observable behind the
	// cloud question -- patchy cirrus raises it, a uniformly brighter sky does
	// not -- and is reported as data, not acted on.
	Background  float64
	Noise       float64
	BgStructure float64
	// FWHMRatio is the frame's FWHM relative to the median of every frame of
	// the same filter in the run. The FWHM gate compares the value itself
	// against a robust sigma bound of that population; the ratio is reported so
	// a rejection can be read against the reference directly.
	FWHMRatio float64
	// Halo is the median wing brightness of the brightest stars, in ADU above
	// the local sky, and HaloRatio is that value relative to the 25th
	// percentile of its session. HaloRatio is what the cloud rule compares:
	// thin cloud scatters starlight into an aureole around bright stars, so the
	// wings grow while the core and the PSF width barely move.
	Halo      float64
	HaloRatio float64
	// StructRatio is the frame's bgStructure relative to its session. It is
	// reported and gated separately as bad seeing: a strongly structured sky
	// produces frames that no amount of stacking repairs.
	StructRatio float64
	Decision    string
	Error       error
}

const (
	decisionApproved  = "APPROVED"
	decisionNoStars   = "REJECTED NO STARS"
	decisionEcc       = "REJECTED ECCENTRICITY"
	decisionFWHM      = "REJECTED FWHM"
	decisionSNR       = "REJECTED SNR"
	decisionScoring   = "REJECTED SCORING"
	decisionClouds    = "REJECTED CLOUDS"
	decisionBadSeeing = "REJECTED BAD SEEING"
)

func parseFilename(name string) (filter string, date string) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "_")
	if len(parts) >= 3 {
		filter = parts[2]
	}
	if len(parts) >= 8 {
		raw := parts[7]
		if len(raw) >= 8 {
			date = raw[:4] + "-" + raw[4:6] + "-" + raw[6:8]
		}
	}
	return
}

// fitBoxScale sets the half-width of the box the star profile is fitted in, as
// a multiple of the detected width.
//
// The box only has to contain the core and enough of the wings for the fit to
// see the profile; it is not the measurement aperture it used to be. Too large
// and a nebula gradient or a neighbour pulls the shape, too small and the
// Gaussian is truncated. Twice the detected FWHM is comfortable for both.
const fitBoxScale = 2.0

// fitBoxMin and fitBoxMax bound the half-width. The floor keeps an undersampled
// star from being fitted in a handful of pixels; the ceiling keeps a broad or
// contaminated one from reaching into the field.
const (
	fitBoxMin = 4
	fitBoxMax = 10
)

// fitBoxRadius is the half-width of the fitting box for a star of the detected
// width.
func fitBoxRadius(fwhm float64) int {
	r := int(math.Round(fitBoxScale * fwhm))
	if r < fitBoxMin {
		return fitBoxMin
	}
	if r > fitBoxMax {
		return fitBoxMax
	}
	return r
}

func processImage(path string, opts analyzeOptions) ImageResult {
	name := filepath.Base(path)
	filter, date := parseFilename(name)

	pixels, err := readfits.ReadFITSImage(path)
	if err != nil {
		return ImageResult{Filename: name, Filter: filter, Date: date, Error: err}
	}

	scales := 5
	mrsResult := mrs.EstimateBackgroundAndNoiseBinned(pixels, scales)
	bgMap := mrsResult.Background
	skyNoise := mrsResult.NoiseSigma
	bgLevel, bgStructure := backgroundStats(bgMap, skyNoise)

	fieldStars := starsmetrics.FindStars(pixels, bgMap, skyNoise)

	sort.Slice(fieldStars, func(i, j int) bool {
		return fieldStars[i].TotalFlux > fieldStars[j].TotalFlux
	})

	// The halo is measured over the full field, including the brightest stars
	// the PSF metrics skip: the aureole cloud produces lives around exactly
	// those stars, and a frame with too few ordinary stars to score may still
	// carry a usable cloud signature.
	halo, _ := starsmetrics.MeasureHalo(pixels, fieldStars, skyNoise, bgLevel)

	if len(fieldStars) == 0 {
		return ImageResult{
			Filename: name, Filter: filter, Date: date,
			Background: bgLevel, Noise: skyNoise, BgStructure: bgStructure,
			Error: fmt.Errorf("no star found"),
		}
	}

	targetCount := opts.limitComputedStars
	if len(fieldStars) < targetCount {
		targetCount = len(fieldStars)
	}

	// Fixed skip of the brightest stars (saturated / non-representative).
	// Skip expensive per-star metrics (eccentricity) when the usable pool
	// is already under minUsableStars, and guard sparse fields from degenerate 0/Inf metrics.
	const brightSkip = 20
	const minUsableStars = 80

	if targetCount-brightSkip < minUsableStars {
		return ImageResult{
			Filename:        name,
			Filter:          filter,
			Date:            date,
			DetectedStars:   len(fieldStars),
			StarCount:       0,
			AvgFWHM:         0,
			AvgSignal:       0,
			AvgEccentricity: 0,
			SNR:             mrsResult.ImageSNR,
			Score:           0,
			Background:      bgLevel,
			Noise:           skyNoise,
			BgStructure:     bgStructure,
			Halo:            halo,
			Decision:        decisionNoStars,
		}
	}

	var sumWeight float64
	var weightedFWHM, weightedEcc float64
	var filteredCount int
	var totalPeakSignal float64

	for i := brightSkip; i < targetCount; i++ {
		if fieldStars[i].Signal < 50.0*skyNoise {
			continue
		}
		// The shape comes from a PSF fit, not from image moments. Moments over a
		// box bias both width and ellipticity whenever the PSF spans only a
		// couple of pixels: on NGC281 the moments read FWHM 1.96 and
		// eccentricity 0.64 where PixInsight's PSF fit reads 1.24 and 0.44 for
		// the same frames.
		fit, okFit := starsmetrics.FitStarProfile(
			pixels, bgMap, fieldStars[i].X, fieldStars[i].Y, fitBoxRadius(fieldStars[i].FWHM), opts.moffat)
		if !okFit {
			continue
		}
		fwhm := fit.FWHM
		ecc := fit.Eccentricity
		if math.IsNaN(ecc) || math.IsNaN(fwhm) {
			continue
		}
		w := fieldStars[i].TotalFlux
		weightedFWHM += fwhm * w
		weightedEcc += ecc * w
		totalPeakSignal += fieldStars[i].Signal
		sumWeight += w
		filteredCount++
	}

	// Every star failed the per-star filters (peak signal below the noise
	// floor, or degenerate eccentricity). The metric averages below would divide 0/0 and produce NaN, and since all NaN comparisons are false
	// that NaN would silently bypass the FWHM/eccentricity/scoring gates and mark the frame APPROVED. Reject before computing any metric.
	if filteredCount == 0 || sumWeight <= 0 {
		return ImageResult{
			Filename:      name,
			Filter:        filter,
			Date:          date,
			DetectedStars: len(fieldStars),
			StarCount:     0,
			SNR:           mrsResult.ImageSNR,
			Background:    bgLevel,
			Noise:         skyNoise,
			BgStructure:   bgStructure,
			Halo:          halo,
			Decision:      decisionNoStars,
		}
	}

	avgFWHM := weightedFWHM / sumWeight
	avgEcc := weightedEcc / sumWeight
	avgPeakSignal := totalPeakSignal / float64(filteredCount)

	snrLinear := mrsResult.ImageSNR
	finalScore := (snrLinear / avgFWHM) * (1.0 - avgEcc)

	decision := decisionApproved
	if len(fieldStars) < opts.minStars {
		decision = decisionNoStars
	} else if avgEcc > opts.maxEcc {
		decision = decisionEcc
	} else if avgFWHM > opts.maxFWHM {
		decision = decisionFWHM
	} else if snrLinear < opts.minSNR {
		decision = decisionSNR
	} else if finalScore < opts.minScore {
		decision = decisionScoring
	}

	return ImageResult{
		Filename:        name,
		Filter:          filter,
		Date:            date,
		DetectedStars:   len(fieldStars),
		StarCount:       filteredCount,
		AvgFWHM:         avgFWHM,
		AvgSignal:       avgPeakSignal,
		AvgEccentricity: avgEcc,
		SNR:             snrLinear,
		Score:           finalScore,
		Background:      bgLevel,
		Noise:           skyNoise,
		BgStructure:     bgStructure,
		Halo:            halo,
		Decision:        decision,
	}
}

// csvSeparator matches the convention used by the reference tool's report
// (data/LBN527_result.csv), so garfield output can be read alongside it
// directly.
const csvSeparator = ';'

// newCSVWriter returns a CSV writer using garfield's separator. Every writer in
// the tool goes through here so the convention cannot drift between commands.
func newCSVWriter(w io.Writer) *csv.Writer {
	cw := csv.NewWriter(w)
	cw.Comma = csvSeparator
	return cw
}

// newCSVReader returns a CSV reader for garfield's output. Kept beside
// newCSVWriter so the two stay in step.
func newCSVReader(r io.Reader) *csv.Reader {
	cr := csv.NewReader(r)
	cr.Comma = csvSeparator
	return cr
}

// imageResultHeader names the metric columns shared by analyze's CSV and
// prepare's frames.csv.
// background, noise and bgStructure are appended at the end so the position of
// every pre-existing column is unchanged for anything already reading this file.
var imageResultHeader = []string{"filename", "filter", "date", "detectedStars", "starCount", "avgFWHM", "avgSignal", "avgEccentricity", "snr", "score", "decision", "error", "background", "noise", "bgStructure", "halo", "haloRatio", "structRatio", "fwhmRatio"}

// imageResultRow renders one result as imageResultHeader columns. Both
// commands go through here so their metric columns and number formatting cannot
// drift apart.
func imageResultRow(r ImageResult) []string {
	errStr := ""
	if r.Error != nil {
		errStr = r.Error.Error()
	}
	return []string{
		r.Filename,
		r.Filter,
		r.Date,
		strconv.Itoa(r.DetectedStars),
		strconv.Itoa(r.StarCount),
		strconv.FormatFloat(r.AvgFWHM, 'f', 4, 64),
		strconv.FormatFloat(r.AvgSignal, 'f', 4, 64),
		strconv.FormatFloat(r.AvgEccentricity, 'f', 4, 64),
		strconv.FormatFloat(r.SNR, 'f', 4, 64),
		strconv.FormatFloat(r.Score, 'f', 4, 64),
		r.Decision,
		errStr,
		strconv.FormatFloat(r.Background, 'f', 4, 64),
		strconv.FormatFloat(r.Noise, 'f', 6, 64),
		strconv.FormatFloat(r.BgStructure, 'f', 6, 64),
		strconv.FormatFloat(r.Halo, 'f', 2, 64),
		strconv.FormatFloat(r.HaloRatio, 'f', 4, 64),
		strconv.FormatFloat(r.StructRatio, 'f', 4, 64),
		strconv.FormatFloat(r.FWHMRatio, 'f', 4, 64),
	}
}

func writeResultsCSV(results []ImageResult, w *csv.Writer) error {
	if err := w.Write(imageResultHeader); err != nil {
		return err
	}
	for _, r := range results {
		if err := w.Write(imageResultRow(r)); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// maxRecursiveDepth is how many levels of subdirectory -r descends. The
// acquisition stores a frame as <target>/<YYYY-MM-DD>/<FILTER>/<frame>.FIT, two
// levels below Lights/<target>, and no frame anywhere in the tree sits deeper --
// every one of the sixteen targets measures exactly two. So this covers a target
// in full, while stopping the scan from wandering into an unrelated subtree.
const maxRecursiveDepth = 2

// discoverFITSFiles collects the FITS frames in dir, descending into
// subdirectories only when recursive is set. The default is deliberately flat:
// analysing a directory should mean the frames in it, and reaching into the tree
// is opt-in so a stray frame below the chosen directory cannot quietly join the
// report. A recursive scan stops at maxRecursiveDepth and says so when it does.
//
// Dotfiles are skipped in both modes, through isFITSName. macOS writes
// AppleDouble sidecars named "._<frame>.FIT" beside every frame on an exFAT
// volume, and they carry a FITS extension, so without this they would all be
// analysed and each would fail as unreadable, filling the report with error
// rows. They appear at every level, not only in subdirectories, so the flat scan
// needs the rule too. prepare applies the same one.
func discoverFITSFiles(dir string, recursive bool) ([]string, bool, error) {
	if recursive {
		return walkFITSFiles(dir)
	}
	fitsFiles, err := topLevelFITSFiles(dir)
	return fitsFiles, false, err
}

// topLevelFITSFiles collects the frames sitting directly in dir.
func topLevelFITSFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("unable to read directory %q: %w", dir, err)
	}

	var fitsFiles []string
	for _, e := range entries {
		// A directory named like a frame is not a frame.
		if e.IsDir() || !isFITSName(e.Name()) {
			continue
		}
		fitsFiles = append(fitsFiles, filepath.Join(dir, e.Name()))
	}
	return fitsFiles, nil
}

// walkFITSFiles collects the frames in dir and its subdirectories, down to
// maxRecursiveDepth levels. dirLevel measures the scan root as 0, so a frame in
// dir/a/b is at depth 2 and one in dir/a/b/c at depth 3.
//
// Directories are cut on their own depth rather than on the frames inside them:
// the directory at depth 3 is skipped whole, which keeps its depth-3 frames out
// while leaving the depth-2 frames of dir/a/b collected.
//
// Two further exclusions. Directories named like a frame are never collected,
// which covers the trap of a directory called "trap.FIT". Symlinked directories
// are not descended, matching filepath.WalkDir: following them risks cycles, and
// the acquisition volumes hold none. A symlink to a frame file is still
// analysed; only directories are left alone.
//
// The second return value reports whether the depth cap stopped the walk, so the
// caller can say the search was bounded rather than complete.
func walkFITSFiles(dir string) ([]string, bool, error) {
	var (
		fitsFiles    []string
		wasTruncated bool
	)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && dirLevel(dir, path) > maxRecursiveDepth {
				wasTruncated = true
				return fs.SkipDir
			}
			return nil
		}
		if !isFITSName(d.Name()) {
			return nil
		}
		fitsFiles = append(fitsFiles, path)
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("unable to read directory %q: %w", dir, err)
	}
	return fitsFiles, wasTruncated, nil
}

// dirLevel counts the subdirectory levels between the scan root and path, the
// root itself being 0.
func dirLevel(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

// warnDuplicateBasenames reports frames whose names collide across directories.
// Only meaningful for a recursive scan, which is the only one that can reach the
// same basename twice.
//
// The report records the bare filename, so two frames called the same thing in
// different subdirectories produce two indistinguishable rows. Real acquisition
// names embed target, filter, date, time and position angle and so never
// collide, but a hand-assembled tree can, and the ambiguity is worth surfacing
// rather than leaving to be noticed later.
func warnDuplicateBasenames(fitsFiles []string) []string {
	seen := map[string]bool{}
	dupes := map[string]bool{}

	for _, path := range fitsFiles {
		base := filepath.Base(path)
		if seen[base] {
			dupes[base] = true
			continue
		}
		seen[base] = true
	}

	names := make([]string, 0, len(dupes))
	for n := range dupes {
		names = append(names, n)
	}
	sort.Strings(names)

	warnings := make([]string, 0, len(names))
	for _, n := range names {
		warnings = append(warnings, fmt.Sprintf(
			"several images are named %q in different subdirectories; "+
				"the report distinguishes them only by position", n))
	}
	return warnings
}

func runAnalyze(dir string, opts analyzeOptions) error {
	switch opts.format {
	case "console", "csv", "both":
	default:
		return fmt.Errorf("invalid format %q: expected console, csv or both", opts.format)
	}
	if opts.format == "both" && opts.output == "" {
		return fmt.Errorf("--output is required with --format both")
	}
	if opts.copyRejected && opts.output == "" {
		return fmt.Errorf("--copy-rejected requires --output")
	}

	fitsFiles, truncated, err := discoverFITSFiles(dir, opts.recursive)
	if err != nil {
		return err
	}

	// WalkDir visits each directory in lexical order, but that is per directory,
	// so a/z.FIT is still reached before b/a.FIT. Sorting the full paths is what
	// makes CSV rows come out alphabetically rather than in completion order,
	// which is otherwise nondeterministic across runs. The flat scan reads a
	// single directory, so this only restores that ordering for it.
	sort.Strings(fitsFiles)

	if len(fitsFiles) == 0 {
		// The flat case is where someone pointed at the wrong level of a tree,
		// so it names the flag rather than just reporting an empty directory.
		if opts.recursive {
			// Not "nor its subdirectories": with a depth cap that would be
			// false, since frames can sit below what was searched.
			return fmt.Errorf(
				"no FITS file found in %q within %d subdirectory levels",
				dir, maxRecursiveDepth)
		}
		return fmt.Errorf(
			"no FITS file found in %q; use -r to walk the subdirectories too", dir)
	}

	// Said whenever the cap actually cut the walk short, so a bounded search is
	// never mistaken for a complete one.
	if truncated {
		fmt.Fprintf(os.Stderr,
			"  note: recursive search capped at %d levels; deeper subdirectories were not walked\n",
			maxRecursiveDepth)
	}

	// Outer workers scale with CPUs; inner convolution workers are divided
	// accordingly so total threads stay close to NumCPU instead of
	// oversubscribing (outer x NumCPU as before).
	numCPU := runtime.NumCPU()
	outerWorkers := opts.workers
	if outerWorkers <= 0 {
		outerWorkers = numCPU / 2
		if outerWorkers < 2 {
			outerWorkers = 2
		}
	}
	if outerWorkers > len(fitsFiles) {
		outerWorkers = len(fitsFiles)
	}
	convWorkers := opts.convWorkers
	if convWorkers <= 0 {
		convWorkers = numCPU / outerWorkers
		if convWorkers < 1 {
			convWorkers = 1
		}
	}
	mrs.ConvWorkers = convWorkers

	// Only reachable when recursing: within a single directory names are
	// unique, so a flat scan cannot produce a collision.
	if opts.recursive {
		for _, w := range warnDuplicateBasenames(fitsFiles) {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
		}
	}

	showConsole := opts.format == "console" || opts.format == "both"
	csvToStdout := opts.format == "csv" && opts.output == ""
	reportPath := ""
	if opts.output != "" {
		reportPath = filepath.Join(opts.output, analyseReportName)
	}
	if !opts.quiet && showConsole {
		fmt.Printf("\nProcessing %d FITS file(s) in %q (%d workers, conv=%d, max-fwhm-sigma=%s, max-halo=%s, max-structure=%s)...\n\n",
			len(fitsFiles), dir, outerWorkers, convWorkers,
			formatThreshold(opts.maxFWHMSigma), formatThreshold(opts.maxHaloRatio),
			formatThreshold(opts.maxStructure))
	}

	results := make([]ImageResult, len(fitsFiles))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var printMu sync.Mutex
	done := 0

	for w := 0; w < outerWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				r := processImage(fitsFiles[idx], opts)
				// Distinct indices: no lock needed for the write.
				results[idx] = r
				// Progress only. The verdict cannot be printed here: the cloud
				// rule needs every frame's measurement before it can compare a
				// session, so decisions are final only after the pool drains.
				printMu.Lock()
				done++
				if !opts.quiet && showConsole && (done%10 == 0 || done == len(fitsFiles)) {
					fmt.Printf("\r  analyse : %d/%d image(s)", done, len(fitsFiles))
				}
				printMu.Unlock()
			}
		}()
	}
	for i := range fitsFiles {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	if !opts.quiet && showConsole {
		if len(fitsFiles) > 0 {
			fmt.Println()
			fmt.Println()
		}
	}

	// Cloud is a session-level decision: it compares a frame against its own
	// night, so it can only run once every frame has been measured.
	applyFWHMRuleByFilter(results, opts.qualityThresholds)
	applySessionRulesBySession(results, opts.qualityThresholds)

	if !opts.quiet && showConsole {
		for i, r := range results {
			if r.Error != nil {
				fmt.Printf("  [%d/%d] %s ... ERROR: %v\n", i+1, len(results), r.Filename, r.Error)
				continue
			}
			// Score is printed to three decimals, like Ecc, because the score
			// gate is the finest of the seven: a frame sitting just above
			// --min-score would render as exactly on it at two.
			line := fmt.Sprintf("  [%d/%d] %s ... OK (%d/%d stars, FWHM=%.2f, Ecc=%.3f, SNR=%.2f, Score=%.3f",
				i+1, len(results), r.Filename, r.StarCount, r.DetectedStars,
				r.AvgFWHM, r.AvgEccentricity, r.SNR, r.Score)
			// The cloud numbers are printed only when they were measured. The
			// session pass leaves both at zero when the rule is disabled or
			// when the session has too few frames to supply a baseline, and a
			// console "cloud=0.000" would then read as "perfectly clear"
			// instead of "no baseline, not tested".
			if r.FWHMRatio > 0 {
				line += fmt.Sprintf(", fwhm=%.2f", r.FWHMRatio)
			}
			if r.HaloRatio > 0 {
				line += fmt.Sprintf(", halo=%.2f", r.HaloRatio)
			}
			if r.StructRatio > 0 {
				line += fmt.Sprintf(", struct=%.3f", r.StructRatio)
			}
			line += fmt.Sprintf(", %s)", r.Decision)
			fmt.Println(line)
		}
	}

	if opts.copyRejected {
		copied, copyWarnings, err := copyRejectedFrames(results, fitsFiles, opts)
		if err != nil {
			return err
		}
		for _, w := range copyWarnings {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
		}
		if !opts.quiet {
			fmt.Printf("Rejected images copied to %q (%d file(s)).\n",
				filepath.Join(opts.output, rejectedOutDir), copied)
		}
	}

	if opts.format == "csv" || opts.format == "both" {
		if csvToStdout {
			w := newCSVWriter(os.Stdout)
			if err := writeResultsCSV(results, w); err != nil {
				return fmt.Errorf("writing CSV: %w", err)
			}
		} else {
			if err := os.MkdirAll(opts.output, 0o755); err != nil {
				return fmt.Errorf("unable to create %q: %w", opts.output, err)
			}
			f, err := os.Create(reportPath)
			if err != nil {
				return fmt.Errorf("unable to create the report %q: %w", reportPath, err)
			}
			w := newCSVWriter(f)
			writeErr := writeResultsCSV(results, w)
			closeErr := f.Close()
			if writeErr != nil {
				return fmt.Errorf("writing CSV: %w", writeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("closing CSV: %w", closeErr)
			}
			if !opts.quiet {
				fmt.Printf("Report written to %q (%d rows).\n", reportPath, len(results))
			}
		}
	}

	if !opts.quiet && showConsole {
		fmt.Printf("\nDone: %d file(s) processed.\n", len(results))
	}
	return nil
}

// backgroundStats summarises the sky: its median level, and how much it varies
// across the frame after dividing the image into large blocks.
//
// The block size matters. A star is a few pixels; thin cirrus is tens to
// hundreds. Sixty-four pixels sits above the star scales and below the frame
// scale, so the MAD of block medians responds to patchy cloud while a uniformly
// brighter sky -- moonlight, haze -- leaves it almost unchanged.
//
// The level is offset-corrected by readfits, so it is a physical ADU value and
// can be compared across frames and nights.
//
// Structure is divided by the noise sigma, not by the level. Dividing by the sky
// looks natural and is wrong: cloud raises the sky, so a brighter frame would
// have its own structure divided down, cancelling part of the signal it is meant
// to measure. Noise is a much weaker function of cloud cover (it grows with the
// square root of the sky), so the ratio stays a genuine measure of patchiness
// and is dimensionless, in units of the per-pixel noise.
func backgroundStats(bgMap [][]float64, noise float64) (level, structure float64) {
	if len(bgMap) == 0 || len(bgMap[0]) == 0 {
		return 0, 0
	}
	level = medianStrided(bgMap, 4)
	if noise <= 0 {
		return level, 0
	}

	const block = 64
	var blocks []float64
	for by := 0; by+block <= len(bgMap); by += block {
		for bx := 0; bx+block <= len(bgMap[by]); bx += block {
			var vals []float64
			for y := by; y < by+block; y += 4 {
				for x := bx; x < bx+block; x += 4 {
					vals = append(vals, bgMap[y][x])
				}
			}
			blocks = append(blocks, medianOf(vals))
		}
	}
	if len(blocks) == 0 {
		return level, 0
	}
	med := medianOf(blocks)
	dev := make([]float64, len(blocks))
	for i, v := range blocks {
		dev[i] = math.Abs(v - med)
	}
	return level, medianOf(dev) / noise
}

// medianStrided takes a median over a regular subsample. The background map is
// smooth at this scale, so every fourth pixel is representative and the whole
// frame does not need sorting.
func medianStrided(m [][]float64, step int) float64 {
	var vals []float64
	for y := 0; y < len(m); y += step {
		for x := 0; x < len(m[y]); x += step {
			vals = append(vals, m[y][x])
		}
	}
	return medianOf(vals)
}

// medianOf returns the middle value of v, sorting a copy so the caller keeps its
// slice. An empty slice yields 0, which the callers treat as "no measurement".
func medianOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// fwhmMinSample is the smallest filter population a robust sigma bound can be
// computed from. Below this the MAD is too unstable to place a 3-sigma line on,
// and the FWHM gate is skipped rather than guessing.
const fwhmMinSample = 8

// applyFWHMRuleByFilter rejects frames whose FWHM is an upper outlier for their
// filter, across the whole run.
//
// Filter, not session: the population is every measured frame of that filter
// passed to the command, so the bound is stable even when a night has only a
// handful of frames. The frame-to-frame reference is the median and the scale
// is 1.4826*MAD, so a soft frame cannot move the bound it is measured against.
// Only the upper tail is rejected; a frame sharper than its filter's median is
// better than average, not faulty.
//
// FWHMRatio is set for every measured frame whenever the median is usable, but
// the gate only runs when the filter has fwhmMinSample frames and a non-zero
// scale. A degenerate MAD would put the bound exactly on the median and reject
// half the filter, so that case is skipped too.
func applyFWHMRuleByFilter(results []ImageResult, q qualityThresholds) {
	byFilter := map[string][]*ImageResult{}
	var order []string
	for i := range results {
		r := &results[i]
		r.FWHMRatio = 0
		if r.AvgFWHM <= 0 {
			continue
		}
		if _, seen := byFilter[r.Filter]; !seen {
			order = append(order, r.Filter)
		}
		byFilter[r.Filter] = append(byFilter[r.Filter], r)
	}

	for _, f := range order {
		group := byFilter[f]
		values := make([]float64, len(group))
		for i, r := range group {
			values[i] = r.AvgFWHM
		}
		median := medianOf(values)

		if q.maxFWHMSigma > 0 && len(group) >= fwhmMinSample {
			dev := make([]float64, len(values))
			for i, v := range values {
				dev[i] = math.Abs(v - median)
			}
			sigma := 1.4826 * medianOf(dev)
			if sigma > 0 {
				upper := median + q.maxFWHMSigma*sigma
				for _, r := range group {
					if r.Decision == decisionApproved && r.AvgFWHM > upper {
						r.Decision = decisionFWHM
					}
				}
			}
		}

		if median <= 0 {
			continue
		}
		for _, r := range group {
			r.FWHMRatio = r.AvgFWHM / median
		}
	}
}

// applySessionRulesBySession groups results by acquisition session and filter,
// and applies the session-relative rules within each group: the star-halo cloud
// rule and the sky-structure bad-seeing rule.
//
// Session, not date. A night's frames routinely cross midnight, so the date
// token in the filename changes part-way through one session; and a date can
// appear in two different sessions (the tail of one night and the start of the
// next). Grouping by date therefore both splits real sessions and merges
// unrelated ones, which moves the baseline and produces false positives --
// observed as a whole night of clean frames rejected when analyzing a target
// directory rather than a single night.
func applySessionRulesBySession(results []ImageResult, q qualityThresholds) {
	if q.maxHaloRatio <= 0 && q.maxStructure <= 0 {
		return
	}
	keys := sessionKeys(results)
	groups := map[string][]*ImageResult{}
	var order []string
	for i := range results {
		k := keys[i]
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], &results[i])
	}
	for _, k := range order {
		applySessionRules(groups[k], q)
	}
}

// sessionGap is how long a pause between frames has to be before the run is
// treated as a new session. Frames within a night are minutes apart; the gap
// between nights is many hours. Two hours is comfortably between the two.
const sessionGap = 2 * time.Hour

// frameTimestampRe matches the acquisition stamp embedded in a frame name,
// e.g. "_20220825_214936_".
var frameTimestampRe = regexp.MustCompile(`_(\d{8})_(\d{6})_`)

// frameTimestamp reads the acquisition time from a frame name.
func frameTimestamp(name string) (time.Time, bool) {
	m := frameTimestampRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	ts, err := time.Parse("20060102150405", m[1]+m[2])
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

// sessionKeys assigns each result a grouping key of "<session>|<filter>".
//
// Sessions are found by clustering on time: sort every frame by its acquisition
// stamp and start a new session whenever the gap exceeds sessionGap. That works
// for a target tree walked recursively and for a flat directory of prepared
// frames, where there is no session directory to read and the filename date is
// the thing that is unreliable.
//
// Filter is part of the key because the halo and the sky differ between
// filters and exposures. A B frame and an L frame from the same night would
// otherwise share one baseline and pollute each other.
//
// Frames whose name carries no stamp cannot be placed in time; they are grouped
// together by filter and, having no session, cannot supply a meaningful
// baseline -- which the caller handles.
func sessionKeys(results []ImageResult) []string {
	type stamped struct {
		idx int
		ts  time.Time
	}
	keys := make([]string, len(results))
	var items []stamped
	for i, r := range results {
		if ts, ok := frameTimestamp(r.Filename); ok {
			items = append(items, stamped{i, ts})
			continue
		}
		keys[i] = "unknown|" + r.Filter
	}

	sort.Slice(items, func(a, b int) bool { return items[a].ts.Before(items[b].ts) })
	cluster := 0
	for n, it := range items {
		if n > 0 && it.ts.Sub(items[n-1].ts) > sessionGap {
			cluster++
		}
		keys[it.idx] = fmt.Sprintf("%d|%s", cluster, results[it.idx].Filter)
	}
	return keys
}

// applySessionRules sets the session-relative numbers on one session's results
// and replaces the decision on frames a rule rejects.
//
// Only APPROVED frames are changed. A frame already rejected for eccentricity
// keeps the more specific reason; overwriting it would lose the diagnosis the
// report exists to provide.
//
// Cloud is tested before bad seeing: when both fire, the aureole is the more
// actionable diagnosis, and a cloudy frame's structure is a consequence.
//
// Both references need a baseline, and a baseline needs frames. With fewer than
// four measured frames neither percentile is meaningful, so neither test runs
// and the session is left alone.
func applySessionRules(results []*ImageResult, q qualityThresholds) {
	var halos, structures []float64
	for _, r := range results {
		r.HaloRatio = 0
		r.StructRatio = 0
		if r.Halo > 0 {
			halos = append(halos, r.Halo)
		}
		if r.BgStructure > 0 {
			structures = append(structures, r.BgStructure)
		}
	}

	// Both references are the 25th percentile of the session: the median would
	// sit inside an event that affects several frames.
	var haloRef, structRef float64
	if len(halos) >= 4 {
		sort.Float64s(halos)
		haloRef = percentileSorted(halos, 0.25)
	}
	if len(structures) >= 4 {
		sort.Float64s(structures)
		structRef = percentileSorted(structures, 0.25)
	}

	for _, r := range results {
		if haloRef > 0 {
			r.HaloRatio = r.Halo / haloRef
		}
		if structRef > 0 {
			r.StructRatio = r.BgStructure / structRef
		}
		if r.Decision != decisionApproved {
			continue
		}
		if q.maxHaloRatio > 0 && r.HaloRatio > q.maxHaloRatio {
			r.Decision = decisionClouds
			continue
		}
		if q.maxStructure > 0 && r.StructRatio > q.maxStructure {
			r.Decision = decisionBadSeeing
		}
	}
}

// percentileSorted returns the p-quantile of an already sorted slice, with
// linear interpolation between neighbours.
func percentileSorted(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	k := p * float64(len(sorted)-1)
	lo := int(k)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	return sorted[lo] + (k-float64(lo))*(sorted[hi]-sorted[lo])
}

// analyseReportName is the fixed name of the CSV inside the output directory.
// Fixed rather than taken from --output, which is now a directory: one run
// writes one report, so the name carries no information the flag does not.
const analyseReportName = "analyse_report.csv"

// rejectedOutDir groups the copied rejected frames beneath the output
// directory. Mirrors the name prepare uses for its quarantine tree.
const rejectedOutDir = "rejected"

// rejectCategories maps each rejection decision to its folder name.
//
// A table rather than string munging: the decision strings are a documented
// contract that includes spaces, and deriving a name from them would silently
// invent a folder the first time a value is added. Anything not listed is
// placed under "other" so a new decision cannot drop frames on the floor.
var rejectCategories = map[string]string{
	decisionNoStars:   "no-stars",
	decisionEcc:       "eccentricity",
	decisionFWHM:      "fwhm",
	decisionSNR:       "snr",
	decisionScoring:   "scoring",
	decisionClouds:    "clouds",
	decisionBadSeeing: "bad-seeing",
}

// rejectCategory returns the folder a rejected frame belongs in.
func rejectCategory(decision string) string {
	if c, ok := rejectCategories[decision]; ok {
		return c
	}
	return "other"
}

// copyRejectedFrames copies every rejected frame into
// <output>/rejected/<category>/<basename>.
//
// A frame whose destination already exists is reported and skipped rather than
// overwritten: analyze can walk several sessions with the same frame numbering,
// so two different sources can share a basename. Losing one silently would be
// worse than telling the operator the category folder is not unique.
func copyRejectedFrames(results []ImageResult, paths []string, opts analyzeOptions) (int, []string, error) {
	root := filepath.Join(opts.output, rejectedOutDir)
	var warnings []string
	copied := 0

	for i, r := range results {
		if !strings.HasPrefix(r.Decision, "REJECTED") {
			continue
		}
		if i >= len(paths) {
			continue
		}
		dir := filepath.Join(root, rejectCategory(r.Decision))
		dst := filepath.Join(dir, filepath.Base(r.Filename))

		if _, err := os.Stat(dst); err == nil {
			warnings = append(warnings, fmt.Sprintf(
				"%q already exists in %q; not overwritten (duplicate basename from another directory?)",
				filepath.Base(r.Filename), dir))
			continue
		}

		if err := os.MkdirAll(dir, 0o755); err != nil {
			return copied, warnings, fmt.Errorf("unable to create %q: %w", dir, err)
		}
		if err := copyFile(paths[i], dst); err != nil {
			return copied, warnings, err
		}
		copied++
	}
	return copied, warnings, nil
}

// copyFile streams src to dst. Frames are around 120 MB, so they are not read
// into memory whole.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("unable to open %q: %w", src, err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("unable to create %q: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying to %q: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %q: %w", dst, err)
	}
	return nil
}
