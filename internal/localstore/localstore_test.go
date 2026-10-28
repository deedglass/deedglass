package localstore

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ulikunitz/xz"
)

type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func makeTxz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link, Size: int64(len(e.body))}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	var xb bytes.Buffer
	xw, err := xz.NewWriter(&xb)
	if err != nil {
		t.Fatal(err)
	}
	xw.Write(tb.Bytes())
	xw.Close()
	return xb.Bytes()
}

func makeJar(t *testing.T, txz []byte) []byte {
	t.Helper()
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("META-INF/MANIFEST.MF")
	w.Write([]byte("Manifest-Version: 1.0\n"))
	w, _ = zw.Create("postgres-test.txz")
	w.Write(txz)
	zw.Close()
	return zb.Bytes()
}

func programs(extra ...entry) []entry {
	es := []entry{
		{name: "bin/", typ: tar.TypeDir, mode: 0o755},
		{name: "bin/initdb", typ: tar.TypeReg, body: "i", mode: 0o755},
		{name: "bin/pg_ctl", typ: tar.TypeReg, body: "c", mode: 0o755},
		{name: "bin/postgres", typ: tar.TypeReg, body: "p", mode: 0o755},
	}
	return append(es, extra...)
}

func TestUnpackGood(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "rt")
	big := strings.Repeat("x", 4096)
	txz := makeTxz(t, programs(
		entry{name: "lib/libicu.77.1.dylib", typ: tar.TypeReg, body: big},
		entry{name: "lib/libicu.77.dylib", typ: tar.TypeReg, body: big},
		entry{name: "lib/libicu.dylib", typ: tar.TypeReg, body: big},
		entry{name: "lib/libpq.so.5", typ: tar.TypeSymlink, link: "libpq.so.5.18"},
	))
	if err := unpackTxz(bytes.NewReader(txz), dst, "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dst, "bin", "postgres"))
	if err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("postgres not executable: %v %v", fi, err)
	}
	a, _ := os.Stat(filepath.Join(dst, "lib", "libicu.77.1.dylib"))
	b, _ := os.Stat(filepath.Join(dst, "lib", "libicu.dylib"))
	if !os.SameFile(a, b) {
		t.Error("identical files should be hard links")
	}
	if runtime.GOOS != "windows" {
		if l, err := os.Readlink(filepath.Join(dst, "lib", "libpq.so.5")); err != nil || l != "libpq.so.5.18" {
			t.Errorf("symlink: %q %v", l, err)
		}
	}
}

func TestUnpackRefuses(t *testing.T) {
	cases := map[string][]entry{
		"climb":          programs(entry{name: "../evil", typ: tar.TypeReg, body: "x"}),
		"absolute":       programs(entry{name: "/etc/evil", typ: tar.TypeReg, body: "x"}),
		"nested climb":   programs(entry{name: "lib/../../evil", typ: tar.TypeReg, body: "x"}),
		"link out":       programs(entry{name: "lib/x", typ: tar.TypeSymlink, link: "../../etc/passwd"}),
		"absolute link":  programs(entry{name: "lib/x", typ: tar.TypeSymlink, link: "/etc/passwd"}),
		"hard link":      programs(entry{name: "lib/x", typ: tar.TypeLink, link: "bin/postgres"}),
		"device":         programs(entry{name: "lib/x", typ: tar.TypeChar}),
		"no postgres":    {{name: "bin/initdb", typ: tar.TypeReg, body: "i"}},
		"backslash name": programs(entry{name: `lib\..\..\x`, typ: tar.TypeReg, body: "x"}),
	}
	for name, es := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dst := filepath.Join(root, "rt")
			if err := unpackTxz(bytes.NewReader(makeTxz(t, es)), dst, "linux/amd64"); err == nil {
				t.Fatal("accepted")
			}
			if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
				t.Fatal("wrote outside the destination")
			}
		})
	}
}

// fatFile builds a universal Mach-O with two minimal 64-bit headers.
func fatFile() (fat, arm, amd []byte) {
	hdr := func(cpu uint32) []byte {
		b := make([]byte, 32)
		binary.LittleEndian.PutUint32(b[0:], 0xfeedfacf)
		binary.LittleEndian.PutUint32(b[4:], cpu)
		binary.LittleEndian.PutUint32(b[12:], 2) // MH_EXECUTE
		return append(b, []byte("payload-"+string(rune('a'+cpu%26)))...)
	}
	amd, arm = hdr(0x01000007), hdr(0x0100000c)
	var f bytes.Buffer
	binary.Write(&f, binary.BigEndian, []uint32{0xcafebabe, 2})
	off1, off2 := uint32(4096), uint32(8192)
	binary.Write(&f, binary.BigEndian, []uint32{0x01000007, 3, off1, uint32(len(amd)), 12})
	binary.Write(&f, binary.BigEndian, []uint32{0x0100000c, 0, off2, uint32(len(arm)), 12})
	b := f.Bytes()
	b = append(b, make([]byte, int(off1)-len(b))...)
	b = append(b, amd...)
	b = append(b, make([]byte, int(off2)-len(b))...)
	b = append(b, arm...)
	return b, arm, amd
}

