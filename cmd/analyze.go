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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"garfield/internal/measurecache"
	"garfield/internal/mrs"
	"garfield/internal/readfits"
	"garfield/internal/starsmetrics"

	"github.com/spf13/cobra"
)

type analyzeOptions struct {
	qualityThresholds

	dir string
	// filter restricts the run to one colour filter (L R G B S H O). Empty
	// analyses every discovered frame. It is matched against the filename token
	// parseFilename reports, which is also the CSV's filter column.
	filter string
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
	// cache reuses measurements stored under output and stores the ones this run
	// computes. Off by default: it writes to the output directory, and a run
	// that only wants a report should not leave a store behind.
	cache bool
	// stackGate rejects a frame whose night the stack says is systematically
	// worse, when the frame itself also sits outside the stack band. Off by
	// default: it is a second opinion on the session rules, and turning it on
	// discards frames.
	stackGate bool
	// suggest derives a threshold set for the selected filter from the run's own
	// measurements and writes it beside the report. It is advisory: the verdicts
	// of this run are not changed by it.
	suggest bool
	// keep is the fraction of the filter's frames a suggestion aims to keep.
	keep  float64
	quiet bool
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
	analyzeCmd.Flags().StringVar(&analyzeOpts.filter, "filter", "",
		"only analyse frames of this filter (one of LRGBSHO)")
	analyzeCmd.Flags().StringVarP(&analyzeOpts.output, "output", "o", "",
		"output directory for the report and copied rejected frames")
	analyzeCmd.Flags().BoolVar(&analyzeOpts.copyRejected, "copy-rejected", false,
		"copy rejected frames into <output>/rejected/<category>/")
	analyzeCmd.Flags().BoolVar(&analyzeOpts.cache, "cache", false,
		"reuse and record measurements in <output>/cache (requires --output)")
	analyzeCmd.Flags().BoolVar(&analyzeOpts.stackGate, "stack-gate", false,
		"reject frames from a night that is systematically worse than its filter's stack")
	analyzeCmd.Flags().BoolVar(&analyzeOpts.suggest, "suggest", false,
		"suggest thresholds for --filter from the measured stack (requires --filter and --output)")
	analyzeCmd.Flags().Float64Var(&analyzeOpts.keep, "keep", defaultKeepBudget,
		"fraction of the good stack a --suggest run aims to keep; 0 = the default band")
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
	// HaloStars is how many stars the halo measurement used. Zero means it could
	// not be made -- too few stars with a measurable aureole -- and the report
	// says so rather than writing 0 ADU, which would read as a measured calm sky.
	HaloStars int
	// Transparency is the frame's image contrast-to-noise divided by the best its
	// own session reached: 1.0 means the clearest conditions of the night, 0.8 that
	// the frame is a fifth dimmer. It is the observable behind thin cloud and any
	// other loss of throughput.
	Transparency float64
	// StructRatio is the frame's bgStructure relative to its session. It is
	// reported and gated separately as bad seeing: a strongly structured sky
	// produces frames that no amount of stacking repairs.
	StructRatio float64
	Decision    string
	Error       error
	// Cached records that this result came from a stored measurement rather than
	// from this run's analysis. It is reported so a mix of fresh and reused rows
	// is visible in the report instead of being assumed.
	Cached bool
	// SessionFlag carries the tokens of the session this frame belongs to, e.g.
	// "soft,cloudy" when its night was systematically worse than the filter's
	// stack. It is a diagnosis of the night, not a verdict on the frame: the
	// verdict stays in Decision.
	SessionFlag string
	// The *RatioStack fields put a frame's measurement next to the median of its
	// filter's whole population. Above 1 means above the median: bad for the
	// width, shape, halo and structure, good for the signal-to-noise.
	FWHMRatioStack   float64
	EccRatioStack    float64
	SNRRatioStack    float64
	HaloRatioStack   float64
	StructRatioStack float64
	// SuggestedDecision is the verdict the gate suggestion would give this frame,
	// set only by `analyze --suggest`. Empty on every other run, so the column
	// says "not asked" rather than implying the current verdict.
	SuggestedDecision string
}

