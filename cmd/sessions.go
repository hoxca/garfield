package cmd

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Session-level quality profiling.
//
// A run already judges every frame against its own session: the halo and the sky
// structure are compared with the session's own 25th percentile. That cannot see
// a night that is uniformly worse than the rest of the filter, because the
// night's own baseline is worse too, so its ratios come out at 1. This pass puts
// each session next to the whole filter population instead, so a systematically
// bad night becomes visible -- and so a frame from such a night can still be
// kept when it individually matches the stack.
//
// Two references, deliberately:
//
//   - the stack reference is a trimmed median and a normal-consistent MAD over
//     every measured frame of the filter;
//   - the within-session scale is the pooled spread inside sessions, which says
//     how much a night varies on its own.
//
// Their ratio is the heterogeneity index H. When H is near 1 the nights are
// interchangeable and the spread between their medians is the noise inside a
// night; ranking them would be ranking that noise, so no session is flagged.
// When H is well above 1 the nights really do differ and the comparison means
// something.

const (
	// sessionSoft and its siblings are the tokens a session is flagged with,
	// one per metric that moved in the direction that hurts.
	sessionSoft      = "soft"
	sessionCloudy    = "cloudy"
	sessionHazy      = "hazy"
	sessionThin      = "thin"
	sessionTrailed   = "trailed"
	sessionSparse    = "sparse"
	sessionScattered = "scattered"

	// sessionK is how many sampling errors a session's median may sit from the
	// stack's before the shift is called systematic.
	sessionK = 3.0
	// sessionShiftFrac is the practical floor on that test, as a fraction of the
	// stack median. With a few hundred frames a statistically significant shift
	// can be physically irrelevant, so the shift must also be worth acting on.
	sessionShiftFrac = 0.10
	// sessionShiftFracDim is the same floor for the two dimensionless metrics,
	// whose session-to-session spread is proportionally wider.
	sessionShiftFracDim = 0.20
	// heterogeneityMin is where the reported heterogeneity index starts to mean
	// "these nights differ". It is a diagnostic, not a gate: see the comment on
	// heterogeneityIndex for why it cannot decide which sessions to flag.
	heterogeneityMin = 1.5
	// trimSigma is where the stack reference drops frames while it is computed,
	// so a bad night cannot move the reference it is judged against.
	trimSigma = 3.5
	// bandSigma is how far from the stack median a frame may sit before it is
	// outside the band. It is the same 3 sigma the FWHM rule already uses.
	bandSigma = 3.0
	// outFracMin is the fraction of a session's frames that must fall outside the
	// band before the session is called scattered -- a night where the median is
	// fine but half the frames are not, which is what cloud rolling in and out
	// looks like.
	outFracMin = 0.5
	// sessionMinSample is the smallest session whose median is worth comparing.
	// Below it the median is too noisy to separate from the stack, and the
	// frames are still judged individually.
	sessionMinSample = 3
)

// metricSpec says how one measurement takes part in the profile: how to read it,
// which direction is bad, the token a shift earns, and the practical floor on
// that shift as a fraction of the stack median.
type metricSpec struct {
	name  string
	value func(ImageResult) float64
	high  bool
	token string
	floor float64
}

// sessionMetrics is the profile's column order, both for the console and for
// sessions_<FILTER>.csv.
var sessionMetrics = []metricSpec{
	{"fwhm", func(r ImageResult) float64 { return r.AvgFWHM }, true, sessionSoft, sessionShiftFrac},
	{"ecc", func(r ImageResult) float64 { return r.AvgEccentricity }, true, sessionTrailed, sessionShiftFrac},
	{"snr", func(r ImageResult) float64 { return r.SNR }, false, sessionThin, sessionShiftFrac},
	{"halo", func(r ImageResult) float64 { return r.Halo }, true, sessionCloudy, sessionShiftFracDim},
	{"struct", func(r ImageResult) float64 { return r.BgStructure }, true, sessionHazy, sessionShiftFracDim},
	{"stars", func(r ImageResult) float64 { return float64(r.DetectedStars) }, false, sessionSparse, sessionShiftFrac},
}

