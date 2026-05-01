// Copyright 2019 Roger Chapman and the v8go contributors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package v8go

// #include <stdlib.h>
// #include "v8go.h"
import "C"

import (
	"errors"
	"sync"
	"sync/atomic"
	"unsafe"
)

var v8once sync.Once

// Isolate is a JavaScript VM instance with its own heap and
// garbage collector. Most applications will create one isolate
// with many V8 contexts for execution.
type Isolate struct {
	ptr C.IsolatePtr

	cbMutex sync.RWMutex
	cbSeq   int
	cbs     map[int]FunctionCallback

	null      *Value
	undefined *Value
}

// HeapStatistics represents V8 isolate heap statistics
type HeapStatistics struct {
	TotalHeapSize            uint64
	TotalHeapSizeExecutable  uint64
	TotalPhysicalSize        uint64
	TotalAvailableSize       uint64
	UsedHeapSize             uint64
	HeapSizeLimit            uint64
	MallocedMemory           uint64
	ExternalMemory           uint64
	PeakMallocedMemory       uint64
	NumberOfNativeContexts   uint64
	NumberOfDetachedContexts uint64
}

// NewIsolate creates a new V8 isolate. Only one thread may access
// a given isolate at a time, but different threads may access
// different isolates simultaneously.
// When an isolate is no longer used its resources should be freed
// by calling iso.Dispose().
// An *Isolate can be used as a v8go.ContextOption to create a new
// Context, rather than creating a new default Isolate.
func NewIsolate() *Isolate {
	initializeIfNecessary()
	iso := &Isolate{
		ptr: C.NewIsolate(),
		cbs: make(map[int]FunctionCallback),
	}
	iso.null = newValueNull(iso)
	iso.undefined = newValueUndefined(iso)
	return iso
}

// IsolateOptions surfaces v8::Isolate::CreateParams::constraints. Any zero
// field is left at the V8 default.
//
// IMPORTANT semantic note about InitialOldSpaceBytes: this is V8's
// "initial GC threshold" hint, NOT an eager-commit directive. V8 will not
// trigger a full GC until the heap reaches this size, but the heap pages
// are still committed on demand as JS allocates. To force eager commit
// (so the OS hands out pages while the system is idle, avoiding peak-time
// VirtualAlloc denial on Windows), use Isolate.WarmupOldGenerationHeap
// after creating the isolate.
//
// MaxOldSpaceBytes / MaxYoungSpaceBytes do bound the heap as you'd expect.
// See docs/v8-windows-oom.md in the deskbot repo for the full analysis.
type IsolateOptions struct {
	InitialOldSpaceBytes uint64
	MaxOldSpaceBytes     uint64
	MaxYoungSpaceBytes   uint64
}

// NewIsolateWithOptions creates an Isolate with explicit ResourceConstraints.
// Behaves identically to NewIsolate when opts is the zero value.
func NewIsolateWithOptions(opts IsolateOptions) *Isolate {
	initializeIfNecessary()
	var copts C.IsolateOptions
	copts.initial_old_space_bytes = C.size_t(opts.InitialOldSpaceBytes)
	copts.max_old_space_bytes = C.size_t(opts.MaxOldSpaceBytes)
	copts.max_young_space_bytes = C.size_t(opts.MaxYoungSpaceBytes)
	iso := &Isolate{
		ptr: C.NewIsolateWithOptions(copts),
		cbs: make(map[int]FunctionCallback),
	}
	iso.null = newValueNull(iso)
	iso.undefined = newValueUndefined(iso)
	return iso
}

// nearHeapLimitGrowthBytes is consulted by the default goNearHeapLimitCallback
// to decide how aggressively to bump the heap limit when V8 is about to OOM.
// Embedders that want different behavior can swap the callback at the v8go
// level (see SetNearHeapLimitGrowthBytes) without touching V8 itself.
var nearHeapLimitGrowthBytes uint64 = 256 * 1024 * 1024

// SetNearHeapLimitGrowthBytes configures the per-event growth applied by
// the default near-heap-limit callback. Pass 0 to disable growth (the
// callback returns the current limit unchanged, letting V8 OOM as usual).
func SetNearHeapLimitGrowthBytes(n uint64) {
	atomic.StoreUint64(&nearHeapLimitGrowthBytes, n)
}

