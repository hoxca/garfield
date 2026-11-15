package cmd

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nightSpec describes one synthetic observing night for the session profile
// tests. Frames are given a fixed jitter pattern so the medians and the within
// night spread are deterministic.
type nightSpec struct {
	date   string // YYYYMMDD
	n      int
	fwhm   float64
	ecc    float64
	snr    float64
	halo   float64
	bgstr  float64
	stars  int
	spread float64 // relative jitter applied to every metric
}

var jitter = []float64{0, 0.4, -0.4, 0.2, -0.2, 0.6, -0.6, 0.1}

// build turns a night into measured results, stamped so sessionClusters sees one
// contiguous session.
func (n nightSpec) build(t *testing.T, filter string) []ImageResult {
	t.Helper()
	offset := 0
	for _, c := range n.date {
		offset += int(c)
	}
	out := make([]ImageResult, 0, n.n)
	for i := 0; i < n.n; i++ {
		j := 1.0
		if n.spread > 0 {
			// Rotate the pattern per night: a jitter pattern shared by every
			// night would make the within-night spread look smaller than it is.
			j += n.spread * jitter[(i+offset)%len(jitter)]
		}
		// Two hours after the first frame is still the same night; the gap
		// constant is what keeps separate nights apart.
		clock := fmt.Sprintf("%02d%02d00", 22+i/10, (i*7)%60)
		out = append(out, ImageResult{
			Filename: fmt.Sprintf("TGT_LIGHT_%s_300s_BIN1_-10C_GA0_%s_%s_%03d_PA310_E.FIT",
				filter, n.date, clock, i),
			Filter:          filter,
			Date:            n.date[:4] + "-" + n.date[4:6] + "-" + n.date[6:],
			DetectedStars:   int(float64(n.stars) * j),
			StarCount:       n.stars / 2,
			AvgFWHM:         n.fwhm * j,
			AvgEccentricity: n.ecc * j,
			SNR:             n.snr * j,
			Halo:            n.halo * j,
			BgStructure:     n.bgstr * j,
			Decision:        decisionApproved,
		})
	}
	return out
}

// threeGoodNights is the reference stack: tight nights, well matched.
func threeGoodNights() []nightSpec {
	return []nightSpec{
		{date: "20260910", n: 8, fwhm: 2.40, ecc: 0.310, snr: 31.0, halo: 1.00, bgstr: 0.0100, stars: 900, spread: 0.05},
		{date: "20260911", n: 8, fwhm: 2.41, ecc: 0.312, snr: 30.8, halo: 1.01, bgstr: 0.0101, stars: 902, spread: 0.05},
		{date: "20260912", n: 8, fwhm: 2.39, ecc: 0.308, snr: 31.2, halo: 0.99, bgstr: 0.0099, stars: 898, spread: 0.05},
	}
}

func buildStack(t *testing.T, filter string, nights ...nightSpec) []ImageResult {
	t.Helper()
	var out []ImageResult
	for _, n := range nights {
		out = append(out, n.build(t, filter)...)
	}
	return out
}

func TestProfileFlagsSystematicallySoftNight(t *testing.T) {
	soft := nightSpec{date: "20260913", n: 8, fwhm: 3.20, ecc: 0.31, snr: 30.8, halo: 1.00,
		bgstr: 0.0100, stars: 900, spread: 0.05}
	results := buildStack(t, "H", append(threeGoodNights(), soft)...)

	prof := profileRun(results)
	profiles := prof.profiles("H")
	if len(profiles) != 4 {
		t.Fatalf("profiled %d sessions, want 4", len(profiles))
	}
	// The index is a robust spread of the session medians, so one bad night in
	// four barely moves it; the flag comes from the shift against the stack,
	// which is the point of having both.
	if profiles[0].H <= 0 {
		t.Errorf("h = %.2f, want a measured heterogeneity", profiles[0].H)
	}

	for _, p := range profiles {
		if p.Date == "2026-09-13" {
			if strings.Join(p.Flags, ",") != sessionSoft {
				t.Errorf("soft night flags = %v, want [%s]", p.Flags, sessionSoft)
			}
			if p.Metrics["fwhm"].Shift <= 0 {
				t.Errorf("soft night fwhm shift = %v, want positive", p.Metrics["fwhm"].Shift)
			}
			continue
		}
		if len(p.Flags) != 0 {
			t.Errorf("%s (%s) flagged %v, want a clean night", p.Session, p.Date, p.Flags)
		}
	}
}

