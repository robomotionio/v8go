#!/usr/bin/env python
import platform
import os
import subprocess
import shutil
import argparse

valid_archs = ['arm64', 'x86_64']
# "x86_64" is called "amd64" on Windows
current_arch = platform.uname()[4].lower().replace("amd64", "x86_64")
default_arch = current_arch if current_arch in valid_archs else None

parser = argparse.ArgumentParser()
parser.add_argument('--debug', dest='debug', action='store_true')
parser.add_argument('--no-clang', dest='clang', action='store_false')
parser.add_argument('--arch',
    dest='arch',
    action='store',
    choices=valid_archs,
    default=default_arch,
    required=default_arch is None)
parser.set_defaults(debug=False, clang=True)
args = parser.parse_args()

deps_path = os.path.dirname(os.path.realpath(__file__))
v8_path = os.path.join(deps_path, "v8")
tools_path = os.path.join(deps_path, "depot_tools")
is_windows = platform.system().lower() == "windows"

gclient_sln = [
    { "name"        : "v8",
        "url"         : "https://chromium.googlesource.com/v8/v8.git",
        "deps_file"   : "DEPS",
        "managed"     : False,
        "custom_deps" : {
            # These deps are unnecessary for building.
            "v8/testing/gmock"                      : None,
            "v8/test/wasm-js"                       : None,
            "v8/third_party/android_tools"          : None,
            "v8/third_party/catapult"               : None,
            "v8/third_party/colorama/src"           : None,
        },
        "custom_vars": {
            "build_for_node" : True,
        },
    },
]

gn_args = """
is_debug=%s
is_clang=%s
target_cpu="%s"
v8_target_cpu="%s"
target_os="%s"
clang_use_chrome_plugins=false
use_custom_libcxx=true
use_sysroot=%s
symbol_level=%s
strip_debug_info=%s
is_component_build=false
v8_monolithic=true
v8_use_external_startup_data=false
treat_warnings_as_errors=false
v8_embedder_string="-v8go"
v8_enable_gdbjit=false
v8_enable_i18n_support=true
icu_use_data_file=false
v8_enable_test_features=false
exclude_unwind_tables=true
v8_enable_sandbox=false
v8_enable_temporal_support=false
"""

def v8deps():
    # gclient sync refuses to run if any of the V8 sub-repos have
    # uncommitted changes. Our apply_local_patches() modifies files in
    # v8/build; revert those before sync so gclient sees a clean tree.
    revert_local_patches()

    spec = "solutions = %s" % gclient_sln
    env = os.environ.copy()
    env["PATH"] = tools_path + os.pathsep + env["PATH"]
    if is_windows:
        # Non-Google users of depot_tools must set this so gclient does not try
        # to download Google's internal Windows toolchain.
        env.setdefault("DEPOT_TOOLS_WIN_TOOLCHAIN", "0")
    subprocess.check_call(cmd(["gclient", "sync", "--spec", spec]),
                        cwd=deps_path,
                        env=env)

def cmd(args):
    return ["cmd", "/c"] + args if is_windows else args

PATCHED_PATHS = [
    # files inside v8/... that apply_local_patches() may modify
    ("v8", "build", "config", "compiler", "BUILD.gn"),
]

def revert_local_patches():
    """Reset any previously-applied local patches back to pristine state so
    gclient sync is happy. Safe to call when the files aren't checked out
    yet (first run) — git returns nonzero but that's fine."""
    for parts in PATCHED_PATHS:
        path = os.path.join(deps_path, *parts)
        if os.path.exists(path):
            subprocess.call(["git", "-C", os.path.dirname(path),
                             "checkout", "--", os.path.basename(path)])

def apply_local_patches():
    """Apply v8go local patches that gclient sync would otherwise reset.

    These are tracked here (not as .patch files) because they are small,
    targeted, and the set is expected to shrink as V8 upstream evolves.
    Any patch added here must be idempotent (safe to apply multiple times).
    """
    # Disable ELF CREL relocations. V8 14.x (with lld) emits CREL via
    # -Wa,--crel,--allow-experimental-crel. The system GNU ld that cgo uses
    # cannot read CREL. Strip the flag so the monolith archive stays
    # linkable by downstream toolchains.
    if not is_windows:
        path = os.path.join(v8_path, "build", "config", "compiler", "BUILD.gn")
        with open(path, "r") as f:
            src = f.read()
        marker = 'cflags += [ "-Wa,--crel,--allow-experimental-crel" ]'
        if marker in src:
            src = src.replace(
                marker,
                '# v8go: CREL disabled, see deps/build.py apply_local_patches()')
            with open(path, "w") as f:
                f.write(src)

