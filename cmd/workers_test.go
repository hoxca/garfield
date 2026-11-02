package cmd

import (
	"runtime"
	"strings"
	"testing"
)

func TestEstimateFrameBytesScalesWithArea(t *testing.T) {
	one := estimateFrameBytes(100, 100)
	two := estimateFrameBytes(200, 100)

	if want := int64(100 * 100 * peakBytesPerPixel); one != want {
		t.Errorf("estimateFrameBytes(100,100) = %d, want %d", one, want)
	}
	if two != 2*one {
		t.Errorf("doubling the width did not double the estimate: %d vs %d", two, 2*one)
	}
}

func TestEstimateFrameBytesFallsBackOnBadDimensions(t *testing.T) {
	for _, tc := range []struct{ w, h int }{{0, 100}, {100, 0}, {-1, 5}} {
		if got, want := estimateFrameBytes(tc.w, tc.h), defaultFrameBytes(); got != want {
			t.Errorf("estimateFrameBytes(%d,%d) = %d, want fallback %d", tc.w, tc.h, got, want)
		}
	}
}

func TestResolveWorkersAutoIsCappedByMemory(t *testing.T) {
	const frame = 1 << 30 // 1 GiB per worker
	const total = 4 << 30 // 4 GiB → 2 GiB budget → 2 workers

	plan := resolveWorkers(0, 0, 1000, frame, total)
	if plan.Outer != 2 {
		t.Errorf("Outer = %d, want 2 (memory cap)", plan.Outer)
	}
	if plan.MemoryLimit != 2 {
		t.Errorf("MemoryLimit = %d, want 2", plan.MemoryLimit)
	}
	if !plan.cappedByMemory() {
		t.Error("cappedByMemory() = false, want true")
	}
	if got, want := plan.Conv, maxInt(1, runtime.NumCPU()/2); got != want {
		t.Errorf("Conv = %d, want %d", got, want)
	}
}

func TestResolveWorkersNeverGoesBelowOne(t *testing.T) {
	const frame = 64 << 30
	const total = 8 << 30

	plan := resolveWorkers(0, 0, 1000, frame, total)
	if plan.Outer != 1 {
		t.Errorf("Outer = %d, want 1 under an impossible memory budget", plan.Outer)
	}
	if plan.Conv < 1 {
		t.Errorf("Conv = %d, want at least 1", plan.Conv)
	}
}

func TestResolveWorkersAutoUsesHalfTheCoresWhenMemoryAllows(t *testing.T) {
	plan := resolveWorkers(0, 0, 1000, 1, 1<<40)

	want := runtime.NumCPU() / 2
	if want < 2 {
		want = 2
	}
	if plan.Outer != want {
		t.Errorf("Outer = %d, want %d", plan.Outer, want)
	}
	if plan.cappedByMemory() {
		t.Error("cappedByMemory() = true with ample memory")
	}
	if wantConv := maxInt(1, runtime.NumCPU()/plan.Outer); plan.Conv != wantConv {
		t.Errorf("Conv = %d, want %d", plan.Conv, wantConv)
	}
}

func TestResolveWorkersExplicitIsNotReduced(t *testing.T) {
	const frame = 8 << 30
	const total = 4 << 30 // cap is 1

	plan := resolveWorkers(6, 0, 1000, frame, total)
	if plan.Outer != 6 {
		t.Errorf("Outer = %d, want the explicit 6 to be honoured", plan.Outer)
	}
	if plan.cappedByMemory() {
		t.Error("cappedByMemory() = true for an explicit count")
	}
	if !plan.explicitExceedsMemory(6) {
		t.Error("explicitExceedsMemory(6) = false, want true")
	}
	warn := plan.workerWarning(6)
	if warn == "" {
		t.Fatal("workerWarning(6) = empty, want a warning")
	}
	if !strings.Contains(warn, "6") || !strings.Contains(warn, "RAM") {
		t.Errorf("warning %q does not name the count and the constraint", warn)
	}
}

func TestResolveWorkersExplicitWithinMemoryDoesNotWarn(t *testing.T) {
	plan := resolveWorkers(2, 0, 1000, 1<<30, 64<<30)
	if plan.explicitExceedsMemory(2) {
		t.Error("explicitExceedsMemory(2) = true, want false")
	}
	if warn := plan.workerWarning(2); warn != "" {
		t.Errorf("workerWarning(2) = %q, want empty", warn)
	}
}

func TestResolveWorkersClampsToJobCount(t *testing.T) {
	plan := resolveWorkers(8, 0, 3, 1, 1<<40)
	if plan.Outer != 3 {
		t.Errorf("Outer = %d, want 3 for three jobs", plan.Outer)
	}
}

func TestResolveWorkersUnknownMemoryDisablesCap(t *testing.T) {
	plan := resolveWorkers(0, 0, 1000, defaultFrameBytes(), 0)
	if plan.MemoryLimit != 0 {
		t.Errorf("MemoryLimit = %d, want 0 when RAM is unknown", plan.MemoryLimit)
	}
	if plan.cappedByMemory() {
		t.Error("cappedByMemory() = true without a memory reading")
	}
}

func TestResolveWorkersHonoursExplicitConvolution(t *testing.T) {
	plan := resolveWorkers(2, 7, 1000, 1, 1<<40)
	if plan.Conv != 7 {
		t.Errorf("Conv = %d, want the explicit 7", plan.Conv)
	}
}

func TestFormatBytes(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 30, "1.0 GiB"},
	} {
		if got := formatBytes(tc.n); got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