// AddNearHeapLimitCallback installs v8go's default near-heap-limit callback
// on the isolate. The callback fires when V8 is about to hit its configured
// max-old-generation cap; it returns a larger limit so V8 can complete the
// current allocation, then AutomaticallyRestoreInitialHeapLimit (if armed)
// brings the limit back down once usage drops.
//
// This catches the "Reached heap limit" V8 fatal subreason. It does NOT
// catch "CALL_AND_RETRY_LAST" — that fires when the OS denies a page
// commit, before V8 reaches its own cap, so the callback is never invoked.
// To prevent OS-denial OOMs, set InitialOldSpaceBytes via NewIsolateWithOptions
// so V8 commits its working memory eagerly when the OS has headroom.
func (i *Isolate) AddNearHeapLimitCallback() {
	if i.ptr == nil {
		return
	}
	C.IsolateAddNearHeapLimitCallback(i.ptr)
}

// RemoveNearHeapLimitCallback removes the v8go default callback. heapLimit
// is the value the limit should be reset to (typically the previously
// configured max-old-generation cap).
func (i *Isolate) RemoveNearHeapLimitCallback(heapLimit uint64) {
	if i.ptr == nil {
		return
	}
	C.IsolateRemoveNearHeapLimitCallback(i.ptr, C.size_t(heapLimit))
}

// AutomaticallyRestoreInitialHeapLimit asks V8 to reset the heap limit to
// its initial value (the configured max-old-generation cap from
// CreateParams::constraints) when the heap usage drops below the given
// threshold fraction. Pair with AddNearHeapLimitCallback to recover from
// transient spikes without leaving the limit permanently elevated.
func (i *Isolate) AutomaticallyRestoreInitialHeapLimit(threshold float64) {
	if i.ptr == nil {
		return
	}
	C.IsolateAutomaticallyRestoreInitialHeapLimit(i.ptr, C.double(threshold))
}

// WarmupOldGenerationHeap forces V8 to commit at least targetBytes of
// old-generation heap pages and to retain them across the call. After
// this returns the isolate's TotalHeapSize will be near targetBytes, and
// subsequent allocations up to that size do not need to ask the OS for
// new pages.
//
// This is the eager-commit primitive: it closes the cold-start
// OS-page-denial window described in docs/v8-windows-oom.md by doing
// the OS-asking work at flow startup, when the host is most likely to
// have memory headroom. Pair with V8 flag --no-memory-reducer (set via
// SetFlags before NewIsolate) so V8 keeps the warmed-up pages instead
// of returning them to the OS during idle GCs.
//
// Cost during the call: peak working set rises by ~targetBytes; V8
// metadata adds ~5-10%. The call returns once the warmup buffer has
// been collected. If targetBytes exceeds available memory the call
// returns an error rather than aborting the process.
//
// No-op if targetBytes is 0 or the isolate is disposed.
func (i *Isolate) WarmupOldGenerationHeap(targetBytes uint64) error {
	if i == nil || i.ptr == nil || targetBytes == 0 {
		return nil
	}
	rc := C.IsolateWarmupOldGenerationHeap(i.ptr, C.size_t(targetBytes))
	if rc != 0 {
		// rc encodes which V8 phase failed (1 source build, 2 compile,
		// 3 run); any non-zero is fatal-for-warmup but recoverable.
		return errIsolateWarmupFailed
	}
	return nil
}

var errIsolateWarmupFailed = errors.New(
	"v8go: WarmupOldGenerationHeap failed (likely OOM); " +
		"system memory may be too tight for the requested target size")

// goNearHeapLimitCallback is V8's near-heap-limit hook, exported for the
// C++ trampoline in v8go.cc. Returns the new heap limit V8 should adopt.
//
//export goNearHeapLimitCallback
func goNearHeapLimitCallback(iso C.IsolatePtr,
	current, initial C.size_t) C.size_t {
	grow := atomic.LoadUint64(&nearHeapLimitGrowthBytes)
	if grow == 0 {
		return current
	}
	return current + C.size_t(grow)
}

// TerminateExecution terminates forcefully the current thread
// of JavaScript execution in the given isolate.
func (i *Isolate) TerminateExecution() {
	C.IsolateTerminateExecution(i.ptr)
}

