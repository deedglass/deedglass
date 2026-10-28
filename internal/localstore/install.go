package localstore

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"

	"github.com/deedglass/deedglass/internal/connector/egress"
)

// Unpack limits: the largest bundle unpacks to about 330 MB with 1,600 entries.
const (
	maxEntries   = 20_000
	maxUnpacked  = 2 << 30 // 2 GiB
	downloadTime = 30 * time.Minute
)

// ErrNoBundle means no PostgreSQL build is published for this platform.
var ErrNoBundle = errors.New("no PostgreSQL build is published for this platform")

// DownloadError is a download that failed; Offline says the network couldn't be reached.
type DownloadError struct {
	URL     string
	Offline bool
	Err     error
}

func (e *DownloadError) Error() string { return "downloading PostgreSQL: " + e.Err.Error() }
func (e *DownloadError) Unwrap() error { return e.Err }

// RuntimeDir is where the downloaded build is unpacked: <dir>/runtime/pg-<version>.
func RuntimeDir(dir string) string { return filepath.Join(dir, "runtime", "pg-"+PGVersion) }

// Progress reports bytes received out of total.
type Progress func(done, total int64)

// Install puts this platform's PostgreSQL build into RuntimeDir(dir). The .jar comes from, in
// order: bundlePath (DEEDGLASS_PG_BUNDLE), a copy already in <dir>/runtime, then the network.
// Whatever its source, its size and SHA-256 must equal the pinned ones before it is unpacked.
func Install(ctx context.Context, dir, bundlePath string, progress Progress) error {
	b, ok := CurrentBundle()
	if !ok {
		return ErrNoBundle
	}
	return install(ctx, b, dir, bundlePath, progress)
}

func install(ctx context.Context, b Bundle, dir, bundlePath string, progress Progress) error {
	rt := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(rt, 0o700); err != nil {
		return err
	}
	jar := bundlePath
	if jar == "" {
		jar = filepath.Join(rt, b.FileName())
		if err := checkFile(jar, b); err != nil {
			if err := download(ctx, b, jar, progress); err != nil {
				return err
			}
		}
	}
	if err := checkFile(jar, b); err != nil {
		return err
	}
	dst := RuntimeDir(dir)
	tmp := dst + ".partial"
	_ = os.RemoveAll(tmp)
	if err := unpackJar(jar, tmp, b.Platform); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	_ = os.RemoveAll(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	if bundlePath == "" {
		_ = os.Remove(jar) // unpacked and checked; the .jar isn't needed again
	}
	return nil
}

// checkFile compares a .jar with the pinned size and SHA-256.
func checkFile(p string, b Bundle) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() != b.Size {
		return fmt.Errorf("%s is %d bytes, expected %d for PostgreSQL %s (%s)", filepath.Base(p), fi.Size(), b.Size, PGVersion, b.Platform)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != b.SHA256 {
		return fmt.Errorf("%s doesn't match the pinned SHA-256 for PostgreSQL %s (%s)", filepath.Base(p), PGVersion, b.Platform)
	}
	return nil
}

// download fetches the .jar to p (through p.part), refusing anything longer than the pinned size.
func download(ctx context.Context, b Bundle, p string, progress Progress) error {
	c := egress.FirstParty(egress.FirstPartyOptions{Timeout: downloadTime, Proxy: true, Redirects: egress.RedirectSameHost})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.URL(), nil)
	if err != nil {
		return err
	}
	res, err := c.Do(req)
	if err != nil {
		return &DownloadError{URL: b.URL(), Offline: true, Err: err}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &DownloadError{URL: b.URL(), Err: fmt.Errorf("the server answered %s", res.Status)}
	}
	part := p + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := &countWriter{w: f, total: b.Size, progress: progress}
	_, err = io.Copy(w, io.LimitReader(res.Body, b.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(part)
		return &DownloadError{URL: b.URL(), Err: err}
	}
	if w.n != b.Size {
		_ = os.Remove(part)
		return &DownloadError{URL: b.URL(), Err: fmt.Errorf("received %d bytes, expected %d", w.n, b.Size)}
	}
	return os.Rename(part, p)
}

type countWriter struct {
	w        io.Writer
	n, total int64
	progress Progress
	last     time.Time
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if c.progress != nil && (time.Since(c.last) > 100*time.Millisecond || c.n == c.total) {
		c.last = time.Now()
		c.progress(c.n, c.total)
	}
	return n, err
}

// unpackJar unpacks the one .txz inside the .jar into dst.
func unpackJar(jar, dst, platform string) error {
	zr, err := zip.OpenReader(jar)
	if err != nil {
		return fmt.Errorf("reading %s: %w", filepath.Base(jar), err)
	}
	defer zr.Close()
	var txz *zip.File
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, ".txz") && !strings.Contains(f.Name, "/") {
			if txz != nil {
				return errors.New("the bundle holds more than one archive")
			}
			txz = f
		}
	}
	if txz == nil {
		return errors.New("the bundle holds no PostgreSQL archive")
	}
	rc, err := txz.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return unpackTxz(rc, dst, platform)
}