def os_arch():
    u = platform.uname()
    return u[0].lower() + "_" + args.arch

def v8_arch():
    if args.arch == "x86_64":
        return "x64"
    return args.arch

def target_os():
    if is_windows:
        return "win"
    u = platform.uname()[0].lower()
    if u == "darwin":
        return "mac"
    return "linux"

def main():
    v8deps()
    apply_local_patches()

    # On Windows depot_tools ships gn/ninja as .bat wrappers that shell out to
    # the cipd-installed binaries; on Linux/macOS it ships posix shell wrappers
    # with no extension. There is no `gn.exe` at the root.
    gn_path = os.path.join(tools_path, "gn.bat" if is_windows else "gn")
    assert os.path.exists(gn_path), f"gn not found at {gn_path}"
    ninja_path = os.path.join(tools_path, "ninja.bat" if is_windows else "ninja")
    if not os.path.exists(ninja_path) and is_windows:
        # Older depot_tools on Windows had `ninja.exe` directly; newer switched
        # to a bat wrapper. Fall back if needed.
        ninja_path = os.path.join(tools_path, "ninja.exe")
    assert os.path.exists(ninja_path), f"ninja not found at {ninja_path}"

    build_path = os.path.join(deps_path, ".build", os_arch())
    env = os.environ.copy()
    if is_windows:
        env.setdefault("DEPOT_TOOLS_WIN_TOOLCHAIN", "0")

    is_debug = 'true' if args.debug else 'false'
    # V8 14.x only builds with clang on all platforms (MSVC support was removed
    # in Sept 2024, so Windows uses clang-cl via is_clang=true).
    is_clang = 'true' if (args.clang or is_windows) else 'false'
    # Always use the host/cross toolchain headers rather than Chromium's
    # sysroot. The debian_bullseye sysroot's libstdc++ lacks C++20 features
    # (std::bit_cast) that V8 14.x source uses. Linux arm64 cross builds
    # install g++-aarch64-linux-gnu which provides a modern libstdc++; macOS
    # uses the Xcode SDK; Windows uses the MSVC SDK via clang-cl.
    use_sysroot = 'false'
    # symbol_level = 1 includes line number information
    # symbol_level = 2 can be used for additional debug information, but it can increase the
    #   compiled library by an order of magnitude and further slow down compilation
    symbol_level = 1 if args.debug else 0
    strip_debug_info = 'false' if args.debug else 'true'

    arch = v8_arch()
    gnargs = gn_args % (is_debug, is_clang, arch, arch, target_os(),
                        use_sysroot, symbol_level, strip_debug_info)
    gen_args = gnargs.replace('\n', ' ')

    subprocess.check_call(cmd([gn_path, "gen", build_path, "--args=" + gen_args]),
                        cwd=v8_path,
                        env=env)
    subprocess.check_call([ninja_path, "-v", "-C", build_path, "v8_monolith"],
                        cwd=v8_path,
                        env=env)

    dest_path = os.path.join(deps_path, os_arch())
    if not os.path.exists(dest_path):
        os.makedirs(dest_path)

    if is_windows:
        # On Windows v8_monolith.lib is just the V8 objects — Chromium's
        # use_custom_libcxx=true builds libc++ and libc++abi as `source_set`
        # targets (loose .obj files at obj/buildtools/third_party/libc++/
        # libc++/*.obj — note the doubled libc++/libc++/), NOT static_library
        # targets. So unlike Linux/macOS there's no libc++.a/lib to point at;
        # we have to glob the individual .obj files and merge them into
        # libv8.lib via llvm-lib /OUT (which accepts a mix of .lib + .obj
        # inputs and produces a single .lib union).
        #
        # Without this merge, downstream consumers compiling cgo bindings
        # against the bundled libc++ headers (std::__Cr namespace) get
        # thousands of unresolved external symbol errors at link time for
        # std::__Cr::cerr, basic_string ctors, __libcpp_verbose_abort,
        # basic_streambuf vtables, etc. — all referenced from inside
        # v8_monolith.lib but defined in the libc++ .obj files.
        #
        # Output is named libv8.lib for naming consistency with libv8.a (the
        # unix archive).
        import glob
        monolith = os.path.join(build_path, "obj", "v8_monolith.lib")

        def find_target_objs(name):
            # libc++ source_set output: obj/buildtools/third_party/<name>/<name>/*.obj
            objs = []
            for path in glob.glob(os.path.join(
                    build_path, "**", "buildtools", "third_party",
                    name, name, "*.obj"), recursive=True):
                if "/clang_" in path.replace("\\", "/") and "_v8_" in path:
                    continue
                objs.append(path)
            return objs

        libcxx_objs = find_target_objs("libc++")
        libcxxabi_objs = find_target_objs("libc++abi")
        dest_fn = os.path.join(dest_path, 'libv8.lib')
        if os.path.exists(dest_fn):
            os.remove(dest_fn)
        if not libcxx_objs or not libcxxabi_objs:
            raise RuntimeError(
                f"libc++/libc++abi target .obj files not found under "
                f"{build_path}; libc++ count={len(libcxx_objs)} "
                f"libc++abi count={len(libcxxabi_objs)}. Inspect the build "
                f"output layout under obj/buildtools/third_party/ and update "
                f"find_target_objs() accordingly.")
        llvm_lib = os.path.join(v8_path, "third_party", "llvm-build",
                                "Release+Asserts", "bin", "llvm-lib.exe")
        if not os.path.exists(llvm_lib):
            llvm_lib = "lib.exe"  # fall back to MSVC's lib.exe on PATH
        print(f"merging {len(libcxx_objs)} libc++ + {len(libcxxabi_objs)} "
              f"libc++abi .obj files into libv8.lib")
        # llvm-lib /OUT:dest input1 input2 ...  — accepts both .lib and .obj
        # inputs; produces a .lib that's a union of all members.
        subprocess.check_call([llvm_lib, "/OUT:" + dest_fn, monolith]
                              + libcxx_objs + libcxxabi_objs)
    else:
        # V8's bundled libc++ and libc++abi live in separate archives that
        # v8_monolith links against at final-binary link time. v8go's
        # consumers link with system libstdc++, so we bundle the libc++
        # .o files into libv8.a here to keep it self-contained. The libc++
        # symbols live in std::__Cr::... so they don't collide with
        # libstdc++'s std::.
        import glob
        monolith = os.path.join(build_path, "obj", "libv8_monolith.a")
        # libc++.a and libc++abi.a are produced for the *target*. On native
        # builds they live at obj/buildtools/third_party/libc++/libc++.a;
        # on cross-compiles (e.g. darwin arm64 host -> x86_64 target) the
        # path can shift. Glob to find the target archive (skip any
        # clang_*_v8_* host-tool subdirs).
        def find_target_archive(name):
            matches = []
            for path in glob.glob(os.path.join(
                    build_path, "**", "buildtools", "third_party",
                    name, name + ".a"), recursive=True):
                # Skip the host-tools mirror (e.g. clang_arm64_v8_x64/...).
                if "/clang_" in path.replace("\\", "/") and "_v8_" in path:
                    continue
                matches.append(path)
            return matches[0] if matches else None

        libcxx = find_target_archive("libc++")
        libcxxabi = find_target_archive("libc++abi")
        dest_fn = os.path.join(dest_path, 'libv8.a')
        if os.path.exists(dest_fn):
            os.remove(dest_fn)
        # MRI-scripted archive merge. macOS's BSD ar doesn't support -M, so
        # we always use V8's bundled llvm-ar (also used by V8's own build).
        llvm_ar = os.path.join(v8_path, "third_party", "llvm-build",
                               "Release+Asserts", "bin", "llvm-ar")
        if not os.path.exists(llvm_ar):
            llvm_ar = "ar"
        if not libcxx or not libcxxabi:
            # Hard fail rather than silently produce a broken archive — the
            # silent-fallback path historically shipped a Mac-Intel libv8.a
            # with 0 __Cr symbols (cross-compile output went to a path the
            # glob missed). Loud failure forces us to fix find_target_archive.
            raise RuntimeError(
                f"libc++/libc++abi target archives not found under {build_path};"
                f" libc++={libcxx} libc++abi={libcxxabi}. Inspect the build"
                f" output layout (likely a cross-compile path the glob missed)"
                f" and update find_target_archive() accordingly.")
        print(f"merging libc++ ({libcxx}) and libc++abi ({libcxxabi}) "
              "into libv8.a")
        script = (
            "CREATE {dest}\n"
            "ADDLIB {monolith}\n"
            "ADDLIB {libcxx}\n"
            "ADDLIB {libcxxabi}\n"
            "SAVE\n"
            "END\n"
        ).format(dest=dest_fn, monolith=monolith,
                 libcxx=libcxx, libcxxabi=libcxxabi)
        subprocess.run([llvm_ar, "-M"], input=script, text=True,
                       check=True)
        # llvm-ar writes a symbol index by default; no separate ranlib pass.


if __name__ == "__main__":
    main()
