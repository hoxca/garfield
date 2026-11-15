package cmd

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"garfield/internal/suggeststore"
)

// Threshold suggestion.
//
// A run judges frames against fixed thresholds, which cannot be right for every
// filter: S and O are weaker than L, sparse fields detect fewer stars, and what
// counts as a soft frame in pixels depends on the sampling of that night. What
// the data can say is where the normal frames of this filter sit and how far out
// its unusual ones are, so the suggestion is a robust band around the good part
// of the stack, tightened or loosened until the requested fraction survives.
//
// The reference is the "good stack": frames from nights the session profile
// cleared, plus the frames of flagged nights that individually match the stack.
// That is what keeps a uniformly bad night from setting the standard, and what
// keeps a single bad frame of a good night from being used either.

const (
	// defaultKeepBudget of 0 means "use the band the FWHM rule already applies". A
	// positive value asks
	// the band to be adjusted until that fraction of the good stack survives,
	// within the bounds below: someone with one clear night should keep nearly
	// everything, someone with twelve nights can afford to discard half.
	defaultKeepBudget = 0.0

	// The band a --keep search may move within, in robust sigmas. Below 1 the
	// gates sit inside the normal scatter of a clean stack; above 5 they stop
	// rejecting anything a person would call an outlier. The ceiling has to sit
	// above the default band, or --keep could only ever tighten.
	keepBandMin  = 1.0
	keepBandMax  = 5.0
	keepBandStep = 0.1

	// Clamps. A quantile alone would happily certify a bad night, so every
	// suggested value is bounded by what the metric can physically mean.
	minSNRFloor   = 3.0 // below this, detection is noise-limited
	maxEccCeiling = 0.8 // beyond this the stars are trailed, not merely elongated
	minScoreFloor = 0.0 // the score is a figure of merit, not a floor
	minStarsFloor = 100 // brightSkip + minUsableStars: below it no metric exists
	minStarsShare = 0.5 // never suggest keeping a filter's star-poor half
)

// suggestionPath is where the suggestions of a run are stored, beside the
// measurement cache so `prepare` can find both.
func suggestionPath(output string) string {
	return filepath.Join(output, cacheDir, "suggestions.jsonl")
}

// suggestedThresholds is one filter's suggestion with the counts behind it.
type suggestedThresholds struct {
	Filter        string
	Thresholds    suggeststore.Thresholds
	Reference     map[string]suggeststore.Reference
	Population    suggeststore.Population
	Heterogeneity float64
	// Kept is the fraction of the filter's measured frames the suggested gates
	// would approve; KeptCount and Total are the same as counts. GoodCount is
	// the good stack the band was derived from, and KeptGood how many of those it
	// keeps -- the budget is spent on those, not on nights already condemned.
	Kept      float64
	KeptCount int
	Total     int
	GoodCount int
	KeptGood  int
	Sessions  []suggeststore.SessionNote
	// Excluded lists the nights left out of the reference, so the suggestion is
	// read for what it is.
	Excluded []sessionProfile
	// Band is the width of the gates in robust sigmas around the good stack's
	// median.
	Band float64
}

