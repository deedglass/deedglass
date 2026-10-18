package embedgz

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRoundTripAndFS(t *testing.T) {
	files := map[string][]byte{"0001_a.sql": []byte("select 1;\n"), "0002_b.sql": bytes.Repeat([]byte("x"), 5000), "empty.txt": {}}
	gz, err := Pack(files)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Pack(files)
	if !bytes.Equal(gz, again) {
		t.Fatal("Pack isn't deterministic")
	}
	if d := Diff(gz, files); d != "" {
		t.Fatal(d)
	}
	fsys := FS(gz)
	if err := fstest.TestFS(fsys, "0001_a.sql", "0002_b.sql", "empty.txt"); err != nil {
		t.Fatal(err)
	}
	b, err := fs.ReadFile(fsys, "0001_a.sql")
	if err != nil || string(b) != "select 1;\n" {
		t.Fatal(string(b), err)
	}
	for _, bad := range []string{"missing.sql", "../x", "a/b"} {
		if _, err := fs.ReadFile(fsys, bad); err == nil {
			t.Errorf("%s: read", bad)
		}
		if _, err := fsys.Open(bad); err == nil {
			t.Errorf("%s: opened", bad)
		}
	}
	if _, err := fs.ReadDir(fsys, "sub"); err == nil {
		t.Error("a subdirectory listed")
	}
	f, _ := fsys.Open(".")
	if _, err := f.Read(make([]byte, 1)); err == nil {
		t.Error("read a directory")
	}
	_ = f.Close()
	changed := map[string][]byte{"0001_a.sql": []byte("select 2;\n"), "0003_c.sql": nil}
	if d := Diff(gz, changed); !strings.Contains(d, "0001_a.sql differs") || !strings.Contains(d, "0003_c.sql is missing") ||
		!strings.Contains(d, "0002_b.sql is extra") {
		t.Fatal(d)
	}
	if d := Diff([]byte("not gzip"), files); d == "" {
		t.Fatal("a broken archive matched")
	}
}

func raw(t *testing.T, hdrs []tar.Header, bodies []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for i, h := range hdrs {
		h.Size = int64(len(bodies[i]))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(tw, bodies[i])
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

func TestUnpackRefusals(t *testing.T) {
	for name, gz := range map[string][]byte{
		"directory": raw(t, []tar.Header{{Typeflag: tar.TypeDir, Name: "d/", Mode: 0o755}}, []string{""}),
		"nested":    raw(t, []tar.Header{{Typeflag: tar.TypeReg, Name: "d/x", Mode: 0o644}}, []string{"x"}),
		"dotfile":   raw(t, []tar.Header{{Typeflag: tar.TypeReg, Name: ".x", Mode: 0o644}}, []string{"x"}),
		"duplicate": raw(t, []tar.Header{{Typeflag: tar.TypeReg, Name: "x", Mode: 0o644}, {Typeflag: tar.TypeReg, Name: "x", Mode: 0o644}}, []string{"a", "b"}),
		"symlink":   raw(t, []tar.Header{{Typeflag: tar.TypeSymlink, Name: "x", Linkname: "/etc/passwd"}}, []string{""}),
		"not gzip":  []byte("plain"),
	} {
		if _, err := Unpack(gz); err == nil {
			t.Errorf("%s: unpacked", name)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("FS accepted a broken archive")
		}
	}()
	FS([]byte("plain"))
}

// TestUnpackTree: names in subdirectories round-trip; dot elements, unclean paths, absolute paths
// and directories are refused, and the flat Unpack still refuses a nested name.
func TestUnpackTree(t *testing.T) {
	files := map[string][]byte{"index.json": []byte("{}"), "a/pack.yaml": []byte("id: a\n"), "a/b/c.yaml": []byte("x\n")}
	gz, err := Pack(files)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnpackTree(gz)
	if err != nil || len(got) != 3 || string(got["a/b/c.yaml"]) != "x\n" {
		t.Fatalf("UnpackTree = %v, %v", got, err)
	}
	if d := Diff(gz, files); d != "" {
		t.Fatalf("Diff = %q", d)
	}
	if _, err := Unpack(gz); err == nil {
		t.Error("Unpack accepted a nested name")
	}
	for name, gz := range map[string][]byte{
		"dot dir":   raw(t, []tar.Header{{Typeflag: tar.TypeReg, Name: ".git/x", Mode: 0o644}}, []string{"x"}),
		"dot file":  raw(t, []tar.Header{{Typeflag: tar.TypeReg, Name: "a/.x", Mode: 0o644}}, []string{"x"}),
		"dot dot":   raw(t, []tar.Header{{Typeflag: tar.TypeReg, Name: "a/../x", Mode: 0o644}}, []string{"x"}),
		"absolute":  raw(t, []tar.Header{{Typeflag: tar.TypeReg, Name: "/x", Mode: 0o644}}, []string{"x"}),
		"backslash": raw(t, []tar.Header{{Typeflag: tar.TypeReg, Name: `a\x`, Mode: 0o644}}, []string{"x"}),
		"empty el":  raw(t, []tar.Header{{Typeflag: tar.TypeReg, Name: "a//x", Mode: 0o644}}, []string{"x"}),
		"directory": raw(t, []tar.Header{{Typeflag: tar.TypeDir, Name: "d/", Mode: 0o755}}, []string{""}),
	} {
		if _, err := UnpackTree(gz); err == nil {
			t.Errorf("%s: unpacked", name)
		}
	}
}
