// Copyright 2019 Roger Chapman and the v8go contributors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package v8go

// #include <stdlib.h>
// #include "v8go.h"
import "C"
import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Value represents all Javascript values and objects
type Value struct {
	ptr C.ValuePtr
	ctx *Context
}

// Valuer is an interface that reperesents anything that extends from a Value
// eg. Object, Array, Date etc
type Valuer interface {
	value() *Value
}

func (v *Value) value() *Value {
	return v
}

func newValueNull(iso *Isolate) *Value {
	return &Value{
		ptr: C.NewValueNull(iso.ptr),
	}
}

func newValueUndefined(iso *Isolate) *Value {
	return &Value{
		ptr: C.NewValueUndefined(iso.ptr),
	}
}

// Undefined returns the `undefined` JS value
func Undefined(iso *Isolate) *Value {
	return iso.undefined
}

// Null returns the `null` JS value
func Null(iso *Isolate) *Value {
	return iso.null
}

// NewValue will create a primitive value. Supported values types to create are:
//
//	string -> V8::String
//	int32 -> V8::Integer
//	uint32 -> V8::Integer
//	int64 -> V8::BigInt
//	uint64 -> V8::BigInt
//	bool -> V8::Boolean
//	*big.Int -> V8::BigInt
func NewValue(iso *Isolate, val interface{}) (*Value, error) {
	if iso == nil {
		return nil, errors.New("v8go: failed to create new Value: Isolate cannot be <nil>")
	}

	var rtnVal *Value

	switch v := val.(type) {
	case string:
		cstr := C.CString(v)
		defer C.free(unsafe.Pointer(cstr))
		rtn := C.NewValueString(iso.ptr, cstr, C.int(len(v)))
		return valueResult(nil, rtn)
	case int32:
		rtnVal = &Value{
			ptr: C.NewValueInteger(iso.ptr, C.int(v)),
		}
	case uint32:
		rtnVal = &Value{
			ptr: C.NewValueIntegerFromUnsigned(iso.ptr, C.uint(v)),
		}
	case int64:
		rtnVal = &Value{
			ptr: C.NewValueBigInt(iso.ptr, C.int64_t(v)),
		}
	case uint64:
		rtnVal = &Value{
			ptr: C.NewValueBigIntFromUnsigned(iso.ptr, C.uint64_t(v)),
		}
	case bool:
		var b int
		if v {
			b = 1
		}
		rtnVal = &Value{
			ptr: C.NewValueBoolean(iso.ptr, C.int(b)),
		}
	case float64:
		rtnVal = &Value{
			ptr: C.NewValueNumber(iso.ptr, C.double(v)),
		}
	case *big.Int:
		if v.IsInt64() {
			rtnVal = &Value{
				ptr: C.NewValueBigInt(iso.ptr, C.int64_t(v.Int64())),
			}
			break
		}

		if v.IsUint64() {
			rtnVal = &Value{
				ptr: C.NewValueBigIntFromUnsigned(iso.ptr, C.uint64_t(v.Uint64())),
			}
			break
		}

		var sign, count int
		if v.Sign() == -1 {
			sign = 1
		}
		bits := v.Bits()
		count = len(bits)

		words := make([]C.uint64_t, count, count)
		for idx, word := range bits {
			words[idx] = C.uint64_t(word)
		}

		rtn := C.NewValueBigIntFromWords(iso.ptr, C.int(sign), C.int(count), &words[0])
		return valueResult(nil, rtn)
	default:
		return nil, fmt.Errorf("v8go: unsupported value type `%T`", v)
	}

	return rtnVal, nil
}

// Format implements the fmt.Formatter interface to provide a custom formatter
// primarily to output the detail string (for debugging) with `%+v` verb.
func (v *Value) Format(s fmt.State, verb rune) {
	switch verb {
	case 'v':
		if s.Flag('+') {
			io.WriteString(s, v.DetailString())
			return
		}
		fallthrough
	case 's':
		io.WriteString(s, v.String())
	case 'q':
		fmt.Fprintf(s, "%q", v.String())
	}
}

