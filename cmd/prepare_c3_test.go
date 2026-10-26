package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for behaviour that exists only on the Moravian C3.
//
// The C3 has a rotator and a gain setting; the G4 has neither (see
// prepare_g4_test.go). Anything here would be meaningless or wrong under
// --camera g4, which is why it is separated from the camera-neutral suite in
// prepare_test.go.
//
// Subsystem tests keep their camera-specific pattern checks: prepare_dark_test.go
// and prepare_bias_test.go are grouped by pipeline stage, and the tests that
// justify those patterns belong beside them.

func TestPrepareParsesLightNames(t *testing.T) {
	// The filename conventions the planner relies on.
	light := lightName("LBN527", "H", "20260910", "025946", "489", "310")
	if !frameDateRe.MatchString(light) {
		t.Errorf("frameDateRe does not match %q", light)
	}
	if m := paRe.FindStringSubmatch(light); m == nil || m[1] != "310" {
		t.Errorf("paRe on %q = %v, want 310", light, m)
	}

	flat := flatName("S", "67.02", "20260916", "071758", "808", "310")
	if m := flatFilterRe.FindStringSubmatch(flat); m == nil || m[1] != "S" {
		t.Errorf("flatFilterRe on %q = %v, want S", flat, m)
	}
	if m := paRe.FindStringSubmatch(flat); m == nil || m[1] != "310" {
		t.Errorf("paRe on %q = %v, want 310", flat, m)
	}

	// The master-flat style seen in the repository must not match. The filter
	// pattern is anchored so a frame that merely contains "FLAT_" somewhere in
	// its name cannot be picked up as a flat.
	for _, name := range []string{
		"masterFlat_BIN-1_9576x6388_FILTER-S_mono_SESSION-1.fit",
		"LBN527_LIGHT_H_600s_BIN1_-10C_GA2750_20260916_062721_275_PA310_E.FIT",
		"FLATL_H_41s_PA310.FIT",
		"NOT_A_FLAT_H_41s_PA310.FIT",
	} {
		if flatFilterRe.MatchString(name) {
			t.Errorf("flatFilterRe matched %q", name)
		}
	}

	// A flat is only usable when both the filter and the angle parse; the
	// indexer drops anything missing either.
	for _, name := range []string{
		"FLAT_H_41.4s_BIN1_PA310.FIT",
	} {
		if !flatFilterRe.MatchString(name) {
			t.Errorf("flatFilterRe did not match %q", name)
		}
	}
	if paRe.MatchString("FLAT_H_41.4s_BIN1.FIT") {
		t.Error("paRe matched a flat with no angle")
	}
}

// TestFlatFilterReAcceptsOnlyKnownFilters pins the narrowed capture group.
//
// The group is a character class spliced in from knownFilters, so the failure
// mode worth guarding is structural: a non-capturing group, or losing the
// brackets, matches nothing at all and drops every flat silently.
func TestFlatFilterReAcceptsOnlyKnownFilters(t *testing.T) {
	for _, f := range []string{"L", "R", "G", "B", "S", "H", "O"} {
		name := fmt.Sprintf("FLAT_%s_41.4s_BIN1_PA310.FIT", f)
		m := flatFilterRe.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("flatFilterRe does not match %q", name)
			continue
		}
		if m[1] != f {
			t.Errorf("flatFilterRe on %q captured %q, want %q (the group must stay capturing)",
				name, m[1], f)
		}
		if len(m) != 2 {
			t.Errorf("flatFilterRe on %q returned %d groups, want exactly 2", name, len(m))
		}
	}

	// Narrowband and unknown filters no longer parse, but they still carry the
	// FLAT_ prefix, which is what earns them a warning rather than a silent
	// drop in indexFlats.
	for _, name := range []string{
		"FLAT_Ha_41.4s_BIN1_PA310.FIT",
		"FLAT_OIII_41.4s_BIN1_PA310.FIT",
		"FLAT_L2_41.4s_BIN1_PA310.FIT",
		"FLAT_X_41.4s_BIN1_PA310.FIT",
	} {
		if flatFilterRe.MatchString(name) {
			t.Errorf("flatFilterRe matched %q, want no match", name)
		}
		if !strings.HasPrefix(name, flatNamePrefix) {
			t.Errorf("%q lacks the %q prefix, so indexFlats would drop it silently",
				name, flatNamePrefix)
		}
	}

	// Not a flat at all: no FLAT_ prefix, so indexFlats leaves them alone
	// rather than mislabelling another frame type as a broken flat.
	for _, name := range []string{
		"FLATL_H_41s_PA310.FIT",
		"masterFlat_BIN-1_9576x6388_FILTER-S_mono_SESSION-1.fit",
		"NOT_A_FLAT_H_41s_PA310.FIT",
	} {
		if strings.HasPrefix(name, flatNamePrefix) {
			t.Errorf("%q carries the %q prefix, so indexFlats would warn on a non-flat",
				name, flatNamePrefix)
		}
	}

	// The prefix and the regex must not drift apart: the regex is what decides
	// a flat is usable, the prefix is what decides an unusable one is worth
	// reporting, and a name that satisfies one but not the other would either
	// vanish or be mislabelled.
	if !strings.HasPrefix("FLAT_S_1s_PA0.FIT", flatNamePrefix) {
		t.Error("a valid flat does not carry flatNamePrefix")
	}
	if !strings.HasPrefix(flatFilterRe.String(), "^"+flatNamePrefix) {
		t.Errorf("flatFilterRe %q does not start with ^%s",
			flatFilterRe.String(), flatNamePrefix)
	}
}

