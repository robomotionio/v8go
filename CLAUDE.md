# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`v8go` is a CGo binding that lets Go programs execute JavaScript via the V8 engine. The Go module path is `github.com/robomotionio/v8go`. Prebuilt V8 static libraries for Linux (x86_64/arm64), macOS (x86_64/arm64), and Windows (x86_64/arm64) are vendored under `deps/`; users of the package should not need to build V8 themselves.

Windows consumers must build with clang (`CC=clang.exe CXX=clang++.exe`) because V8's Windows binary is MSVC-ABI (clang-cl). MinGW-GCC is not supported.

## Build, test, format

- `go test ./...` — run the full test suite. The CI sets `CGO_CXXFLAGS="-Werror"`; use the same locally when touching C/C++.
- `go test -run TestName` — run a single test. Note that because cgo compiles `v8go.cc` first, each `go test` invocation takes ~30s of C++ compilation even for one test.
- `go generate` — runs `clang-format -i -style=Chromium v8go.h v8go.cc`. Any changes to the C/C++ sources MUST be formatted with the "Chromium" style (`brew install clang-format` or distro equivalent).
- `go test -c --tags leakcheck && ./v8go.test` — leak-check tests locally via LLVM's LeakSanitizer. Use clang (`CC=clang-12 CXX=clang++-12 …`) for usable backtraces. See `leakcheck.go` and README "Local leak checking" for the macOS variant.
- `deps/build.py --debug` — rebuild V8 locally with debug info + DCHECKs enabled. Takes up to 30 min. Only needed when diagnosing a V8/v8go crash; normal development uses the vendored prebuilt `libv8.a`.

Minimum Go version: 1.17 (required for SharedArrayBuffer support). CI tests against Go 1.22.x and 1.23.x.

### Windows toolchain

- **Building V8**: `deps/build.py` selects V8's clang-cl path automatically on Windows (`is_clang=true`, `target_os="win"`). CI sets `DEPOT_TOOLS_WIN_TOOLCHAIN=0` so depot_tools uses the local VS Build Tools rather than the Google-internal toolchain. Produces `deps/windows_{x86_64,arm64}/v8_monolith.lib`.
- **Building v8go (cgo)**: Windows consumers must set `CC=clang.exe CXX=clang++.exe` (LLVM installation) so the cgo-compiled objects match V8's MSVC ABI. MinGW-GCC will not link against V8's `v8_monolith.lib`.

## Architecture

The binding is a **three-layer sandwich**:

1. **Go API** (`*.go`, excluding `v8go.cc/h`) — the idiomatic surface: `Isolate`, `Context`, `Value`, `Object`, `Function`, `FunctionTemplate`, `ObjectTemplate`, `Promise`, `CPUProfiler`, `UnboundScript`. Errors surface as `*JSError` (see `errors.go`).
2. **C ABI shim** (`v8go.h`) — a flat `extern "C"` interface. `RtnValue`/`RtnError`/`RtnString` structs carry both a value and a possible exception across the boundary, since CGo can't see C++ exceptions. Opaque `m_ctx`/`m_value`/`m_template`/`m_unboundScript` structs are typedef'd to pointers for both the C-side (forward decls) and C++-side (real types) — see the `__cplusplus` branches.
3. **C++ bridge** (`v8go.cc`) — the only file that includes V8 headers. Translates between the C ABI and the V8 C++ API, owns HandleScopes, converts V8 exceptions into `RtnError`, and dispatches callbacks back into Go via the exported functions below.

### Go ↔ V8 callback dispatch

CGo can't pass Go function pointers to C. Callbacks are registered by integer ref and looked up on dispatch:

- `context.go` maintains `ctxRegistry` (ref int → `*Context`), exports `goContext(ref)`. Each `Context` gets a monotonically increasing `ref` passed to `NewContext` on the C side.
- `isolate.go` registers `FunctionCallback`s per isolate (`iso.registerCallback` → cbref int). `function_template.go` exports `goFunctionCallback(ctxref, cbref, thisAndArgs, argc)`, which re-hydrates `Context`, `*Object` (receiver), and `[]*Value` args from the pointer array. The `thisAndArgs` combined pointer is a workaround for a Windows `ERROR_COMMITMENT_LIMIT` observed on 2021-era CI. Windows CI has been restored; re-evaluating whether the workaround is still needed is a tracked follow-up. Until then, don't split it.
- Similarly, `promise.go` has promise-then/catch callback refs; `context_test.go` has more examples of callback usage.

