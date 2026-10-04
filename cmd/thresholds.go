package cmd

import (
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
}

// defaultThresholds is the single point of configuration for those criteria.
//
// These values were tuned empirically against real frames, so treat a change as
// a decision rather than a tweak: --max-ecc was raised from 0.50 to 0.54 during
// development after frames with eccentricities just above 0.50 were being
// discarded. Editing a value here changes both commands at once.
var defaultThresholds = qualityThresholds{
	minSNR:   11.0,
	maxFWHM:  5.0,
	maxEcc:   0.54,
	minScore: 2.0,
	minStars: 680,
}

// registerQualityFlags binds the shared thresholds to dst. Keeping the flag
// names and help text here as well as the values means the two commands present
// an identical interface.
func registerQualityFlags(fs *pflag.FlagSet, dst *qualityThresholds) {
	fs.Float64Var(&dst.minSNR, "min-snr", defaultThresholds.minSNR, "SNR minimal pour APPROUVÉE")
	fs.Float64Var(&dst.maxFWHM, "max-fwhm", defaultThresholds.maxFWHM, "FWHM maximale pour APPROUVÉE")
	fs.Float64Var(&dst.maxEcc, "max-ecc", defaultThresholds.maxEcc, "excentricité maximale pour APPROUVÉE")
	fs.Float64Var(&dst.minScore, "min-score", defaultThresholds.minScore, "score minimal pour APPROUVÉE")
	fs.IntVar(&dst.minStars, "min-stars", defaultThresholds.minStars, "nombre minimum d'étoiles détectées pour APPROUVÉE")
}

// registerWorkerFlags binds the parallelism knobs, which both commands expose
// with identical meaning: workers drives the outer per-frame pool and
// convWorkers the inner convolution. Both default to 0, meaning auto.
func registerWorkerFlags(fs *pflag.FlagSet, workers, convWorkers *int) {
	fs.IntVarP(workers, "workers", "w", 0, "workers de traitement (0 = auto : NumCPU/2)")
	fs.IntVar(convWorkers, "conv-workers", 0, "workers de convolution internes (0 = auto)")
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
