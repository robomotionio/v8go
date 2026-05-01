// Copyright 2026 the v8go contributors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Tests for IsOneByteSafe and NewExternalOneByteValue — the zero-copy
// String path. Targets the cgo triple-copy pathology described in
// docs/v8-windows-oom.md ("defect #2").
//
// The load-bearing assertion is the head-to-head comparison test
// (TestNewExternalOneByteValue_FixHeadToHeadVsInternal) that demonstrates
// the V8 heap footprint of an external string is essentially zero compared
// to the internal-string path. This is the test that "shows the fix".

package v8go_test

import (
	"bytes"
	"fmt"
	"runtime"
	"testing"

	v8 "github.com/robomotionio/v8go"
)

func TestIsOneByteSafe_AcceptsASCII(t *testing.T) {
	t.Parallel()
	cases := [][]byte{
		nil,
		[]byte(""),
		[]byte("hello"),
		[]byte(`{"name":"world","count":42}`),
		bytes.Repeat([]byte("x"), 1024),
		// 0x7F is the boundary; permitted.
		bytes.Repeat([]byte{0x7F}, 64),
		// Unaligned tails (force the SWAR loop's tail handling).
		bytes.Repeat([]byte("a"), 9),
		bytes.Repeat([]byte("a"), 15),
		bytes.Repeat([]byte("a"), 17),
	}
	for i, b := range cases {
		if !v8.IsOneByteSafe(b) {
			t.Errorf("case %d (%q): IsOneByteSafe = false, want true", i, b)
		}
	}
}

func TestIsOneByteSafe_RejectsHighBytes(t *testing.T) {
	t.Parallel()
	// 0x80 is the first byte beyond ASCII.
	if v8.IsOneByteSafe([]byte{0x80}) {
		t.Error("0x80 was accepted as one-byte-safe")
	}
	// Turkish "ş" is 0xC5 0x9F in UTF-8 — both bytes are >0x7F.
	if v8.IsOneByteSafe([]byte("naşıl")) {
		t.Error("Turkish UTF-8 was accepted as one-byte-safe")
	}
	// High byte buried after an aligned ASCII run.
	b := bytes.Repeat([]byte("x"), 32)
	b = append(b, 0xC3)
	b = append(b, bytes.Repeat([]byte("y"), 32)...)
	if v8.IsOneByteSafe(b) {
		t.Error("high byte after aligned ASCII run was missed")
	}
	// High byte in the unaligned tail (after the last 8-byte word).
	b = bytes.Repeat([]byte("z"), 9)
	b[8] = 0xFF
	if v8.IsOneByteSafe(b) {
		t.Error("high byte in unaligned tail was missed")
	}
}

func TestNewExternalOneByteValue_BasicRoundTrip(t *testing.T) {
	t.Parallel()
	iso := v8.NewIsolate()
	defer iso.Dispose()
	ctx := v8.NewContext(iso)
	defer ctx.Close()

	payload := []byte(`{"hello":"world","count":42,"items":["a","b","c"]}`)
	val, err := v8.NewExternalOneByteValue(iso, payload)
	if err != nil {
		t.Fatalf("NewExternalOneByteValue: %v", err)
	}
	if err := ctx.Global().Set("__inMsg__", val); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Read it back through JS and verify content matches byte-for-byte.
	got, err := ctx.RunScript(`globalThis.__inMsg__`, "echo.js")
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if got.String() != string(payload) {
		t.Errorf("round-trip mismatch:\n  got:  %q\n  want: %q",
			got.String(), payload)
	}

	// JSON.parse should work transparently against an external string.
	parsed, err := ctx.RunScript(
		`JSON.parse(globalThis.__inMsg__).count`, "parse.js")
	if err != nil {
		t.Fatalf("JSON.parse via external string: %v", err)
	}
	if parsed.Integer() != 42 {
		t.Errorf("JSON.parse(external).count = %d, want 42", parsed.Integer())
	}
}

