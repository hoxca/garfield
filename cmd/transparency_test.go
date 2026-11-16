package cmd

import (
	"fmt"
	"testing"
)

// exhaust builds n frames spaced minutes apart, all at the given contrast, so a
// windowed reference has a flat neighbourhood to work with.
func exhaust(n, startMin int, snr float64) []*ImageResult {
	out := make([]*ImageResult, n)
	for i := range out {
		total := startMin + i*5
		out[i] = &ImageResult{
			Filename: fmt.Sprintf("TGT_LIGHT_G_300s_BIN1_-10C_GA0_20260101_%02d%02d00_%03d_PA310_E.FIT",
				total/60, total%60, i),
			SNR:      snr,
			Halo:     50,
			AvgFWHM:  2.0,
			Decision: decisionApproved,
		}
	}
	return out
}

func TestTransparencyRejectsAFrameDimmerThanItsNeighbours(t *testing.T) {
	q := qualityThresholds{minTransparency: 0.90}

	group := exhaust(12, 0, 20.0)
	// One frame a third dimmer than the hour around it: the obvious case.
	group[6].SNR = 13.0

	applySessionRules(group, q)

	if group[6].Decision != decisionTransparency {
		t.Errorf("decision = %q for a frame at %.0f%% of its neighbours, want %q",
			group[6].Decision, 100*group[6].Transparency, decisionTransparency)
	}
	for i, r := range group {
		if i == 6 {
			continue
		}
		if r.Decision != decisionApproved {
			t.Errorf("frame %d rejected at transparency %.2f", i, r.Transparency)
		}
	}
}

// TestTransparencyIsRelativeToTheHour is the behaviour that keeps a hazy hour from
// condemning the clear one: each frame is judged against its own neighbourhood,
// not against the best of the whole night.
func TestTransparencyIsRelativeToTheHour(t *testing.T) {
	q := qualityThresholds{minTransparency: 0.90}

	// An hour of good conditions, then three hours later an hour of poorer ones,
	// uniformly so: there is no cloud in the second block, only a lower sky.
	group := exhaust(12, 0, 20.0)
	group = append(group, exhaust(12, 180, 15.0)...)

	applySessionRules(group, q)

	for i, r := range group {
		if r.Decision != decisionApproved {
			t.Errorf("frame %d rejected at transparency %.2f: a uniformly poorer hour is not cloud",
				i, r.Transparency)
		}
	}
	if got := group[12].Transparency; got < 0.95 || got > 1.05 {
		t.Errorf("second block transparency = %.2f against its own hour, want ~1", got)
	}
}

func TestTransparencyUsesAFrozenBaseline(t *testing.T) {
	q := qualityThresholds{minTransparency: 0.90}
	group := exhaust(3, 0, 10.0)

	// A suggestion pins the reference, so three frames suffice and the ratio is
	// against the stack the suggestion saw rather than against themselves.
	applySessionRulesWith(group, q, 0, 0, 0, 20.0)

	if got := group[0].Transparency; got < 0.49 || got > 0.51 {
		t.Errorf("transparency = %.2f with a frozen baseline of 20, want 0.5", got)
	}
	if group[0].Decision != decisionTransparency {
		t.Errorf("decision = %q, want %q", group[0].Decision, decisionTransparency)
	}
}

func TestTransparencyNeedsANeighbourhood(t *testing.T) {
	q := qualityThresholds{minTransparency: 0.90}
	group := exhaust(3, 0, 20.0)
	group[1].SNR = 2.0

	applySessionRules(group, q)

	if group[1].Transparency != 0 {
		t.Errorf("transparency = %.2f on three frames, want 0: no reference", group[1].Transparency)
	}
	if group[1].Decision != decisionApproved {
		t.Errorf("decision = %q without a reference, want APPROVED", group[1].Decision)
	}
}

func TestTransparencyDisabled(t *testing.T) {
	q := qualityThresholds{minTransparency: 0}
	group := exhaust(12, 0, 20.0)
	group[3].SNR = 5.0

	applySessionRules(group, q)

	if group[3].Decision != decisionApproved {
		t.Errorf("decision = %q with the rule disabled, want APPROVED", group[3].Decision)
	}
}