const (
	decisionApproved     = "APPROVED"
	decisionNoStars      = "REJECTED NO STARS"
	decisionEcc          = "REJECTED ECCENTRICITY"
	decisionFWHM         = "REJECTED FWHM"
	decisionSNR          = "REJECTED SNR"
	decisionScoring      = "REJECTED SCORING"
	decisionClouds       = "REJECTED CLOUDS"
	decisionTransparency = "REJECTED TRANSPARENCY"
	decisionStructure    = "REJECTED STRUCTURE"
)

// Reasons recorded for frames that were rejected without a quality verdict.
// They live beside the decision values because prepare reports both through the
// same console summary, and a reason printed to the operator should be a known
// token rather than an ad-hoc string.
const (
	// reasonError marks a frame that could not be analysed at all, e.g. an
	// unreadable file or one with no HDU.
	reasonError = "ERROR"
	// reasonUnknown marks a frame rejected with an empty decision, which the
	// cascade should never produce; recording it separately makes the bug
	// visible instead of hiding it under an empty label.
	reasonUnknown = "UNKNOWN"
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

// normaliseFilter canonicalises --filter. Empty means no restriction; any other
// value must be one of the single-letter filters, so a typo fails before a scan
// rather than producing an empty report under a misspelled filename.
func normaliseFilter(value string) (string, error) {
	filter := strings.ToUpper(strings.TrimSpace(value))
	if filter == "" {
		return "", nil
	}
	if !isKnownFilter(filter) {
		return "", fmt.Errorf("invalid --filter %q: expected one of %s", value, knownFilters)
	}
	return filter, nil
}

// framesForFilter keeps the frames whose parsed filter matches. The comparison is
// against parseFilename, the same positional rule the report's filter column
// uses, so the selection and the CSV can never disagree.
func framesForFilter(paths []string, filter string) []string {
	if filter == "" {
		return paths
	}
	kept := make([]string, 0, len(paths))
	for _, path := range paths {
		fileFilter, _ := parseFilename(filepath.Base(path))
		if fileFilter == filter {
			kept = append(kept, path)
		}
	}
	return kept
}

// The cameras' filename grammars, as the console reads them.
//
// Two shapes, and they are mutually exclusive: the G4 and the ASI2600 write a
// frame index where the C3 writes its gain, so the token before the date is
// either `001` or `GA0`. A name that matches neither keeps its full filename:
// shortening a name that is not understood would invent fields.
//
// The date shown is the acquisition date from the name. That is not always the
// session directory's date -- SH2-142's frames sit under 2026-08-11 and are
// stamped 20260812 -- and the acquisition stamp is the truthful one.
var (
	// frameIDIndexed matches the grammars carrying a frame index: G4 and ASI2600.
	frameIDIndexed = regexp.MustCompile(
		`^([^_]+)_LIGHT_([A-Za-z])_\d+s_BIN\d_[-+]?\d+C_(\d+)_(\d{8})_(\d{6})_(\d{1,3})_`)
	// frameIDGain matches the C3 grammar, which has no index to show.
	frameIDGain = regexp.MustCompile(
		`^([^_]+)_LIGHT_([A-Za-z])_\d+s_BIN\d_[-+]?\d+C_GA\d+_(\d{8})_(\d{6})_(\d{1,3})_`)
)

// frameLabel shortens a frame's filename to <target>_<filter>[_<index>]_<date>_<time>_<ms>,
// so a console line keeps the same width from frame to frame.
//
// ok is false when the name follows no known grammar; the caller then prints the
// filename untouched.
func frameLabel(name string) (label string, ok bool) {
	if m := frameIDIndexed.FindStringSubmatch(name); m != nil {
		return fmt.Sprintf("%s_%s_%s_%s_%s_%s", m[1], m[2], m[3], m[4], m[5], padMillis(m[6])), true
	}
	if m := frameIDGain.FindStringSubmatch(name); m != nil {
		return fmt.Sprintf("%s_%s_%s_%s_%s", m[1], m[2], m[3], m[4], padMillis(m[5])), true
	}
	return "", false
}

// padMillis zero-pads the millisecond field to three digits. The cameras write
// three, but a shorter stamp would shift every column to its right.
func padMillis(v string) string {
	for len(v) < 3 {
		v = "0" + v
	}
	return v
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

// measureFrame reads one frame and computes its quality measurements.
//
// It deliberately stops at the measurements: Decision is left empty. The verdict
// depends on the thresholds, and two of the rules compare a frame against the
// population it belongs to, so judging belongs to the batch passes
// (applyBaseDecision and the two outlier rules). Keeping the two apart is also
// what lets a measurement be reused under different thresholds.
func measureFrame(path string, opts analyzeOptions) ImageResult {
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
	halo, haloStars := starsmetrics.MeasureHalo(pixels, fieldStars, skyNoise, bgLevel)

	if len(fieldStars) == 0 {
		return ImageResult{
			Filename: name, Filter: filter, Date: date,
			Background: bgLevel, Noise: skyNoise, BgStructure: bgStructure,
			Error: errNoStarFound,
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
			HaloStars:       haloStars,
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
			HaloStars:     haloStars,
		}
	}

	avgFWHM := weightedFWHM / sumWeight
	avgEcc := weightedEcc / sumWeight
	avgPeakSignal := totalPeakSignal / float64(filteredCount)

	snrLinear := mrsResult.ImageSNR
	finalScore := frameScore(snrLinear, avgFWHM, avgEcc)

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
		HaloStars:       haloStars,
	}
}

// frameScore is the combined figure of merit: signal-to-noise against width,
// penalised by elongation. It is a pure function of three other measurements, so
// the cache stores those and this recomputes the score on the way out rather
// than duplicating a derived value.
func frameScore(snr, fwhm, ecc float64) float64 {
	if fwhm == 0 {
		return 0
	}
	return (snr / fwhm) * (1.0 - ecc)
}

// applyBaseDecision sets each frame's verdict from its own measurements and the
// given thresholds. Frames that could not be measured keep an empty decision,
// which the reports and the console render as an error rather than as a verdict.
func applyBaseDecision(results []ImageResult, q qualityThresholds) {
	applyBaseDecisionGated(results, baseGates(q))
}

// baseDecision is the threshold cascade, in precedence order: star count first,
// then shape, width, signal and finally the combined score.
//
// StarCount == 0 is tested first because a frame whose stars all failed the
// per-star filters has no metrics to judge. Its FWHM is zero, so the score would
// divide by zero and yield +Inf, which passes every gate below.
func baseDecision(r ImageResult, q qualityThresholds) string {
	switch {
	case r.StarCount == 0:
		return decisionNoStars
	case r.DetectedStars < q.minStars:
		return decisionNoStars
	case r.AvgEccentricity > q.maxEcc:
		return decisionEcc
	case r.AvgFWHM > q.maxFWHM:
		return decisionFWHM
	case r.SNR < q.minSNR:
		return decisionSNR
	case r.Score < q.minScore:
		return decisionScoring
	}
	return decisionApproved
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
var imageResultHeader = []string{"filename", "filter", "date", "detectedStars", "starCount", "avgFWHM", "avgSignal", "avgEccentricity", "snr", "score", "decision", "error", "background", "noise", "bgStructure", "halo", "haloRatio", "structRatio", "fwhmRatio", "cached", "sessionFlag",
	"fwhmRatioStack", "eccRatioStack", "snrRatioStack", "haloRatioStack", "structRatioStack",
	"suggestedDecision", "haloStars", "transparency"}

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
		formatHalo(r),
		strconv.FormatFloat(r.HaloRatio, 'f', 4, 64),
		strconv.FormatFloat(r.StructRatio, 'f', 4, 64),
		strconv.FormatFloat(r.FWHMRatio, 'f', 4, 64),
		strconv.FormatBool(r.Cached),
		r.SessionFlag,
		strconv.FormatFloat(r.FWHMRatioStack, 'f', 4, 64),
		strconv.FormatFloat(r.EccRatioStack, 'f', 4, 64),
		strconv.FormatFloat(r.SNRRatioStack, 'f', 4, 64),
		strconv.FormatFloat(r.HaloRatioStack, 'f', 4, 64),
		strconv.FormatFloat(r.StructRatioStack, 'f', 4, 64),
		r.SuggestedDecision,
		strconv.Itoa(r.HaloStars),
		formatRatio(r.Transparency),
	}
}