// suggestForFilter derives a threshold set for one filter from the run's
// measurements.
func suggestForFilter(results []ImageResult, prof sessionProfiles, filter string, opts analyzeOptions) (suggestedThresholds, error) {
	var out suggestedThresholds
	out.Filter = filter
	out.Total = 0

	indices := measuredFrames(results, filter)
	if len(indices) == 0 {
		return out, fmt.Errorf("no measured frame for filter %s to suggest from", filter)
	}
	out.Total = len(indices)

	good := goodStack(results, prof, filter)
	if len(good) < fwhmMinSample {
		return out, fmt.Errorf(
			"only %d frame(s) of filter %s belong to a night the stack does not condemn (need %d): analyse more nights before asking for a suggestion",
			len(good), filter, fwhmMinSample)
	}

	refs := stackRefs(results, good)
	out.Reference = referenceFromRefs(refs)

	profiles := prof.profiles(filter)
	out.Heterogeneity = 0
	from, to := "", ""
	for _, p := range profiles {
		out.Sessions = append(out.Sessions, suggeststore.SessionNote{Date: p.Date, N: p.N, Flags: p.Flags})
		if len(p.Flags) > 0 {
			out.Excluded = append(out.Excluded, p)
			continue
		}
		out.Heterogeneity = math.Max(out.Heterogeneity, p.H)
		if from == "" || p.Date < from {
			from = p.Date
		}
		if p.Date > to {
			to = p.Date
		}
	}
	if len(out.Excluded) > 0 {
		// The heterogeneity index of an excluded night is the filter's; keep the
		// largest so the report says how distinguishable the nights were.
		out.Heterogeneity = math.Max(out.Heterogeneity, profiles[0].H)
	}
	out.Population = suggeststore.Population{
		N:        out.Total,
		Sessions: len(profiles),
		From:     from,
		To:       to,
	}

	// The budget is spent on the good stack, not on the whole population: nights
	// the profile condemned are meant to be discarded, so counting them against
	// the budget would demand gates wide enough to re-admit them.
	out.GoodCount = len(good)

	// The band is the one the FWHM outlier rule already applies, so a suggestion
	// never disagrees with the gate it replaces: tightening --max-fwhm-sigma
	// tightens both. Zero means that rule is disabled, and the default applies.
	//
	// A larger band keeps more, so with a budget the tightest band that still
	// meets it is the most useful one.
	band := opts.maxFWHMSigma
	if band <= 0 {
		band = defaultThresholds.maxFWHMSigma
	}
	if opts.keep > 0 {
		steps := int(math.Round((keepBandMax - keepBandMin) / keepBandStep))
		band = keepBandMax
		for i := 0; i <= steps; i++ {
			k := roundTo(keepBandMin+float64(i)*keepBandStep, 2)
			band = k
			th := thresholdsFromRefs(refs, k)
			if float64(countAccepted(results, good, th))/float64(out.GoodCount) >= opts.keep {
				break
			}
		}
	}

	out.Band = band
	out.Thresholds = thresholdsFromRefs(refs, band)
	out.KeptGood = countAccepted(results, good, out.Thresholds)
	out.KeptCount = countAccepted(results, indices, out.Thresholds)
	out.Kept = float64(out.KeptCount) / float64(out.Total)
	return out, nil
}

// thresholdsFromRefs turns a band width in robust sigmas into the threshold
// values the commands take, applying the clamps that keep a band from certifying
// what it should not.
func thresholdsFromRefs(refs map[string]stackRef, band float64) suggeststore.Thresholds {
	fwhm := refs["fwhm"]
	ecc := refs["ecc"]
	snr := refs["snr"]
	score := refs["score"]
	stars := refs["stars"]
	halo := refs["halo"]
	structure := refs["struct"]

	t := suggeststore.Thresholds{
		MaxFWHM:      roundTo(fwhm.Median+band*fwhm.Sigma, 2),
		MaxEcc:       roundTo(math.Min(ecc.Median+band*ecc.Sigma, maxEccCeiling), 3),
		MinSNR:       roundTo(math.Max(snr.Median-band*snr.Sigma, minSNRFloor), 2),
		MinScore:     roundTo(math.Max(score.Median-band*score.Sigma, minScoreFloor), 2),
		MinStars:     int(math.Max(minStarsFloor, math.Min(stars.Median-band*stars.Sigma, minStarsShare*stars.Median))),
		MaxFWHMSigma: band,
	}
	if halo.P25 > 0 {
		t.MaxHaloRatio = roundTo((halo.Median+band*halo.Sigma)/halo.P25, 2)
	}
	if structure.P25 > 0 {
		t.MaxStructure = roundTo((structure.Median+band*structure.Sigma)/structure.P25, 2)
	}
	return t
}

// measuredFrames returns the indices of a filter's frames that carry a
// measurement.
func measuredFrames(results []ImageResult, filter string) []int {
	var out []int
	for i, r := range results {
		if r.Error != nil || r.StarCount == 0 || r.Filter != filter {
			continue
		}
		out = append(out, i)
	}
	return out
}

// goodStackSigma is the band a frame of a condemned night must sit inside before
// it may describe the reference. It is deliberately fixed and tighter than any
// gate: the band being searched must not decide what may pull it wider, or a
// wider band would admit more of a bad night, widen the reference, and justify
// itself.
const goodStackSigma = 3.0

