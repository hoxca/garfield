package cmd

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// frameName identifies one planned light within a session. The filter is part of
// the key because a session holds one directory per filter and a duplicate
// basename across them would otherwise be ambiguous.
type sourceFrame struct {
	filter string
	name   string
}

// lightSlotRe splits a light name at the slot the acquisition uses for its frame
// index: everything up to and including the sensor temperature, then the rest.
// The token after the temperature is the frame number on the G4 and ASI2600 and
// the gain token (GA0) on the C3.
var lightSlotRe = regexp.MustCompile(`^(.*_LIGHT_[^_]+_[^_]+_BIN\d+_[-+]?\d+C_)(.*)$`)

// dateTailRe matches the acquisition stamp that follows a frame index.
var dateTailRe = regexp.MustCompile(`^\d{8}_\d{6}_\d{1,3}_`)

// indexSessions assigns one running frame index per filter, oldest first, over
// every planned light of the target -- approved and rejected alike -- and records
// the resulting copy name on each session.
//
// The sequence is per filter rather than per session because the prepared tree is
// imported as whole nights: an index that restarts every evening tells the
// importer nothing about order, while one running number per filter does. Rejected
// frames take their place in the sequence, so the approved subset may have gaps.
//
// A light whose name has no index slot is copied unchanged and does not consume a
// number; the warning names it. Nothing here touches the source tree.
func indexSessions(sessions []sessionPlan) []string {
	type planned struct {
		session int
		filter  string
		name    string
		at      time.Time
		timed   bool
	}

	byFilter := map[string][]planned{}
	for i := range sessions {
		sessions[i].outputNames = make(map[sourceFrame]string, sessions[i].lightCount)
		for _, filter := range sessions[i].filterNames() {
			for _, name := range sessions[i].filters[filter] {
				at, ok := frameTimestamp(name)
				byFilter[filter] = append(byFilter[filter], planned{
					session: i, filter: filter, name: name, at: at, timed: ok,
				})
			}
		}
	}

	filters := make([]string, 0, len(byFilter))
	for filter := range byFilter {
		filters = append(filters, filter)
	}
	sortFilters(filters)

	var warnings []string
	for _, filter := range filters {
		frames := byFilter[filter]
		sort.Slice(frames, func(a, b int) bool {
			fa, fb := frames[a], frames[b]
			if fa.timed != fb.timed {
				return fa.timed
			}
			if fa.timed && !fa.at.Equal(fb.at) {
				return fa.at.Before(fb.at)
			}
			if fa.name != fb.name {
				return fa.name < fb.name
			}
			return fa.session < fb.session
		})

		next := 0
		for _, f := range frames {
			newName, ok := reindexedName(f.name, next+1)
			if !ok {
				warnings = append(warnings, fmt.Sprintf(
					"%s: %q has no frame-index slot; copied unchanged", filter, f.name))
				continue
			}
			next++
			sessions[f.session].outputNames[sourceFrame{filter: f.filter, name: f.name}] = newName
		}
	}
	return warnings
}

// reindexedName writes index into a light frame's index slot. A numeric token
// followed by the acquisition stamp is the old index and is replaced; anything
// else -- the C3's gain token, for instance -- is kept and the new index is
// inserted before it.
func reindexedName(name string, index int) (string, bool) {
	m := lightSlotRe.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	token, after, found := strings.Cut(m[2], "_")
	if !found || token == "" {
		return "", false
	}

	number := fmt.Sprintf("%03d", index)
	if isDigits(token) && dateTailRe.MatchString(after) {
		return m[1] + number + "_" + after, true
	}
	return m[1] + number + "_" + m[2], true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
