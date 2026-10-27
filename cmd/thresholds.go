package cmd

import (
	"strconv"

	"github.com/spf13/pflag"
)

// qualityThresholds are the acceptance criteria a light frame must meet to be
// approved. Both analyze and prepare apply them, so the two commands can never
// disagree about what a usable frame is.
type qualityThresholds struct {
	minSNR   float64
	maxFWHM  float64
	maxEcc   float64
	minScore float64
	minStars int
	// maxCloud and maxStructure are the cloud rule, both session-relative.
	// maxCloud bounds the cloud index; maxStructure bounds the sky's
	// block-to-block variation. Either may be 0 to disable that half. They
	// are provisional: calibrated on one night, so they are configurable rather
	// than baked in.
	maxCloud     float64
	maxStructure float64
	// moffat selects the PSF profile used to measure width and shape. It is not
	// a threshold: it decides how the metrics are obtained, not whether a frame
	// passes. It lives here because this struct is the one thing analyze and
	// prepare already share, so one switch governs both commands instead of two
	// that can drift apart.
	moffat bool
}

// defaultThresholds is the single point of configuration for those criteria.
//
// These values were tuned empirically against real frames, so treat a change as
// a decision rather than a tweak: --max-ecc was raised from 0.50 to 0.54 during
// development after frames with eccentricities just above 0.50 were being
// discarded. Editing a value here changes both commands at once.
var defaultThresholds = qualityThresholds{
	minSNR:       11.0,
	maxFWHM:      5.0,
	maxEcc:       0.54,
	minScore:     2.0,
	minStars:     680,
	maxCloud:     1.08,
	maxStructure: 1.12,
}

// registerQualityFlags binds the shared thresholds to dst. Keeping the flag
// names and help text here as well as the values means the two commands present
// an identical interface.
func registerQualityFlags(fs *pflag.FlagSet, dst *qualityThresholds) {
	fs.Float64Var(&dst.minSNR, "min-snr", defaultThresholds.minSNR, "minimum SNR for APPROVED")
	fs.Float64Var(&dst.maxFWHM, "max-fwhm", defaultThresholds.maxFWHM, "maximum FWHM for APPROVED")
	fs.Float64Var(&dst.maxEcc, "max-ecc", defaultThresholds.maxEcc, "maximum eccentricity for APPROVED")
	fs.Float64Var(&dst.minScore, "min-score", defaultThresholds.minScore, "minimum score for APPROVED")
	fs.IntVar(&dst.minStars, "min-stars", defaultThresholds.minStars, "minimum number of detected stars for APPROVED")
	fs.Float64Var(&dst.maxCloud, "max-cloud", defaultThresholds.maxCloud,
		"reject a frame whose session-relative cloud index exceeds this; 0 disables")
	fs.Float64Var(&dst.maxStructure, "max-structure", defaultThresholds.maxStructure,
		"reject a frame whose sky structure exceeds this multiple of its session's; 0 disables")
	fs.BoolVar(&dst.moffat, "moffat", false,
		"fit a Moffat PSF profile instead of a Gaussian (slower, FWHM agrees with PixInsight)")
}

// formatThreshold renders a cloud threshold for the console.
//
// Zero disables that half of the rule, so it is printed as "disabled" rather
// than "0": a line reading max-cloud=0 invites the reading "a threshold of
// zero", which is the opposite of what it means. Otherwise the exact value is
// printed, untrimmed, so a tuned 1.125 is not rounded to some prettier number
// that would suggest a threshold the run did not use.
func formatThreshold(v float64) string {
	if v <= 0 {
		return "disabled"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// registerWorkerFlags binds the parallelism knobs, which both commands expose
// with identical meaning: workers drives the outer per-frame pool and
// convWorkers the inner convolution. Both default to 0, meaning auto.
func registerWorkerFlags(fs *pflag.FlagSet, workers, convWorkers *int) {
	fs.IntVarP(workers, "workers", "w", 0, "processing workers (0 = auto: NumCPU/2)")
	fs.IntVar(convWorkers, "conv-workers", 0, "inner convolution workers (0 = auto)")
}

// analyze builds the analyzeOptions carrying these thresholds, for prepare to
// reuse processImage with the same criteria analyze uses.
func (q qualityThresholds) analyze() analyzeOptions {
	return analyzeOptions{
		qualityThresholds: q,
		// limitComputedStars caps how many of the brightest stars are measured,
		// and processImage needs it set; prepare exposes no flag for it.
		limitComputedStars: analyzeDefaultLimitComputedStars,
	}
}
