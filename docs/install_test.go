package docs_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// TestInstallScript runs scripts/install.sh against a fake release on 127.0.0.1: the latest version from
// the /latest redirect, the sha256 check against checksums.txt, the binary in the chosen directory, and a
// tampered archive refused with nothing installed.
func TestInstallScript(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh covers macOS and Linux")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("no release build for " + runtime.GOARCH)
	}
	for _, tool := range []string{"sh", "tar", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " isn't installed")
		}
	}
	const version = "9.9.9"
	binary := []byte("#!/bin/sh\necho deedglass " + version + "\n")
	archiveName := fmt.Sprintf("deedglass_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	archive := tarGz(t, map[string][]byte{"deedglass": binary, "LICENSE": []byte("licence\n"), "ee/LICENSE": []byte("ee licence\n")})
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + archiveName + "\n" + strings.Repeat("0", 64) + "  deedglass_" + version + "_windows_amd64.zip\n"

	var tampered atomic.Bool // set by the test, read by the handler's goroutine
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v"+version, http.StatusFound)
		case "/releases/tag/v" + version:
			fmt.Fprintln(w, "release page")
		case "/releases/download/v" + version + "/checksums.txt":
			fmt.Fprint(w, checksums)
		case "/releases/download/v" + version + "/" + archiveName:
			b := archive
			if tampered.Load() {
				b = append(append([]byte{}, archive...), 0)
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	run := func(args ...string) (string, string, error) {
		home := t.TempDir()
		dir := filepath.Join(home, "bin")
		cmd := exec.Command(shell(), append([]string{"../scripts/install.sh"}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+home, "DEEDGLASS_RELEASES_URL="+srv.URL+"/releases", "DEEDGLASS_INSTALL_DIR="+dir, "DEEDGLASS_VERSION=")
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return dir, out.String() + errb.String(), err
	}

	dir, out, err := run()
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "deedglass"))
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("installed binary: %v %q\n%s", err, got, out)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "deedglass")); fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary isn't executable: %v", fi.Mode())
	}
	for _, want := range []string{"Installing deedglass " + version + " for " + runtime.GOOS + "/" + runtime.GOARCH, "✓ sha256 matches checksums.txt", "isn't on your PATH"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("install dir holds %d entries, want only deedglass", len(entries))
	}

	if dir, out, err := run("--version", "v"+version); err != nil || !fileIs(filepath.Join(dir, "deedglass"), binary) {
		t.Errorf("--version v%s: %v\n%s", version, err, out)
	}

	tampered.Store(true)
	dir, out, err = run()
	if err == nil || !strings.Contains(out, "doesn't match checksums.txt") || !strings.Contains(out, "Nothing was installed.") {
		t.Errorf("a tampered archive must fail with nothing installed: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "deedglass")); statErr == nil {
		t.Error("a tampered archive was installed")
	}
	tampered.Store(false)

	if _, out, err := run("--version", "1.2.3"); err == nil || !strings.Contains(out, "couldn't download") {
		t.Errorf("a missing release must fail and say what it couldn't download: %v\n%s", err, out)
	}
	if _, out, err := run("--frobnicate"); err == nil || !strings.Contains(out, "unknown option --frobnicate") {
		t.Errorf("an unknown option must fail: %v\n%s", err, out)
	}
}

func fileIs(path string, want []byte) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.Equal(b, want)
}

func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"deedglass", "LICENSE", "ee/LICENSE"} {
		b := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(b)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// shell is sh, or DEEDGLASS_TEST_SH (for example dash, Ubuntu's sh, on a Mac).
func shell() string {
	if s := os.Getenv("DEEDGLASS_TEST_SH"); s != "" {
		return s
	}
	return "sh"
}
