package cmd

import (
	"fmt"
	"math"

	"github.com/spf13/viper"
)

// suggestionFactors are the robust-sigma offsets --suggest uses, one per gate.
//
// MaxFWHM is both the offset for the suggested max-fwhm gate and the width
// written for the suggested max-fwhm-sigma rule: the two describe one rule, so
// a separate width could only disagree with the gate it came from. A zero value
// means "inherit thresholds.max-fwhm-sigma", the band the FWHM rule already
// uses, so a configuration that only sets the threshold keeps the coupling.
type suggestionFactors struct {
	MaxFWHM      float64
	MaxEcc       float64
	MinSNR       float64
	MinScore     float64
	MinStars     float64
	MaxHaloRatio float64
	MaxStructure float64
}

// defaultSuggestionFactors are the values shipped in conf/garfield.yaml. The
// FWHM band stays zero so it inherits the analysis one; everything else is the
// offset the suggestion has always used.
var defaultSuggestionFactors = suggestionFactors{
	MaxEcc:       3.0,
	MinSNR:       3.0,
	MinScore:     4.0,
	MinStars:     1.85,
	MaxHaloRatio: 4.0,
	MaxStructure: 4.0,
}

// resolveSuggestionFactors fills the unset offsets. A zero field is "not
// configured": the fixed gates fall back to their compiled values, and the FWHM
// band falls back to the run's max-fwhm-sigma, which is the band that rule uses.
func (o analyzeOptions) resolveSuggestionFactors() suggestionFactors {
	f := o.suggestion
	def := defaultSuggestionFactors
	if f.MaxEcc <= 0 {
		f.MaxEcc = def.MaxEcc
	}
	if f.MinSNR <= 0 {
		f.MinSNR = def.MinSNR
	}
	if f.MinScore <= 0 {
		f.MinScore = def.MinScore
	}
	if f.MinStars <= 0 {
		f.MinStars = def.MinStars
	}
	if f.MaxHaloRatio <= 0 {
		f.MaxHaloRatio = def.MaxHaloRatio
	}
	if f.MaxStructure <= 0 {
		f.MaxStructure = def.MaxStructure
	}

	band := o.maxFWHMSigma
	if band <= 0 {
		band = defaultThresholds.maxFWHMSigma
	}
	if f.MaxFWHM <= 0 {
		f.MaxFWHM = band
	}
	return f
}

// suggestionConfigKeys maps the YAML key to the field it sets. The names are the
// gate flags, so the section reads like the thresholds block.
var suggestionConfigKeys = []struct {
	key string
	set func(*suggestionFactors, float64)
	// inherit marks keys where zero means "use thresholds.max-fwhm-sigma" rather
	// than an invalid zero-width band.
	inherit bool
}{
	{"max-fwhm", func(f *suggestionFactors, v float64) { f.MaxFWHM = v }, true},
	{"max-ecc", func(f *suggestionFactors, v float64) { f.MaxEcc = v }, false},
	{"min-snr", func(f *suggestionFactors, v float64) { f.MinSNR = v }, false},
	{"min-score", func(f *suggestionFactors, v float64) { f.MinScore = v }, false},
	{"min-stars", func(f *suggestionFactors, v float64) { f.MinStars = v }, false},
	{"max-halo-ratio", func(f *suggestionFactors, v float64) { f.MaxHaloRatio = v }, false},
	{"max-structure", func(f *suggestionFactors, v float64) { f.MaxStructure = v }, false},
}

// suggestionFactorsFromConfig reads the suggestion section and overlays it on
// the compiled defaults. A sigma offset must be a positive finite number; the
// FWHM band may be zero to inherit the analysis one.
func suggestionFactorsFromConfig(v *viper.Viper) (suggestionFactors, error) {
	f := defaultSuggestionFactors
	for _, entry := range suggestionConfigKeys {
		key := "suggestion." + entry.key
		if !v.IsSet(key) {
			continue
		}
		value := v.GetFloat64(key)
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 ||
			(value == 0 && !entry.inherit) {
			return suggestionFactors{}, fmt.Errorf(
				"%s: %v is not a positive sigma factor", key, value)
		}
		entry.set(&f, value)
	}
	return f, nil
}

// suggestionConfigNames lists the keys the suggestion section owns, so the
// unknown-key warning accepts them and reports typos inside the block.
func suggestionConfigNames() []string {
	names := make([]string, 0, len(suggestionConfigKeys))
	for _, entry := range suggestionConfigKeys {
		names = append(names, "suggestion."+entry.key)
	}
	return names
}
