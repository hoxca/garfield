package cmd

import (
	"reflect"
	"testing"
)

// The wheel is L R G B S H O, and every filter list must read that way. Sorting
// by name would put B before R, which is not where the wheel holds it.
func TestSortFiltersUsesWheelOrder(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"all seven shuffled", []string{"O", "B", "H", "S", "G", "R", "L"}, []string{"L", "R", "G", "B", "S", "H", "O"}},
		{"alphabetical input is not the order", []string{"B", "G", "H", "O", "R", "S"}, []string{"R", "G", "B", "S", "H", "O"}},
		{"two", []string{"S", "L"}, []string{"L", "S"}},
		{"one", []string{"H"}, []string{"H"}},
		{"none", nil, nil},
		// A token outside the wheel sorts last rather than mixing in; two sort by
		// name among themselves so the result stays deterministic.
		{"unknown last", []string{"X", "B", "A"}, []string{"B", "A", "X"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := append([]string(nil), tc.in...)
			sortFilters(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("sortFilters(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// filterRank is the ordering key; pin it so a reordered knownFilters is caught
// here rather than in the order of a run.
func TestFilterRankFollowsKnownFilters(t *testing.T) {
	for i, filter := range knownFilters {
		if got := filterRank(string(filter)); got != i {
			t.Errorf("filterRank(%q) = %d, want %d", string(filter), got, i)
		}
	}
	if got, want := filterRank("X"), len(knownFilters); got != want {
		t.Errorf("filterRank(%q) = %d, want %d", "X", got, want)
	}
	// The length guard: a multi-character token is not a member even when its
	// characters are.
	if got, want := filterRank("BG"), len(knownFilters); got != want {
		t.Errorf("filterRank(%q) = %d, want %d", "BG", got, want)
	}
}
