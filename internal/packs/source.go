package packs

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// FromDir reads a local copy of the policy-packs repo. Symbolic links are refused, so a copy can't
// point outside itself.
func FromDir(dir string) (*Source, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Source{Origin: dir, Files: map[string][]byte{}}
	total := 0
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !d.Type().IsRegular() {
			return &FormatError{File: rel, Why: "is not a regular file (links are refused)"}
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
		if err != nil {
			return err
		}
		return s.add(rel, b, &total)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// FromTarGz reads a .tar.gz of the policy-packs repo. Only regular files with clean relative
// names are accepted; everything is bounded. When the archive holds a single top directory (a
// GitHub release tarball), that directory is the root.
func FromTarGz(r io.Reader, origin string) (*Source, error) {
	zr, err := gzip.NewReader(io.LimitReader(r, MaxTotalBytes))
	if err != nil {
		return nil, &FormatError{File: origin, Why: "isn't a gzip file"}
	}
	defer zr.Close()
	tr := tar.NewReader(io.LimitReader(zr, 2*MaxTotalBytes))
	raw := &Source{Origin: origin, Files: map[string][]byte{}}
	total := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, &FormatError{File: origin, Why: "isn't a readable tar archive"}
		}
		name := strings.TrimPrefix(h.Name, "./")
		switch h.Typeflag {
		case tar.TypeDir, tar.TypeXGlobalHeader:
			continue
		case tar.TypeReg:
		default:
			return nil, &FormatError{File: name, Why: "is not a regular file (links and devices are refused)"}
		}
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return nil, &FormatError{File: name, Why: "has a name outside the archive"}
		}
		if h.Size > MaxFileBytes {
			return nil, &FormatError{File: name, Why: fmt.Sprintf("is larger than %d KiB", MaxFileBytes>>10)}
		}
		b, err := io.ReadAll(io.LimitReader(tr, MaxFileBytes+1))
		if err != nil {
			return nil, &FormatError{File: name, Why: "couldn't be read"}
		}
		if err := raw.add(name, b, &total); err != nil {
			return nil, err
		}
	}
	return stripTop(raw), nil
}

// stripTop drops a single shared top directory when index.json isn't at the root.
func stripTop(s *Source) *Source {
	if _, ok := s.Files[IndexFile]; ok {
		return s
	}
	top := ""
	for p := range s.Files {
		i := strings.IndexByte(p, '/')
		if i < 0 {
			return s
		}
		if top == "" {
			top = p[:i]
		} else if p[:i] != top {
			return s
		}
	}
	out := &Source{Origin: s.Origin, Files: make(map[string][]byte, len(s.Files))}
	for p, b := range s.Files {
		out.Files[strings.TrimPrefix(p, top+"/")] = b
	}
	return out
}

// LatestURL is the one network path: the newest policy-packs release asset.
const LatestURL = "https://github.com/deedglass/policy-packs/releases/latest/download/policy-packs.tar.gz"

// FetchTimeout bounds the whole download.
const FetchTimeout = 20 * time.Second

// redirectHosts are the hosts a release download may move to. The seal is what makes a download
// trusted; this only keeps the client on GitHub.
var redirectHosts = []string{"github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"}

// Fetch downloads the archive at u with a client that follows no redirects on its own (the egress
// first-party client); it follows at most 3 https redirects to GitHub's hosts.
func Fetch(ctx context.Context, c *http.Client, u string) (*Source, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	for hop := 0; ; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(req)
		if err != nil {
			return nil, fmt.Errorf("packs: couldn't reach %s: %w", req.URL.Host, err)
		}
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			loc, err := resp.Location()
			resp.Body.Close()
			if err != nil || hop >= 3 || !allowedRedirect(loc) {
				return nil, fmt.Errorf("packs: the download redirected somewhere it shouldn't")
			}
			u = loc.String()
			continue
		case resp.StatusCode != http.StatusOK:
			resp.Body.Close()
			return nil, fmt.Errorf("packs: the download answered %d", resp.StatusCode)
		}
		defer resp.Body.Close()
		return FromTarGz(resp.Body, LatestURL)
	}
}

func allowedRedirect(u *url.URL) bool {
	return u.Scheme == "https" && u.User == nil && slices.Contains(redirectHosts, u.Hostname()) && u.Port() == ""
}
