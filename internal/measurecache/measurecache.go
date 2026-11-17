// Package measurecache stores per-frame quality measurements so that a run can
// reuse them instead of recomputing them.
//
// The store is a JSON-lines file next to the reports: one object per frame,
// appended, with the last entry for a key winning. Appending keeps a crash from
// costing more than the line that was in flight, where a rewritten file would
// risk the whole cache, and JSON keeps it readable with jq when the question is
// why a frame was measured again.
//
// A key is the frame's absolute path, size and modification time. Anything that
// rewrites a frame moves its modification time, so a stale measurement can only
// cause a miss, never a wrong hit. The cost is that a tree copied without
// preserving times is measured once more, which is the safe direction.
package measurecache

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// flushEvery is how many entries are buffered before they reach the file. Small
// enough that a long run does not lose its work, large enough that the write
// syscalls stay negligible beside a frame's analysis.
const flushEvery = 64

// Measurement statuses. A frame that yielded no usable star is worth caching:
// detecting that costs a full background estimate and star search, and the
// outcome is deterministic in the pixels.
const (
	StatusOK      = 0
	StatusNoStars = 1
)

// Key identifies one version of one frame.
type Key struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Mod is the modification time in Unix nanoseconds, which is finer than the
	// filesystem's own granularity on every platform garfield runs on.
	Mod int64 `json:"mtime"`
}

// KeyFor builds the key of a frame on disk.
func KeyFor(path string) (Key, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Key{}, fmt.Errorf("resolve %q: %w", path, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return Key{}, err
	}
	return Key{Path: abs, Size: fi.Size(), Mod: fi.ModTime().UnixNano()}, nil
}

// Entry is everything a run caches about one frame.
//
// It holds measurements only: no verdict, no session-relative ratio, no score.
// Those depend on the thresholds and on the population the frame belongs to, so
// they are derived again on every run. Caching them would tie the store to one
// configuration and throw it away each time a threshold is tuned.
type Entry struct {
	// Version is the measurement schema the entry was written with. The caller
	// owns the number and bumps it whenever the measurement code changes.
	Version int `json:"v"`
	Key     `json:"key"`
	// Limit and Moffat are the measurement options that change the numbers, as
	// opposed to the thresholds that only change the verdict.
	Limit     int   `json:"limit"`
	Moffat    bool  `json:"moffat,omitempty"`
	ElapsedMS int64 `json:"elapsedMs"`
	Status    int   `json:"status"`

	DetectedStars   int     `json:"detectedStars"`
	StarCount       int     `json:"starCount"`
	AvgFWHM         float64 `json:"avgFWHM"`
	AvgSignal       float64 `json:"avgSignal"`
	AvgEccentricity float64 `json:"avgEccentricity"`
	SNR             float64 `json:"snr"`
	Background      float64 `json:"background"`
	Noise           float64 `json:"noise"`
	BgStructure     float64 `json:"bgStructure"`
	Halo            float64 `json:"halo"`
	HaloStars       int     `json:"haloStars"`
	Airmass         float64 `json:"airmass,omitempty"`
	// SunBelow is how far the Sun sat below the horizon at DATE-OBS, read from
	// the header rather than from the pixels and kept to a hundredth of a degree.
	// A nil pointer marks an entry written before the field existed, so a later
	// run reads the header once and fills it in; zero is a genuine answer (no
	// site recorded, or the Sun on the horizon), which the report renders empty.
	SunBelow *float64 `json:"sunBelow,omitempty"`
}

// Cache is an open measurement store. It is safe for concurrent use: the
// per-frame workers share one.
type Cache struct {
	path string

	mu      sync.Mutex
	entries map[Key]Entry
	pending []Entry
	out     *os.File
	// lines counts the records on disk, so Close can tell when the file has
	// accumulated enough superseded entries to be worth rewriting.
	lines int
	// torn records that a crash left behind, or that a newer schema wrote and an
	// older binary cannot parse. Either way the file wants rewriting.
	torn bool
}