// stackRef is the robust centre and scale of one metric over a filter's frames.
type stackRef struct {
	Median float64
	Sigma  float64
	N      int
}

// newStackRef computes a median and a normal-consistent MAD, dropping the frames
// furthest from the median first so one bad night cannot set the scale.
func newStackRef(values []float64) stackRef {
	ref := stackRef{N: len(values)}
	if len(values) == 0 {
		return ref
	}
	kept := append([]float64(nil), values...)
	// Two rounds is enough to drop a contiguous bad night: the first sets a wide
	// scale from the contaminated set, the second uses the trimmed one.
	for round := 0; round < 2; round++ {
		median := medianOf(kept)
		dev := make([]float64, len(kept))
		for i, v := range kept {
			dev[i] = math.Abs(v - median)
		}
		sigma := 1.4826 * medianOf(dev)
		ref.Median, ref.Sigma = median, sigma
		if sigma <= 0 {
			break
		}
		next := make([]float64, 0, len(kept))
		for _, v := range kept {
			if math.Abs(v-median) <= trimSigma*sigma {
				next = append(next, v)
			}
		}
		if len(next) == len(kept) || len(next) < 3 {
			break
		}
		kept = next
	}
	return ref
}

// metricProfile is one session's position compared with the stack on one metric.
type metricProfile struct {
	Median float64
	Shift  float64
	Z      float64
	Out    int
}

// sessionProfile is one session of one filter, next to the stack it belongs to.
type sessionProfile struct {
	Session string
	Date    string
	Filter  string
	N       int
	// H is the heterogeneity index of the filter: how far the sessions' medians
	// are spread, in units of the within-session spread, taken over the metric
	// that differs most. Near 1 the nights are interchangeable.
	H       float64
	Metrics map[string]metricProfile
	OutFrac float64
	Flags   []string
}

// sessionProfiles is the whole pass's output.
type sessionProfiles struct {
	byFilter map[string][]sessionProfile
	refs     map[string]map[string]stackRef
	// flags maps a result index to the tokens of its session, so the report can
	// carry them per frame.
	flags map[int][]string
	// order keeps the filters in the order they first appear, so the console and
	// the files come out the same way twice.
	order []string
}

// profiles returns the profile of a filter, or nil when it has none.
func (p sessionProfiles) profiles(filter string) []sessionProfile {
	if p.byFilter == nil {
		return nil
	}
	return p.byFilter[filter]
}

// empty reports whether the pass produced nothing to show.
func (p sessionProfiles) empty() bool { return len(p.byFilter) == 0 }

// Flagged reports the sessions of a filter that carry at least one token.
func (p sessionProfiles) flagged(filter string) []sessionProfile {
	var out []sessionProfile
	for _, s := range p.profiles(filter) {
		if len(s.Flags) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// sessionClusters groups results into acquisition sessions and returns, per
// result, the index of its session. Sessions are numbered in chronological
// order, which is the order prepare names them in too. Frames with no usable
// timestamp share the trailing cluster and cannot define a session of their own.
func sessionClusters(results []ImageResult) ([]int, int) {
	idx := make([]int, len(results))
	type stamped struct {
		i  int
		ts time.Time
	}
	var items []stamped
	for i, r := range results {
		ts, ok := frameTimestamp(r.Filename)
		if !ok {
			idx[i] = -1
			continue
		}
		items = append(items, stamped{i, ts})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].ts.Before(items[b].ts) })

	cluster := 0
	for n, it := range items {
		if n > 0 && it.ts.Sub(items[n-1].ts) > sessionGap {
			cluster++
		}
		idx[it.i] = cluster
	}
	if len(items) == 0 {
		return idx, 0
	}
	return idx, cluster + 1
}

