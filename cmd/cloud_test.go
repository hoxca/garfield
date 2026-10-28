package cmd

import (
	"strings"
	"testing"
)

// quietGroup builds n frames whose halo and structure are identical, so the
// session baselines are unambiguous.
func quietGroup(n int) []*ImageResult {
	out := make([]*ImageResult, n)
	for i := range out {
		out[i] = &ImageResult{
			Filename:    "f",
			Halo:        50,
			BgStructure: 0.50,
			Decision:    decisionApproved,
		}
	}
	return out
}

func TestApplyHaloRuleRejectsClouds(t *testing.T) {
	q := qualityThresholds{maxHaloRatio: 2.0, maxStructure: 1.12}

	// Four quiet frames plus one whose bright-star wings are 2.5x the session.
	// The 25th percentile of five values is the second smallest, i.e. the quiet
	// level, so the outlier must land above maxHaloRatio.
	group := quietGroup(5)
	group[4].Halo = 50 * 2.5

	applySessionRules(group, q)

	if group[4].Decision != decisionClouds {
		t.Errorf("halo outlier decision = %q, want %q (ratio %.3f)",
			group[4].Decision, decisionClouds, group[4].HaloRatio)
	}
	if group[4].HaloRatio < 2.49 || group[4].HaloRatio > 2.51 {
		t.Errorf("haloRatio = %.4f, want ~2.5", group[4].HaloRatio)
	}
	for i := 0; i < 4; i++ {
		if group[i].Decision != decisionApproved {
			t.Errorf("quiet frame %d rejected: %q", i, group[i].Decision)
		}
	}
}

func TestApplyStructureRuleRejectsBadSeeing(t *testing.T) {
	q := qualityThresholds{maxHaloRatio: 2.0, maxStructure: 1.12}

	// Structure catches the patchy frame whose halo is normal. It is labelled
	// bad seeing, not clouds: the fix is to discard the frame, not to blame the
	// sky's transparency.
	group := quietGroup(5)
	group[2].BgStructure = 0.65

	applySessionRules(group, q)

	if group[2].Decision != decisionBadSeeing {
		t.Errorf("structured frame decision = %q, want %q", group[2].Decision, decisionBadSeeing)
	}
	if group[2].HaloRatio < 0.99 || group[2].HaloRatio > 1.01 {
		t.Errorf("haloRatio = %.4f, want ~1.0: structure, not halo, should have flagged it",
			group[2].HaloRatio)
	}
	if group[0].Decision != decisionApproved {
		t.Errorf("a quiet frame was rejected: %q", group[0].Decision)
	}
}

func TestCloudTakesPrecedenceOverBadSeeing(t *testing.T) {
	q := qualityThresholds{maxHaloRatio: 2.0, maxStructure: 1.12}
	group := quietGroup(5)
	group[1].Halo = 200
	group[1].BgStructure = 0.90

	applySessionRules(group, q)

	if group[1].Decision != decisionClouds {
		t.Errorf("decision = %q, want %q: cloud is the more specific diagnosis",
			group[1].Decision, decisionClouds)
	}
}

func TestApplyRulesDoNotOverwriteASpecificReason(t *testing.T) {
	q := qualityThresholds{maxHaloRatio: 2.0, maxStructure: 1.12}
	group := quietGroup(5)
	group[1].Halo = 500
	group[1].BgStructure = 0.99
	group[1].Decision = decisionEcc

	applySessionRules(group, q)

	if group[1].Decision != decisionEcc {
		t.Errorf("decision = %q, want %q: a more specific rejection must survive",
			group[1].Decision, decisionEcc)
	}
}

func TestApplyRulesNeedABaseline(t *testing.T) {
	q := qualityThresholds{maxHaloRatio: 2.0, maxStructure: 1.12}

	// Too few frames for a percentile: both tests are skipped, otherwise a
	// three-frame run would compare each frame against a meaningless reference.
	group := quietGroup(3)
	group[2].Halo = 50 * 4
	applySessionRules(group, q)
	if group[2].Decision != decisionApproved {
		t.Errorf("a 4x halo outlier among three frames was rejected; baseline is unreliable")
	}
	if group[2].HaloRatio != 0 {
		t.Errorf("haloRatio = %v, want 0 when there is no baseline", group[2].HaloRatio)
	}

	group = quietGroup(3)
	group[0].BgStructure = 0.70
	applySessionRules(group, q)
	if group[0].Decision != decisionApproved {
		t.Errorf("structure was applied without a session baseline")
	}
	if group[0].StructRatio != 0 {
		t.Errorf("structRatio = %v, want 0 when there is no baseline", group[0].StructRatio)
	}
}