// formatRatio renders a derived ratio, empty when it was not measured.
func formatRatio(v float64) string {
	if v <= 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', 4, 64)
}

// formatHalo renders the halo, or an empty field when it could not be measured.
// "0.00 ADU" and "not measured" are different statements and the report must not
// conflate them: a reader who sees zero would conclude the sky was calm.
func formatHalo(r ImageResult) string {
	if r.HaloStars == 0 {
		return ""
	}
	return strconv.FormatFloat(r.Halo, 'f', 2, 64)
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
	if opts.cache && opts.output == "" {
		return fmt.Errorf("--cache requires --output: measurements are stored under %s", cachePath("<output>"))
	}

	var measures *measurecache.Cache
	if opts.cache {
		c, err := openMeasureCache(opts.output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %v\n", err)
		} else {
			measures = c
			defer closeMeasureCache(c)
		}
	}

	filter, err := normaliseFilter(opts.filter)
	if err != nil {
		return err
	}
	opts.filter = filter

	if opts.suggest {
		if filter == "" {
			return fmt.Errorf("--suggest requires --filter: a suggestion is derived from, and belongs to, one filter's population")
		}
		if opts.output == "" {
			return fmt.Errorf("--suggest requires --output: the suggestion is written under %s", suggestionPath("<output>"))
		}
		if opts.keep < 0 || opts.keep >= 1 {
			return fmt.Errorf("invalid --keep %v: expected 0 (the default band) or a fraction below 1", opts.keep)
		}
	}

	fitsFiles, truncated, err := discoverFITSFiles(dir, opts.recursive)
	if err != nil {
		return err
	}
	fitsFiles = framesForFilter(fitsFiles, filter)

	// WalkDir visits each directory in lexical order, but that is per directory,
	// so a/z.FIT is still reached before b/a.FIT. Sorting the full paths is what
	// makes CSV rows come out alphabetically rather than in completion order,
	// which is otherwise nondeterministic across runs. The flat scan reads a
	// single directory, so this only restores that ordering for it.
	sort.Strings(fitsFiles)

	if len(fitsFiles) == 0 {
		if filter != "" {
			if opts.recursive {
				return fmt.Errorf(
					"no FITS file for filter %s in %q within %d subdirectory levels",
					filter, dir, maxRecursiveDepth)
			}
			return fmt.Errorf(
				"no FITS file for filter %s in %q; use -r to walk the subdirectories too",
				filter, dir)
		}
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
	// oversubscribing. The outer count is also capped by the memory one frame's
	// analysis needs, since NumCPU/2 frames of a 61 Mpx sensor is far more RAM
	// than most machines hold.
	plan := resolveWorkers(opts.workers, opts.convWorkers, len(fitsFiles),
		frameWorkingSetFor(fitsFiles[0]), physicalMemory())
	outerWorkers, convWorkers := plan.Outer, plan.Conv
	mrs.ConvWorkers = convWorkers
	if warn := plan.workerWarning(opts.workers); warn != "" {
		fmt.Fprintf(os.Stderr, "  warning: %s\n", warn)
	}

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
		reportPath = filepath.Join(opts.output, analyseReportNameFor(filter))
	}
	if !opts.quiet && showConsole {
		scope := ""
		if filter != "" {
			scope = " for filter " + filter
		}
		fmt.Printf("\nProcessing %d FITS file(s)%s in %q (%d workers, conv=%d, max-fwhm-sigma=%s, max-halo=%s, max-structure=%s)...\n\n",
			len(fitsFiles), scope, dir, outerWorkers, convWorkers,
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
				r := measureWithCache(fitsFiles[idx], opts, measures)
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

	// Said before the verdicts, because it explains the timings: a second run
	// over the same tree spends its time in the outlier rules, not in pixels.
	if measures != nil && !opts.quiet {
		cached, measured := countCached(results)
		fmt.Printf("  cache: %d measured, %d reused from %q\n", measured, cached, cachePath(opts.output))
	}

	// Cloud is a session-level decision: it compares a frame against its own
	// night, so it can only run once every frame has been measured.
	applyBaseDecision(results, opts.qualityThresholds)
	applyFWHMRuleByFilter(results, opts.qualityThresholds)
	applySessionRulesBySession(results, opts.qualityThresholds)

	// Session-level pass. It changes no verdict: it says which nights are
	// systematically worse than the filter's stack, and those tokens travel on
	// the report so a rejected frame can be traced to its night.
	prof := profileRun(results)
	attachSessionFlags(results, prof)
	applyStackRatios(results, prof)
	if opts.stackGate {
		applyStackGate(results, prof)
	}
	if opts.output != "" && !prof.empty() {
		for _, f := range prof.order {
			if err := writeSessionsProfileCSV(opts.output, f, prof.profiles(f), prof.refs[f]); err != nil {
				return err
			}
		}
	}

	// The suggestion is derived before the verdicts are printed, so each frame can
	// say what the suggestion would do with it. It never changes the run's own
	// decisions: those come from the thresholds the run was given.
	var (
		suggestion *suggestedThresholds
		simulated  []string
		nowCounts  rejectionCounts
		thenCounts rejectionCounts
	)
	if opts.suggest {
		s, err := suggestForFilter(results, prof, filter, opts)
		if err != nil {
			return err
		}
		suggestion = &s
		simulated = simulateSuggestion(results, s, opts, prof)
		for i := range results {
			if results[i].Error != nil {
				continue
			}
			results[i].SuggestedDecision = simulated[i]
		}
		nowCounts = countRejections(results, nil)
		thenCounts = countRejections(results, simulated)
		if err := writeSuggestion(opts.output, s); err != nil {
			return err
		}
	}

	if !opts.quiet && showConsole {
		// The counter is padded to the width the run needs, so a run past 99
		// frames does not shift every line one column to the right.
		counterWidth := len(strconv.Itoa(len(results)))
		if counterWidth < 3 {
			counterWidth = 3
		}
		for i, r := range results {
			label, known := frameLabel(r.Filename)
			if !known {
				label = r.Filename
			}
			if r.Error != nil {
				fmt.Printf("  [%0*d/%d] %s ... ERROR: %v\n",
					counterWidth, i+1, len(results), label, r.Error)
				continue
			}
			// Score is printed to three decimals, like Ecc, because the score
			// gate is the finest of the seven: a frame sitting just above
			// --min-score would render as exactly on it at two.
			line := fmt.Sprintf("  [%0*d/%d] %s ... OK (%d/%d stars, FWHM=%.2f, Ecc=%.3f, SNR=%.2f, Score=%.3f",
				counterWidth, i+1, len(results), label, r.StarCount, r.DetectedStars,
				r.AvgFWHM, r.AvgEccentricity, r.SNR, r.Score)
			// The cloud numbers are printed only when they were measured. The
			// session pass leaves both at zero when the rule is disabled or
			// when the session has too few frames to supply a baseline, and a
			// console "cloud=0.000" would then read as "perfectly clear"
			// instead of "no baseline, not tested".
			if r.FWHMRatio > 0 {
				line += fmt.Sprintf(", fwhmRatio=%.2f", r.FWHMRatio)
			}
			if r.HaloRatio > 0 {
				line += fmt.Sprintf(", halo=%.2f", r.HaloRatio)
			}
			if r.StructRatio > 0 {
				line += fmt.Sprintf(", struct=%.3f", r.StructRatio)
			}
			line += fmt.Sprintf(", %s)", r.Decision)
			// Inline rather than a second list: the question is always about this
			// frame, and a marker on it cannot drift out of step with the line.
			if r.SuggestedDecision != "" && r.SuggestedDecision != r.Decision {
				line += fmt.Sprintf(" [sugg: %s]", r.SuggestedDecision)
			}
			fmt.Println(line)
		}
	}

	// The nights, then the what-if: the verdicts above are the facts, and these
	// two blocks explain them in that order.
	if !opts.quiet && showConsole && !prof.empty() {
		printSessionProfile(prof)
	}
	if suggestion != nil && !opts.quiet {
		printSuggestion(*suggestion, opts.qualityThresholds, nowCounts, thenCounts)
		changed, rejected, approved := suggestionDelta(results, simulated)
		if changed == 0 {
			fmt.Printf("  the suggestion would judge every frame the same way\n\n")
		} else {
			fmt.Printf("  the suggestion would change %d frame(s): %d newly rejected, %d newly approved\n\n",
				changed, rejected, approved)
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
// half the filter, so that case is skipped too. When a suggestion pinned the
// reference, that reference is used instead and the sample-size guard does not
// apply.
func applyFWHMRuleByFilter(results []ImageResult, q qualityThresholds) {
	applyFWHMRuleGated(results, baseGates(q))
}

// applySessionRulesBySession groups results by acquisition session and filter,
// and applies the session-relative rules within each group: the star-halo cloud
// rule and the sky-structure rule.
//
// Session, not date. A night's frames routinely cross midnight, so the date
// token in the filename changes part-way through one session; and a date can
// appear in two different sessions (the tail of one night and the start of the
// next). Grouping by date therefore both splits real sessions and merges
// unrelated ones, which moves the baseline and produces false positives --
// observed as a whole night of clean frames rejected when analyzing a target
// directory rather than a single night.
func applySessionRulesBySession(results []ImageResult, q qualityThresholds) {
	applySessionRulesBySessionGated(results, baseGates(q))
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
	applySessionRulesWith(results, q, 0, 0, 0, 0)
}

// structureSigma is how many robust sigmas above the session's median structure a
// frame must lie before its sky counts as anomalous, and structureMinSample is
// the smallest session that bound can be computed from.
//
// A fixed multiplier of the 25th percentile is not enough on its own: on a night
// whose structure is tightly distributed -- which is what a good night looks like
// -- the multiplier cuts the top few percent whatever those frames are. The
// default 1.12 sat on the 95th percentile of one target's G frames, so it
// rejected eleven frames whose PSFs were the sharpest of the night. Requiring the
// frame to be an outlier by the session's own robust scale, and to be soft as
// well, is what separates a defect from the field's own nebulosity.
const (
	structureSigma     = 4.0
	structureMinSample = 4

	// transparencyP90 is where the reference sits in the surrounding frames'
	// distribution of contrast-to-noise: the 90th percentile is the best of those
	// conditions, not the median, so a hazy hour cannot lower the bar it is judged
	// against.
	transparencyP90 = 0.90
	// transparencyMinSample is the smallest neighbourhood a reference can be taken
	// from.
	transparencyMinSample = 8
	// transparencyWindow is how far either side of a frame the reference looks.
	//
	// Not the whole session: conditions drift over a night with airmass, moonlight
	// and cloud, so a night-wide percentile calls the second half of a clear night
	// untransparent merely because the first half was better. An hour is longer
	// than a cloud passage and short enough that the airmass has barely moved.
	transparencyWindow = 60 * time.Minute
)

// applyTransparencyRatios fills each frame's Transparency: its contrast-to-noise
// divided by the 90th percentile of the frames taken within an hour of it, or by
// the baseline a suggestion pinned.
func applyTransparencyRatios(results []*ImageResult, frozenBaseline float64) {
	for _, r := range results {
		r.Transparency = 0
	}

	if frozenBaseline > 0 {
		for _, r := range results {
			if r.SNR > 0 {
				r.Transparency = r.SNR / frozenBaseline
			}
		}
		return
	}

	type stamped struct {
		r  *ImageResult
		ts time.Time
		ok bool
	}
	frames := make([]stamped, 0, len(results))
	var all []float64
	for _, r := range results {
		ts, ok := frameTimestamp(r.Filename)
		frames = append(frames, stamped{r: r, ts: ts, ok: ok})
		if ok && r.SNR > 0 {
			all = append(all, r.SNR)
		}
	}
	sort.Slice(frames, func(i, j int) bool {
		if frames[i].ok != frames[j].ok {
			return frames[i].ok
		}
		return frames[i].ts.Before(frames[j].ts)
	})

	// A frame with no usable stamp cannot be placed in time, so the whole group
	// is its neighbourhood -- which is what a run with no timestamps at all gets.
	windowRef := 0.0
	if len(all) >= transparencyMinSample {
		sort.Float64s(all)
		windowRef = percentileSorted(all, transparencyP90)
	}

	for i, f := range frames {
		if f.r.SNR <= 0 {
			continue
		}
		if !f.ok {
			if windowRef > 0 {
				f.r.Transparency = f.r.SNR / windowRef
			}
			continue
		}

		var near []float64
		for j := i; j >= 0 && f.ts.Sub(frames[j].ts) <= transparencyWindow; j-- {
			if frames[j].ok && frames[j].r.SNR > 0 {
				near = append(near, frames[j].r.SNR)
			}
		}
		for j := i + 1; j < len(frames) && frames[j].ts.Sub(f.ts) <= transparencyWindow; j++ {
			if frames[j].r.SNR > 0 {
				near = append(near, frames[j].r.SNR)
			}
		}
		if len(near) < transparencyMinSample {
			continue
		}
		sort.Float64s(near)
		if ref := percentileSorted(near, transparencyP90); ref > 0 {
			f.r.Transparency = f.r.SNR / ref
		}
	}
}

// applySessionRulesWith is the session rule with its baselines supplied.
//
// A zero baseline means "compute it from the session", which is what a run
// without a suggestion does; a non-zero one is the value a suggestion pinned, so
// a night is judged against the stack it was analysed with rather than against
// itself.
func applySessionRulesWith(results []*ImageResult, q qualityThresholds, haloBaseline, structBaseline, fwhmBaseline, transparencyBaseline float64) {
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
	// sit inside an event that affects several frames. A supplied baseline
	// replaces the session's own.
	haloRef, structRef := haloBaseline, structBaseline
	if haloRef == 0 && len(halos) >= 4 {
		sort.Float64s(halos)
		haloRef = percentileSorted(halos, 0.25)
	}
	if structRef == 0 && len(structures) >= 4 {
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
	}

	// How unusual a frame's sky has to be, by the session's own robust scale. A
	// degenerate scale means every frame looks alike, so there is nothing for a
	// sigma bound to say and the absolute multiplier decides alone.
	structBound := 0.0
	ratios := make([]float64, 0, len(results))
	for _, r := range results {
		if r.StructRatio > 0 {
			ratios = append(ratios, r.StructRatio)
		}
	}
	if len(ratios) >= structureMinSample {
		median := medianOf(ratios)
		dev := make([]float64, len(ratios))
		for i, v := range ratios {
			dev[i] = math.Abs(v - median)
		}
		if sigma := 1.4826 * medianOf(dev); sigma > 0 {
			structBound = median + structureSigma*sigma
		}
	}

	// Transparency: the frame's contrast-to-noise against the best of the hour
	// around it. A night can be uniformly hazy, so the comparison is relative, and
	// it is local because conditions drift: airmass and moonlight move the contrast
	// over a night faster than cloud does.
	applyTransparencyRatios(results, transparencyBaseline)

	// Bad seeing means soft stars. Structure on its own is not evidence: an
	// extended object, or the scattered light around bright stars, raises it
	// while the PSF stays as sharp as the night allows. Without this test the
	// rule rejects the most transparent frames of a nebula field.
	fwhmRef := fwhmBaseline
	if fwhmRef <= 0 {
		fwhms := make([]float64, 0, len(results))
		for _, r := range results {
			if r.AvgFWHM > 0 {
				fwhms = append(fwhms, r.AvgFWHM)
			}
		}
		fwhmRef = medianOf(fwhms)
	}

	for _, r := range results {
		if r.Decision != decisionApproved {
			continue
		}
		switch {
		case q.maxHaloRatio > 0 && r.HaloRatio > q.maxHaloRatio:
			// Cloud first: it is the more specific diagnosis, and a cloud dims the
			// field as a consequence.
			r.Decision = decisionClouds
		case q.minTransparency > 0 && r.Transparency > 0 && r.Transparency < q.minTransparency:
			r.Decision = decisionTransparency
		case q.maxStructure > 0 && r.StructRatio > q.maxStructure &&
			(structBound <= 0 || r.StructRatio > structBound) &&
			(fwhmRef <= 0 || r.AvgFWHM > fwhmRef):
			r.Decision = decisionStructure
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

// analyseReportName is the fixed name of the CSV inside the output directory
// when no filter is selected. Fixed rather than taken from --output, which is
// now a directory: one run writes one report, so the name carries no information
// the flag does not.
const analyseReportName = "analyse_report.csv"

// analyseReportNameFor names the report for a run. A filtered run says so in the
// filename, so reports for different filters can share one output directory
// without overwriting each other.
func analyseReportNameFor(filter string) string {
	if filter == "" {
		return analyseReportName
	}
	return "analyse_report_" + filter + ".csv"
}

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
	decisionNoStars:      "no-stars",
	decisionEcc:          "eccentricity",
	decisionFWHM:         "fwhm",
	decisionSNR:          "snr",
	decisionScoring:      "scoring",
	decisionClouds:       "clouds",
	decisionStructure:    "structure",
	decisionTransparency: "transparency",
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
		if err := copyFileAtomic(paths[i], dst); err != nil {
			return copied, warnings, err
		}
		copied++
	}
	return copied, warnings, nil
}