// profileRun compares every session of every filter with the whole filter stack.
//
// Frames that could not be measured are left out: they have no position to
// compare, and including their zeros would drag every median down.
func profileRun(results []ImageResult) sessionProfiles {
	out := sessionProfiles{
		byFilter: map[string][]sessionProfile{},
		refs:     map[string]map[string]stackRef{},
		flags:    map[int][]string{},
	}
	clusters, _ := sessionClusters(results)

	byFilterIdx := map[string][]int{}
	for i, r := range results {
		if r.Error != nil || r.StarCount == 0 {
			continue
		}
		if _, seen := byFilterIdx[r.Filter]; !seen {
			out.order = append(out.order, r.Filter)
		}
		byFilterIdx[r.Filter] = append(byFilterIdx[r.Filter], i)
	}

	for _, filter := range out.order {
		pop := byFilterIdx[filter]
		refs := stackRefs(results, pop)
		out.refs[filter] = refs

		// Session → its frames, in cluster order so Session_01 is the first night.
		groups := map[int][]int{}
		var order []int
		for _, i := range pop {
			c := clusters[i]
			if _, seen := groups[c]; !seen {
				order = append(order, c)
			}
			groups[c] = append(groups[c], i)
		}
		sort.Ints(order)

		within := pooledWithinScale(results, groups)
		sessionH := 0.0
		for _, spec := range sessionMetrics {
			if h := heterogeneityIndex(sessionMetricMedians(results, groups, order, spec), within[spec.name]); h > sessionH {
				sessionH = h
			}
		}

		profiles := make([]sessionProfile, 0, len(order))
		for n, c := range order {
			p := sessionProfile{
				Session: fmt.Sprintf("Session_%02d", n+1),
				Date:    sessionDate(results, groups[c]),
				Filter:  filter,
				N:       len(groups[c]),
				H:       sessionH,
				Metrics: map[string]metricProfile{},
			}

			outside := map[int]bool{}
			for _, spec := range sessionMetrics {
				ref := refs[spec.name]
				values := make([]float64, 0, len(groups[c]))
				for _, i := range groups[c] {
					values = append(values, spec.value(results[i]))
				}
				median := medianOf(values)
				mp := metricProfile{Median: median, Shift: median - ref.Median}
				if ref.Sigma > 0 && len(values) > 0 {
					se := 1.2533 * ref.Sigma / math.Sqrt(float64(len(values)))
					if se > 0 {
						mp.Z = mp.Shift / se
					}
				}
				for _, i := range groups[c] {
					if outsideBand(spec.value(results[i]), ref, spec.high) {
						mp.Out++
						outside[i] = true
					}
				}
				p.Metrics[spec.name] = mp
			}
			p.OutFrac = float64(len(outside)) / float64(len(groups[c]))

			// A shift has to be both real and worth acting on: three sampling
			// errors past the stack centre, and a tenth of the median. Between
			// them they bound the false-flag rate without needing the
			// heterogeneity index, which is blind to a single bad night.
			for _, spec := range sessionMetrics {
				if len(groups[c]) < sessionMinSample {
					continue
				}
				mp := p.Metrics[spec.name]
				worse := mp.Shift
				if !spec.high {
					worse = -mp.Shift
				}
				ref := refs[spec.name]
				floor := spec.floor * ref.Median
				if worse <= 0 || ref.Sigma <= 0 {
					continue
				}
				se := 1.2533 * ref.Sigma / math.Sqrt(float64(len(groups[c])))
				if worse > math.Max(sessionK*se, floor) {
					p.Flags = append(p.Flags, spec.token)
				}
			}
			if len(p.Flags) == 0 && len(groups[c]) >= sessionMinSample && p.OutFrac >= outFracMin {
				p.Flags = append(p.Flags, sessionScattered)
			}

			for _, i := range groups[c] {
				if len(p.Flags) > 0 {
					out.flags[i] = p.Flags
				}
			}
			profiles = append(profiles, p)
		}
		out.byFilter[filter] = profiles
	}
	return out
}