func TestNewExternalOneByteValue_EmptyAndNil(t *testing.T) {
	t.Parallel()
	iso := v8.NewIsolate()
	defer iso.Dispose()
	ctx := v8.NewContext(iso)
	defer ctx.Close()

	for _, in := range [][]byte{nil, {}} {
		val, err := v8.NewExternalOneByteValue(iso, in)
		if err != nil {
			t.Fatalf("empty/nil input: %v", err)
		}
		if err := ctx.Global().Set("__e__", val); err != nil {
			t.Fatalf("Set: %v", err)
		}
		got, err := ctx.RunScript(`globalThis.__e__.length`, "len.js")
		if err != nil {
			t.Fatalf("RunScript: %v", err)
		}
		if got.Integer() != 0 {
			t.Errorf("empty input produced length %d, want 0", got.Integer())
		}
	}
}

func TestNewExternalOneByteValue_RejectsNilIsolate(t *testing.T) {
	t.Parallel()
	_, err := v8.NewExternalOneByteValue(nil, []byte("hi"))
	if err == nil {
		t.Fatal("nil isolate accepted")
	}
}

// TestNewExternalOneByteValue_FixHeadToHeadVsInternal is the test that
// demonstrates the fix: at the same input size, the external-string path
// produces a tiny V8 heap footprint while the internal-string path
// produces a footprint roughly equal to the input.
//
// This is the unit-test analogue of the Windows-only crash test in
// deskbot/nodes/flow/v8_large_inmsg_windows_test.go. On Linux we can't
// reproduce the OS-level commit denial but we CAN measure the heap
// footprint differential — and that differential is what matters on
// Windows because every byte saved is a byte the OS doesn't have to
// commit.
func TestNewExternalOneByteValue_FixHeadToHeadVsInternal(t *testing.T) {
	t.Parallel()
	const sizeBytes = 16 * 1024 * 1024 // 16 MB; matches the customer's typical inMsg.

	payload := bytes.Repeat([]byte{'A'}, sizeBytes)
	if !v8.IsOneByteSafe(payload) {
		t.Fatal("test payload must be one-byte-safe")
	}

	measure := func(t *testing.T, label string, set func(iso *v8.Isolate, ctx *v8.Context) error) uint64 {
		t.Helper()
		iso := v8.NewIsolate()
		defer iso.Dispose()
		ctx := v8.NewContext(iso)
		defer ctx.Close()

		// Warm the isolate with a tiny allocation so V8 has its baseline
		// committed before we measure.
		if _, err := ctx.RunScript(`(function(){return 1})()`, "warm.js"); err != nil {
			t.Fatalf("%s: warm: %v", label, err)
		}
		baseline := iso.GetHeapStatistics().UsedHeapSize

		if err := set(iso, ctx); err != nil {
			t.Fatalf("%s: set: %v", label, err)
		}

		// Force any pending micro/minor work, then take the after measurement.
		runtime.GC()
		after := iso.GetHeapStatistics().UsedHeapSize
		delta := after - baseline
		t.Logf("%s: UsedHeapSize before=%.2f MB after=%.2f MB delta=%.2f MB",
			label,
			float64(baseline)/(1024*1024),
			float64(after)/(1024*1024),
			float64(delta)/(1024*1024))
		return delta
	}

	internalDelta := measure(t, "internal-string", func(iso *v8.Isolate, ctx *v8.Context) error {
		val, err := v8.NewValue(iso, string(payload))
		if err != nil {
			return err
		}
		return ctx.Global().Set("__inMsg__", val)
	})

	externalDelta := measure(t, "external-one-byte-string", func(iso *v8.Isolate, ctx *v8.Context) error {
		val, err := v8.NewExternalOneByteValue(iso, payload)
		if err != nil {
			return err
		}
		return ctx.Global().Set("__inMsg__", val)
	})

	// Internal: V8 allocates a SeqOneByteString of ~size bytes plus
	// metadata. Expect delta to be at least ~50% of input size.
	if internalDelta < uint64(sizeBytes/2) {
		t.Errorf("internal-string delta=%d is unexpectedly small "+
			"(< %d); test threshold may be wrong",
			internalDelta, sizeBytes/2)
	}

	// External: V8 only allocates the ~32-byte wrapper. Allow up to
	// 1 MB of slack for V8-internal bookkeeping that may fluctuate
	// between versions, but it should be ORDERS OF MAGNITUDE smaller
	// than internalDelta. If this assertion fails, the external string
	// path is silently copying data into the V8 heap — defect #2 has
	// regressed.
	const externalCeiling = 1 * 1024 * 1024
	if externalDelta > externalCeiling {
		t.Errorf("external-string delta=%d exceeds %d — V8 is copying "+
			"the data into its heap; the zero-copy path is broken",
			externalDelta, externalCeiling)
	}

	ratio := float64(internalDelta) / float64(externalDelta+1) // +1 to avoid div-by-zero
	t.Logf("FIX SHOWS: internal/external ratio = %.1fx (higher is better)", ratio)
	if ratio < 4.0 {
		t.Errorf("ratio=%.1fx, want >= 4x; the external-string fix "+
			"is not delivering the expected memory savings", ratio)
	}
}

