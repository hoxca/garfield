package cmd

import (
	"sync"

	"garfield/internal/measurecache"
)

// measurePaths measures every frame, in the order given, reusing an entry from
// cache when one is usable. A nil cache measures without storing anything, which
// is what an ephemeral analyze over an isolated directory does.
//
// onProgress, when non-nil, is called after each frame completes; it is already
// serialised by the helper, so callers do not need their own lock. Results are
// index-aligned with paths: the pool writes distinct indices.
func measurePaths(paths []string, opts analyzeOptions, cache *measurecache.Cache, workers int, onProgress func(done, total int)) []ImageResult {
	results := make([]ImageResult, len(paths))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var progressMu sync.Mutex
	done := 0

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				results[idx] = measureWithCache(paths[idx], opts, cache)
				if onProgress == nil {
					continue
				}
				progressMu.Lock()
				done++
				onProgress(done, len(paths))
				progressMu.Unlock()
			}
		}()
	}
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}