func TestThin(t *testing.T) {
	fat, arm, amd := fatFile()
	if got := thin(fat, "darwin/arm64"); !bytes.Equal(got, arm) {
		t.Errorf("arm64 slice: %d bytes", len(got))
	}
	if got := thin(fat, "darwin/amd64"); !bytes.Equal(got, amd) {
		t.Errorf("amd64 slice: %d bytes", len(got))
	}
	plain := []byte("#!/bin/sh\n")
	if got := thin(plain, "darwin/arm64"); !bytes.Equal(got, plain) {
		t.Error("a non-Mach-O file changed")
	}
	bad := append([]byte{0xca, 0xfe, 0xba, 0xbe}, make([]byte, 20)...)
	if got := thin(bad, "darwin/arm64"); !bytes.Equal(got, bad) {
		t.Error("a damaged fat header changed")
	}
}

func TestControlPlaneStatements(t *testing.T) {
	m, a := strings.Repeat("ab", 12), strings.Repeat("cd", 12)
	st, err := controlPlaneStatements(m, a)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(st, "\n")
	for _, want := range []string{"CREATE ROLE deedglass_migrator", "PASSWORD '" + m + "'", "PASSWORD '" + a + "'", "CREATE DATABASE deedglass OWNER deedglass_migrator", "GRANT CONNECT ON DATABASE deedglass TO deedglass_app"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if len(st) != 5 {
		t.Errorf("%d statements, want 5", len(st))
	}
	if _, err := controlPlaneStatements("x'; DROP ROLE postgres; --", a); err == nil {
		t.Error("a non-hex password was accepted")
	}
}

func TestInstallFromServer(t *testing.T) {
	jar := makeJar(t, makeTxz(t, programs()))
	sum := sha256.Sum256(jar)
	b := Bundle{Platform: "linux/amd64", Artifact: "test", Size: int64(len(jar)), SHA256: hex.EncodeToString(sum[:])}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(jar)
	}))
	defer srv.Close()
	old := mavenBase
	mavenBase = srv.URL
	defer func() { mavenBase = old }()
	dir := t.TempDir()
	var last int64
	if err := install(context.Background(), b, dir, "", func(d, _ int64) { last = d }); err != nil {
		t.Fatal(err)
	}
	if last != b.Size || hits != 1 {
		t.Errorf("progress %d, hits %d", last, hits)
	}
	if _, err := os.Stat(filepath.Join(RuntimeDir(dir), "bin", "pg_ctl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runtime", b.FileName())); err == nil {
		t.Error("the .jar should be removed after unpacking")
	}

	// A file that differs from the pin is refused before anything is unpacked.
	bad := b
	bad.SHA256 = strings.Repeat("0", 64)
	if err := install(context.Background(), bad, t.TempDir(), "", nil); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("got %v", err)
	}
	short := b
	short.Size++
	if err := install(context.Background(), short, t.TempDir(), "", nil); err == nil {
		t.Fatal("a short download was accepted")
	}

	// Offline: a bundle on disk installs with no network.
	mavenBase = "http://127.0.0.1:1"
	p := filepath.Join(t.TempDir(), "bundle.jar")
	os.WriteFile(p, jar, 0o600)
	if err := install(context.Background(), b, t.TempDir(), p, nil); err != nil {
		t.Fatal(err)
	}
	var de *DownloadError
	if err := install(context.Background(), b, t.TempDir(), "", nil); !errors.As(err, &de) {
		t.Fatalf("got %v, want a DownloadError", err)
	}
}

func TestBundlesPinned(t *testing.T) {
	for _, p := range []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64"} {
		b, ok := bundles[p]
		if !ok || len(b.SHA256) != 64 || b.Size < 1_000_000 || !strings.Contains(b.URL(), "/"+PGVersion+"/") {
			t.Errorf("%s: %+v", p, b)
		}
	}
}

// TestStoreLive runs a real cluster with the PostgreSQL found on this computer (DEEDGLASS_PG_BIN,
// or Homebrew/system 16+): create, start, connect as deedglass_app, stop, start again with the
// data kept. It is skipped when none is installed and in -short mode.
func TestStoreLive(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	dir := filepath.Join(t.TempDir(), "with space")
	s := New(dir, os.Getenv)
	ctx := context.Background()
	b, err := s.Binaries(ctx)
	if errors.Is(err, NeedDownload) {
		t.Skip("no PostgreSQL 16+ on this computer (set DEEDGLASS_PG_BIN)")
	}
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Start(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop(context.Background())
	if !r.Created {
		t.Error("first start should create the cluster")
	}
	conn, err := pgx.Connect(ctx, r.AppURL())
	if err != nil {
		t.Fatal(err)
	}
	var wal, listen string
	conn.QueryRow(ctx, "SHOW wal_level").Scan(&wal)
	conn.QueryRow(ctx, "SHOW listen_addresses").Scan(&listen)
	conn.Close(ctx)
	if wal != "logical" || listen != "127.0.0.1" {
		t.Errorf("wal_level %q, listen_addresses %q", wal, listen)
	}
	fi, _ := os.Stat(filepath.Join(dir, StateFile))
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("store.json mode %v", fi.Mode().Perm())
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if run, _ := s.Status(ctx); run {
		t.Fatal("still running after Stop")
	}
	r2, err := s.Start(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Created || r2.AppPassword != r.AppPassword {
		t.Error("second start should reuse the cluster")
	}
	// Starting again while running reuses it.
	if _, err := s.Start(ctx, b); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("stop took %s", time.Since(start))
}
