// Copyright 2019 Roger Chapman and the v8go contributors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package v8go

//go:generate clang-format -i --verbose -style=Chromium v8go.h v8go.cc

// #cgo CXXFLAGS: -fno-rtti -std=c++20 -DV8_COMPRESS_POINTERS -DV8_31BIT_SMIS_ON_64BIT_ARCH -I${SRCDIR}/deps/include -Wall -Wno-comment -Wno-vla-cxx-extension
// #cgo !windows CXXFLAGS: -fPIC
// #cgo linux CXXFLAGS: -stdlib=libc++
// #cgo darwin CXXFLAGS: -stdlib=libc++
// #cgo windows CXXFLAGS: -I${SRCDIR}/deps/include_libcxx -I${SRCDIR}/deps/include_libcxxabi -D_LIBCPP_HARDENING_MODE=_LIBCPP_HARDENING_MODE_EXTENSIVE -D_LIBCPP_DISABLE_VISIBILITY_ANNOTATIONS -D_LIBCXXABI_DISABLE_VISIBILITY_ANNOTATIONS
// #cgo !windows LDFLAGS: -pthread -lv8
// #cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/deps/darwin_x86_64
// #cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/deps/darwin_arm64
// #cgo linux,amd64 LDFLAGS: -L${SRCDIR}/deps/linux_x86_64 -ldl
// #cgo linux,arm64 LDFLAGS: -L${SRCDIR}/deps/linux_arm64 -ldl
// #cgo windows,amd64 LDFLAGS: -L${SRCDIR}/deps/windows_x86_64 -llibv8 -llibvcruntime -llibcmt -llibcpmt -llibucrt -loldnames -ldbghelp -lwinmm -lshlwapi -ladvapi32
// #cgo windows,arm64 LDFLAGS: -L${SRCDIR}/deps/windows_arm64 -llibv8 -llibvcruntime -llibcmt -llibcpmt -llibucrt -loldnames -ldbghelp -lwinmm -lshlwapi -ladvapi32
import "C"

// These imports forces `go mod vendor` to pull in all the folders that
// contain V8 libraries and headers which otherwise would be ignored.
// DO NOT REMOVE
import (
	_ "github.com/robomotionio/v8go/deps/darwin_arm64"
	_ "github.com/robomotionio/v8go/deps/darwin_x86_64"
	_ "github.com/robomotionio/v8go/deps/include"
	_ "github.com/robomotionio/v8go/deps/linux_arm64"
	_ "github.com/robomotionio/v8go/deps/linux_x86_64"
	_ "github.com/robomotionio/v8go/deps/windows_arm64"
	_ "github.com/robomotionio/v8go/deps/windows_x86_64"
)