// Open reads the store at path, which may not exist yet.
func Open(path string) (*Cache, error) {
	c := &Cache{path: path, entries: map[Key]Entry{}}

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open measurement cache: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Entries are a few hundred bytes; the default 64 KiB is generous already,
	// but a path can be long and a scanner error would be silent otherwise.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		c.lines++
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			// One unreadable record is not a reason to discard the rest: a run
			// killed mid-write leaves a partial last line, and every earlier
			// measurement is still valid.
			c.torn = true
			continue
		}
		c.entries[e.Key] = e
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read measurement cache: %w", err)
	}
	return c, nil
}

// Lookup returns the entry recorded for key. The caller decides whether it is
// still usable: only the caller knows the current version and options.
func (c *Cache) Lookup(key Key) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	return e, ok
}

// Len is the number of frames the store knows, after deduplication.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Put records one frame's measurement.
func (c *Cache) Put(e Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[e.Key] = e
	c.pending = append(c.pending, e)
	if len(c.pending) < flushEvery {
		return nil
	}
	return c.flushLocked()
}

// flushLocked appends buffered entries. The file is opened on first use so that
// a run that hits the cache on every frame writes nothing at all.
func (c *Cache) flushLocked() error {
	if len(c.pending) == 0 {
		return nil
	}
	if c.out == nil {
		if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
			return fmt.Errorf("create measurement cache directory: %w", err)
		}
		f, err := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open measurement cache for append: %w", err)
		}
		c.out = f
	}

	buf := make([]byte, 0, len(c.pending)*256)
	for _, e := range c.pending {
		line, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("encode measurement: %w", err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	if _, err := c.out.Write(buf); err != nil {
		return fmt.Errorf("write measurement cache: %w", err)
	}
	c.lines += len(c.pending)
	c.pending = c.pending[:0]
	return nil
}

// Close flushes and, when the file has filled with superseded records, rewrites
// it in place. Rewriting drops entries for frames that have since been modified
// or removed, which keeps the store proportional to the tree without needing a
// pruning policy anywhere else.
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	err := c.flushLocked()
	if c.out != nil {
		if cerr := c.out.Close(); cerr != nil && err == nil {
			err = cerr
		}
		c.out = nil
	}
	if err != nil {
		return err
	}

	// A freshly created store with nothing in it should not leave an empty file
	// behind, and an untouched one should not be rewritten for nothing.
	if c.lines == 0 {
		return nil
	}
	if !c.torn && c.lines <= 2*len(c.entries) {
		return nil
	}
	return c.compactLocked()
}

// compactLocked rewrites the store with one record per live frame.
func (c *Cache) compactLocked() error {
	live := make([]Entry, 0, len(c.entries))
	for k, e := range c.entries {
		fi, err := os.Stat(k.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			// Unreadable for another reason (permissions, a vanished mount):
			// keep it rather than throw away a measurement that may still be
			// good the next time the tree is reachable.
			live = append(live, e)
			continue
		}
		if fi.Size() != k.Size || fi.ModTime().UnixNano() != k.Mod {
			continue
		}
		live = append(live, e)
	}

	if len(live) == 0 {
		if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove measurement cache: %w", err)
		}
		c.entries = map[Key]Entry{}
		c.lines = 0
		c.torn = false
		return nil
	}

	tmp := c.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("rewrite measurement cache: %w", err)
	}
	w := bufio.NewWriter(f)
	for _, e := range live {
		line, err := json.Marshal(e)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("encode measurement: %w", err)
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("rewrite measurement cache: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("rewrite measurement cache: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rewrite measurement cache: %w", err)
	}
	// The rename is what makes the rewrite atomic: a reader either sees the old
	// file or the new one, never a half-written store.
	if err := os.Rename(tmp, c.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace measurement cache: %w", err)
	}

	c.entries = make(map[Key]Entry, len(live))
	for _, e := range live {
		c.entries[e.Key] = e
	}
	c.lines = len(live)
	c.torn = false
	return nil
}
