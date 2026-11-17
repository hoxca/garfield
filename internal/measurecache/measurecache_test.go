package measurecache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// storePath returns a cache path inside a fresh directory.
func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "cache", "measurements.jsonl")
}

// frame writes a file of the given size and returns its key.
func frame(t *testing.T, dir, name string, size int) Key {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	key, err := KeyFor(path)
	if err != nil {
		t.Fatalf("KeyFor: %v", err)
	}
	return key
}

func entryFor(key Key, fwhm float64) Entry {
	return Entry{
		Version:   1,
		Key:       key,
		Limit:     500,
		Status:    StatusOK,
		StarCount: 42,
		AvgFWHM:   fwhm,
	}
}

func TestKeyForUsesAbsolutePathAndStat(t *testing.T) {
	dir := t.TempDir()
	key := frame(t, dir, "a.FIT", 10)

	if !filepath.IsAbs(key.Path) {
		t.Errorf("path = %q, want absolute", key.Path)
	}
	if key.Size != 10 {
		t.Errorf("size = %d, want 10", key.Size)
	}
	if key.Mod == 0 {
		t.Error("mtime = 0, want the file's modification time")
	}

	// Relative and absolute spellings of one frame must produce one key, or a
	// run would store the same frame twice depending on how it was invoked.
	rel, err := filepath.Rel(mustGetwd(t), key.Path)
	if err != nil {
		t.Skipf("frame is not under the working directory: %v", err)
	}
	relKey, err := KeyFor(rel)
	if err != nil {
		t.Fatalf("KeyFor(relative): %v", err)
	}
	if relKey != key {
		t.Errorf("relative key %+v != absolute key %+v", relKey, key)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

func TestKeyForMissingFile(t *testing.T) {
	if _, err := KeyFor(filepath.Join(t.TempDir(), "absent.FIT")); err == nil {
		t.Fatal("KeyFor on a missing file: got no error")
	}
}

func TestOpenMissingStoreIsEmptyAndCreatesNothing(t *testing.T) {
	path := storePath(t)
	c, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d, want 0", c.Len())
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A run that never measured anything must not litter the output directory.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cache file exists after an empty run: %v", err)
	}
}

func TestPutAndReopen(t *testing.T) {
	path := storePath(t)
	dir := t.TempDir()
	key := frame(t, dir, "a.FIT", 8)

	c, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := c.Put(entryFor(key, 2.5)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	c2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer c2.Close()

	got, ok := c2.Lookup(key)
	if !ok {
		t.Fatal("Lookup after reopen: not found")
	}
	if got.AvgFWHM != 2.5 || got.StarCount != 42 || got.Limit != 500 {
		t.Errorf("entry = %+v, want the stored measurement", got)
	}

	if _, ok := c2.Lookup(Key{Path: key.Path + ".other"}); ok {
		t.Error("Lookup returned an entry for an unknown key")
	}
}

// A nil SunBelow means the entry predates the field, and a non-nil zero means
// the header was read and held no site: the difference is what lets a later run
// backfill once instead of reopening the frame on every hit.
func TestSunBelowNilIsNotZero(t *testing.T) {
	path := storePath(t)
	dir := t.TempDir()
	old := frame(t, dir, "old.FIT", 8)
	absent := frame(t, dir, "absent.FIT", 8)
	known := frame(t, dir, "known.FIT", 8)

	oldEntry := entryFor(old, 2.5) // SunBelow nil: written before the field existed
	absentEntry := entryFor(absent, 2.5)
	zero := 0.0
	absentEntry.SunBelow = &zero
	knownEntry := entryFor(known, 2.5)
	below := 25.85
	knownEntry.SunBelow = &below

	c, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, e := range []Entry{oldEntry, absentEntry, knownEntry} {
		if err := c.Put(e); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	c2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer c2.Close()

	if got, _ := c2.Lookup(old); got.SunBelow != nil {
		t.Errorf("old entry gained a SunBelow: %v", *got.SunBelow)
	}
	if got, _ := c2.Lookup(absent); got.SunBelow == nil || *got.SunBelow != 0 {
		t.Errorf("absent entry = %v, want a recorded zero", got.SunBelow)
	}
	if got, _ := c2.Lookup(known); got.SunBelow == nil || *got.SunBelow != below {
		t.Errorf("known entry = %v, want %v", got.SunBelow, below)
	}
}

func TestLastWriteWins(t *testing.T) {
	path := storePath(t)
	dir := t.TempDir()
	key := frame(t, dir, "a.FIT", 8)

	c, _ := Open(path)
	if err := c.Put(entryFor(key, 1.0)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Put(entryFor(key, 3.0)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	c2, _ := Open(path)
	defer c2.Close()
	if got, _ := c2.Lookup(key); got.AvgFWHM != 3.0 {
		t.Errorf("AvgFWHM = %v, want the later measurement 3", got.AvgFWHM)
	}
	if c2.Len() != 1 {
		t.Errorf("Len = %d, want 1 after deduplication", c2.Len())
	}
}

func TestTornLastLineIsIgnoredAndRepaired(t *testing.T) {
	path := storePath(t)
	dir := t.TempDir()
	key := frame(t, dir, "a.FIT", 8)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	good, err := json.Marshal(entryFor(key, 2.0))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// A crash mid-write leaves a partial line. Everything before it is valid and
	// must survive.
	body := string(good) + "\n" + `{"v":1,"key":{"path":"/trunc` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write store: %v", err)
	}

	c, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got, ok := c.Lookup(key); !ok || got.AvgFWHM != 2.0 {
		t.Fatalf("Lookup = %+v, %v; want the readable record", got, ok)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repaired store: %v", err)
	}
	if strings.Contains(string(raw), "/trunc") {
		t.Errorf("torn record survived the rewrite:\n%s", raw)
	}
	c2, _ := Open(path)
	defer c2.Close()
	if c2.Len() != 1 {
		t.Errorf("Len = %d after repair, want 1", c2.Len())
	}
}

func TestCloseDropsStaleEntries(t *testing.T) {
	path := storePath(t)
	dir := t.TempDir()
	kept := frame(t, dir, "kept.FIT", 8)
	modified := frame(t, dir, "modified.FIT", 8)
	removed := frame(t, dir, "removed.FIT", 8)

	c, _ := Open(path)
	for _, k := range []Key{kept, modified, removed} {
		if err := c.Put(entryFor(k, 2.0)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	// Touch one frame so its key no longer matches, and delete another: after the
	// rewrite neither should still occupy a record.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(modified.Path, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := os.Remove(removed.Path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Force a rewrite without waiting for the 2x threshold by leaving a torn
	// record behind.
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("reopen append: %v", err)
	}
	if _, err := f.WriteString("{torn\n"); err != nil {
		t.Fatalf("append torn: %v", err)
	}
	f.Close()

	c2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if c2.Len() != 3 {
		t.Fatalf("Len before compaction = %d, want 3", c2.Len())
	}
	if err := c2.Close(); err != nil {
		t.Fatalf("Close after torn: %v", err)
	}

	c3, _ := Open(path)
	defer c3.Close()
	if c3.Len() != 1 {
		t.Errorf("Len after compaction = %d, want 1 (only the untouched frame)", c3.Len())
	}
	if _, ok := c3.Lookup(kept); !ok {
		t.Error("the live frame's entry was dropped")
	}
}

func TestEmptyStoreIsRemovedOnCompaction(t *testing.T) {
	path := storePath(t)
	dir := t.TempDir()
	key := frame(t, dir, "gone.FIT", 8)

	c, _ := Open(path)
	if err := c.Put(entryFor(key, 2.0)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := os.Remove(key.Path); err != nil {
		t.Fatalf("remove frame: %v", err)
	}
	// Append a torn record so the next Close rewrites, then check that a store
	// with nothing left in it disappears rather than lingering empty.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	f.WriteString("{torn\n")
	f.Close()

	c2, _ := Open(path)
	if err := c2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cache file still present with no live entries: %v", err)
	}
}

func TestConcurrentPut(t *testing.T) {
	path := storePath(t)
	dir := t.TempDir()

	// Every frame exists before the workers start: t.Fatalf in the helper may
	// only run on the test's own goroutine.
	const workers, perWorker = 8, 20
	keys := make([][]Key, workers)
	for w := 0; w < workers; w++ {
		keys[w] = make([]Key, perWorker)
		for i := 0; i < perWorker; i++ {
			keys[w][i] = frame(t, filepath.Join(dir, fmt.Sprintf("w%d", w)), fmt.Sprintf("f%d.FIT", i), 4)
		}
	}

	c, _ := Open(path)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if err := c.Put(entryFor(keys[w][i], float64(i))); err != nil {
					t.Errorf("Put: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	c2, _ := Open(path)
	defer c2.Close()
	if c2.Len() != workers*perWorker {
		t.Errorf("Len = %d, want %d", c2.Len(), workers*perWorker)
	}
}