// ArrayIndex attempts to converts a string to an array index. Returns ok false if conversion fails.
func (v *Value) ArrayIndex() (idx uint32, ok bool) {
	arrayIdx := C.ValueToArrayIndex(v.ptr)
	defer C.free(unsafe.Pointer(arrayIdx))
	if arrayIdx == nil {
		return 0, false
	}
	return uint32(*arrayIdx), true
}

// BigInt perform the equivalent of `BigInt(value)` in JS.
func (v *Value) BigInt() *big.Int {
	if v == nil {
		return nil
	}
	bint := C.ValueToBigInt(v.ptr)
	defer C.free(unsafe.Pointer(bint.word_array))
	if bint.word_array == nil {
		return nil
	}
	words := (*[1 << 30]big.Word)(unsafe.Pointer(bint.word_array))[:bint.word_count:bint.word_count]

	abs := make([]big.Word, len(words))
	copy(abs, words)

	b := &big.Int{}
	b.SetBits(abs)

	if bint.sign_bit == 1 {
		b.Neg(b)
	}

	return b
}

// Boolean perform the equivalent of `Boolean(value)` in JS. This can never fail.
func (v *Value) Boolean() bool {
	return C.ValueToBoolean(v.ptr) != 0
}

// DetailString provide a string representation of this value usable for debugging.
func (v *Value) DetailString() string {
	rtn := C.ValueToDetailString(v.ptr)
	if rtn.data == nil {
		err := newJSError(rtn.error)
		panic(err) // TODO: Return a fallback value
	}
	defer C.free(unsafe.Pointer(rtn.data))
	return C.GoStringN(rtn.data, rtn.length)
}

// Int32 perform the equivalent of `Number(value)` in JS and convert the result to a
// signed 32-bit integer by performing the steps in https://tc39.es/ecma262/#sec-toint32.
func (v *Value) Int32() int32 {
	return int32(C.ValueToInt32(v.ptr))
}

// Integer perform the equivalent of `Number(value)` in JS and convert the result to an integer.
// Negative values are rounded up, positive values are rounded down. NaN is converted to 0.
// Infinite values yield undefined results.
func (v *Value) Integer() int64 {
	return int64(C.ValueToInteger(v.ptr))
}

// Number perform the equivalent of `Number(value)` in JS.
func (v *Value) Number() float64 {
	return float64(C.ValueToNumber(v.ptr))
}

// Object perform the equivalent of Object(value) in JS.
// To just cast this value as an Object use AsObject() instead.
func (v *Value) Object() *Object {
	rtn := C.ValueToObject(v.ptr)
	obj, err := objectResult(v.ctx, rtn)
	if err != nil {
		panic(err) // TODO: Return error
	}
	return obj
}

// String perform the equivalent of `String(value)` in JS. Primitive values
// are returned as-is, objects will return `[object Object]` and functions will
// print their definition.
func (v *Value) String() string {
	s := C.ValueToString(v.ptr)
	defer C.free(unsafe.Pointer(s.data))
	return C.GoStringN(s.data, C.int(s.length))
}

// Uint32 perform the equivalent of `Number(value)` in JS and convert the result to an
// unsigned 32-bit integer by performing the steps in https://tc39.es/ecma262/#sec-touint32.
func (v *Value) Uint32() uint32 {
	return uint32(C.ValueToUint32(v.ptr))
}

// SameValue returns true if the other value is the same value.
// This is equivalent to `Object.is(v, other)` in JS.
func (v *Value) SameValue(other *Value) bool {
	return C.ValueSameValue(v.ptr, other.ptr) != 0
}

// IsUndefined returns true if this value is the undefined value. See ECMA-262 4.3.10.
func (v *Value) IsUndefined() bool {
	return C.ValueIsUndefined(v.ptr) != 0
}

// IsNull returns true if this value is the null value. See ECMA-262 4.3.11.
func (v *Value) IsNull() bool {
	return C.ValueIsNull(v.ptr) != 0
}

// IsNullOrUndefined returns true if this value is either the null or the undefined value.
// See ECMA-262 4.3.11. and 4.3.12
// This is equivalent to `value == null` in JS.
func (v *Value) IsNullOrUndefined() bool {
	return C.ValueIsNullOrUndefined(v.ptr) != 0
}

