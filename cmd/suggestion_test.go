package cmd

import (
	"math"
	"testing"
)

// allFactors builds a factor set from one sigma value, so a test can prove each
// gate is using its own field rather than the shared band.
func allFactors(sigma float64) suggestionFactors {
	return suggestionFactors{
		MaxFWHM: sigma, MaxFWHMSigma: sigma, MaxEcc: sigma,
		MinSNR: sigma, MinScore: sigma, MinStars: sigma,
		MaxHaloRatio: sigma, MaxStructure: sigma,
	}
}

func TestSuggestionFactorsAreUsedPerGate(t *testing.T) {
	results := buildStack(t, "H", uniformNights(3, 8)...)
	prof := profileRun(results)
	opts := defaultOpts()
	opts.suggestion = allFactors(2)

	s, err := suggestForFilter(results, prof, "H", opts)
	if err != nil {
		t.Fatalf("suggestForFilter: %v", err)
	}

	fwhm := s.Reference["fwhm"]
	if want := roundTo(fwhm.Median+2*fwhm.Sigma, 2); s.Thresholds.MaxFWHM != want {
		t.Errorf("max-fwhm = %v, want %v (2 sigma)", s.Thresholds.MaxFWHM, want)
	}
	ecc := s.Reference["ecc"]
	if want := roundTo(math.Min(ecc.Median+2*ecc.Sigma, maxEccCeiling), 3); s.Thresholds.MaxEcc != want {
		t.Errorf("max-ecc = %v, want %v (2 sigma)", s.Thresholds.MaxEcc, want)
	}
	snr := s.Reference["snr"]
	if want := roundTo(math.Max(snr.Median-2*snr.Sigma, minSNRFloor), 2); s.Thresholds.MinSNR != want {
		t.Errorf("min-snr = %v, want %v (2 sigma)", s.Thresholds.MinSNR, want)
	}
	score := s.Reference["score"]
	if want := roundTo(math.Max(score.Median-2*score.Sigma, minScoreFloor), 2); s.Thresholds.MinScore != want {
		t.Errorf("min-score = %v, want %v (2 sigma)", s.Thresholds.MinScore, want)
	}
	stars := s.Reference["stars"]
	if want := int(math.Max(minStarsFloor, stars.Median-2*stars.Sigma)); s.Thresholds.MinStars != want {
		t.Errorf("min-stars = %d, want %d (2 sigma)", s.Thresholds.MinStars, want)
	}
	halo := s.Reference["halo"]
	if want := roundTo((halo.Median+2*halo.Sigma)/halo.P25, 2); s.Thresholds.MaxHaloRatio != want {
		t.Errorf("max-halo-ratio = %v, want %v (2 sigma)", s.Thresholds.MaxHaloRatio, want)
	}
	structure := s.Reference["struct"]
	if want := roundTo((structure.Median+2*structure.Sigma)/structure.P25, 2); s.Thresholds.MaxStructure != want {
		t.Errorf("max-structure = %v, want %v (2 sigma)", s.Thresholds.MaxStructure, want)
	}
	if s.Thresholds.MaxFWHMSigma != 2 {
		t.Errorf("max-fwhm-sigma = %v, want 2", s.Thresholds.MaxFWHMSigma)
	}
}

func TestSuggestionFactorsInheritTheAnalysisBand(t *testing.T) {
	opts := defaultOpts()
	opts.maxFWHMSigma = 3.0
	f := opts.resolveSuggestionFactors()
	if f.MaxFWHM != 3.0 || f.MaxFWHMSigma != 3.0 {
		t.Errorf("FWHM pair = %v/%v, want the analysis band 3.0", f.MaxFWHM, f.MaxFWHMSigma)
	}
	if f.MaxEcc != defaultSuggestionFactors.MaxEcc || f.MinStars != defaultSuggestionFactors.MinStars {
		t.Errorf("fixed factors = %+v, want the compiled defaults", f)
	}

	opts.suggestion.MaxFWHM = 5
	f = opts.resolveSuggestionFactors()
	if f.MaxFWHM != 5 || f.MaxFWHMSigma != 3.0 {
		t.Errorf("FWHM pair = %v/%v, want 5.0 and the inherited 3.0", f.MaxFWHM, f.MaxFWHMSigma)
	}
}