// TestIndexFlatsWarnsOnUnusableFlats checks the two ways a real flat is
// dropped, both of which used to be silent: an unrecognised filter, and a
// filter with no position angle.
func TestIndexFlatsWarnsOnUnusableFlats(t *testing.T) {
	dir := t.TempDir()
	usable := flatName("S", "67.02", "20260916", "071758", "808", "310")
	for _, n := range []string{
		usable,
		"FLAT_Ha_41.4s_BIN1_-10C_GA2750_20260916_070000_000_PA310.FIT",
		"FLAT_OIII_41.4s_BIN1_-10C_GA2750_20260916_070100_000_PA310.FIT",
		"FLAT_H_41.4s_BIN1_-10C_GA2750_20260916_070200_000.FIT",
		// Carries the prefix but no usable token, so it warns. The prefix test
		// is looser than the filter regex on purpose: a name that is plainly
		// meant to be a flat is worth naming even when it is malformed, and
		// the warning says why.
		"FLAT_-weird_name.FIT",
		"FLAT__PA310.FIT",
		// No prefix at all, so it is another frame type sharing the directory.
		"FLATL_H_41.4s_PA310.FIT",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	index, warnings := indexFlats(dir, c3Camera)

	if got := index[flatKey("S", "310")]; len(got) != 1 || got[0] != usable {
		t.Errorf("index = %v, want the one usable flat keyed S@310", index)
	}

	// Two warnings, one per dropped real flat. Both name the file and say why.
	// Five warnings: two unknown filters, two malformed names, one missing angle.
	if len(warnings) != 5 {
		t.Fatalf("got %d warnings (%v), want 5", len(warnings), warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{
		"FLAT_Ha_", "no known filter",
		"FLAT_OIII_",
		"FLAT_H_41.4s", "no position angle",
		"FLAT_-weird_name.FIT",
		"FLAT__PA310.FIT",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q do not mention %q", joined, want)
		}
	}
	// FLATL_ is not a flat, so it must not be reported as a broken one.
	if strings.Contains(joined, "FLATL_") {
		t.Errorf("warnings %q report a non-flat as a broken flat", joined)
	}
}

// TestPrepareFiltersFlatsByAngle is the load-bearing case: Flats/<date> can
// hold frames at several parallactic angles, and only the one matching the
// session's lights is valid calibration.
func TestPrepareFiltersFlatsByAngle(t *testing.T) {
	const target = "SH2-101"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-21": {"O": {
					lightName(target, "O", "20260921", "220000", "000", "240"),
				}},
			},
		},
		flats: map[string][]string{
			// Two angles on the same night; only PA240 matches.
			"2026-09-21": {
				flatName("O", "41.4", "20260921", "070000", "000", "240"),
				flatName("O", "41.5", "20260921", "070100", "000", "196"),
				flatName("O", "41.6", "20260921", "070200", "000", "310"),
				// Right filter, right angle not required: a different filter
				// at the matching angle must still be excluded.
				flatName("H", "41.7", "20260921", "070300", "000", "240"),
			},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	dir := filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "flats", "O")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read flats: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d flats, want 1 (only the PA240 O frame)", len(files))
	}
	if !strings.Contains(files[0].Name(), "PA240") {
		t.Errorf("flat = %q, want the PA240 frame", files[0].Name())
	}
	// The other filter's directory must not exist.
	if _, err := os.Stat(filepath.Join(sessionOutDir(filepath.Join(out, target), "Session_01"), "flats", "H")); err == nil {
		t.Error("flats/H was created but no H lights exist")
	}
}