// IsTrue returns true if this value is true.
// This is not the same as `BooleanValue()`. The latter performs a conversion to boolean,
// i.e. the result of `Boolean(value)` in JS, whereas this checks `value === true`.
func (v *Value) IsTrue() bool {
	return C.ValueIsTrue(v.ptr) != 0
}

// IsFalse returns true if this value is false.
// This is not the same as `!BooleanValue()`. The latter performs a conversion to boolean,
// i.e. the result of `!Boolean(value)` in JS, whereas this checks `value === false`.
func (v *Value) IsFalse() bool {
	return C.ValueIsFalse(v.ptr) != 0
}

// IsName returns true if this value is a symbol or a string.
// This is equivalent to `typeof value === 'string' || typeof value === 'symbol'` in JS.
func (v *Value) IsName() bool {
	return C.ValueIsName(v.ptr) != 0
}

// IsString returns true if this value is an instance of the String type. See ECMA-262 8.4.
// This is equivalent to `typeof value === 'string'` in JS.
func (v *Value) IsString() bool {
	return C.ValueIsString(v.ptr) != 0
}

// IsSymbol returns true if this value is a symbol.
// This is equivalent to `typeof value === 'symbol'` in JS.
func (v *Value) IsSymbol() bool {
	return C.ValueIsSymbol(v.ptr) != 0
}

// IsFunction returns true if this value is a function.
// This is equivalent to `typeof value === 'function'` in JS.
func (v *Value) IsFunction() bool {
	return C.ValueIsFunction(v.ptr) != 0
}

// IsObject returns true if this value is an object.
func (v *Value) IsObject() bool {
	return v.ctx != nil && C.ValueIsObject(v.ptr) != 0
}

// IsBigInt returns true if this value is a bigint.
// This is equivalent to `typeof value === 'bigint'` in JS.
func (v *Value) IsBigInt() bool {
	return C.ValueIsBigInt(v.ptr) != 0
}

// IsBoolean returns true if this value is boolean.
// This is equivalent to `typeof value === 'boolean'` in JS.
func (v *Value) IsBoolean() bool {
	return C.ValueIsBoolean(v.ptr) != 0
}

// IsNumber returns true if this value is a number.
// This is equivalent to `typeof value === 'number'` in JS.
func (v *Value) IsNumber() bool {
	return C.ValueIsNumber(v.ptr) != 0
}

// IsExternal returns true if this value is an `External` object.
func (v *Value) IsExternal() bool {
	// TODO(rogchap): requires test case
	return v.ctx != nil && C.ValueIsExternal(v.ptr) != 0
}

// IsInt32 returns true if this value is a 32-bit signed integer.
func (v *Value) IsInt32() bool {
	return C.ValueIsInt32(v.ptr) != 0
}

// IsUint32 returns true if this value is a 32-bit unsigned integer.
func (v *Value) IsUint32() bool {
	return C.ValueIsUint32(v.ptr) != 0
}

// IsDate returns true if this value is a `Date`.
func (v *Value) IsDate() bool {
	return C.ValueIsDate(v.ptr) != 0
}

// IsArgumentsObject returns true if this value is an Arguments object.
func (v *Value) IsArgumentsObject() bool {
	return C.ValueIsArgumentsObject(v.ptr) != 0
}

// IsBigIntObject returns true if this value is a BigInt object.
func (v *Value) IsBigIntObject() bool {
	return C.ValueIsBigIntObject(v.ptr) != 0
}

// IsNumberObject returns true if this value is a `Number` object.
func (v *Value) IsNumberObject() bool {
	return C.ValueIsNumberObject(v.ptr) != 0
}

// IsStringObject returns true if this value is a `String` object.
func (v *Value) IsStringObject() bool {
	return C.ValueIsStringObject(v.ptr) != 0
}

// IsSymbolObject returns true if this value is a `Symbol` object.
func (v *Value) IsSymbolObject() bool {
	return C.ValueIsSymbolObject(v.ptr) != 0
}

