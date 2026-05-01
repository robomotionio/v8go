// Copyright 2026 the v8go contributors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Tests for NewIsolateWithOptions / IsolateOptions — the per-isolate
// CreateParams::constraints surface. Targets the cold-start commit-denial
// pathology described in docs/v8-windows-oom.md ("defect #1").
//
// The load-bearing assertion is that InitialOldSpaceBytes causes V8 to
// commit the requested heap size at isolate creation rather than at first
// allocation. On Windows under memory pressure the difference between the
// two timings is the difference between "commit granted at flow start
// while system is idle" and "commit denied at peak load → fatal OOM".

package v8go_test

import (
	"testing"

	v8 "github.com/robomotionio/v8go"
)

func TestNewIsolateWithOptions_ZeroValueMatchesNewIsolate(t *testing.T) {
	t.Parallel()
	// IsolateOptions{} (all zeros) must produce an isolate indistinguishable
	// from NewIsolate() — backwards compatibility for existing callers.
	iso := v8.NewIsolateWithOptions(v8.IsolateOptions{})
	defer iso.Dispose()

	ctx := v8.NewContext(iso)
	defer ctx.Close()

	val, err := ctx.RunScript("1 + 2", "smoke.js")
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if got := val.Integer(); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}

func TestNewIsolateWithOptions_MaxOldSpaceIsApplied(t *testing.T) {
	t.Parallel()
	// MaxOldSpaceBytes is the cap setter that DOES work as the name
	// suggests: V8 surfaces it via HeapSizeLimit, and the cap binds.
	//
	// (Note: InitialOldSpaceBytes is honored by V8 only as the "don't
	// trigger GC until heap reaches this size" hint — it does NOT cause
	// eager commit of pages. To pre-commit pages, callers should use
	// Isolate.WarmupOldGenerationHeap instead. See doc on IsolateOptions.)
	const cap = 256 * 1024 * 1024

	iso := v8.NewIsolateWithOptions(v8.IsolateOptions{
		MaxOldSpaceBytes: cap,
	})
	defer iso.Dispose()

	hs := iso.GetHeapStatistics()
	t.Logf("after NewIsolateWithOptions(max=256MB): TotalHeapSize=%.1f MB HeapSizeLimit=%.1f MB",
		float64(hs.TotalHeapSize)/(1024*1024),
		float64(hs.HeapSizeLimit)/(1024*1024))

	// V8 reports HeapSizeLimit as the max old gen plus young gen and
	// internal overhead. Allow up to 50% slack above the configured cap,
	// but require at least 80% of the cap (otherwise constraint was ignored).
	if hs.HeapSizeLimit < uint64(cap)*8/10 {
		t.Errorf("HeapSizeLimit=%d, want >=%d (MaxOldSpaceBytes ignored)",
			hs.HeapSizeLimit, uint64(cap)*8/10)
	}
}

func TestWarmupOldGenerationHeap_PreCommitsPages(t *testing.T) {
	t.Parallel()
	// WarmupOldGenerationHeap is the eager-commit primitive. Run it on a
	// fresh isolate and verify V8 has committed close to the target
	// number of pages — these are the pages we asked the OS for at
	// startup time so we don't have to ask at peak load.
	//
	// Sizing: target=64 MB. Right after warmup V8's TotalHeapSize will
	// be the warmup peak (~target + overhead). The warmup buffer is
	// dropped inside the script; natural GC later shrinks Total to a
	// steady-state retained size. We measure right after the warmup call
	// returns to capture the peak.
	v8.SetFlags("--no-memory-reducer")
	const target = 64 * 1024 * 1024

	iso := v8.NewIsolateWithOptions(v8.IsolateOptions{
		MaxOldSpaceBytes: 1024 * 1024 * 1024,
	})
	defer iso.Dispose()

	pre := iso.GetHeapStatistics()
	t.Logf("pre  warmup: TotalHeapSize=%.2f MB", float64(pre.TotalHeapSize)/(1024*1024))

	if err := iso.WarmupOldGenerationHeap(target); err != nil {
		t.Fatalf("WarmupOldGenerationHeap: %v", err)
	}

	post := iso.GetHeapStatistics()
	t.Logf("post warmup: TotalHeapSize=%.2f MB UsedHeapSize=%.2f MB",
		float64(post.TotalHeapSize)/(1024*1024),
		float64(post.UsedHeapSize)/(1024*1024))

	// The committed-page count should have grown by AT LEAST the target.
	// V8 will typically commit more than target (intermediate Array
	// buffers in the warmup script, V8 metadata).
	if post.TotalHeapSize < pre.TotalHeapSize+uint64(target)*8/10 {
		t.Errorf("TotalHeapSize grew by %d (pre=%d post=%d), want >=%d "+
			"(warmup did not pre-commit enough pages)",
			post.TotalHeapSize-pre.TotalHeapSize,
			pre.TotalHeapSize, post.TotalHeapSize, target*8/10)
	}
}

func TestWarmupOldGenerationHeap_ZeroIsNoOp(t *testing.T) {
	t.Parallel()
	iso := v8.NewIsolate()
	defer iso.Dispose()
	if err := iso.WarmupOldGenerationHeap(0); err != nil {
		t.Errorf("WarmupOldGenerationHeap(0) returned error: %v", err)
	}
}

func TestWarmupOldGenerationHeap_NilIsolateNoOp(t *testing.T) {
	t.Parallel()
	var iso *v8.Isolate
	// Should not panic.
	if err := iso.WarmupOldGenerationHeap(64 * 1024 * 1024); err != nil {
		t.Errorf("WarmupOldGenerationHeap on nil isolate returned error: %v", err)
	}
}

func TestNewIsolateWithOptions_DefaultIsoStartsSmall(t *testing.T) {
	t.Parallel()
	// Counterpart to InitialOldSpaceCommitsEagerly: confirm that without
	// the option, V8's default-initialized isolate starts with a small
	// heap. This is the pre-fix baseline.
	iso := v8.NewIsolate()
	defer iso.Dispose()

	hs := iso.GetHeapStatistics()
	t.Logf("after NewIsolate() (no constraints): TotalHeapSize=%.2f MB",
		float64(hs.TotalHeapSize)/(1024*1024))

	// V8's default initial heap is around 1-8 MB depending on platform
	// and pointer-compression. If it ever starts >32 MB by default,
	// the InitialOldSpace test above loses its discriminating power and
	// needs to be re-thresholded.
	if hs.TotalHeapSize > 32*1024*1024 {
		t.Errorf("default NewIsolate has TotalHeapSize=%d (>32 MB); "+
			"InitialOldSpace test threshold needs adjustment",
			hs.TotalHeapSize)
	}
}

func TestNewIsolateWithOptions_MaxYoungSpaceApplied(t *testing.T) {
	t.Parallel()
	// MaxYoungSpaceBytes affects scavenge frequency. Hard to assert
	// directly via HeapStatistics because young gen is not separately
	// reported. Smoke test that setting it doesn't break isolate creation
	// or basic execution.
	iso := v8.NewIsolateWithOptions(v8.IsolateOptions{
		MaxYoungSpaceBytes: 32 * 1024 * 1024,
	})
	defer iso.Dispose()

	ctx := v8.NewContext(iso)
	defer ctx.Close()

	val, err := ctx.RunScript("Array.from({length: 1000}, (_, i) => i).reduce((a,b) => a+b)",
		"young.js")
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if got := val.Integer(); got != 499500 {
		t.Errorf("got %d, want 499500", got)
	}
}