func TestProfileDoesNotFlagABetterThanAverageNight(t *testing.T) {
	sharp := nightSpec{date: "20260913", n: 8, fwhm: 2.00, ecc: 0.28, snr: 33.0, halo: 0.95,
		bgstr: 0.0095, stars: 930, spread: 0.05}
	results := buildStack(t, "H", append(threeGoodNights(), sharp)...)

	for _, p := range profileRun(results).profiles("H") {
		if len(p.Flags) != 0 {
			t.Errorf("%s flagged %v: a night sharper than the stack is not a defect", p.Session, p.Flags)
		}
	}
}

func TestProfileSaysNothingWhenNightsAreInterchangeable(t *testing.T) {
	// Four nights drawn from the same distribution: the spread between their
	// medians is the spread inside a night, so there is nothing to rank.
	nights := threeGoodNights()
	nights = append(nights, nightSpec{date: "20260913", n: 8, fwhm: 2.41, ecc: 0.31,
		snr: 30.9, halo: 1.01, bgstr: 0.0101, stars: 890, spread: 0.05})
	results := buildStack(t, "H", nights...)

	prof := profileRun(results)
	profiles := prof.profiles("H")
	// The index is reported, so it must agree that these nights belong together;
	// the gate is the practical floor, so nothing may be flagged.
	if h := profiles[0].H; h >= heterogeneityMin {
		t.Errorf("h = %.2f for interchangeable nights, want below %.1f", h, heterogeneityMin)
	}
	if got := prof.flagged("H"); len(got) != 0 {
		t.Errorf("flagged %d sessions of an interchangeable stack: %+v", len(got), got)
	}
}

func TestProfileIgnoresASingleNight(t *testing.T) {
	results := buildStack(t, "H", threeGoodNights()[0])
	prof := profileRun(results)
	profiles := prof.profiles("H")
	if len(profiles) != 1 {
		t.Fatalf("profiled %d sessions, want 1", len(profiles))
	}
	if len(profiles[0].Flags) != 0 {
		t.Errorf("single-night stack flagged %v; there is nothing to compare with", profiles[0].Flags)
	}
}

func TestProfileIgnoresATinySession(t *testing.T) {
	// Two frames cannot support a median; the night is left alone and its frames
	// are still judged individually.
	tiny := nightSpec{date: "20260913", n: 2, fwhm: 4.00, ecc: 0.31, snr: 30.8, halo: 1.00,
		bgstr: 0.0100, stars: 900, spread: 0.05}
	results := buildStack(t, "H", append(threeGoodNights(), tiny)...)

	for _, p := range profileRun(results).profiles("H") {
		if p.Date == "2026-09-13" && len(p.Flags) != 0 {
			t.Errorf("a %d-frame night was flagged %v", p.N, p.Flags)
		}
	}
}

func TestProfileFlagsCloudyAndHazyNights(t *testing.T) {
	cloudy := nightSpec{date: "20260913", n: 8, fwhm: 2.41, ecc: 0.31, snr: 30.9, halo: 1.90,
		bgstr: 0.0100, stars: 900, spread: 0.05}
	results := buildStack(t, "H", append(threeGoodNights(), cloudy)...)

	for _, p := range profileRun(results).profiles("H") {
		if p.Date != "2026-09-13" {
			continue
		}
		if !strings.Contains(strings.Join(p.Flags, ","), sessionCloudy) {
			t.Errorf("halo shifted to 1.9x the stack: flags = %v, want %s", p.Flags, sessionCloudy)
		}
	}
}

func TestProfileFlagsScatteredNightWithoutMedianShift(t *testing.T) {
	// Half the frames are soft, the other half hazy: no single metric's median
	// moves far, but most frames are outside some band.
	results := buildStack(t, "H", threeGoodNights()...)
	base := nightSpec{date: "20260913", n: 8, fwhm: 2.41, ecc: 0.310, snr: 30.9, halo: 1.00,
		bgstr: 0.0100, stars: 900, spread: 0.05}.build(t, "H")
	for i := range base {
		switch {
		case i < 3:
			base[i].AvgFWHM = 4.50
		case i < 6:
			base[i].Halo = 4.00
		}
	}
	results = append(results, base...)

	for _, p := range profileRun(results).profiles("H") {
		if p.Date != "2026-09-13" {
			continue
		}
		if p.OutFrac < outFracMin {
			t.Errorf("outFrac = %.2f, want at least %.2f", p.OutFrac, outFracMin)
		}
		if !strings.Contains(strings.Join(p.Flags, ","), sessionScattered) {
			t.Errorf("flags = %v, want %s", p.Flags, sessionScattered)
		}
	}
}

