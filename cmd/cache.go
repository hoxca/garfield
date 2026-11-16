package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"garfield/internal/measurecache"
)

// measureVersion is the schema of a cached measurement.
//
// Bump it whenever a change to measureFrame, to internal/starsmetrics or to
// internal/mrs would change the numbers for the same pixels. Older entries are
// then ignored rather than read as if they were current, so a stale cache can
// only cost time, never correctness. A threshold change is not a schema change:
// verdicts are derived from measurements on every run.
const measureVersion = 1

// measuresCacheName is the store's file name inside <output>/cache.
const measuresCacheName = "measurements.jsonl"

// cacheDir is the subdirectory of the output that holds the store, kept apart
// from the reports so deleting the cache is an obvious, self-contained action.
const cacheDir = "cache"

// errNoStarFound is the one measurement failure worth caching: it is
// deterministic in the pixels and costs a full background estimate plus a star
// search to rediscover.
var errNoStarFound = errors.New("no star found")

// cachePath is where a run stores its measurements. It lives in the output
// directory, never in the acquisition tree: a frame is only ever read.
func cachePath(output string) string {
	return filepath.Join(output, cacheDir, measuresCacheName)
}

// openMeasureCache opens the store under output. A store that cannot be read is
// reported and skipped rather than fatal: the run then measures everything,
// which is what it would do without --cache.
func openMeasureCache(output string) (*measurecache.Cache, error) {
	c, err := measurecache.Open(cachePath(output))
	if err != nil {
		return nil, fmt.Errorf("%s; measuring every frame instead", err)
	}
	return c, nil
}

// closeMeasureCache flushes the store, reporting a failure as a warning: the
// measurements of this run are already in hand, so a cache that cannot be
// written costs the next run time but not this one's result.
func closeMeasureCache(c *measurecache.Cache) {
	if c == nil {
		return
	}
	if err := c.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: measurement cache not saved: %v\n", err)
	}
}

// measureWithCache returns a frame's measurements, reusing a stored entry when
// one matches this frame and this run's measurement options, and storing the
// fresh measurement when it does not.
func measureWithCache(path string, opts analyzeOptions, c *measurecache.Cache) ImageResult {
	if c == nil {
		return measureFrame(path, opts)
	}

	key, err := measurecache.KeyFor(path)
	if err != nil {
		// The frame cannot be identified, so there is nothing to key a cache
		// entry on. Measuring it yields the error the report should carry.
		return measureFrame(path, opts)
	}
	if e, ok := c.Lookup(key); ok && cacheEntryUsable(e, opts) {
		return resultFromEntry(path, e)
	}

	start := time.Now()
	r := measureFrame(path, opts)
	status, storable := cacheStatus(r)
	if storable {
		// A failed write must not fail the run: the measurement in hand is
		// valid, and the next run simply measures the frame again.
		_ = c.Put(entryFromResult(key, r, opts, time.Since(start), status))
	}
	return r
}

// cacheEntryUsable reports whether a stored entry was produced by this build,
// with the options that change the measurements themselves.
func cacheEntryUsable(e measurecache.Entry, opts analyzeOptions) bool {
	return e.Version == measureVersion &&
		e.Limit == opts.limitComputedStars &&
		e.Moffat == opts.moffat
}

// cacheStatus classifies a result for storage. Errors other than "no star found"
// say nothing about the pixels -- an unreadable file is an environment problem --
// so they are not stored, and a later run gets to try again.
func cacheStatus(r ImageResult) (int, bool) {
	switch {
	case r.Error == nil:
		return measurecache.StatusOK, true
	case errors.Is(r.Error, errNoStarFound):
		return measurecache.StatusNoStars, true
	default:
		return 0, false
	}
}

// entryFromResult renders a measurement for the store.
func entryFromResult(key measurecache.Key, r ImageResult, opts analyzeOptions, elapsed time.Duration, status int) measurecache.Entry {
	return measurecache.Entry{
		Version:         measureVersion,
		Key:             key,
		Limit:           opts.limitComputedStars,
		Moffat:          opts.moffat,
		ElapsedMS:       elapsed.Milliseconds(),
		Status:          status,
		DetectedStars:   r.DetectedStars,
		StarCount:       r.StarCount,
		AvgFWHM:         r.AvgFWHM,
		AvgSignal:       r.AvgSignal,
		AvgEccentricity: r.AvgEccentricity,
		SNR:             r.SNR,
		Background:      r.Background,
		Noise:           r.Noise,
		BgStructure:     r.BgStructure,
		Halo:            r.Halo,
		HaloStars:       r.HaloStars,
	}
}

// resultFromEntry rebuilds a frame's result from a stored measurement. Only the
// measurement is restored: the score is recomputed from the three values it is
// a function of, and the verdict and session ratios are left for the batch passes
// that own them.
func resultFromEntry(path string, e measurecache.Entry) ImageResult {
	name := filepath.Base(path)
	filter, date := parseFilename(name)

	r := ImageResult{
		Filename:        name,
		Filter:          filter,
		Date:            date,
		DetectedStars:   e.DetectedStars,
		StarCount:       e.StarCount,
		AvgFWHM:         e.AvgFWHM,
		AvgSignal:       e.AvgSignal,
		AvgEccentricity: e.AvgEccentricity,
		SNR:             e.SNR,
		Score:           frameScore(e.SNR, e.AvgFWHM, e.AvgEccentricity),
		Background:      e.Background,
		Noise:           e.Noise,
		BgStructure:     e.BgStructure,
		Halo:            e.Halo,
		HaloStars:       e.HaloStars,
		Cached:          true,
	}
	if e.Status == measurecache.StatusNoStars {
		r.Error = errNoStarFound
	}
	return r
}

// countCached reports how many results were reused, and how many were measured.
func countCached(results []ImageResult) (cached, measured int) {
	for _, r := range results {
		if r.Cached {
			cached++
			continue
		}
		measured++
	}
	return cached, measured
}