// TestPrepareMixedAnglesWarns covers a session whose lights somehow span two
// parallactic angles, which the acquisition software should prevent.
// TestPrepareMixedAnglesIsFatal checks that a session holding two angles stops
// the run.
//
// This used to be a warning that took the lowest angle and carried on, which
// calibrated part of the night against flats shot for a different angle while
// the report named only one. A target is acquired at a single angle chosen
// before the run, so a second angle means the instrument was rotated and the
// data cannot be trusted.
func TestPrepareMixedAnglesIsFatal(t *testing.T) {
	const target = "TEST"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"H": {
					lightName(target, "H", "20260910", "220000", "000", "310"),
					lightName(target, "H", "20260910", "230000", "000", "240"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {
				flatName("H", "41.4", "20260910", "070000", "000", "240"),
				flatName("H", "41.5", "20260910", "070100", "000", "310"),
			},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	err := runPrepare(prepareOptionsForTest(target, root, out))
	if err == nil {
		t.Fatal("runPrepare succeeded on a mixed-angle session, want a failure")
	}

	// Both angles named, so the operator can see what the session actually
	// holds rather than being told one of them was chosen.
	for _, want := range []string{"multiple angles", "240", "310", "Session_01"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// Planning happens before anything is written, so the failure leaves no
	// tree behind: no half-prepared session to mistake for real output.
	if _, statErr := os.Stat(filepath.Join(out, target)); !os.IsNotExist(statErr) {
		t.Errorf("output tree exists after a fatal error: %v", statErr)
	}
}

// TestPrepareMixedAnglesAcrossSessionsIsFatal covers the case that was
// silently accepted before any of this: each session internally consistent, but
// the target rotated between nights. Every session matched its own date's flats,
// so nothing looked wrong and sessions.csv simply carried two angles.
func TestPrepareMixedAnglesAcrossSessionsIsFatal(t *testing.T) {
	const target = "TEST"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"H": {
					lightName(target, "H", "20260910", "220000", "000", "310"),
				}},
				"2026-09-11": {"H": {
					lightName(target, "H", "20260911", "220000", "000", "240"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {flatName("H", "41.4", "20260910", "070000", "000", "310")},
			"2026-09-11": {flatName("H", "41.5", "20260911", "070000", "000", "240")},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	err := runPrepare(prepareOptionsForTest(target, root, out))
	if err == nil {
		t.Fatal("runPrepare succeeded on a target acquired at two angles, want a failure")
	}

	// The whole set is reported, not one "expected" angle: with the target
	// rotated more than once there is no principled choice to name.
	for _, want := range []string{"TEST", "2 different angles", "240/310"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(out, target)); !os.IsNotExist(statErr) {
		t.Errorf("output tree exists after a fatal error: %v", statErr)
	}
}

// TestPrepareReportsEveryMixedAngleAtOnce checks the failures batch rather than
// stopping at the first, so one run names everything that needs fixing instead
// of demanding a fix-and-rerun per session.
func TestPrepareReportsEveryMixedAngleAtOnce(t *testing.T) {
	const target = "TEST"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				// Mixed within the session.
				"2026-09-10": {"H": {
					lightName(target, "H", "20260910", "220000", "000", "310"),
					lightName(target, "H", "20260910", "230000", "000", "240"),
				}},
				// Clean, and at an angle of its own, so it survives planning
				// while disagreeing with the target's other sessions.
				"2026-09-11": {"H": {
					lightName(target, "H", "20260911", "220000", "000", "90"),
				}},
				// Also mixed within the session.
				"2026-09-12": {"H": {
					lightName(target, "H", "20260912", "220000", "000", "310"),
					lightName(target, "H", "20260912", "230000", "000", "180"),
				}},
				// Mixed, at a third angle, so the batching assertion below needs
				// no cross-session check: three offenders must all be named.
				"2026-09-13": {"H": {
					lightName(target, "H", "20260913", "220000", "000", "90"),
					lightName(target, "H", "20260913", "230000", "000", "180"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {flatName("H", "41.4", "20260910", "070000", "000", "310")},
			"2026-09-11": {flatName("H", "41.5", "20260911", "070000", "000", "90")},
			"2026-09-12": {flatName("H", "41.6", "20260912", "070000", "000", "310")},
			"2026-09-13": {flatName("H", "41.7", "20260913", "070000", "000", "90")},
			"2026-09-14": {flatName("H", "41.8", "20260914", "070000", "000", "90")},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	err := runPrepare(prepareOptionsForTest(target, root, out))
	if err == nil {
		t.Fatal("runPrepare succeeded on a rotated target with mixed sessions, want a failure")
	}

	// Three mixed sessions, all named in one error. The cross-session
	// disagreement does not fire here: every session that survived planning
	// shares PA90, so only the within-session faults are reported.
	msg := err.Error()
	for _, want := range []string{"Session_01", "Session_03", "Session_04"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "different angles") {
		t.Errorf("error %q reports a cross-session rotation that did not occur: %q", msg, msg)
	}
}

// TestPrepareSingleAngleTargetIsUnaffected guards the other side: a target
// acquired at one angle throughout must still plan and copy as before.
func TestPrepareSingleAngleTargetIsUnaffected(t *testing.T) {
	const target = "TEST"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"H": {
					lightName(target, "H", "20260910", "220000", "000", "310"),
					lightName(target, "H", "20260910", "230000", "000", "310"),
				}},
				"2026-09-11": {"R": {
					lightName(target, "R", "20260911", "220000", "000", "310"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {flatName("H", "41.4", "20260910", "070000", "000", "310")},
			"2026-09-11": {flatName("R", "41.5", "20260911", "070000", "000", "310")},
		},
	}
	f.build(t, root)
	out := t.TempDir()

	if err := runPrepare(prepareOptionsForTest(target, root, out)); err != nil {
		t.Fatalf("runPrepare: %v", err)
	}

	recs := readSessionsCSV(t, metricsPath(filepath.Join(out, target), sessionsCSVName))
	if len(recs) != 3 {
		t.Fatalf("got %d rows, want header plus two sessions", len(recs))
	}
	for _, row := range recs[1:] {
		if row[2] != "PA310" {
			t.Errorf("%s pa = %q, want PA310", row[0], row[2])
		}
	}
}

// TestMixedAnglesAcrossSessions covers the predicate on its own, including the
// cases that must not be mistaken for a rotation: a target with one angle, and
// sessions carrying no angle at all.
func TestMixedAnglesAcrossSessions(t *testing.T) {
	tests := []struct {
		name     string
		sessions []sessionPlan
		want     bool
	}{
		{"no sessions", nil, false},
		{"one angle", []sessionPlan{{pa: "310"}}, false},
		{"two sessions one angle", []sessionPlan{{pa: "310"}, {pa: "310"}}, false},
		{"sessions without an angle", []sessionPlan{{pa: ""}, {pa: ""}}, false},
		{"mixed present and absent", []sessionPlan{{pa: "310"}, {pa: ""}}, false},
		{"two angles", []sessionPlan{{pa: "310"}, {pa: "240"}}, true},
		{"three angles", []sessionPlan{{pa: "310"}, {pa: "240"}, {pa: "90"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mixedAnglesAcrossSessions(tc.sessions, "TGT") != ""
			if got != tc.want {
				t.Errorf("mixedAnglesAcrossSessions = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPlanSessionLeavesAngleUnsetOnMixedAngles pins that the failing session
// carries no position angle. A half-built session with an arbitrary angle
// recorded is worse than none: it looks like a usable plan.
func TestPlanSessionLeavesAngleUnsetOnMixedAngles(t *testing.T) {
	const target = "TEST"
	root := t.TempDir()

	f := prepareFixture{
		lights: map[string]map[string]map[string][]string{
			target: {
				"2026-09-10": {"H": {
					lightName(target, "H", "20260910", "220000", "000", "310"),
					lightName(target, "H", "20260910", "230000", "000", "240"),
				}},
			},
		},
		flats: map[string][]string{
			"2026-09-10": {flatName("H", "41.4", "20260910", "070000", "000", "310")},
		},
	}
	f.build(t, root)

	s, _, err := planSession(filepath.Join(root, lightsDir, target, "2026-09-10"),
		filepath.Join(root, flatsDir), 1, "2026-09-10", []string{"2026-09-10"}, 0, c3Camera)
	if err == nil {
		t.Fatal("planSession succeeded on a mixed-angle session, want a failure")
	}
	if !errors.Is(err, errMixedAngles) {
		t.Errorf("error %v does not wrap errMixedAngles", err)
	}
	if s.pa != "" {
		t.Errorf("pa = %q, want empty on the failure path", s.pa)
	}
}