// IsNativeError returns true if this value is a NativeError.
func (v *Value) IsNativeError() bool {
	return C.ValueIsNativeError(v.ptr) != 0
}

// IsRegExp returns true if this value is a `RegExp`.
func (v *Value) IsRegExp() bool {
	return C.ValueIsRegExp(v.ptr) != 0
}

// IsAsyncFunc returns true if this value is an async function.
func (v *Value) IsAsyncFunction() bool {
	return C.ValueIsAsyncFunction(v.ptr) != 0
}

// Is IsGeneratorFunc returns true if this value is a Generator function.
func (v *Value) IsGeneratorFunction() bool {
	return C.ValueIsGeneratorFunction(v.ptr) != 0
}

// IsGeneratorObject returns true if this value is a Generator object (iterator).
func (v *Value) IsGeneratorObject() bool {
	return C.ValueIsGeneratorObject(v.ptr) != 0
}

// IsPromise returns true if this value is a `Promise`.
func (v *Value) IsPromise() bool {
	return C.ValueIsPromise(v.ptr) != 0
}

// IsMap returns true if this value is a `Map`.
func (v *Value) IsMap() bool {
	return C.ValueIsMap(v.ptr) != 0
}

// IsSet returns true if this value is a `Set`.
func (v *Value) IsSet() bool {
	return C.ValueIsSet(v.ptr) != 0
}

// IsMapIterator returns true if this value is a `Map` Iterator.
func (v *Value) IsMapIterator() bool {
	return C.ValueIsMapIterator(v.ptr) != 0
}

// IsSetIterator returns true if this value is a `Set` Iterator.
func (v *Value) IsSetIterator() bool {
	return C.ValueIsSetIterator(v.ptr) != 0
}

// IsWeakMap returns true if this value is a `WeakMap`.
func (v *Value) IsWeakMap() bool {
	return C.ValueIsWeakMap(v.ptr) != 0
}

// IsWeakSet returns true if this value is a `WeakSet`.
func (v *Value) IsWeakSet() bool {
	return C.ValueIsWeakSet(v.ptr) != 0
}

// IsArray returns true if this value is an array.
// Note that it will return false for a `Proxy` of an array.
func (v *Value) IsArray() bool {
	return C.ValueIsArray(v.ptr) != 0
}

// IsArrayBuffer returns true if this value is an `ArrayBuffer`.
func (v *Value) IsArrayBuffer() bool {
	return C.ValueIsArrayBuffer(v.ptr) != 0
}

// IsArrayBufferView returns true if this value is an `ArrayBufferView`.
func (v *Value) IsArrayBufferView() bool {
	return C.ValueIsArrayBufferView(v.ptr) != 0
}

// IsTypedArray returns true if this value is one of TypedArrays.
func (v *Value) IsTypedArray() bool {
	return C.ValueIsTypedArray(v.ptr) != 0
}

// IsUint8Array returns true if this value is an `Uint8Array`.
func (v *Value) IsUint8Array() bool {
	return C.ValueIsUint8Array(v.ptr) != 0
}

// IsUint8ClampedArray returns true if this value is an `Uint8ClampedArray`.
func (v *Value) IsUint8ClampedArray() bool {
	return C.ValueIsUint8ClampedArray(v.ptr) != 0
}

// IsInt8Array returns true if this value is an `Int8Array`.
func (v *Value) IsInt8Array() bool {
	return C.ValueIsInt8Array(v.ptr) != 0
}

// IsUint16Array returns true if this value is an `Uint16Array`.
func (v *Value) IsUint16Array() bool {
	return C.ValueIsUint16Array(v.ptr) != 0
}

// IsInt16Array returns true if this value is an `Int16Array`.
func (v *Value) IsInt16Array() bool {
	return C.ValueIsInt16Array(v.ptr) != 0
}

// IsUint32Array returns true if this value is an `Uint32Array`.
func (v *Value) IsUint32Array() bool {
	return C.ValueIsUint32Array(v.ptr) != 0
}

// IsInt32Array returns true if this value is an `Int32Array`.
func (v *Value) IsInt32Array() bool {
	return C.ValueIsInt32Array(v.ptr) != 0
}

