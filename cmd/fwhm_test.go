package cmd

import "testing"

// fwhmFrames builds a filter population with the given FWHM values.
func fwhmFrames(filter string, values ...float64) []ImageResult {
	out := make([]ImageResult, len(values))
	for i, v := range values {
		out[i] = ImageResult{
			Filename: "f",
			Filter:   filter,
			AvgFWHM:  v,
			Decision: decisionApproved,
		}
	}
	return out
}

// steady is nine frames clustered tightly enough to give a usable scale.
func steady() []float64 {
	return []float64{1.98, 2.02, 1.98, 2.02, 1.98, 2.02, 1.98, 2.02, 2.02}
}

func TestApplyFWHMRuleRejectsUpperOutlier(t *testing.T) {
	vals := append(steady(), 2.30)
	results := fwhmFrames("L", vals...)

	applyFWHMRuleByFilter(results, qualityThresholds{maxFWHMSigma: 3.0})

	if results[len(results)-1].Decision != decisionFWHM {
		t.Errorf("outlier decision = %q, want %q", results[len(results)-1].Decision, decisionFWHM)
	}
	if got := results[len(results)-1].FWHMRatio; got < 1.13 || got > 1.15 {
		t.Errorf("fwhmRatio = %.4f, want ~1.14", got)
	}
	for i := 0; i < len(results)-1; i++ {
		if results[i].Decision != decisionApproved {
			t.Errorf("steady frame %d rejected: %q", i, results[i].Decision)
		}
		if results[i].FWHMRatio <= 0 {
			t.Errorf("steady frame %d has no measured fwhmRatio", i)
		}
	}
}

func TestApplyFWHMRuleLeavesTheSharpSideAlone(t *testing.T) {
	vals := append(steady(), 1.50)
	results := fwhmFrames("L", vals...)

	applyFWHMRuleByFilter(results, qualityThresholds{maxFWHMSigma: 3.0})

	if results[len(results)-1].Decision != decisionApproved {
		t.Errorf("a frame sharper than the median was rejected as %q", results[len(results)-1].Decision)
	}
}

func TestApplyFWHMRuleNeedsEnoughFrames(t *testing.T) {
	// Seven frames is below the sample floor: the scale is too unstable to
	// place a bound on, so even a clear outlier is left alone.
	results := fwhmFrames("L", 1.98, 2.02, 1.98, 2.02, 1.98, 2.02, 3.00)

	applyFWHMRuleByFilter(results, qualityThresholds{maxFWHMSigma: 3.0})

	for i, r := range results {
		if r.Decision != decisionApproved {
			t.Errorf("frame %d rejected with only %d frames in the filter", i, len(results))
		}
	}
}

func TestApplyFWHMRuleSkipsAZeroScale(t *testing.T) {
	// Nine identical values give a MAD of zero. Applying the bound would put
	// it exactly on the median and reject half the filter, so the gate is
	// skipped; the ratio is still reported for the outlier.
	vals := []float64{2, 2, 2, 2, 2, 2, 2, 2, 2, 3}
	results := fwhmFrames("L", vals...)

	applyFWHMRuleByFilter(results, qualityThresholds{maxFWHMSigma: 3.0})

	for i, r := range results {
		if r.Decision != decisionApproved {
			t.Errorf("frame %d rejected despite a zero MAD", i)
		}
	}
	if results[len(results)-1].FWHMRatio != 1.5 {
		t.Errorf("fwhmRatio = %v, want 1.5", results[len(results)-1].FWHMRatio)
	}
}

func TestApplyFWHMRuleDisabled(t *testing.T) {
	vals := append(steady(), 4.0)
	results := fwhmFrames("L", vals...)

	applyFWHMRuleByFilter(results, qualityThresholds{maxFWHMSigma: 0})

	for i, r := range results {
		if r.Decision != decisionApproved {
			t.Errorf("frame %d rejected with the gate disabled", i)
		}
	}
}

func TestApplyFWHMRuleUsesEachFilterPopulation(t *testing.T) {
	// The same 2.30 is an outlier among steady L frames but ordinary among B
	// frames, whose population is broader. The gate must be per filter.
	var results []ImageResult
	results = append(results, fwhmFrames("L", append(steady(), 2.30)...)...)
	results = append(results, fwhmFrames("B", 1.40, 1.55, 1.70, 1.85, 2.00, 2.15, 2.30, 2.45, 2.60, 2.75)...)

	applyFWHMRuleByFilter(results, qualityThresholds{maxFWHMSigma: 3.0})

	if results[9].Decision != decisionFWHM {
		t.Errorf("L outlier decision = %q, want %q", results[9].Decision, decisionFWHM)
	}
	for i := 10; i < len(results); i++ {
		if results[i].Decision != decisionApproved {
			t.Errorf("B frame %d rejected: %q", i-10, results[i].Decision)
		}
	}
}

func TestFWHMTakesPrecedenceOverCloudAndSeeing(t *testing.T) {
	results := fwhmFrames("L", append(steady(), 3.0)...)
	results[len(results)-1].Halo = 500
	results[len(results)-1].BgStructure = 0.99
	results[len(results)-1].Filename = "outlier_20260101_000000_000.FIT"

	applyFWHMRuleByFilter(results, qualityThresholds{maxFWHMSigma: 3.0, maxHaloRatio: 2.0, maxStructure: 1.12})

	// The session pass must not overwrite the more specific reason.
	ptrs := make([]*ImageResult, len(results))
	for i := range results {
		ptrs[i] = &results[i]
	}
	applySessionRules(ptrs, qualityThresholds{maxHaloRatio: 2.0, maxStructure: 1.12})

	if results[len(results)-1].Decision != decisionFWHM {
		t.Errorf("decision = %q, want %q: FWHM is the more specific diagnosis",
			results[len(results)-1].Decision, decisionFWHM)
	}
}

func TestApplyFWHMRuleIgnoresUnmeasuredFrames(t *testing.T) {
	results := fwhmFrames("L", append(steady(), 0)...)
	results[len(results)-1].Decision = decisionNoStars

	applyFWHMRuleByFilter(results, qualityThresholds{maxFWHMSigma: 3.0})

	if results[len(results)-1].FWHMRatio != 0 {
		t.Errorf("unmeasured frame has fwhmRatio %v, want 0", results[len(results)-1].FWHMRatio)
	}
	if results[len(results)-1].Decision != decisionNoStars {
		t.Errorf("unmeasured frame decision = %q, want %q", results[len(results)-1].Decision, decisionNoStars)
	}
}
