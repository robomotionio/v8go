//go:build ignore

// Command fetch-libv8 downloads the prebuilt V8 static library for the
// current platform from the GitHub release tagged with deps/v8_version.
//
// Usage (from the repository root):
//
//	go run scripts/fetch-libv8.go
//
// Flags:
//
//	-tag    override the release tag (default: v<deps/v8_version>)
//	-os     override GOOS (default: runtime.GOOS)
//	-arch   override GOARCH (default: runtime.GOARCH)
//	-force  overwrite an existing file
//
// The downloaded file is written to deps/{os}_{arch}/libv8.{a,lib} —
// libv8.a on unix, libv8.lib on Windows.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const repo = "robomotionio/v8go"

func main() {
	var (
		tagFlag   = flag.String("tag", "", "release tag (default: v<deps/v8_version>)")
		osFlag    = flag.String("os", runtime.GOOS, "target OS (linux, darwin, windows)")
		archFlag  = flag.String("arch", runtime.GOARCH, "target arch (amd64, arm64)")
		forceFlag = flag.Bool("force", false, "overwrite existing file")
	)
	flag.Parse()

	root, err := findRepoRoot()
	if err != nil {
		die(err)
	}

	arch := normalizeArch(*archFlag)
	goos := *osFlag

	tag := *tagFlag
	if tag == "" {
		tag, err = defaultTag(root)
		if err != nil {
			die(err)
		}
	}

	asset, destName := assetFor(goos, arch)
	destDir := filepath.Join(root, "deps", fmt.Sprintf("%s_%s", goos, arch))
	dest := filepath.Join(destDir, destName)

	if _, err := os.Stat(dest); err == nil && !*forceFlag {
		fmt.Printf("already present: %s (use -force to overwrite)\n", dest)
		return
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		die(err)
	}

	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
	fmt.Printf("fetching %s\n", url)
	if err := download(url, dest); err != nil {
		die(err)
	}
	fmt.Printf("wrote %s\n", dest)
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "deps", "v8_version")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find repo root (no deps/v8_version found in any parent of %s)", dir)
		}
		dir = parent
	}
}

func defaultTag(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "deps", "v8_version"))
	if err != nil {
		return "", err
	}
	return "v" + strings.TrimSpace(string(b)), nil
}

func normalizeArch(a string) string {
	switch a {
	case "amd64", "x86_64":
		return "x86_64"
	case "arm64", "aarch64":
		return "arm64"
	}
	return a
}

func assetFor(goos, arch string) (asset, destName string) {
	switch goos {
	case "windows":
		return fmt.Sprintf("libv8-windows-%s.lib", arch), "libv8.lib"
	default:
		return fmt.Sprintf("libv8-%s-%s.a", goos, arch), "libv8.a"
	}
}

func download(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".fetch-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, dest)
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "fetch-libv8: %v\n", err)
	os.Exit(1)
}
