package cmd

import (
	"fmt"
	"runtime"

	"garfield/internal/readfits"
)

// peakBytesPerPixel estimates the resident working set the analysis of one
// frame needs, per image pixel. It is a measured budget, not a guess:
//
//	decoded frame (float64)          8 bytes/pixel
//	mrs flattened copy               8
//	4 binned a-trous layers (2 each) 16
//	upsampled background map         8
//	binned working buffers           2
//	pixel mask and star records      ~1
//
// The total is about 44; 48 leaves a little headroom for the per-star fit
// buffers. Anything that changes mrs's allocation pattern invalidates this
// constant, so it lives beside the planner that consumes it.
const peakBytesPerPixel = 48

// defaultFramePixels is used when the first frame's header cannot be read. It
// is the sensor size of the acquisition camera (9576x6388, about 61 Mpx), so a
// damaged or non-standard header falls back to a conservative estimate rather
// than to no cap at all.
const defaultFramePixels = 61_000_000

// memoryBudgetDivisor reserves the other half of physical memory for the OS,
// the Go runtime and everything else on the machine. Capping at half keeps a
// run from pushing a laptop into swap, which is slower than running fewer
// workers.
const memoryBudgetDivisor = 2

// workerPlan is the resolved outer/inner worker split for one run.
type workerPlan struct {
	Outer int
	Conv  int
	// MemoryLimit is the largest outer count the frame working set leaves room
	// for. Zero means physical memory could not be read and no cap was applied.
	MemoryLimit int
	FrameBytes  int64
	MemoryBytes int64
}

// resolveWorkers sizes the worker pools. The outer pool runs whole frames and
// the inner one parallelises the convolution inside each frame, so their
// product should stay near the core count rather than multiplying the load.
//
// An automatic outer count is capped by memory as well as by CPU: each frame
// carries a multi-gigabyte working set, and NumCPU/2 of those can exhaust a
// machine long before the cores are busy. An explicit --workers is never
// reduced -- the operator asked for it -- but the plan records the limit so the
// caller can warn when it is unsafe.
func resolveWorkers(requested, requestedConv, jobs int, frameBytes, totalMemory int64) workerPlan {
	plan := workerPlan{FrameBytes: frameBytes, MemoryBytes: totalMemory}
	if jobs < 1 {
		jobs = 1
	}

	if frameBytes > 0 && totalMemory > 0 {
		budget := totalMemory / memoryBudgetDivisor
		plan.MemoryLimit = int(budget / frameBytes)
		if plan.MemoryLimit < 1 {
			plan.MemoryLimit = 1
		}
	}

	numCPU := runtime.NumCPU()
	if requested > 0 {
		plan.Outer = requested
	} else {
		plan.Outer = numCPU / 2
		if plan.Outer < 2 {
			plan.Outer = 2
		}
		if plan.MemoryLimit > 0 && plan.Outer > plan.MemoryLimit {
			plan.Outer = plan.MemoryLimit
		}
	}

	if plan.Outer > jobs {
		plan.Outer = jobs
	}
	if plan.Outer < 1 {
		plan.Outer = 1
	}

	if requestedConv > 0 {
		plan.Conv = requestedConv
	} else {
		plan.Conv = numCPU / plan.Outer
		if plan.Conv < 1 {
			plan.Conv = 1
		}
	}
	return plan
}

// cappedByMemory reports whether the automatic count was reduced.
func (p workerPlan) cappedByMemory() bool {
	return p.MemoryLimit > 0 && p.Outer == p.MemoryLimit
}

// explicitExceedsMemory reports whether the operator pinned --workers above the
// count the frame working set can hold at once.
func (p workerPlan) explicitExceedsMemory(requested int) bool {
	return requested > 0 && p.MemoryLimit > 0 && requested > p.MemoryLimit
}

// workerWarning renders the warning for an explicit --workers count that will
// not fit, or "" when it fits or the limit is unknown.
func (p workerPlan) workerWarning(requested int) string {
	if !p.explicitExceedsMemory(requested) {
		return ""
	}
	return fmt.Sprintf(
		"--workers %d needs about %s of frame working set, more than half of %s of RAM; the run may swap",
		requested, formatBytes(p.FrameBytes*int64(requested)), formatBytes(p.MemoryBytes))
}

// estimateFrameBytes returns the working set of one frame from its dimensions.
func estimateFrameBytes(width, height int) int64 {
	if width <= 0 || height <= 0 {
		return defaultFrameBytes()
	}
	return int64(width) * int64(height) * peakBytesPerPixel
}

// defaultFrameBytes is the conservative fallback when dimensions are unknown.
func defaultFrameBytes() int64 {
	return int64(defaultFramePixels) * peakBytesPerPixel
}

// frameWorkingSetFor reads the first frame's header to size the working set one
// worker holds. A header that cannot be read falls back to the acquisition
// camera's sensor size.
func frameWorkingSetFor(path string) int64 {
	width, height, err := readfits.ReadDimensions(path)
	if err != nil {
		return defaultFrameBytes()
	}
	return estimateFrameBytes(width, height)
}

// formatBytes renders a byte count in binary units, for memory messages.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