// TestNewExternalOneByteValue_PinReleasedOnIsolateDispose verifies that
// disposing the isolate runs the C++ destructors of all live external
// string resources, dropping their pins on the Go side. Without this,
// long-running processes would leak [N MB] per disposed isolate that had
// external strings.
func TestNewExternalOneByteValue_PinReleasedOnIsolateDispose(t *testing.T) {
	startPins := v8.LiveExternalStringPins()

	for i := 0; i < 5; i++ {
		iso := v8.NewIsolate()
		ctx := v8.NewContext(iso)

		payload := []byte(fmt.Sprintf("pin-test-iter-%d", i))
		val, err := v8.NewExternalOneByteValue(iso, payload)
		if err != nil {
			t.Fatalf("iter %d: NewExternalOneByteValue: %v", i, err)
		}
		if err := ctx.Global().Set("__p__", val); err != nil {
			t.Fatalf("iter %d: Set: %v", i, err)
		}

		// Live pin must be present while isolate is live.
		if got := v8.LiveExternalStringPins(); got <= startPins {
			t.Errorf("iter %d: pin not registered (count=%d, baseline=%d)",
				i, got, startPins)
		}

		ctx.Close()
		iso.Dispose()
	}

	endPins := v8.LiveExternalStringPins()
	if endPins != startPins {
		t.Errorf("pin leak: started with %d, ended with %d (Δ=%d)",
			startPins, endPins, endPins-startPins)
	}
}

// TestNewExternalOneByteValue_PinSurvivesGCWhilePinned exercises the
// failure mode the pin map exists to prevent: V8 holds a pointer into the
// Go []byte; if Go GC ran and freed the slice, V8 would read garbage.
// The pin keeps a Go reference alive in extPins, so even after the
// caller drops their reference, the slice remains valid until V8 disposes
// the wrapper.
func TestNewExternalOneByteValue_PinSurvivesGCWhilePinned(t *testing.T) {
	iso := v8.NewIsolate()
	defer iso.Dispose()
	ctx := v8.NewContext(iso)
	defer ctx.Close()

	// Build the payload and immediately drop our local reference. The
	// Go GC may now consider the slice unreachable from the test goroutine
	// — but the pin keeps it alive.
	const sentinel = "pin-survives-gc"
	{
		buf := []byte(sentinel)
		val, err := v8.NewExternalOneByteValue(iso, buf)
		if err != nil {
			t.Fatalf("NewExternalOneByteValue: %v", err)
		}
		if err := ctx.Global().Set("__pinned__", val); err != nil {
			t.Fatalf("Set: %v", err)
		}
		buf = nil // explicit drop; pin must hold the slice
		_ = buf
	}

	for i := 0; i < 5; i++ {
		runtime.GC()
	}
	// Allocate noise to push other goroutines around the heap.
	noise := make([][]byte, 0, 1024)
	for i := 0; i < 1024; i++ {
		noise = append(noise, make([]byte, 4096))
	}
	_ = noise

	got, err := ctx.RunScript(`globalThis.__pinned__`, "read.js")
	if err != nil {
		t.Fatalf("RunScript after GC pressure: %v", err)
	}
	if got.String() != sentinel {
		t.Errorf("external string content corrupted after GC: got %q want %q",
			got.String(), sentinel)
	}
}