func TestAttachSessionFlagsMarksEveryFrameOfANight(t *testing.T) {
	soft := nightSpec{date: "20260913", n: 8, fwhm: 3.20, ecc: 0.31, snr: 30.8, halo: 1.00,
		bgstr: 0.0100, stars: 900, spread: 0.05}
	results := buildStack(t, "H", append(threeGoodNights(), soft)...)

	attachSessionFlags(results, profileRun(results))

	softFrames, cleanFrames := 0, 0
	for _, r := range results {
		if r.Date == "2026-09-13" {
			if r.SessionFlag != sessionSoft {
				t.Errorf("%s carries sessionFlag %q, want %q", r.Filename, r.SessionFlag, sessionSoft)
			}
			softFrames++
			continue
		}
		if r.SessionFlag != "" {
			t.Errorf("%s carries sessionFlag %q, want empty", r.Filename, r.SessionFlag)
		}
		cleanFrames++
	}
	if softFrames == 0 || cleanFrames == 0 {
		t.Fatalf("fixture covered %d soft and %d clean frames", softFrames, cleanFrames)
	}
}

func TestWriteSessionsProfileCSV(t *testing.T) {
	results := buildStack(t, "H", threeGoodNights()...)
	prof := profileRun(results)
	dir := t.TempDir()

	if err := writeSessionsProfileCSV(dir, "H", prof.profiles("H"), prof.refs["H"]); err != nil {
		t.Fatalf("writeSessionsProfileCSV: %v", err)
	}
	path := filepath.Join(dir, "sessions_H.csv")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}

	recs, err := newCSVReader(strings.NewReader(string(b))).ReadAll()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 5 { // header, three nights, and the stack row
		t.Fatalf("got %d records, want header, 3 sessions and the stack row: %v", len(recs), recs)
	}

	want := sessionsProfileHeader()
	if len(recs[0]) != len(want) {
		t.Fatalf("header has %d columns, want %d", len(recs[0]), len(want))
	}
	for i := range want {
		if recs[0][i] != want[i] {
			t.Errorf("header[%d] = %q, want %q", i, recs[0][i], want[i])
		}
	}

	if recs[1][0] != "Session_01" || recs[2][0] != "Session_02" {
		t.Errorf("sessions are not numbered in order: %q, %q", recs[1][0], recs[2][0])
	}

	stack := recs[len(recs)-1]
	if stack[0] != "stack" {
		t.Fatalf("last row is %q, want the stack reference", stack[0])
	}
	// The stack row's shifts are zero by definition, and its value column holds
	// the reference the sessions were compared with.
	valueCol := 5
	shiftCol := valueCol + 1
	if stack[shiftCol] != "0.0000" {
		t.Errorf("stack shift = %q, want 0.0000", stack[shiftCol])
	}
	if stack[valueCol] == "0.0000" {
		t.Errorf("stack median is not recorded: %q", stack[valueCol])
	}
	if n := stack[3]; n == "0" {
		t.Errorf("stack row reports n=0, want the filter population")
	}
}

func TestStackRefTrimsABadNightOutOfTheReference(t *testing.T) {
	// One night out of four is soft. The trimmed reference must stay on the good
	// nights, or the band would be drawn from a contaminated population.
	var values []float64
	for _, n := range []float64{2.40, 2.40, 2.40, 2.40, 2.42, 2.41, 2.39, 3.20, 3.20, 3.20, 3.20} {
		values = append(values, n)
	}
	ref := newStackRef(values)
	if math.Abs(ref.Median-2.40) > 0.02 {
		t.Errorf("trimmed median = %.3f, want ~2.40 (the soft frames must not pull it)", ref.Median)
	}
	if ref.Sigma > 0.1 {
		t.Errorf("trimmed sigma = %.3f, want a tight scale from the good frames", ref.Sigma)
	}
}
