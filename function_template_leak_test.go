// Copyright 2026 the v8go contributors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package v8go_test

import (
	"fmt"
	"testing"

	v8 "github.com/robomotionio/v8go"
)

// TestFunctionTemplateReturnValueNotLeaked guards the callback-return-value
// leak fix in FunctionTemplateCallback.
//
// A Go callback builds its return value with NewValue(iso, ...), which tracks
// the wrapper in the *isolate's internal* context (iso->GetData(0)->vals) —
// freed only at IsolateDispose, never at the per-call Context::Close, and the
// Go *Value has no finalizer. Before the fix every callback invocation leaked
// one strongly-reachable m_value there for the isolate's whole lifetime. A JS
// Proxy that calls a host function on every property access (the deskbot
// msgbridge) turned that into unbounded, un-GC-able old-space growth and a
// "last resort GC frees 0 bytes" CALL_AND_RETRY_LAST OOM on long-running flows.
//
// After the fix the return value is released once V8 copies its Local into the
// return slot, so the internal-context value count stays flat no matter how
// many times the callback is invoked.
func TestFunctionTemplateReturnValueNotLeaked(t *testing.T) {
	t.Parallel()

	iso := v8.NewIsolate()
	defer iso.Dispose()

	global := v8.NewObjectTemplate(iso)
	// Mirrors msgbridge.__msgGet: return a freshly-created value from Go data.
	host := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v, _ := v8.NewValue(iso, `{"kind":"string","value":"x"}`)
		return v
	})
	global.Set("host", host)

	ctx := v8.NewContext(iso, global)
	defer ctx.Close()

	// Warm up so one-time setup values (the function object, etc.) are already
	// accounted for before we measure the per-call delta.
	if _, err := ctx.RunScript("host();", "warmup.js"); err != nil {
		t.Fatal(err)
	}
	base := iso.InternalContextValueCount()

	const N = 2000
	if _, err := ctx.RunScript(fmt.Sprintf("for (let i=0;i<%d;i++){host();}", N), "loop.js"); err != nil {
		t.Fatal(err)
	}

	delta := iso.InternalContextValueCount() - base
	// Allow a tiny constant slack for any incidental internal-context value;
	// the leak signature is delta ≈ N.
	if delta > 8 {
		t.Fatalf("isolate internal-context tracked values grew by %d over %d host() calls "+
			"(want ~0): FunctionTemplate return values are leaking into the isolate "+
			"internal context", delta, N)
	}
}

func TestFunctionTemplateReusableReturnValues(t *testing.T) {
	t.Parallel()

	iso := v8.NewIsolate()
	defer iso.Dispose()

	cached, err := v8.NewValue(iso, "cached")
	if err != nil {
		t.Fatal(err)
	}
	global := v8.NewObjectTemplate(iso)
	global.Set("cached", v8.NewFunctionTemplate(iso, func(*v8.FunctionCallbackInfo) *v8.Value {
		return cached
	}))
	global.Set("nullValue", v8.NewFunctionTemplate(iso, func(*v8.FunctionCallbackInfo) *v8.Value {
		return v8.Null(iso)
	}))
	global.Set("undefinedValue", v8.NewFunctionTemplate(iso, func(*v8.FunctionCallbackInfo) *v8.Value {
		return v8.Undefined(iso)
	}))

	ctx := v8.NewContext(iso, global)
	defer ctx.Close()
	result, err := ctx.RunScript(`
		for (let i = 0; i < 2000; i++) {
			if (cached() !== "cached") throw new Error("cached value changed");
			if (nullValue() !== null) throw new Error("null value changed");
			if (undefinedValue() !== undefined) throw new Error("undefined value changed");
		}
		42
	`, "reusable-return-values.js")
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Integer(); got != 42 {
		t.Fatalf("result = %d, want 42", got)
	}
}