// goodStack returns the frames a suggestion may be derived from: the nights the
// profile cleared, plus the frames of condemned nights that match the stack.
//
// The second half matters on a small stack, where one bad night would otherwise
// leave fewer frames than a robust band can be built from, and it is right in
// principle: a frame of a bad night that matches the good nights is a good frame,
// which is the same rule the stack gate applies when it decides what to reject.
func goodStack(results []ImageResult, prof sessionProfiles, filter string) []int {
	refs := prof.refs[filter]
	var out []int
	for _, i := range measuredFrames(results, filter) {
		if _, flagged := prof.flags[i]; !flagged {
			out = append(out, i)
			continue
		}
		if insideStack(results[i], refs) {
			out = append(out, i)
		}
	}
	return out
}

// insideStack reports whether a frame matches the stack on every metric, in the
// direction that matters, using the fixed goodStackSigma band.
func insideStack(r ImageResult, refs map[string]stackRef) bool {
	for _, spec := range sessionMetrics {
		ref := refs[spec.name]
		if ref.Sigma <= 0 {
			continue
		}
		v := spec.value(r)
		if spec.high {
			if v > ref.Median+goodStackSigma*ref.Sigma {
				return false
			}
			continue
		}
		if v < ref.Median-goodStackSigma*ref.Sigma {
			return false
		}
	}
	return true
}

// countAccepted counts how many frames the thresholds would approve.
func countAccepted(results []ImageResult, indices []int, t suggeststore.Thresholds) int {
	accepted := 0
	for _, i := range indices {
		r := results[i]
		switch {
		case t.MaxFWHM > 0 && r.AvgFWHM > t.MaxFWHM:
			continue
		case t.MaxEcc > 0 && r.AvgEccentricity > t.MaxEcc:
			continue
		case t.MinSNR > 0 && r.SNR < t.MinSNR:
			continue
		case t.MinScore > 0 && r.Score < t.MinScore:
			continue
		case t.MinStars > 0 && r.DetectedStars < t.MinStars:
			continue
		}
		accepted++
	}
	return accepted
}

// referenceFromRefs renders the stack statistics for the store and the YAML.
func referenceFromRefs(refs map[string]stackRef) map[string]suggeststore.Reference {
	out := make(map[string]suggeststore.Reference, len(refs))
	for name, ref := range refs {
		out[name] = suggeststore.Reference{Median: ref.Median, Sigma: ref.Sigma, P25: ref.P25}
	}
	return out
}

func roundTo(v float64, decimals int) float64 {
	scale := math.Pow(10, float64(decimals))
	return math.Round(v*scale) / scale
}

// gateRows is the order rejections are reported in: the base cascade first, then
// the two session rules. Both FWHM rules share one row because the report gives
// them one token, and attributing a rejection to the wrong one would be a guess.
var gateRows = []struct{ key, decision string }{
	{"min-stars", decisionNoStars},
	{"max-ecc", decisionEcc},
	{"max-fwhm", decisionFWHM},
	{"min-snr", decisionSNR},
	{"min-score", decisionScoring},
	{"max-halo-ratio", decisionClouds},
	{"max-structure", decisionStructure},
}

// rejectionCounts is how many frames each gate rejected.
//
// Rejections are attributed by the verdict the pipeline produced, not by
// re-testing the metrics, so the column sums to the rejected total exactly and
// agrees with the decision column of the report.
type rejectionCounts struct {
	byGate    map[string]int
	noMetrics int
	approved  int
	total     int
}

func (c rejectionCounts) rejected() int { return c.total - c.approved }

// countRejections tallies the verdicts in results, or in a simulated set of them.
func countRejections(results []ImageResult, decisions []string) rejectionCounts {
	c := rejectionCounts{byGate: map[string]int{}, total: len(results)}
	for i, r := range results {
		decision := r.Decision
		if decisions != nil {
			decision = decisions[i]
		}
		switch {
		case r.Error != nil:
			c.noMetrics++
		case decision == decisionApproved:
			c.approved++
		default:
			for _, row := range gateRows {
				if row.decision == decision {
					c.byGate[row.key]++
					break
				}
			}
		}
	}
	return c
}