// IsFloat32Array returns true if this value is a `Float32Array`.
func (v *Value) IsFloat32Array() bool {
	return C.ValueIsFloat32Array(v.ptr) != 0
}

// IsFloat64Array returns true if this value is a `Float64Array`.
func (v *Value) IsFloat64Array() bool {
	return C.ValueIsFloat64Array(v.ptr) != 0
}

// IsBigInt64Array returns true if this value is a `BigInt64Array`.
func (v *Value) IsBigInt64Array() bool {
	return C.ValueIsBigInt64Array(v.ptr) != 0
}

// IsBigUint64Array returns true if this value is a BigUint64Array`.
func (v *Value) IsBigUint64Array() bool {
	return C.ValueIsBigUint64Array(v.ptr) != 0
}

// IsDataView returns true if this value is a `DataView`.
func (v *Value) IsDataView() bool {
	return C.ValueIsDataView(v.ptr) != 0
}

// IsSharedArrayBuffer returns true if this value is a `SharedArrayBuffer`.
func (v *Value) IsSharedArrayBuffer() bool {
	return C.ValueIsSharedArrayBuffer(v.ptr) != 0
}

// IsProxy returns true if this value is a JavaScript `Proxy`.
func (v *Value) IsProxy() bool {
	return C.ValueIsProxy(v.ptr) != 0
}

// Release this value.  Using the value after calling this function will result in undefined behavior.
func (v *Value) Release() {
	C.ValueRelease(v.ptr)
}

// IsWasmModuleObject returns true if this value is a `WasmModuleObject`.
func (v *Value) IsWasmModuleObject() bool {
	// TODO(rogchap): requires test case
	return C.ValueIsWasmModuleObject(v.ptr) != 0
}

// IsModuleNamespaceObject returns true if the value is a `Module` Namespace `Object`.
func (v *Value) IsModuleNamespaceObject() bool {
	// TODO(rogchap): requires test case
	return C.ValueIsModuleNamespaceObject(v.ptr) != 0
}

// AsObject will cast the value to the Object type. If the value is not an Object
// then an error is returned. Use `value.Object()` to do the JS equivalent of `Object(value)`.
func (v *Value) AsObject() (*Object, error) {
	if !v.IsObject() {
		return nil, errors.New("v8go: value is not an Object")
	}

	return &Object{v}, nil
}

func (v *Value) AsPromise() (*Promise, error) {
	if !v.IsPromise() {
		return nil, errors.New("v8go: value is not a Promise")
	}
	return &Promise{&Object{v}}, nil
}

func (v *Value) AsFunction() (*Function, error) {
	if !v.IsFunction() {
		return nil, errors.New("v8go: value is not a Function")
	}
	return &Function{v}, nil
}

// MarshalJSON implements the json.Marshaler interface.
func (v *Value) MarshalJSON() ([]byte, error) {
	jsonStr, err := JSONStringify(nil, v)
	if err != nil {
		return nil, err
	}
	return []byte(jsonStr), nil
}

func (v *Value) SharedArrayBufferGetContents() ([]byte, func(), error) {
	if !v.IsSharedArrayBuffer() {
		return nil, nil, errors.New("v8go: value is not a SharedArrayBuffer")
	}

	backingStore := C.SharedArrayBufferGetBackingStore(v.ptr)
	release := func() {
		C.BackingStoreRelease(backingStore)
	}

	byte_ptr := (*byte)(unsafe.Pointer(C.BackingStoreData(backingStore)))
	byte_size := C.BackingStoreByteLength(backingStore)
	byte_slice := unsafe.Slice(byte_ptr, byte_size)

	return byte_slice, release, nil
}

