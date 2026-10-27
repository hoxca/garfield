package cmd

import (
	"math"
	"strings"
	"testing"
)

func TestCloudIndexFor(t *testing.T) {
	r := ImageResult{SNR: 20, Background: 1000}
	want := (1.0 / 20) / math.Sqrt(1000)
	if got := cloudIndexFor(r); math.Abs(got-want) > 1e-12 {
		t.Errorf("cloudIndexFor = %.10f, want %.10f", got, want)
	}

	// Degenerate inputs must yield 0 rather than Inf/NaN: a NaN here would
	// compare false against every threshold and silently disable the rule.
	for _, bad := range []ImageResult{
		{SNR: 0, Background: 1000},
		{SNR: -1, Background: 1000},
		{SNR: 20, Background: 0},
		{SNR: 20, Background: -5},
	} {
		if got := cloudIndexFor(bad); got != 0 {
			t.Errorf("cloudIndexFor(%+v) = %v, want 0", bad, got)
		}
	}
}

// quietGroup builds n frames whose cloud index is identical, so the session
// baseline is unambiguous.
func quietGroup(n int) []*ImageResult {
	out := make([]*ImageResult, n)
	for i := range out {
		out[i] = &ImageResult{
			Filename:    "f",
			SNR:         25,
			Background:  1200,
			BgStructure: 0.50,
			Decision:    decisionApproved,
		}
	}
	return out
}

func TestApplyCloudRuleRatio(t *testing.T) {
	q := qualityThresholds{maxCloud: 1.08, maxStructure: 1.12}

	// Four quiet frames plus one 15% worse than the baseline. The 25th
	// percentile of five values is the second smallest, i.e. the quiet level, so
	// the outlier must land above maxCloud.
	group := quietGroup(5)
	group[4].SNR = 25 / 1.15 // same background, so the index scales by 1.15

	applyCloudRule(group, q)

	if group[4].Decision != decisionClouds {
		t.Errorf("ratio outlier decision = %q, want %q (ratio %.3f)",
			group[4].Decision, decisionClouds, group[4].CloudRatio)
	}
	if group[4].CloudRatio < 1.14 || group[4].CloudRatio > 1.16 {
		t.Errorf("cloudRatio = %.4f, want ~1.15", group[4].CloudRatio)
	}
	for i := 0; i < 4; i++ {
		if group[i].Decision != decisionApproved {
			t.Errorf("quiet frame %d rejected: %q", i, group[i].Decision)
		}
	}
}

func TestApplyCloudRuleStructure(t *testing.T) {
	q := qualityThresholds{maxCloud: 1.08, maxStructure: 1.12}

	// Structure catches the patchy case that leaves the ratio untouched: all
	// frames measure identically except one, whose sky varies more across the
	// field.
	group := quietGroup(5)
	group[2].BgStructure = 0.65

	applyCloudRule(group, q)

	if group[2].Decision != decisionClouds {
		t.Errorf("structurally clouded frame decision = %q, want %q", group[2].Decision, decisionClouds)
	}
	if group[2].CloudRatio < 0.99 || group[2].CloudRatio > 1.01 {
		t.Errorf("cloudRatio = %.4f, want ~1.0: structure, not ratio, should have flagged it",
			group[2].CloudRatio)
	}
}

func TestApplyCloudRuleDoesNotOverwriteASpecificReason(t *testing.T) {
	q := qualityThresholds{maxCloud: 1.08, maxStructure: 1.12}
	group := quietGroup(5)
	group[1].BgStructure = 0.99
	group[1].Decision = decisionEcc

	applyCloudRule(group, q)

	if group[1].Decision != decisionEcc {
		t.Errorf("decision = %q, want %q: a more specific rejection must survive",
			group[1].Decision, decisionEcc)
	}
}

func TestApplyCloudRuleNeedsABaseline(t *testing.T) {
	q := qualityThresholds{maxCloud: 1.08, maxStructure: 1.12}

	// Too few frames for a percentile: the ratio test is skipped, otherwise a
	// three-frame run would compare each frame against a meaningless reference.
	group := quietGroup(3)
	group[2].SNR = 25 / 2
	applyCloudRule(group, q)
	if group[2].Decision != decisionApproved {
		t.Errorf("a 2x outlier among three frames was rejected on ratio; baseline is unreliable")
	}
	if group[2].CloudRatio != 0 {
		t.Errorf("cloudRatio = %v, want 0 when there is no baseline", group[2].CloudRatio)
	}

	// Structure is relative to the session now, so it needs the same baseline
	// and is skipped for the same reason.
	group = quietGroup(3)
	group[0].BgStructure = 0.70
	applyCloudRule(group, q)
	if group[0].Decision != decisionApproved {
		t.Errorf("structure was applied without a session baseline")
	}
	if group[0].StructRatio != 0 {
		t.Errorf("structRatio = %v, want 0 when there is no baseline", group[0].StructRatio)
	}
}

func TestApplyCloudRuleDisabled(t *testing.T) {
	group := quietGroup(5)
	group[0].BgStructure = 0.99
	group[0].SNR = 1

	vals := toValues(group)
	applyCloudRuleBySession(vals, qualityThresholds{maxCloud: 0, maxStructure: 0})

	for i, r := range vals {
		if r.Decision != decisionApproved {
			t.Errorf("frame %d rejected with both thresholds disabled", i)
		}
		if r.CloudIndex != 0 || r.CloudRatio != 0 {
			t.Errorf("frame %d carries cloud numbers with the rule disabled", i)
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
	for _, k := range []string{"thresholds.max-cloud", "thresholds.max-structure"} {
		if !keys[k] {
			t.Errorf("%s is not a known config key", k)
		}
	}
}

// TestSessionKeys pins the grouping the cloud rule depends on. Both defects it
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