func TestKeepOverridesTheBandGroupButNotFixedOffsets(t *testing.T) {
	results := buildStack(t, "H", uniformNights(3, 8)...)
	prof := profileRun(results)
	opts := defaultOpts()
	opts.keep = 0.6
	opts.suggestion = allFactors(2)

	s, err := suggestForFilter(results, prof, "H", opts)
	if err != nil {
		t.Fatalf("suggestForFilter: %v", err)
	}
	band := s.Band
	if band < keepBandMin || band > keepBandMax {
		t.Fatalf("keep band = %v, outside [%v, %v]", band, keepBandMin, keepBandMax)
	}

	fwhm := s.Reference["fwhm"]
	if want := roundTo(fwhm.Median+band*fwhm.Sigma, 2); s.Thresholds.MaxFWHM != want {
		t.Errorf("max-fwhm = %v, want the searched band %v", s.Thresholds.MaxFWHM, want)
	}
	ecc := s.Reference["ecc"]
	if want := roundTo(math.Min(ecc.Median+2*ecc.Sigma, maxEccCeiling), 3); s.Thresholds.MaxEcc != want {
		t.Errorf("max-ecc = %v, want the fixed 2-sigma value %v", s.Thresholds.MaxEcc, want)
	}
	snr := s.Reference["snr"]
	if want := roundTo(math.Max(snr.Median-2*snr.Sigma, minSNRFloor), 2); s.Thresholds.MinSNR != want {
		t.Errorf("min-snr = %v, want the fixed 2-sigma value %v", s.Thresholds.MinSNR, want)
	}
	stars := s.Reference["stars"]
	if want := int(math.Max(minStarsFloor, stars.Median-2*stars.Sigma)); s.Thresholds.MinStars != want {
		t.Errorf("min-stars = %d, want the fixed 2-sigma value %d", s.Thresholds.MinStars, want)
	}
}

func TestSuggestionFactorsFromConfig(t *testing.T) {
	path := writeConfig(t, `
suggestion:
  max-fwhm: 2.5
  max-ecc: 2.0
  min-snr: 2.5
  min-stars: 1.0
`)
	v := mustReadConfig(t, path)
	f, err := suggestionFactorsFromConfig(v)
	if err != nil {
		t.Fatalf("suggestionFactorsFromConfig: %v", err)
	}
	if f.MaxFWHM != 2.5 || f.MaxEcc != 2.0 || f.MinSNR != 2.5 || f.MinStars != 1.0 {
		t.Errorf("factors = %+v, want the configured values", f)
	}
	if f.MaxFWHMSigma != 0 {
		t.Errorf("max-fwhm-sigma = %v, want 0 so the analysis band is inherited", f.MaxFWHMSigma)
	}
	if f.MinScore != defaultSuggestionFactors.MinScore {
		t.Errorf("min-score = %v, want the compiled default", f.MinScore)
	}

	// Zero is valid for the FWHM pair (inherit) and invalid elsewhere.
	inherit := mustReadConfig(t, writeConfig(t, "suggestion:\n  max-fwhm: 0\n"))
	if _, err := suggestionFactorsFromConfig(inherit); err != nil {
		t.Errorf("zero max-fwhm should mean inherit: %v", err)
	}
	for _, body := range []string{
		"suggestion:\n  max-ecc: 0\n",
		"suggestion:\n  min-snr: -1\n",
		"suggestion:\n  min-stars: -0.5\n",
	} {
		v := mustReadConfig(t, writeConfig(t, body))
		if _, err := suggestionFactorsFromConfig(v); err == nil {
			t.Errorf("accepted invalid factors in %q", body)
		}
	}
}

// applyConfig is the production path: a suggestion block must reach the options
// analyze's suggestion pass reads, not just the parser.
func TestConfigAppliesSuggestionFactors(t *testing.T) {
	path := writeConfig(t, "suggestion:\n  max-ecc: 2.0\n  min-stars: 1.0\n")

	origPath, origFactors := configPath, analyzeOpts.suggestion
	t.Cleanup(func() {
		configPath, analyzeOpts.suggestion = origPath, origFactors
	})
	configPath = path

	if err := applyConfig(analyzeCmd); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	if got := analyzeOpts.suggestion.MaxEcc; got != 2.0 {
		t.Errorf("max-ecc factor = %v, want 2.0", got)
	}
	if got := analyzeOpts.suggestion.MinStars; got != 1.0 {
		t.Errorf("min-stars factor = %v, want 1.0", got)
	}
	if got := analyzeOpts.suggestion.MaxFWHM; got != 0 {
		t.Errorf("max-fwhm factor = %v, want 0 so the band is inherited", got)
	}
}