// simulateSuggestion judges a copy of the run with the suggested gates and
// returns the verdict each frame would get.
//
// The copy is judged from scratch rather than by adjusting the existing verdicts,
// because the relative rules would otherwise be applied twice: a frame the
// current thresholds rejected is not APPROVED to the suggested ones.
func simulateSuggestion(results []ImageResult, s suggestedThresholds, opts analyzeOptions, prof sessionProfiles) []string {
	gates := gateSet{
		base:     opts.qualityThresholds,
		byFilter: map[string]qualityThresholds{},
		refs:     map[string]frozenReference{},
	}
	gates.byFilter[s.Filter] = qualityThresholds{
		minSNR:       s.Thresholds.MinSNR,
		maxFWHM:      s.Thresholds.MaxFWHM,
		maxEcc:       s.Thresholds.MaxEcc,
		minScore:     s.Thresholds.MinScore,
		minStars:     s.Thresholds.MinStars,
		maxFWHMSigma: s.Thresholds.MaxFWHMSigma,
		maxHaloRatio: s.Thresholds.MaxHaloRatio,
		maxStructure: s.Thresholds.MaxStructure,
		moffat:       opts.moffat,
	}
	refs := map[string]stackRef{}
	for name, ref := range s.Reference {
		refs[name] = stackRef{Median: ref.Median, Sigma: ref.Sigma, P25: ref.P25}
	}
	gates.refs[s.Filter] = frozenReference{Refs: refs}

	sim := make([]ImageResult, len(results))
	copy(sim, results)
	for i := range sim {
		sim[i].Decision = ""
		sim[i].FWHMRatio = 0
		sim[i].HaloRatio = 0
		sim[i].StructRatio = 0
	}
	applyBaseDecisionGated(sim, gates)
	applyFWHMRuleGated(sim, gates)
	applySessionRulesBySessionGated(sim, gates)
	if opts.stackGate {
		applyStackGate(sim, prof)
	}

	decisions := make([]string, len(sim))
	for i := range sim {
		decisions[i] = sim[i].Decision
	}
	return decisions
}

// printSuggestion reports the suggestion as a table: the values, and what each
// gate costs now and would cost under the suggestion.
//
// The count column is aligned on the arrow so the eye can run down the deltas,
// and the header uses the same field widths as the rows.
func printSuggestion(s suggestedThresholds, current qualityThresholds, now, then rejectionCounts) {
	fmt.Printf("\nsuggested thresholds for filter %s (%d frame(s), %d night(s), %s..%s)\n",
		s.Filter, s.Population.N, s.Population.Sessions, s.Population.From, s.Population.To)
	if len(s.Excluded) > 0 {
		var parts []string
		for _, p := range s.Excluded {
			parts = append(parts, fmt.Sprintf("%s (%s)", p.Date, strings.Join(p.Flags, ",")))
		}
		fmt.Printf("  reference excludes %d night(s): %s\n", len(s.Excluded), strings.Join(parts, "; "))
	}
	fmt.Printf("  band %.1f sigma around the good stack: keeps %d/%d good frames; %d/%d of the filter overall (%.0f%%)\n\n",
		s.Band, s.KeptGood, s.GoodCount, s.KeptCount, s.Total, 100*s.Kept)

	const arrowWidth = 12
	fmt.Printf("  %-14s %10s %10s %*s → %s\n", "gate", "current", "suggested", arrowWidth, "rejects now", "then")
	rows := []struct {
		key            string
		cur, suggested float64
	}{
		{"min-stars", float64(current.minStars), float64(s.Thresholds.MinStars)},
		{"max-ecc", current.maxEcc, s.Thresholds.MaxEcc},
		{"max-fwhm", current.maxFWHM, s.Thresholds.MaxFWHM},
		{"min-snr", current.minSNR, s.Thresholds.MinSNR},
		{"min-score", current.minScore, s.Thresholds.MinScore},
		{"max-halo-ratio", current.maxHaloRatio, s.Thresholds.MaxHaloRatio},
		{"max-structure", current.maxStructure, s.Thresholds.MaxStructure},
	}
	for _, row := range rows {
		fmt.Printf("  %-14s %10s %10s %*d → %d\n", row.key,
			formatSuggestion(row.cur), formatSuggestion(row.suggested),
			arrowWidth, now.byGate[row.key], then.byGate[row.key])
	}
	fmt.Printf("  %-14s %10s %10s %*d → %d\n", "no metrics", "-", "-",
		arrowWidth, now.noMetrics, then.noMetrics)
	fmt.Printf("  %-14s %10s %10s %*d → %d     (%d/%d approved now, %d/%d under the suggestion)\n",
		"total", "-", "-", arrowWidth, now.rejected(), then.rejected(),
		now.approved, now.total, then.approved, then.total)

	fmt.Println()
}