// IsExecutionTerminating returns whether V8 is currently terminating
// Javascript execution. If true, there are still JavaScript frames
// on the stack and the termination exception is still active.
func (i *Isolate) IsExecutionTerminating() bool {
	return C.IsolateIsExecutionTerminating(i.ptr) == 1
}

type CompileOptions struct {
	CachedData *CompilerCachedData

	Mode CompileMode
}

// CompileUnboundScript will create an UnboundScript (i.e. context-indepdent)
// using the provided source JavaScript, origin (a.k.a. filename), and options.
// If options contain a non-null CachedData, compilation of the script will use
// that code cache.
// error will be of type `JSError` if not nil.
func (i *Isolate) CompileUnboundScript(source, origin string, opts CompileOptions) (*UnboundScript, error) {
	cSource := C.CString(source)
	cOrigin := C.CString(origin)
	defer C.free(unsafe.Pointer(cSource))
	defer C.free(unsafe.Pointer(cOrigin))

	var cOptions C.CompileOptions
	if opts.CachedData != nil {
		if opts.Mode != 0 {
			panic("On CompileOptions, Mode and CachedData can't both be set")
		}
		cOptions.compileOption = C.ScriptCompilerConsumeCodeCache
		cOptions.cachedData = C.ScriptCompilerCachedData{
			data:   (*C.uchar)(unsafe.Pointer(&opts.CachedData.Bytes[0])),
			length: C.int(len(opts.CachedData.Bytes)),
		}
	} else {
		cOptions.compileOption = C.int(opts.Mode)
	}

	rtn := C.IsolateCompileUnboundScript(i.ptr, cSource, cOrigin, cOptions)
	if rtn.ptr == nil {
		return nil, newJSError(rtn.error)
	}
	if opts.CachedData != nil {
		opts.CachedData.Rejected = int(rtn.cachedDataRejected) == 1
	}
	return &UnboundScript{
		ptr: rtn.ptr,
		iso: i,
	}, nil
}

// GetHeapStatistics returns heap statistics for an isolate.
func (i *Isolate) GetHeapStatistics() HeapStatistics {
	hs := C.IsolationGetHeapStatistics(i.ptr)

	return HeapStatistics{
		TotalHeapSize:            uint64(hs.total_heap_size),
		TotalHeapSizeExecutable:  uint64(hs.total_heap_size_executable),
		TotalPhysicalSize:        uint64(hs.total_physical_size),
		TotalAvailableSize:       uint64(hs.total_available_size),
		UsedHeapSize:             uint64(hs.used_heap_size),
		HeapSizeLimit:            uint64(hs.heap_size_limit),
		MallocedMemory:           uint64(hs.malloced_memory),
		ExternalMemory:           uint64(hs.external_memory),
		PeakMallocedMemory:       uint64(hs.peak_malloced_memory),
		NumberOfNativeContexts:   uint64(hs.number_of_native_contexts),
		NumberOfDetachedContexts: uint64(hs.number_of_detached_contexts),
	}
}

// Dispose will dispose the Isolate VM; subsequent calls will panic.
func (i *Isolate) Dispose() {
	if i.ptr == nil {
		return
	}
	C.IsolateDispose(i.ptr)
	i.ptr = nil
}

// ThrowException schedules an exception to be thrown when returning to
// JavaScript. When an exception has been scheduled it is illegal to invoke
// any JavaScript operation; the caller must return immediately and only after
// the exception has been handled does it become legal to invoke JavaScript operations.
func (i *Isolate) ThrowException(value *Value) *Value {
	if i.ptr == nil {
		panic("Isolate has been disposed")
	}
	return &Value{
		ptr: C.IsolateThrowException(i.ptr, value.ptr),
	}
}

// Deprecated: use `iso.Dispose()`.
func (i *Isolate) Close() {
	i.Dispose()
}

func (i *Isolate) apply(opts *contextOptions) {
	opts.iso = i
}

func (i *Isolate) registerCallback(cb FunctionCallback) int {
	i.cbMutex.Lock()
	i.cbSeq++
	ref := i.cbSeq
	i.cbs[ref] = cb
	i.cbMutex.Unlock()
	return ref
}

func (i *Isolate) getCallback(ref int) FunctionCallback {
	i.cbMutex.RLock()
	defer i.cbMutex.RUnlock()
	return i.cbs[ref]
}