func TestApplyRulesDisabled(t *testing.T) {
	group := quietGroup(5)
	group[0].Halo = 500
	group[0].BgStructure = 0.99

	vals := toValues(group)
	applySessionRulesBySession(vals, qualityThresholds{maxHaloRatio: 0, maxStructure: 0})

	for i, r := range vals {
		if r.Decision != decisionApproved {
			t.Errorf("frame %d rejected with both thresholds disabled", i)
		}
		if r.HaloRatio != 0 || r.StructRatio != 0 {
			t.Errorf("frame %d carries session ratios with the rules disabled", i)
		}
	}
}

// toValues copies pointers into a slice of values, as analyze holds them.
func toValues(ps []*ImageResult) []ImageResult {
	out := make([]ImageResult, len(ps))
	for i, p := range ps {
		out[i] = *p
	}
	return out
}

func TestPercentileSorted(t *testing.T) {
	v := []float64{1, 2, 3, 4, 5}
	if got := percentileSorted(v, 0.25); got != 2 {
		t.Errorf("p25 = %v, want 2", got)
	}
	if got := percentileSorted(v, 0.5); got != 3 {
		t.Errorf("p50 = %v, want 3", got)
	}
	if got := percentileSorted(v, 1); got != 5 {
		t.Errorf("p100 = %v, want 5", got)
	}
	if got := percentileSorted(nil, 0.25); got != 0 {
		t.Errorf("p25 of empty = %v, want 0", got)
	}
	// Interpolation between neighbours.
	if got := percentileSorted([]float64{0, 10}, 0.5); got != 5 {
		t.Errorf("p50 of two = %v, want 5", got)
	}
}

func TestCloudThresholdsAreConfigKeys(t *testing.T) {
	keys := knownConfigKeys()
	for _, k := range []string{"thresholds.max-fwhm-sigma", "thresholds.max-halo-ratio", "thresholds.max-structure"} {
		if !keys[k] {
			t.Errorf("%s is not a known config key", k)
		}
	}
}

// TestSessionKeys pins the grouping the session rules depend on. Both defects it
// fixes were observed on real data: a night that crosses midnight, and two
// different sessions sharing a date string.
func TestSessionKeys(t *testing.T) {
	mk := func(name, filter string) ImageResult {
		return ImageResult{Filename: name, Filter: filter, Decision: decisionApproved}
	}

	results := []ImageResult{
		// One session, 21:49 -> 00:03, crossing midnight.
		mk("VDB149_LIGHT_R_600s_BIN1_-10C_001_20220825_214936_014_W.FIT", "R"),
		mk("VDB149_LIGHT_R_600s_BIN1_-10C_010_20220825_235227_356_W.FIT", "R"),
		mk("VDB149_LIGHT_R_600s_BIN1_-10C_011_20220826_000329_116_W.FIT", "R"),
		// Same filter, next night, 23 hours later.
		mk("VDB149_LIGHT_R_600s_BIN1_-10C_001_20220826_234900_000_W.FIT", "R"),
		// Same session, different filter: must not share a baseline.
		mk("VDB149_LIGHT_B_600s_BIN1_-10C_001_20220825_220000_000_W.FIT", "B"),
		// No stamp at all.
		mk("no-stamp.FIT", "L"),
	}

	keys := sessionKeys(results)
	key := func(i int) string { return keys[i] }

	if key(0) != key(1) || key(1) != key(2) {
		t.Errorf("a session crossing midnight was split: %q %q %q", key(0), key(1), key(2))
	}
	if key(0) == key(3) {
		t.Errorf("two nights were merged into one session: %q", key(0))
	}
	// The B frame shares the session but not the filter.
	sessionOf := func(k string) string { return k[:strings.IndexByte(k, '|')] }
	if sessionOf(key(0)) != sessionOf(key(4)) {
		t.Errorf("frames from one night landed in different sessions: %q vs %q", key(0), key(4))
	}
	if key(0) == key(4) {
		t.Errorf("different filters shared one group: %q", key(0))
	}
	if key(5) != "unknown|L" {
		t.Errorf("unstamped frame key = %q, want %q", key(5), "unknown|L")
	}
}

// TestSessionKeysOvernightGap checks a real overnight gap splits, without
// relying on the date token changing.
func TestSessionKeysOvernightGap(t *testing.T) {
	results := []ImageResult{
		{Filename: "a_20220825_230000_000_W.FIT", Filter: "R"},
		{Filename: "b_20220826_000000_000_W.FIT", Filter: "R"}, // 1h later, same night
		{Filename: "c_20220826_200000_000_W.FIT", Filter: "R"}, // 20h later
	}
	keys := sessionKeys(results)
	if keys[0] != keys[1] {
		t.Errorf("frames an hour apart split: %q vs %q", keys[0], keys[1])
	}
	if keys[1] == keys[2] {
		t.Errorf("a 20-hour gap did not start a new session: %q", keys[1])
	}
}
