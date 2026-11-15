package cmd

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"garfield/internal/suggeststore"
)

// gateSet is everything a run needs to judge frames: the thresholds to apply and,
// when a suggestion is in force, the reference statistics those thresholds were
// derived from.
//
// Carrying the reference matters because the relative rules -- the FWHM outlier
// bound, the halo and structure multiples -- are comparisons against a
// population. Recomputing that population from a different set of frames moves
// the bound, so the same frame would be judged differently depending on what
// else happened to be in the run. A frozen reference makes the verdict depend on
// the frame, not on its neighbours.
type gateSet struct {
	base     qualityThresholds
	byFilter map[string]qualityThresholds
	refs     map[string]frozenReference
}

// frozenReference is the stack statistics of one filter, as a suggestion saw
// them.
type frozenReference struct {
	Refs map[string]stackRef
}

// baseGates judges with one threshold set for every filter, computing the
// references from the run itself. That is what a run without a suggestion does.
func baseGates(q qualityThresholds) gateSet {
	return gateSet{base: q}
}

// thresholds returns the thresholds in force for a filter.
func (g gateSet) thresholds(filter string) qualityThresholds {
	if q, ok := g.byFilter[filter]; ok {
		return q
	}
	return g.base
}

// frozen returns the reference pinned for a filter, when a suggestion is in
// force.
func (g gateSet) frozen(filter string) (frozenReference, bool) {
	ref, ok := g.refs[filter]
	return ref, ok
}

// suggested reports whether any filter is judged from a suggestion, so the run
// can say so.
func (g gateSet) suggested() bool { return len(g.byFilter) > 0 }

// gatesFromSuggestions builds the judging context prepare applies: the suggested
// thresholds per filter and the reference they were derived from.
//
// A filter without a suggestion is a warning rather than an error: falling back
// to the config for it is better than refusing to prepare the target, and the
// console names the filter so the mix is visible.
func gatesFromSuggestions(store *suggeststore.Store, filters []string, sessions []sessionPlan) (gateSet, []string) {
	gates := gateSet{
		base:     defaultThresholds,
		byFilter: map[string]qualityThresholds{},
		refs:     map[string]frozenReference{},
	}
	var warnings []string

	for _, filter := range filters {
		rec, ok := store.Lookup(filter)
		if !ok {
			warnings = append(warnings, fmt.Sprintf(
				"no suggested thresholds for filter %s: using the config", filter))
			continue
		}
		if rec.SuggestVersion != suggeststore.SuggestVersion {
			warnings = append(warnings, fmt.Sprintf(
				"suggestion for %s was written with schema %d, this build reads %d: using it anyway",
				filter, rec.SuggestVersion, suggeststore.SuggestVersion))
		}
		if rec.MeasureVersion != measureVersion {
			warnings = append(warnings, fmt.Sprintf(
				"suggestion for %s was derived from measurement schema %d, this build produces %d: re-run analyze --suggest",
				filter, rec.MeasureVersion, measureVersion))
		}

		// Coverage: a suggestion derived before a night was acquired does not speak
		// for that night, and applying it silently would hide the gap.
		for _, s := range sessions {
			if !hasFilter(s, filter) {
				continue
			}
			if rec.Population.From != "" && s.date < rec.Population.From {
				warnings = append(warnings, fmt.Sprintf(
					"suggestion for %s was derived from %s..%s and does not cover session %s",
					filter, rec.Population.From, rec.Population.To, s.date))
				break
			}
			if rec.Population.To != "" && s.date > rec.Population.To {
				warnings = append(warnings, fmt.Sprintf(
					"suggestion for %s was derived from %s..%s and does not cover session %s",
					filter, rec.Population.From, rec.Population.To, s.date))
				break
			}
		}

		gates.byFilter[filter] = qualityThresholds{
			minSNR:       rec.Thresholds.MinSNR,
			maxFWHM:      rec.Thresholds.MaxFWHM,
			maxEcc:       rec.Thresholds.MaxEcc,
			minScore:     rec.Thresholds.MinScore,
			minStars:     rec.Thresholds.MinStars,
			maxFWHMSigma: rec.Thresholds.MaxFWHMSigma,
			maxHaloRatio: rec.Thresholds.MaxHaloRatio,
			maxStructure: rec.Thresholds.MaxStructure,
			// The profile is a measurement choice, not a threshold: it comes from
			// the run's own flags, never from a suggestion.
			moffat: gates.base.moffat,
		}

		refs := map[string]stackRef{}
		for name, ref := range rec.Reference {
			refs[name] = stackRef{Median: ref.Median, Sigma: ref.Sigma, P25: ref.P25}
		}
		gates.refs[filter] = frozenReference{Refs: refs}
	}
	return gates, warnings
}

