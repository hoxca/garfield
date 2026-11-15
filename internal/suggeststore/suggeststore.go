// Package suggeststore keeps the thresholds a run suggested for a filter, so a
// later `prepare` can judge frames with the same numbers and the same reference
// statistics they were derived from.
//
// One record per filter, last one wins, in a JSON-lines file beside the
// measurement cache. Appending keeps a crash from costing more than the record
// in flight, and the record is small enough that a full rewrite on close is
// nothing next to the analysis that produced it.
package suggeststore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SuggestVersion is the schema of a suggestion. Bump it when the derivation
// changes in a way that makes an older record mean something else.
const SuggestVersion = 1

// Reference is the stack statistic one metric was judged against.
type Reference struct {
	Median float64 `json:"median"`
	Sigma  float64 `json:"sigma"`
	// P25 is the 25th percentile, the baseline the session-relative rules use
	// for the halo and the sky structure.
	P25 float64 `json:"p25,omitempty"`
}

// Thresholds are the values to apply, in config-key form.
type Thresholds struct {
	MinSNR       float64 `json:"min-snr"`
	MaxFWHM      float64 `json:"max-fwhm"`
	MaxEcc       float64 `json:"max-ecc"`
	MinScore     float64 `json:"min-score"`
	MinStars     int     `json:"min-stars"`
	MaxFWHMSigma float64 `json:"max-fwhm-sigma"`
	MaxHaloRatio float64 `json:"max-halo-ratio"`
	MaxStructure float64 `json:"max-structure"`
}

// Population describes the frames the suggestion was derived from, so a later
// run can tell when it has grown beyond them.
type Population struct {
	N        int    `json:"n"`
	Sessions int    `json:"sessions"`
	From     string `json:"from"`
	To       string `json:"to"`
}

// SessionNote records how one night of the population was judged.
type SessionNote struct {
	Date  string   `json:"date"`
	N     int      `json:"n"`
	Flags []string `json:"flags,omitempty"`
}

// Suggestion is one filter's suggested thresholds and the context behind them.
type Suggestion struct {
	SuggestVersion int    `json:"suggestVersion"`
	MeasureVersion int    `json:"measureVersion"`
	Filter         string `json:"filter"`

	Population    Population `json:"population"`
	Heterogeneity float64    `json:"heterogeneity"`
	// Band is how many robust sigmas around the good stack's median the gates
	// sit, kept so a later run can describe the suggestion it is applying.
	Band       float64              `json:"band"`
	Kept       float64              `json:"kept"`
	Reference  map[string]Reference `json:"reference"`
	Thresholds Thresholds           `json:"thresholds"`
	Sessions   []SessionNote        `json:"sessions,omitempty"`
	Created    time.Time            `json:"created"`
}

// Store is the open file of suggestions.
type Store struct {
	path string
	byID map[string]Suggestion
	// pending holds records written since the last flush, and lines counts the
	// records on disk so a rewrite can be decided on.
	pending []Suggestion
	lines   int
	torn    bool
	out     *os.File
}

// Open reads the store at path, which may not exist yet.
func Open(path string) (*Store, error) {
	s := &Store{path: path, byID: map[string]Suggestion{}}

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open suggestion store: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		s.lines++
		var rec Suggestion
		if err := json.Unmarshal(line, &rec); err != nil {
			// A record killed mid-write must not take the readable ones with it.
			s.torn = true
			continue
		}
		s.byID[rec.Filter] = rec
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read suggestion store: %w", err)
	}
	return s, nil
}

// Lookup returns the suggestion recorded for a filter.
func (s *Store) Lookup(filter string) (Suggestion, bool) {
	rec, ok := s.byID[filter]
	return rec, ok
}

// Put records a suggestion, replacing any earlier one for the same filter.
func (s *Store) Put(rec Suggestion) error {
	s.byID[rec.Filter] = rec
	s.pending = append(s.pending, rec)
	return nil
}

// Filters returns the filters the store knows, in no particular order.
func (s *Store) Filters() []string {
	out := make([]string, 0, len(s.byID))
	for f := range s.byID {
		out = append(out, f)
	}
	return out
}

// Close flushes the run and compacts the file when it holds superseded records
// or a torn one.
func (s *Store) Close() error {
	if err := s.flush(); err != nil {
		return err
	}
	if s.out != nil {
		if err := s.out.Close(); err != nil {
			return err
		}
		s.out = nil
	}
	if s.lines == 0 {
		return nil
	}
	if !s.torn && s.lines <= 2*len(s.byID) {
		return nil
	}
	return s.compact()
}

func (s *Store) flush() error {
	if len(s.pending) == 0 {
		return nil
	}
	if s.out == nil {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
			return fmt.Errorf("create suggestion directory: %w", err)
		}
		f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open suggestion store for append: %w", err)
		}
		s.out = f
	}
	buf := make([]byte, 0, len(s.pending)*512)
	for _, rec := range s.pending {
		line, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("encode suggestion: %w", err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	if _, err := s.out.Write(buf); err != nil {
		return fmt.Errorf("write suggestion store: %w", err)
	}
	s.lines += len(s.pending)
	s.pending = s.pending[:0]
	return nil
}

// compact rewrites the store with one record per filter.
func (s *Store) compact() error {
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("rewrite suggestion store: %w", err)
	}
	w := bufio.NewWriter(f)
	for _, rec := range s.byID {
		line, err := json.Marshal(rec)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("encode suggestion: %w", err)
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("rewrite suggestion store: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("rewrite suggestion store: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rewrite suggestion store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace suggestion store: %w", err)
	}
	s.lines = len(s.byID)
	s.torn = false
	return nil
}
