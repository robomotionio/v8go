// Copyright 2026 the v8go contributors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Tests for AddNearHeapLimitCallback / AutomaticallyRestoreInitialHeapLimit /
// SetNearHeapLimitGrowthBytes — V8's near-heap-limit hook plumbing.
// Targets the "Reached heap limit" V8 fatal subreason ("defect #3" in
// docs/v8-windows-oom.md).
//
// Note: this hook does NOT save us from the OS-denial subreason
// (CALL_AND_RETRY_LAST). That fires when VirtualAlloc(MEM_COMMIT) is
// denied, before V8 ever reaches its own configured cap, so the callback
// never gets to run. The defense for OS-denial is InitialOldSpaceBytes
// (defect #1), tested in isolate_constraints_test.go.

package v8go_test

import (
	"testing"

	v8 "github.com/robomotionio/v8go"
)

// allocateScript pushes 1 MB strings into a long-lived array until V8
// fails. Returns nil on success (n iterations completed) or the JS error
// on failure.
const allocateScript = `
var __chunks__ = [];
var __ONE_MB__ = "A".repeat(1048576);
function grow(n) {
  for (var i = 0; i < n; i++) {
    __chunks__.push(__ONE_MB__.split("").join(""));
  }
}
`

func TestAddNearHeapLimitCallback_BumpsLimitOnDemand(t *testing.T) {
	// Configure a tight cap of 64 MB. Without the callback, V8 fatals at
	// the cap. With the callback armed and a 256 MB growth, V8 should
	// successfully grow past 64 MB.
	const capBytes = 64 * 1024 * 1024

	iso := v8.NewIsolateWithOptions(v8.IsolateOptions{
		MaxOldSpaceBytes: capBytes,
	})
	defer iso.Dispose()

	v8.SetNearHeapLimitGrowthBytes(256 * 1024 * 1024)
	t.Cleanup(func() {
		v8.SetNearHeapLimitGrowthBytes(256 * 1024 * 1024)
	})

	iso.AddNearHeapLimitCallback()

	ctx := v8.NewContext(iso)
	defer ctx.Close()

	if _, err := ctx.RunScript(allocateScript, "setup.js"); err != nil {
		t.Fatalf("setup script: %v", err)
	}

	// Push past the 64 MB cap. With the callback bumping the limit by
	// 256 MB on demand, this should succeed.
	if _, err := ctx.RunScript(`grow(80)`, "grow.js"); err != nil {
		t.Fatalf("grow(80) errored despite near-heap-limit callback "+
			"installed (callback did not bump limit): %v", err)
	}

	hs := iso.GetHeapStatistics()
	t.Logf("post-grow: UsedHeapSize=%.1f MB TotalHeapSize=%.1f MB HeapSizeLimit=%.1f MB",
		float64(hs.UsedHeapSize)/(1024*1024),
		float64(hs.TotalHeapSize)/(1024*1024),
		float64(hs.HeapSizeLimit)/(1024*1024))

	if hs.UsedHeapSize < uint64(capBytes) {
		t.Errorf("UsedHeapSize=%d, want > %d (didn't actually push past cap)",
			hs.UsedHeapSize, capBytes)
	}
	if hs.HeapSizeLimit <= uint64(capBytes) {
		t.Errorf("HeapSizeLimit=%d, want > %d (callback didn't raise limit)",
			hs.HeapSizeLimit, capBytes)
	}
}

// Note: a "no-growth" test would deliberately drive V8 past its cap
// without bumping. V8 14.x's fatal-OOM path is non-recoverable from JS
// (it aborts the process, not a RangeError), so such a test cannot run
// in-process — it would have to run as a subprocess and assert the exit
// code. We document that behavior here and rely on
// TestAddNearHeapLimitCallback_BumpsLimitOnDemand (above) to verify the
// recovery path. The pre-fix-vs-post-fix subprocess test that exercises
// the abort path lives in deskbot's nodes/flow/v8_large_inmsg_test.go.

func TestAutomaticallyRestoreInitialHeapLimit_NoSmoke(t *testing.T) {
	// Smoke test that arming the auto-restore doesn't break the isolate.
	// Verifying the actual restore behavior would require triggering V8's
	// post-spike heap usage check, which is timer-driven and not directly
	// observable from a unit test.
	iso := v8.NewIsolateWithOptions(v8.IsolateOptions{
		MaxOldSpaceBytes: 64 * 1024 * 1024,
	})
	defer iso.Dispose()

	iso.AddNearHeapLimitCallback()
	iso.AutomaticallyRestoreInitialHeapLimit(0.5)

	ctx := v8.NewContext(iso)
	defer ctx.Close()

	val, err := ctx.RunScript(`"hello"`, "smoke.js")
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if got := val.String(); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestRemoveNearHeapLimitCallback_AddThenRemove(t *testing.T) {
	// Add then remove — must not crash. V8 enforces "never remove a
	// callback that wasn't added", so this test does NOT attempt the
	// double-remove case (V8 14.x aborts on that with "unreachable code").
	iso := v8.NewIsolate()
	defer iso.Dispose()
	iso.AddNearHeapLimitCallback()
	iso.RemoveNearHeapLimitCallback(64 * 1024 * 1024)
}

func TestSetNearHeapLimitGrowthBytes_RoundTrip(t *testing.T) {
	// Sanity: the setter is goroutine-safe (atomic). No-op test that just
	// covers the API surface.
	v8.SetNearHeapLimitGrowthBytes(0)
	v8.SetNearHeapLimitGrowthBytes(1 << 30)
	v8.SetNearHeapLimitGrowthBytes(256 * 1024 * 1024)
}

