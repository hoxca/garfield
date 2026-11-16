package suggeststore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rec(filter string, maxFWHM float64) Suggestion {
	return Suggestion{
		SuggestVersion: SuggestVersion,
		MeasureVersion: 1,
		Filter:         filter,
		Population:     Population{N: 20, Sessions: 3, From: "2026-09-01", To: "2026-09-03"},
		Kept:           0.8,
		Reference: map[string]Reference{
			"fwhm": {Median: 2.4, Sigma: 0.1, P25: 2.35},
		},
		Thresholds: Thresholds{MinSNR: 11, MaxFWHM: maxFWHM, MinStars: 300},
	}
}

func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "cache", "suggestions.jsonl")
}

func TestPutAndReopen(t *testing.T) {
	path := storePath(t)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Put(rec("H", 2.9)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Put(rec("S", 3.4)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got, ok := s2.Lookup("H")
	if !ok {
		t.Fatal("H not found after reopen")
	}
	if got.Thresholds.MaxFWHM != 2.9 {
		t.Errorf("max-fwhm = %v, want 2.9", got.Thresholds.MaxFWHM)
	}
	if ref := got.Reference["fwhm"]; ref.Sigma != 0.1 || ref.P25 != 2.35 {
		t.Errorf("reference did not survive the round trip: %+v", ref)
	}
	if _, ok := s2.Lookup("O"); ok {
		t.Error("Lookup returned a filter that was never stored")
	}
	if len(s2.Filters()) != 2 {
		t.Errorf("stored %d filters, want 2", len(s2.Filters()))
	}
}

// The display snapshot is additive: a record written before it existed still
// loads (and simply has no table), and one carrying it round-trips intact.
func TestDisplaySnapshotRoundTrip(t *testing.T) {
	path := storePath(t)

	old := rec("H", 2.9)
	withTable := rec("O", 1.8)
	withTable.Display = &Display{
		Current:   Thresholds{MaxFWHM: 3.2, MinStars: 380, MinTransparency: 0.8},
		Rejected:  map[string]int{"max-ecc": 1, "min-transparency": 2},
		NoMetrics: 1,
		Total:     20,
		Approved:  17,
		Pinned:    []string{"min-snr"},
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Put(old); err != nil {
		t.Fatalf("Put old: %v", err)
	}
	if err := s.Put(withTable); err != nil {
		t.Fatalf("Put with table: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	gotOld, _ := s2.Lookup("H")
	if gotOld.Display != nil {
		t.Errorf("record without a snapshot gained one: %+v", gotOld.Display)
	}

	got, ok := s2.Lookup("O")
	if !ok {
		t.Fatal("O not found after reopen")
	}
	if got.Display == nil {
		t.Fatal("display snapshot did not survive the round trip")
	}
	if got.Display.Current.MaxFWHM != 3.2 || got.Display.Current.MinStars != 380 {
		t.Errorf("current thresholds = %+v, want max-fwhm 3.2 and min-stars 380", got.Display.Current)
	}
	if got.Display.Total != 20 || got.Display.Approved != 17 || got.Display.NoMetrics != 1 {
		t.Errorf("counts = %d/%d/%d, want 17 approved of 20 with 1 unmeasured",
			got.Display.Approved, got.Display.Total, got.Display.NoMetrics)
	}
	if got.Display.Rejected["max-ecc"] != 1 || got.Display.Rejected["min-transparency"] != 2 {
		t.Errorf("rejections = %v, want max-ecc 1 and min-transparency 2", got.Display.Rejected)
	}
	if len(got.Display.Pinned) != 1 || got.Display.Pinned[0] != "min-snr" {
		t.Errorf("pinned = %v, want [min-snr]", got.Display.Pinned)
	}
}

func TestLastWriteWins(t *testing.T) {
	path := storePath(t)
	s, _ := Open(path)
	if err := s.Put(rec("H", 2.9)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Put(rec("H", 3.1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, _ := Open(path)
	defer s2.Close()
	if got, _ := s2.Lookup("H"); got.Thresholds.MaxFWHM != 3.1 {
		t.Errorf("max-fwhm = %v, want the later 3.1", got.Thresholds.MaxFWHM)
	}
}

func TestTornRecordIsIgnoredAndRepaired(t *testing.T) {
	path := storePath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"filter":"H","thresholds":{"max-fwhm":2.9}}` + "\n" + `{"filter":"S","thresh` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := s.Lookup("H"); !ok {
		t.Fatal("the readable record was lost")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), `"filter":"H"`) {
		t.Errorf("repaired store lost the surviving record: %s", raw)
	}
	if strings.Contains(string(raw), `"filter":"S"`) {
		t.Errorf("repaired store kept the torn record: %s", raw)
	}
	s2, _ := Open(path)
	defer s2.Close()
	if _, ok := s2.Lookup("S"); ok {
		t.Error("a torn record was parsed as a suggestion")
	}
}