// hasFilter reports whether a session holds frames of a filter.
func hasFilter(s sessionPlan, filter string) bool {
	_, ok := s.filters[filter]
	return ok
}

// describeGates renders the provenance of a filter's judging context, for the
// console line that says where the numbers came from.
func describeGates(store *suggeststore.Store, filter string) string {
	rec, ok := store.Lookup(filter)
	if !ok {
		return "config"
	}
	since := "unknown"
	if !rec.Created.IsZero() {
		since = rec.Created.Format(time.RFC3339)
	}
	band := ""
	if rec.Band > 0 {
		band = fmt.Sprintf(", band %.1f sigma", rec.Band)
	}
	return fmt.Sprintf("suggestion of %s (n=%d, %d night(s)%s)",
		since, rec.Population.N, rec.Population.Sessions, band)
}

// sortedFilters returns the filters a set of sessions holds, sorted, so the
// console and the warnings come out the same way twice.
func sortedFilters(sessions []sessionPlan) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sessions {
		for _, f := range s.filterNames() {
			if seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// applyBaseDecisionGated sets each frame's base verdict with the thresholds in
// force for its filter.
func applyBaseDecisionGated(results []ImageResult, gates gateSet) {
	for i := range results {
		if results[i].Error != nil {
			continue
		}
		results[i].Decision = baseDecision(results[i], gates.thresholds(results[i].Filter))
	}
}

// applyFWHMRuleGated applies the FWHM outlier rule, using the frozen reference
// when a suggestion pinned one and the run's own population otherwise.
func applyFWHMRuleGated(results []ImageResult, gates gateSet) {
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
		q := gates.thresholds(f)

		var median, sigma float64
		if ref, ok := gates.frozen(f); ok {
			median, sigma = ref.Refs["fwhm"].Median, ref.Refs["fwhm"].Sigma
		} else {
			values := make([]float64, len(group))
			for i, r := range group {
				values[i] = r.AvgFWHM
			}
			median = medianOf(values)
			if q.maxFWHMSigma > 0 && len(group) >= fwhmMinSample {
				dev := make([]float64, len(values))
				for i, v := range values {
					dev[i] = abs(v - median)
				}
				sigma = 1.4826 * medianOf(dev)
			}
		}

		if q.maxFWHMSigma > 0 && sigma > 0 && (gates.hasFrozen(f) || len(group) >= fwhmMinSample) {
			upper := median + q.maxFWHMSigma*sigma
			for _, r := range group {
				if r.Decision == decisionApproved && r.AvgFWHM > upper {
					r.Decision = decisionFWHM
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

// hasFrozen reports whether a filter's reference came from a suggestion, in
// which case the sample-size guard does not apply: the reference is given, not
// estimated.
func (g gateSet) hasFrozen(filter string) bool {
	_, ok := g.refs[filter]
	return ok
}

// applySessionRulesBySessionGated applies the session-relative rules, taking the
// halo and structure baselines from the frozen reference when a suggestion
// pinned them.
func applySessionRulesBySessionGated(results []ImageResult, gates gateSet) {
	if !gates.suggested() && gates.base.maxHaloRatio <= 0 && gates.base.maxStructure <= 0 {
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
		filter := filterOfSessionKey(k)
		q := gates.thresholds(filter)
		var haloRef, structRef, fwhmRef float64
		if ref, ok := gates.frozen(filter); ok {
			haloRef = ref.Refs["halo"].P25
			structRef = ref.Refs["struct"].P25
			fwhmRef = ref.Refs["fwhm"].Median
		}
		applySessionRulesWith(groups[k], q, haloRef, structRef, fwhmRef)
	}
}

// filterOfSessionKey recovers the filter from a "<session>|<filter>" key.
func filterOfSessionKey(key string) string {
	if i := strings.LastIndex(key, "|"); i >= 0 {
		return key[i+1:]
	}
	return ""
}

// abs is math.Abs without the import in the callers that only need it here.
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