### Memory & lifetime model

Important — getting this wrong causes use-after-free or leaks:

- `Isolate.Dispose()` frees the V8 isolate; any `Value` or `Context` from it becomes invalid.
- `Context.Close()` deregisters from `ctxRegistry` and frees the V8 context.
- `Value.Release()` is **manual** (added in v0.8.0). Release is required in long-running contexts to prevent accumulating persistent handles; short-lived scripts can rely on isolate disposal. `FunctionCallbackInfo.Release()` releases all arg values and `this`.
- `runtime.SetFinalizer` is used on `*template` (see `function_template.go:69`, `template.go`) so V8 template data is released if the user doesn't. `runtime.KeepAlive` is sprinkled liberally to hold Go objects alive across CGo calls — preserve these when refactoring.
- `CompilerCachedData` is a serializable code cache produced via `UnboundScript.CreateCodeCache()`; feeding it back into `CompileUnboundScript` via `CompileOptions{CachedData: …}` skips recompilation. `Rejected` is set on the `CachedData` if V8 refused it.

### CGo build configuration (`cgo.go`)

`cgo.go` is the single source of truth for build flags: `-std=c++20 -DV8_COMPRESS_POINTERS -DV8_31BIT_SMIS_ON_64BIT_ARCH -DV8_ENABLE_SANDBOX`, plus per-platform link flags from `deps/{os}_{arch}/`. On Linux/macOS it links `libv8.a` (`-lv8 -pthread`); on Windows it links `v8_monolith.lib` plus `dbghelp`, `winmm`, `shlwapi`, `advapi32`. The `_ "github.com/robomotionio/v8go/deps/..."` blank imports exist **only** to force `go mod vendor` to include those directories — don't remove them.

## V8 dependency & upgrades

Current V8: see `deps/v8_version` (currently `14.7.173.21`). V8 14.x mandates C++20 and builds with clang on every platform — on Windows that means clang-cl driven by `is_clang=true, target_os="win"` in `deps/build.py`. The old MinGW-w64 patch branch has been removed. Submodules `deps/v8` and `deps/depot_tools` are only needed when rebuilding V8 (`git submodule update --init --recursive`).

Upgrades are automated via the **`v8upgrade.yml`** workflow (runs daily; compares `deps/v8_version` against latest stable and opens a PR on `v8_upgrade/<version>`). After the upgrade PR is open: manually trigger the **`V8 Build`** workflow on that branch, which produces six binary PRs (linux × {x86_64, arm64}, darwin × {x86_64, arm64}, windows × {x86_64, arm64}) to merge into the upgrade branch. Only then is it ready to merge to `master`. The `v8build.yml` / `v8upgrade.yml` workflows in `.github/workflows/` are the source of truth.

## Conventions specific to this project

- C/C++ files use **Chromium** style via `clang-format`, invoked through `go generate`. Go uses standard `go fmt`.
- Error-returning functions in v8go surface JS errors as `*JSError` (with `Message`, `Location`, `StackTrace`). `%+v` formatting on a `JSError` prints the full stack trace — this is the documented API, preserve it.
- `NewValue(iso, v)` accepts a fixed set of Go primitive types (string, int32/uint32, int64/uint64, bool, float64, `*big.Int`). Other types must go through templates or JSON.
- `Context.Global()` returns the **global proxy**, not the global object itself — V8 security model requires this. Don't change its prototype.
- When adding a new V8 type check (e.g. `Value.IsX()`), the pattern is: declare `ValueIsX` in `v8go.h`, implement in `v8go.cc` as a one-liner calling `Is...()`, add a Go wrapper in `value.go`. See existing `IsSharedArrayBuffer` / `IsProxy` etc. for reference.
