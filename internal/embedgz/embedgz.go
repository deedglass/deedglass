// Package embedgz keeps embedded text files gzip-compressed in the binary (the image size budget,
// ADR-032 as amended by DL-031): a directory's files are committed beside a deterministic
// files.tar.gz that go:embed carries, and FS turns it back into a read-only fs.FS at first use.
// The files on disk stay the source of truth; each package's test compares the archive with them
// (make embed-gz rewrites it).
package embedgz

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxFile bounds one file in an archive, and MaxTotal the whole archive, unpacked.
const (
	MaxFile  = 4 << 20
	MaxTotal = 32 << 20
)

// FS unpacks a tar.gz of plain files in one directory into a read-only fs.FS. It panics on a
// malformed archive: archives are build inputs, checked by tests, never user data.
func FS(gz []byte) fs.FS {
	files, err := Unpack(gz)
	if err != nil {
		panic("embedgz: " + err.Error())
	}
	return memFS(files)
}

// Unpack reads the archive into name → bytes. Every name is a plain file name (no directory).
func Unpack(gz []byte) (map[string][]byte, error) { return unpack(gz, false) }

// UnpackTree is Unpack for an archive whose names may sit in subdirectories ("pack/pack.yaml"):
// each name must be a valid, clean fs path, and no element of it may start with a dot.
func UnpackTree(gz []byte) (map[string][]byte, error) { return unpack(gz, true) }

func validName(name string, tree bool) bool {
	if !tree {
		return name != "" && name[0] != '.' && !strings.ContainsAny(name, `/\`)
	}
	if !fs.ValidPath(name) || name == "." || strings.Contains(name, `\`) {
		return false
	}
	for _, el := range strings.Split(name, "/") {
		if el[0] == '.' {
			return false
		}
	}
	return true
}

func unpack(gz []byte, tree bool) (map[string][]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	out := map[string][]byte{}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || !validName(h.Name, tree) || h.Size > MaxFile {
			return nil, fmt.Errorf("%q isn't a plain file of the archive", h.Name)
		}
		if total += h.Size; total > MaxTotal {
			return nil, errors.New("the archive is too large")
		}
		b, err := io.ReadAll(io.LimitReader(tr, h.Size))
		if err != nil {
			return nil, err
		}
		if _, dup := out[h.Name]; dup {
			return nil, fmt.Errorf("%q appears twice", h.Name)
		}
		out[h.Name] = b
	}
}

// Pack writes files as a deterministic tar.gz: names sorted, mode 0644, owner 0, mtime 0, a gzip
// header with no name or time.
func Pack(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Header = gzip.Header{OS: 255}
	tw := tar.NewWriter(zw)
	for _, n := range names {
		b := files[n]
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: n, Mode: 0o644, Size: int64(len(b)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadTree reads every regular file under dir into slash-separated path → bytes, the input Pack
// takes for an archive UnpackTree reads. Directories and files whose name starts with a dot are
// skipped, as the packs reader skips them; anything that isn't a regular file is an error.
func ReadTree(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	fsys := os.DirFS(dir)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s isn't a regular file", p)
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		files[p] = b
		return nil
	})
	return files, err
}

// Diff says how an archive differs from the files it should hold ("" when it matches). Names may
// sit in subdirectories (UnpackTree).
func Diff(gz []byte, want map[string][]byte) string {
	got, err := UnpackTree(gz)
	if err != nil {
		return err.Error()
	}
	var out []string
	for n, b := range want {
		if g, ok := got[n]; !ok {
			out = append(out, n+" is missing")
		} else if !bytes.Equal(g, b) {
			out = append(out, n+" differs")
		}
	}
	for n := range got {
		if _, ok := want[n]; !ok {
			out = append(out, n+" is extra")
		}
	}
	sort.Strings(out)
	return strings.Join(out, "; ")
}

// memFS is a flat, read-only fs.FS.
type memFS map[string][]byte

var (
	_ fs.ReadFileFS = memFS(nil)
	_ fs.ReadDirFS  = memFS(nil)
)

func (m memFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		ents, _ := m.ReadDir(".")
		return &dir{ents: ents}, nil
	}
	b, ok := m[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &file{name: name, r: bytes.NewReader(b), size: int64(len(b))}, nil
}

func (m memFS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid}
	}
	b, ok := m[name]
	if !ok {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), b...), nil
}

func (m memFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	out := make([]fs.DirEntry, 0, len(m))
	for n, b := range m {
		out = append(out, fs.FileInfoToDirEntry(info{name: n, size: int64(len(b))}))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

type info struct {
	name string
	size int64
	dir  bool
}

func (i info) Name() string       { return path.Base(i.name) }
func (i info) Size() int64        { return i.size }
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return i.dir }
func (i info) Sys() any           { return nil }
func (i info) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}

type file struct {
	name string
	r    *bytes.Reader
	size int64
}

func (f *file) Stat() (fs.FileInfo, error) { return info{name: f.name, size: f.size}, nil }
func (f *file) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *file) Close() error               { return nil }

type dir struct {
	ents []fs.DirEntry
	off  int
}

func (d *dir) Stat() (fs.FileInfo, error) { return info{name: ".", dir: true}, nil }
func (d *dir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: ".", Err: errors.New("is a directory")}
}
func (d *dir) Close() error { return nil }
func (d *dir) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := d.ents[d.off:]
	if n <= 0 {
		d.off = len(d.ents)
		return rest, nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	if n > len(rest) {
		n = len(rest)
	}
	d.off += n
	return rest[:n], nil
}

// Files is an archive unpacked at its first read, for packages that read a few files by name (the
// sign-in and approval pages, single templates). ReadFile ignores a leading directory, so a call
// site that read "pages/auth.js" from an embed.FS keeps its name.
type Files struct {
	gz   []byte
	once sync.Once
	fs   memFS
}

// NewFiles wraps an embedded archive.
func NewFiles(gz []byte) *Files { return &Files{gz: gz} }

// ReadFile returns one file's bytes (a shared copy-free slice: callers must not change it).
func (f *Files) ReadFile(name string) ([]byte, error) {
	f.once.Do(func() {
		m, err := Unpack(f.gz)
		if err != nil {
			panic("embedgz: " + err.Error())
		}
		f.fs = memFS(m)
	})
	return f.fs.ReadFile(path.Base(name))
}

// MustString is one file of an archive as a string, unpacked once at first use.
func MustString(f *Files, name string) func() string {
	return sync.OnceValue(func() string {
		b, err := f.ReadFile(name)
		if err != nil {
			panic("embedgz: " + name + ": " + err.Error())
		}
		return string(b)
	})
}