// suggestionDelta says how many frames the suggestion would judge differently,
// and in which direction.
func suggestionDelta(results []ImageResult, simulated []string) (changed, newlyRejected, newlyApproved int) {
	for i := range results {
		was, now := results[i].Decision, simulated[i]
		if results[i].Error != nil || was == now {
			continue
		}
		changed++
		if now == decisionApproved {
			newlyApproved++
			continue
		}
		newlyRejected++
	}
	return changed, newlyRejected, newlyApproved
}

// formatSuggestion renders a threshold the way the report renders numbers: as
// few digits as distinguish it, never rounded into something it is not.
func formatSuggestion(v float64) string {
	if math.Abs(v-math.Round(v)) < 1e-9 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(roundTo(v, 2), 'f', -1, 64)
}

// renderSuggestionYAML renders the config block for conf/garfield.yaml, with the
// provenance in comments so a stale suggestion is recognisable.
func renderSuggestionYAML(s suggestedThresholds) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# garfield thresholds suggested for filter %s\n", s.Filter)
	fmt.Fprintf(&b, "# analysed %s from %d frame(s) over %d night(s)", time.Now().UTC().Format(time.RFC3339), s.Population.N, s.Population.Sessions)
	if s.Population.From != "" {
		fmt.Fprintf(&b, " (%s..%s)", s.Population.From, s.Population.To)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "# band %.1f robust sigma around the good stack: keeps %d/%d good frames, %d/%d of the filter (%.0f%%)\n",
		s.Band, s.KeptGood, s.GoodCount, s.KeptCount, s.Total, 100*s.Kept)
	if len(s.Excluded) > 0 {
		var parts []string
		for _, p := range s.Excluded {
			parts = append(parts, fmt.Sprintf("%s %s", p.Date, strings.Join(p.Flags, ",")))
		}
		fmt.Fprintf(&b, "# excluded from the reference: %s\n", strings.Join(parts, "; "))
	}
	b.WriteString("thresholds:\n")
	fmt.Fprintf(&b, "  min-snr: %s\n", formatSuggestion(s.Thresholds.MinSNR))
	fmt.Fprintf(&b, "  max-fwhm: %s\n", formatSuggestion(s.Thresholds.MaxFWHM))
	fmt.Fprintf(&b, "  max-ecc: %s\n", formatSuggestion(s.Thresholds.MaxEcc))
	fmt.Fprintf(&b, "  min-score: %s\n", formatSuggestion(s.Thresholds.MinScore))
	fmt.Fprintf(&b, "  min-stars: %d\n", s.Thresholds.MinStars)
	fmt.Fprintf(&b, "  max-fwhm-sigma: %s\n", formatSuggestion(s.Thresholds.MaxFWHMSigma))
	fmt.Fprintf(&b, "  max-halo-ratio: %s\n", formatSuggestion(s.Thresholds.MaxHaloRatio))
	fmt.Fprintf(&b, "  max-structure: %s\n", formatSuggestion(s.Thresholds.MaxStructure))
	return b.String()
}

// suggestionFileName names the pasteable artifact for a filter.
func suggestionFileName(filter string) string {
	if filter == "" {
		filter = "ALL"
	}
	return "suggested_" + filter + ".yaml"
}

// writeSuggestion stores the suggestion and writes the YAML artifact.
func writeSuggestion(output string, s suggestedThresholds) error {
	rec := suggeststore.Suggestion{
		SuggestVersion: suggeststore.SuggestVersion,
		MeasureVersion: measureVersion,
		Filter:         s.Filter,
		Population:     s.Population,
		Heterogeneity:  s.Heterogeneity,
		Band:           s.Band,
		Kept:           s.Kept,
		Reference:      s.Reference,
		Thresholds:     s.Thresholds,
		Sessions:       s.Sessions,
		Created:        time.Now().UTC(),
	}

	store, err := suggeststore.Open(suggestionPath(output))
	if err != nil {
		return err
	}
	if err := store.Put(rec); err != nil {
		store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}

	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("unable to create %q: %w", output, err)
	}
	path := filepath.Join(output, suggestionFileName(s.Filter))
	if err := os.WriteFile(path, []byte(renderSuggestionYAML(s)), 0o644); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	return nil
}