// unpackTxz unpacks an xz-compressed tar into dst. Only directories, regular files and (Linux)
// relative symlinks that stay inside dst are accepted. On macOS each universal program is cut
// down to this machine's architecture, and identical files become hard links: the published
// build ships three copies of a 64 MB library and both architectures of everything.
func unpackTxz(r io.Reader, dst, platform string) error {
	xr, err := xz.NewReader(r)
	if err != nil {
		return fmt.Errorf("reading the archive: %w", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(xr)
	seen := map[string]string{} // sha256 → first path with that content
	var total int64
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading the archive: %w", err)
		}
		if n >= maxEntries {
			return errors.New("the archive has too many entries")
		}
		name, ok := cleanName(h.Name)
		if !ok {
			return fmt.Errorf("the archive holds an unsafe path %q", h.Name)
		}
		if name == "" {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if total > maxUnpacked {
				return errors.New("the archive unpacks to more than 2 GiB")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeEntry(tr, target, h, platform, seen); err != nil {
				return err
			}
		case tar.TypeSymlink:
			link := h.Linkname
			if path.IsAbs(link) || strings.Contains(link, "\\") {
				return fmt.Errorf("the archive holds an unsafe link %q", h.Name)
			}
			if j := path.Join(path.Dir(name), link); j == ".." || strings.HasPrefix(j, "../") {
				return fmt.Errorf("the archive holds a link that leaves it: %q", h.Name)
			}
			if runtime.GOOS == "windows" {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
		default:
			// Hard links, devices and the rest are never part of a PostgreSQL build.
			return fmt.Errorf("the archive holds an unexpected entry %q", h.Name)
		}
	}
	for _, p := range []string{"bin/initdb", "bin/pg_ctl", "bin/postgres"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(p)+exeSuffix(platform))); err != nil {
			return fmt.Errorf("the archive has no %s", p)
		}
	}
	return nil
}

func exeSuffix(platform string) string {
	if strings.HasPrefix(platform, "windows/") {
		return ".exe"
	}
	return ""
}

// cleanName returns the entry's slash path relative to the archive root; ok false when it is
// absolute or climbs out.
func cleanName(n string) (string, bool) {
	if n == "" || strings.Contains(n, "\\") || path.IsAbs(n) || filepath.IsAbs(n) || filepath.VolumeName(n) != "" {
		return "", false
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", false
		}
	}
	c := path.Clean(n)
	if c == "." {
		return "", true
	}
	return c, true
}

func writeEntry(r io.Reader, target string, h *tar.Header, platform string, seen map[string]string) error {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(r, h.Size)); err != nil {
		return err
	}
	data := buf.Bytes()
	if strings.HasPrefix(platform, "darwin/") {
		data = thin(data, platform)
	}
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	mode := fs.FileMode(0o644)
	if h.FileInfo().Mode()&0o111 != 0 {
		mode = 0o755
	}
	if first, ok := seen[key]; ok {
		if fi, err := os.Stat(first); err == nil && fi.Mode().Perm() == mode {
			if os.Link(first, target) == nil {
				return nil
			}
		}
	}
	if err := os.WriteFile(target, data, mode); err != nil {
		return err
	}
	if len(data) > 0 {
		if _, ok := seen[key]; !ok {
			seen[key] = target
		}
	}
	return nil
}

// thin returns this platform's slice of a universal Mach-O file, or data unchanged. Each slice is
// a complete program with its own code signature, so the slice runs as it is.
func thin(data []byte, platform string) []byte {
	if len(data) < 8 || !bytes.Equal(data[:4], []byte{0xca, 0xfe, 0xba, 0xbe}) {
		return data
	}
	ff, err := macho.NewFatFile(bytes.NewReader(data))
	if err != nil {
		return data
	}
	defer ff.Close()
	want := macho.CpuArm64
	if strings.HasSuffix(platform, "/amd64") {
		want = macho.CpuAmd64
	}
	for _, a := range ff.Arches {
		if a.Cpu == want && uint64(a.Offset)+uint64(a.Size) <= uint64(len(data)) {
			return bytes.Clone(data[a.Offset : a.Offset+a.Size])
		}
	}
	return data
}