// stackRefs computes the stack reference of every profiled metric, from the
// frames of one filter.
func stackRefs(results []ImageResult, pop []int) map[string]stackRef {
	refs := map[string]stackRef{}
	for _, spec := range sessionMetrics {
		values := make([]float64, 0, len(pop))
		for _, i := range pop {
			values = append(values, spec.value(results[i]))
		}
		refs[spec.name] = newStackRef(values)
	}
	return refs
}

// pooledWithinScale estimates how much a metric varies inside a night, pooling
// every session of the filter.
//
// It is computed on the residuals from each session's own median rather than
// from the session MADs, because a session holds a handful of frames and a MAD
// over four values is mostly noise.
func pooledWithinScale(results []ImageResult, groups map[int][]int) map[string]float64 {
	scale := map[string]float64{}
	for _, spec := range sessionMetrics {
		var residuals []float64
		for _, frames := range groups {
			values := make([]float64, 0, len(frames))
			for _, i := range frames {
				values = append(values, spec.value(results[i]))
			}
			median := medianOf(values)
			for _, v := range values {
				residuals = append(residuals, v-median)
			}
		}
		dev := make([]float64, len(residuals))
		for i, v := range residuals {
			dev[i] = math.Abs(v)
		}
		scale[spec.name] = 1.4826 * medianOf(dev)
	}
	return scale
}

// sessionMetricMedians returns one median per session, in cluster order.
func sessionMetricMedians(results []ImageResult, groups map[int][]int, order []int, spec metricSpec) []float64 {
	medians := make([]float64, 0, len(order))
	for _, c := range order {
		values := make([]float64, 0, len(groups[c]))
		for _, i := range groups[c] {
			values = append(values, spec.value(results[i]))
		}
		medians = append(medians, medianOf(values))
	}
	return medians
}

// heterogeneityIndex is the spread of the session medians over the spread within
// a session: near 1 the nights are interchangeable, and 0 means there is nothing
// to compare (one night, or no spread inside the others).
//
// It is reported, not used to gate. A robust spread of the session medians
// cannot see one bad night among few -- which is exactly the night worth
// catching -- while with many nights it begins to see sampling noise. The flag
// decision is bounded instead by the practical floor (a shift must be worth
// acting on) and by three sampling errors (a shift must be real), which is tight
// at both ends.
func heterogeneityIndex(sessionMedians []float64, within float64) float64 {
	if len(sessionMedians) < 2 || within <= 0 {
		return 0
	}
	centre := medianOf(sessionMedians)
	dev := make([]float64, len(sessionMedians))
	for i, v := range sessionMedians {
		dev[i] = math.Abs(v - centre)
	}
	return 1.4826 * medianOf(dev) / within
}

// outsideBand reports whether a value sits beyond the stack band, on the side
// that hurts.
func outsideBand(value float64, ref stackRef, high bool) bool {
	if ref.Sigma <= 0 {
		return false
	}
	if high {
		return value > ref.Median+bandSigma*ref.Sigma
	}
	return value < ref.Median-bandSigma*ref.Sigma
}

// sessionDate is the acquisition date of a session's first frame: sessions run
// past midnight, so the date token of the last frame is not the night.
func sessionDate(results []ImageResult, frames []int) string {
	first, ok := "", false
	var firstTS time.Time
	for _, i := range frames {
		ts, has := frameTimestamp(results[i].Filename)
		if !has {
			continue
		}
		if !ok || ts.Before(firstTS) {
			first, firstTS, ok = results[i].Date, ts, true
		}
	}
	return first
}

