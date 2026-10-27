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
	var signed atomic.Bool   // serve checksums.txt.sigstore.json
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v"+version, http.StatusFound)
		case "/releases/tag/v" + version:
			fmt.Fprintln(w, "release page")
		case "/releases/download/v" + version + "/checksums.txt":
			fmt.Fprint(w, checksums)
		case "/releases/download/v" + version + "/checksums.txt.sigstore.json":
			if !signed.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`)
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

	cosign := "none" // DEEDGLASS_COSIGN for the next run
	run := func(args ...string) (string, string, error) {
		home := t.TempDir()
		dir := filepath.Join(home, "bin")
		cmd := exec.Command(shell(), append([]string{"../scripts/install.sh"}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+home, "DEEDGLASS_RELEASES_URL="+srv.URL+"/releases", "DEEDGLASS_INSTALL_DIR="+dir, "DEEDGLASS_VERSION=",
			"DEEDGLASS_COSIGN="+cosign)
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
	for _, want := range []string{"Installing deedglass " + version + " for " + runtime.GOOS + "/" + runtime.GOARCH, "✓ sha256 matches checksums.txt",
		"cosign isn't installed", "cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json", "isn't on your PATH", "/deedglass start"} {
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

	// The signature (audit M-05): a stand-in cosign records its arguments and answers as told.
	fake := filepath.Join(t.TempDir(), "cosign")
	argsLog := fake + ".args"
	writeFake := func(exit int) {
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nexit %d\n", argsLog, exit)
		if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cosign = fake
	writeFake(0)
	if dir, out, err := run(); err == nil || !strings.Contains(out, "has no checksums.txt.sigstore.json") || fileIs(filepath.Join(dir, "deedglass"), binary) {
		t.Errorf("cosign installed and no signature in the release must fail with nothing installed: %v\n%s", err, out)
	}
	signed.Store(true)
	if dir, out, err := run(); err != nil || !strings.Contains(out, "✓ cosign: checksums.txt was signed") || !fileIs(filepath.Join(dir, "deedglass"), binary) {
		t.Errorf("a signature cosign accepts: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(argsLog); !strings.Contains(string(b), "verify-blob") ||
		!strings.Contains(string(b), `^https://github.com/deedglass/deedglass/\.github/workflows/release\.yml@refs/tags/v`) ||
		!strings.Contains(string(b), "https://token.actions.githubusercontent.com") {
		t.Errorf("cosign was called with %q", b)
	}
	writeFake(1)
	if dir, out, err := run(); err == nil || !strings.Contains(out, "signature doesn't check out") || fileIs(filepath.Join(dir, "deedglass"), binary) {
		t.Errorf("a signature cosign refuses must fail with nothing installed: %v\n%s", err, out)
	}
	signed.Store(false)
	cosign = "none"

	if _, out, err := run("--version", "1.2.3"); err == nil || !strings.Contains(out, "couldn't download") {
		t.Errorf("a missing release must fail and say what it couldn't download: %v\n%s", err, out)
	}
	if _, out, err := run("--frobnicate"); err == nil || !strings.Contains(out, "unknown option --frobnicate") {
		t.Errorf("an unknown option must fail: %v\n%s", err, out)
	}
}

// TestInstallScriptMirror: the deedglass.com/dl copy comes first (its /latest names the version, and
// GitHub isn't asked for anything it serves); a file the copy lacks comes from GitHub; a copy that serves
// a changed archive is refused by the same sha256 check, with nothing installed.
func TestInstallScriptMirror(t *testing.T) {
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
	const version = "9.9.8"
	binary := []byte("#!/bin/sh\necho deedglass " + version + "\n")
	archiveName := fmt.Sprintf("deedglass_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	archive := tarGz(t, map[string][]byte{"deedglass": binary, "LICENSE": []byte("licence\n"), "ee/LICENSE": []byte("ee licence\n")})
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + archiveName + "\n"

	var github atomic.Int32        // requests GitHub answered
	var mirrorArchive atomic.Bool  // the copy serves the archive
	var mirrorTampered atomic.Bool // the copy's archive has a byte appended
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dl/latest":
			fmt.Fprintln(w, version)
		case "/dl/v" + version + "/checksums.txt":
			fmt.Fprint(w, checksums)
		case "/dl/v" + version + "/" + archiveName:
			if !mirrorArchive.Load() {
				http.NotFound(w, r)
				return
			}
			b := archive
			if mirrorTampered.Load() {
				b = append(append([]byte{}, archive...), 0)
			}
			w.Write(b)
		case "/releases/latest":
			github.Add(1)
			http.Redirect(w, r, "/releases/tag/v"+version, http.StatusFound)
		case "/releases/tag/v" + version:
			fmt.Fprintln(w, "release page")
		case "/releases/download/v" + version + "/checksums.txt":
			github.Add(1)
			fmt.Fprint(w, checksums)
		case "/releases/download/v" + version + "/" + archiveName:
			github.Add(1)
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	run := func() (string, string, error) {
		home := t.TempDir()
		dir := filepath.Join(home, "bin")
		cmd := exec.Command(shell(), "../scripts/install.sh")
		cmd.Env = append(os.Environ(), "HOME="+home, "DEEDGLASS_RELEASES_URL="+srv.URL+"/releases", "DEEDGLASS_MIRROR_URL="+srv.URL+"/dl/",
			"DEEDGLASS_INSTALL_DIR="+dir, "DEEDGLASS_VERSION=", "DEEDGLASS_COSIGN=none")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return dir, out.String(), err
	}

	mirrorArchive.Store(true)
	dir, out, err := run()
	if err != nil || !fileIs(filepath.Join(dir, "deedglass"), binary) {
		t.Fatalf("install from the copy: %v\n%s", err, out)
	}
	if n := github.Load(); n != 0 {
		t.Errorf("GitHub answered %d requests although the copy had every file:\n%s", n, out)
	}
	if !strings.Contains(out, "Installing deedglass "+version) || !strings.Contains(out, "from 127.0.0.1") {
		t.Errorf("output doesn't name the version and the copy:\n%s", out)
	}

	mirrorArchive.Store(false)
	dir, out, err = run()
	if err != nil || !fileIs(filepath.Join(dir, "deedglass"), binary) || !strings.Contains(out, "trying GitHub") || github.Load() != 1 {
		t.Errorf("an archive the copy lacks must come from GitHub (GitHub answered %d): %v\n%s", github.Load(), err, out)
	}

	mirrorArchive.Store(true)
	mirrorTampered.Store(true)
	dir, out, err = run()
	if err == nil || !strings.Contains(out, "doesn't match checksums.txt") || fileIs(filepath.Join(dir, "deedglass"), binary) {
		t.Errorf("a changed archive on the copy must fail with nothing installed: %v\n%s", err, out)
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
