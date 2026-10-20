package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"strings"

	"github.com/deedglass/deedglass/internal/deed"
)

// Reader reads one export bundle from a stream, in order, without writing anything to disk.
// Open reads README.txt and keys.json; Deeds streams deeds.jsonl; Finish reads the manifest and its
// seal and checks every file against the manifest.
type Reader struct {
	tr      *tar.Reader
	cap     *capReader
	zr      *gzip.Reader
	lim     Limits
	seen    map[string]bool
	entries int

	readme, keys []byte
	timestamps   []byte // nil when the bundle has no timestamps.json
	deeds        *deedsReader
	manifest     []byte
}

var errTooBig = errors.New("archive: expands past the limit")

// Open starts reading a bundle. A zero Limits means DefaultLimits. It returns a *deed.VerifyError
// with code bundle_invalid for anything that is not a well-formed v1 bundle.
func Open(r io.Reader, lim Limits) (*Reader, error) {
	if lim == (Limits{}) {
		lim = DefaultLimits()
	}
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, invalid("This file is not a gzip-compressed deed bundle.")
	}
	zr.Multistream(false)
	cr := &capReader{r: zr, left: lim.total()}
	rd := &Reader{tr: tar.NewReader(cr), cap: cr, zr: zr, lim: lim, seen: map[string]bool{}}
	if rd.readme, err = rd.small(NameREADME); err != nil {
		return nil, err
	}
	if rd.keys, err = rd.small(NameKeys); err != nil {
		return nil, err
	}
	// timestamps.json is optional and comes before the deeds, so a reader knows which heads to
	// compare while it streams (TY-03).
	h, err := rd.tr.Next()
	switch {
	case errors.Is(err, io.EOF):
		return nil, invalid("This bundle has no %s.", NameDeeds)
	case err != nil:
		return nil, rd.damaged(err)
	}
	if err := rd.check(h); err != nil {
		return nil, err
	}
	if h.Name == NameTimestamps {
		if rd.timestamps, err = rd.read(h); err != nil {
			return nil, err
		}
		if rd.timestamps == nil {
			rd.timestamps = []byte{}
		}
		if _, err := rd.next(NameDeeds); err != nil {
			return nil, err
		}
	} else if h.Name != NameDeeds {
		return nil, invalid("This bundle's files are out of order: found %q where %s should be.", h.Name, NameDeeds)
	}
	rd.deeds = &deedsReader{r: rd.tr, sum: sha256.New(), damaged: rd.damaged}
	return rd, nil
}

// README returns README.txt.
func (r *Reader) README() []byte { return r.readme }

// Keys returns keys.json as it is in the bundle (the exporter's keys: integrity, not authenticity).
func (r *Reader) Keys() []byte { return r.keys }

// Timestamps returns timestamps.json as it is in the bundle, or nil when there is none. Its bytes
// are checked against the manifest in Finish; stamp.ParseFile reads them.
func (r *Reader) Timestamps() []byte { return r.timestamps }

// Deeds streams deeds.jsonl, hashing as it goes.
func (r *Reader) Deeds() io.Reader { return r.deeds }

// ManifestBytes returns manifest.json's exact bytes (after Finish), which its seal signs.
func (r *Reader) ManifestBytes() []byte { return r.manifest }

// Finish reads the rest of deeds.jsonl if the caller stopped early, then manifest.json and the
// optional manifest.seal.json, and checks every file's size and hash (and the line count) against
// the manifest. The seal is returned unverified (nil when absent): deed.VerifyBundleSeal checks it
// with the key set the caller trusts.
func (r *Reader) Finish() (*Manifest, *deed.BundleSeal, error) {
	if _, err := io.Copy(io.Discard, r.deeds); err != nil {
		return nil, nil, err
	}
	mb, err := r.small(NameManifest)
	if err != nil {
		return nil, nil, err
	}
	m, err := parseManifest(mb)
	if err != nil {
		return nil, nil, err
	}
	r.manifest = mb
	var seal *deed.BundleSeal
	switch h, err := r.tr.Next(); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, nil, r.damaged(err)
	default:
		if err := r.check(h); err != nil {
			return nil, nil, err
		}
		if h.Name != NameManifestSeal {
			return nil, nil, invalid("This bundle's files are out of order: %q comes after manifest.json.", h.Name)
		}
		sb, err := r.read(h)
		if err != nil {
			return nil, nil, err
		}
		if seal, err = deed.ParseBundleSeal(sb); err != nil {
			return nil, nil, invalid("This bundle's manifest seal is not valid JSON of the right shape.")
		}
		if c, err := deed.StrictCanonical(seal); err != nil || !bytes.Equal(c, sb) {
			return nil, nil, invalid("This bundle's manifest seal is not canonical JSON.")
		}
		switch h, err := r.tr.Next(); {
		case errors.Is(err, io.EOF):
		case err != nil:
			return nil, nil, r.damaged(err)
		default:
			if err := r.check(h); err != nil {
				return nil, nil, err
			}
			return nil, nil, invalid("This bundle has more than %d entries.", r.maxEntries())
		}
	}
	// Read to the end of the gzip stream so its checksum is checked.
	if _, err := io.Copy(io.Discard, r.cap); err != nil {
		return nil, nil, r.damaged(err)
	}
	small := []struct {
		name string
		b    []byte
	}{{NameREADME, r.readme}, {NameKeys, r.keys}}
	_, listed := m.Files[NameTimestamps]
	switch {
	case r.timestamps != nil:
		small = append(small, struct {
			name string
			b    []byte
		}{NameTimestamps, r.timestamps})
		if !listed {
			return nil, nil, invalid("This bundle's manifest does not list %s.", NameTimestamps)
		}
	case listed:
		return nil, nil, invalid("This bundle has no %s, but its manifest lists one.", NameTimestamps)
	}
	for _, f := range small {
		sum := sha256.Sum256(f.b)
		if want := m.Files[f.name]; want.Bytes != int64(len(f.b)) || want.SHA256 != hex.EncodeToString(sum[:]) {
			return nil, nil, invalid("This bundle's manifest does not match %s.", f.name)
		}
	}
	d := m.Files[NameDeeds]
	switch {
	case d.Bytes != r.deeds.n:
		return nil, nil, invalid("This bundle's manifest does not match deeds.jsonl (size).")
	case d.SHA256 != hex.EncodeToString(r.deeds.sum.Sum(nil)):
		return nil, nil, invalid("This bundle's manifest does not match deeds.jsonl (sha256).")
	case *d.Lines != r.deeds.lines:
		return nil, nil, invalid("This bundle's manifest does not match deeds.jsonl (line count).")
	}
	return m, seal, nil
}