// External one-byte strings — see NewExternalOneByteValue.
//
// V8 holds a pointer into caller-owned memory rather than copying the
// string contents into the V8 heap. Lifetime: the Go runtime cannot know
// that V8 still references the slice, so we keep a Go-side reference in
// the extPins map keyed by an opaque uint64. The C++ resource destructor
// (~GoExternalOneByteResource in v8go.cc) calls back into Go via
// goReleaseExternalString to drop the pin when V8 disposes the wrapper.
//
// The pin is process-global rather than per-Isolate because v8go's value
// lifetime is also process-global from Go's perspective (the destructor
// fires from V8's GC, which we don't synchronously control). The map's
// entries are small (one slice header per live external string) and are
// removed promptly when V8 collects the wrapper.
var (
	extPinSeq  uint64
	extPinLock sync.Mutex
	extPins    = make(map[uint64][]byte)
)

// IsOneByteSafe reports whether every byte in b is <= 0x7F (plain ASCII).
// Callers MUST verify this before NewExternalOneByteValue, because V8
// reads external one-byte strings as raw Latin-1 — passing UTF-8 multibyte
// sequences would silently corrupt JS string content.
//
// Implementation uses an 8-byte SWAR scan: any high bit set in the word
// short-circuits to false. ~6 GB/s on modern x86, so it's cheap relative
// to the cost it avoids (the cgo string copy + V8 SeqString allocation).
func IsOneByteSafe(b []byte) bool {
	const hiMask = uint64(0x8080808080808080)
	i := 0
	for ; i+8 <= len(b); i += 8 {
		w := uint64(b[i]) |
			uint64(b[i+1])<<8 |
			uint64(b[i+2])<<16 |
			uint64(b[i+3])<<24 |
			uint64(b[i+4])<<32 |
			uint64(b[i+5])<<40 |
			uint64(b[i+6])<<48 |
			uint64(b[i+7])<<56
		if w&hiMask != 0 {
			return false
		}
	}
	for ; i < len(b); i++ {
		if b[i] > 0x7F {
			return false
		}
	}
	return true
}

// NewExternalOneByteValue creates a V8 String backed by the caller's Go
// memory — no copy is made. V8 keeps a pointer into the slice for the
// lifetime of the resulting JS String. The slice is pinned in a process-
// global map; the pin is released when V8 disposes the resource (typically
// during a later GC or at Isolate::Dispose).
//
// The slice MUST contain only one-byte content (ASCII or Latin-1); use
// IsOneByteSafe to check first. Passing UTF-8 multibyte sequences here
// will produce a JS String whose code units do not match the original
// text — silent corruption.
//
// Why use this: avoiding the Go-string + C.CString + V8 SeqString triple
// copy collapses ~3-4× peak memory pressure for large inputs to ~1×.
// On Windows under commit pressure this can be the difference between
// "passes" and "Fatal JavaScript out of memory: CALL_AND_RETRY_LAST".
// See docs/v8-windows-oom.md in the deskbot repo.
//
// Note: callers should hold the slice in a local variable (not pass a
// short-lived expression) to avoid the slice header being collected
// before the C call returns.
func NewExternalOneByteValue(iso *Isolate, data []byte) (*Value, error) {
	if iso == nil {
		return nil, errors.New("v8go: NewExternalOneByteValue: Isolate cannot be <nil>")
	}
	if len(data) == 0 {
		return NewValue(iso, "")
	}
	pin := atomic.AddUint64(&extPinSeq, 1)
	extPinLock.Lock()
	extPins[pin] = data
	extPinLock.Unlock()
	rtn := C.NewExternalOneByteString(
		iso.ptr,
		(*C.char)(unsafe.Pointer(&data[0])),
		C.int(len(data)),
		C.uint64_t(pin))
	v, err := valueResult(nil, rtn)
	if err != nil {
		// The C++ side already deleted the resource on failure (it never
		// reached V8), so the destructor will not fire — release the pin
		// here ourselves.
		extPinLock.Lock()
		delete(extPins, pin)
		extPinLock.Unlock()
		return nil, err
	}
	return v, nil
}

// goReleaseExternalString is invoked from v8go.cc by the C++ destructor
// of GoExternalOneByteResource when V8 collects the wrapping String.
// Removes the pin so the underlying []byte can be reclaimed by Go GC.
//
//export goReleaseExternalString
func goReleaseExternalString(pinID C.uint64_t) {
	extPinLock.Lock()
	delete(extPins, uint64(pinID))
	extPinLock.Unlock()
}
