// Copyright 2019 Roger Chapman and the v8go contributors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package v8go

// RegisterCallback is exported for testing only.
func (i *Isolate) RegisterCallback(cb FunctionCallback) int {
	return i.registerCallback(cb)
}

// GetCallback is exported for testing only.
func (i *Isolate) GetCallback(ref int) FunctionCallback {
	return i.getCallback(ref)
}

// GetContext is exported for testing only.
var GetContext = getContext

// Ref is exported for testing only.
func (c *Context) Ref() int {
	return c.ref
}

// LiveExternalStringPins returns the current count of []byte slices pinned
// behind live external one-byte strings. Exported for testing the lifetime
// of GoExternalOneByteResource against V8 GC and Isolate::Dispose.
func LiveExternalStringPins() int {
	extPinLock.Lock()
	defer extPinLock.Unlock()
	return len(extPins)
}