// small reads the next entry, which must be name, into memory.
func (r *Reader) small(name string) ([]byte, error) {
	h, err := r.next(name)
	if err != nil {
		return nil, err
	}
	return r.read(h)
}

func (r *Reader) read(h *tar.Header) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.tr, r.lim.of(h.Name)+1))
	if err != nil {
		return nil, r.damaged(err)
	}
	if int64(len(b)) > r.lim.of(h.Name) {
		return nil, tooLarge(h.Name, r.lim.of(h.Name))
	}
	return b, nil
}

// next reads the next header, which must be want.
func (r *Reader) next(want string) (*tar.Header, error) {
	h, err := r.tr.Next()
	if errors.Is(err, io.EOF) {
		return nil, invalid("This bundle has no %s.", want)
	}
	if err != nil {
		return nil, r.damaged(err)
	}
	if err := r.check(h); err != nil {
		return nil, err
	}
	if h.Name != want {
		return nil, invalid("This bundle's files are out of order: found %q where %s should be.", h.Name, want)
	}
	return h, nil
}

// check applies the entry rules before any byte of the entry is used.
func (r *Reader) check(h *tar.Header) error {
	r.entries++
	name := h.Name
	known := false
	for _, n := range order {
		known = known || n == name
	}
	switch {
	case !known || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || strings.HasPrefix(name, "."):
		return invalid("This bundle contains an entry named %q.", name)
	case h.Typeflag != tar.TypeReg || h.Linkname != "":
		return invalid("This bundle contains %q, which is not a regular file.", name)
	case r.seen[name]:
		return invalid("This bundle contains %q twice.", name)
	case r.entries > r.maxEntries():
		return invalid("This bundle has more than %d entries.", r.maxEntries())
	case h.Size < 0 || h.Size > r.lim.of(name):
		return tooLarge(name, r.lim.of(name))
	}
	r.seen[name] = true
	return nil
}

// maxEntries is 5, or 6 for a bundle with timestamps.json.
func (r *Reader) maxEntries() int {
	if r.timestamps != nil || r.seen[NameTimestamps] {
		return 6
	}
	return 5
}

func (r *Reader) damaged(err error) error {
	if errors.Is(err, errTooBig) {
		return invalid("This bundle expands to more than %s.", size(r.lim.total()))
	}
	return invalid("This bundle is damaged (%s).", strings.TrimPrefix(strings.TrimPrefix(err.Error(), "archive/tar: "), "gzip: "))
}

func tooLarge(name string, lim int64) error {
	return invalid("This bundle's %s is larger than %s.", name, size(lim))
}

// size prints a limit as 64 KiB, 1 MiB or 8 GiB.
func size(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return deed.FormatCount(n>>30) + " GiB"
	case n >= 1<<20 && n%(1<<20) == 0:
		return deed.FormatCount(n>>20) + " MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return deed.FormatCount(n>>10) + " KiB"
	}
	return deed.FormatCount(n) + " bytes"
}

// deedsReader hashes and counts deeds.jsonl as the verifier reads it.
type deedsReader struct {
	r       io.Reader
	sum     hash.Hash
	n       int64
	lines   int64
	damaged func(error) error
}

// Read passes deeds.jsonl through; a damaged stream becomes bundle_invalid.
func (d *deedsReader) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	if n > 0 {
		d.sum.Write(p[:n])
		d.n += int64(n)
		d.lines += int64(bytes.Count(p[:n], []byte{'\n'}))
	}
	if err != nil && !errors.Is(err, io.EOF) {
		err = d.damaged(err)
	}
	return n, err
}

// capReader fails once more than left bytes have been decompressed (a gzip bomb).
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errTooBig
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