// attachSessionFlags copies each session's tokens onto its frames.
func attachSessionFlags(results []ImageResult, prof sessionProfiles) {
	for i := range results {
		flags, ok := prof.flags[i]
		if !ok {
			results[i].SessionFlag = ""
			continue
		}
		results[i].SessionFlag = strings.Join(flags, ",")
	}
}

// sessionsProfileName names the per-session report of a filter. A frame with no
// parseable filter is filed under ALL rather than producing "sessions_.csv".
func sessionsProfileName(filter string) string {
	if filter == "" {
		filter = "ALL"
	}
	return "sessions_" + filter + ".csv"
}

// sessionsProfileHeader is the column order of sessions_<FILTER>.csv. The metric
// columns are generated from sessionMetrics so the two cannot drift.
func sessionsProfileHeader() []string {
	header := []string{"session", "sessionDate", "filter", "n", "h"}
	for _, spec := range sessionMetrics {
		header = append(header, spec.name, spec.name+"Shift", spec.name+"Z")
	}
	return append(header, "outFrac", "flag")
}

// writeSessionsProfileCSV records one row per session, plus a trailing row for
// the stack the sessions were compared with, so the shifts have a visible
// reference.
func writeSessionsProfileCSV(dir, filter string, profiles []sessionProfile, refs map[string]stackRef) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("unable to create %q: %w", dir, err)
	}
	path := filepath.Join(dir, sessionsProfileName(filter))
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("unable to create %q: %w", path, err)
	}
	defer f.Close()

	w := newCSVWriter(f)
	if err := w.Write(sessionsProfileHeader()); err != nil {
		return fmt.Errorf("writing CSV: %w", err)
	}

	row := func(p sessionProfile) []string {
		out := []string{p.Session, p.Date, p.Filter, strconv.Itoa(p.N), strconv.FormatFloat(p.H, 'f', 2, 64)}
		for _, spec := range sessionMetrics {
			mp := p.Metrics[spec.name]
			out = append(out,
				strconv.FormatFloat(mp.Median, 'f', 4, 64),
				strconv.FormatFloat(mp.Shift, 'f', 4, 64),
				strconv.FormatFloat(mp.Z, 'f', 2, 64),
			)
		}
		out = append(out, strconv.FormatFloat(p.OutFrac, 'f', 3, 64), strings.Join(p.Flags, ","))
		return out
	}

	for _, p := range profiles {
		if err := w.Write(row(p)); err != nil {
			return fmt.Errorf("writing CSV: %w", err)
		}
	}

	if len(profiles) > 0 {
		stack := sessionProfile{
			Session: "stack",
			Filter:  filter,
			H:       profiles[0].H,
			Metrics: map[string]metricProfile{},
		}
		for _, spec := range sessionMetrics {
			ref := refs[spec.name]
			stack.Metrics[spec.name] = metricProfile{Median: ref.Median}
			stack.N = ref.N
		}
		if err := w.Write(row(stack)); err != nil {
			return fmt.Errorf("writing CSV: %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("writing CSV: %w", err)
	}
	return nil
}

// printSessionProfile reports the flagged sessions on the console, one line per
// filter. A run with comparable nights says so once, because silence would look
// like the pass had not run.
func printSessionProfile(prof sessionProfiles) {
	for _, filter := range prof.order {
		profiles := prof.profiles(filter)
		if len(profiles) < 2 {
			continue
		}
		h := profiles[0].H
		flagged := prof.flagged(filter)
		if len(flagged) == 0 {
			fmt.Printf("  sessions %s: %d night(s), h=%.2f, none flagged\n", filter, len(profiles), h)
			continue
		}
		var parts []string
		for _, p := range flagged {
			parts = append(parts, fmt.Sprintf("%s %s (%s)", p.Session, strings.Join(p.Flags, ","), p.Date))
		}
		fmt.Printf("  sessions %s: %d night(s), h=%.2f, %d flagged: %s\n",
			filter, len(profiles), h, len(flagged), strings.Join(parts, "; "))
	}
}
