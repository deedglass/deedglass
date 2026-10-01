package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// Header is what a writer knows before the first deed: who exports, when, and the range it will
// write (README.txt, the first file, states it).
type Header struct {
	Org       string
	Issuer    string // the issuer's base URL, e.g. https://api.deedglass.com
	CreatedAt time.Time
	Generator Generator
	FirstSeq  int64
	LastSeq   int64
	From, To  string // issued_at of the first and last deed
}

// Writer streams one export bundle. Call WriteDeed once per deed in seq order, then Close.
type Writer struct {
	gz    *gzip.Writer
	tw    *tar.Writer
	h     Header
	mtime time.Time
	files map[string]File

	wantBytes, wantLines int64
	n, lines             int64
	sum                  hash.Hash
	kinds                map[string]int64
	firstPrev            string
	head                 ChainHead
	lastTime             string
	err                  error
}

// ErrWriter is every Writer misuse: the deeds don't match what the header and sizes promised.
var ErrWriter = errors.New("archive: writer")

// NewWriter writes README.txt and keys.json and opens deeds.jsonl. deedsBytes (every line plus its
// '\n') and deedsLines must be exact: tar headers carry sizes, so S06 measures them in a first pass.
func NewWriter(w io.Writer, h Header, keysJSON []byte, deedsBytes, deedsLines int64) (*Writer, error) {
	switch {
	case h.Org == "" || h.Issuer == "":
		return nil, fmt.Errorf("%w: org and issuer are required", ErrWriter)
	case deedsLines < 1 || h.FirstSeq < 1 || h.LastSeq-h.FirstSeq+1 != deedsLines:
		return nil, fmt.Errorf("%w: the range %d–%d does not hold %d deeds", ErrWriter, h.FirstSeq, h.LastSeq, deedsLines)
	case deedsBytes < 2*deedsLines:
		return nil, fmt.Errorf("%w: %d bytes cannot hold %d deeds", ErrWriter, deedsBytes, deedsLines)
	case int64(len(keysJSON)) > DefaultLimits().Keys:
		return nil, fmt.Errorf("%w: keys.json is larger than 1 MiB", ErrWriter)
	}
	if _, err := deed.ParseKeySet(keysJSON); err != nil {
		return nil, fmt.Errorf("%w: keys.json: %w", ErrWriter, err)
	}
	gz, _ := gzip.NewWriterLevel(w, gzip.DefaultCompression)
	gz.Header = gzip.Header{OS: 255} // no name, mtime 0, unknown OS: the same bytes on every machine
	wr := &Writer{gz: gz, tw: tar.NewWriter(gz), h: h, mtime: h.CreatedAt.UTC().Truncate(time.Second),
		files: map[string]File{}, wantBytes: deedsBytes, wantLines: deedsLines, sum: sha256.New(),
		kinds: map[string]int64{"action": 0, "admin": 0, "amendment": 0}}
	if err := wr.file(NameREADME, readme(h)); err != nil {
		return nil, err
	}
	if err := wr.file(NameKeys, keysJSON); err != nil {
		return nil, err
	}
	if err := wr.tw.WriteHeader(wr.header(NameDeeds, deedsBytes)); err != nil {
		return nil, err
	}
	return wr, nil
}

func (w *Writer) header(name string, size int64) *tar.Header {
	f := tar.FormatUSTAR
	if size > 1<<33-1 { // USTAR's 11 octal digits
		f = tar.FormatPAX
	}
	return &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: size, Mode: 0o644, ModTime: w.mtime, Format: f}
}

// file writes one small file and records its size and hash for the manifest.
func (w *Writer) file(name string, b []byte) error {
	if err := w.tw.WriteHeader(w.header(name, int64(len(b)))); err != nil {
		return err
	}
	if _, err := w.tw.Write(b); err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	w.files[name] = File{Bytes: int64(len(b)), SHA256: hex.EncodeToString(sum[:])}
	return nil
}

// facts is what the writer reads from each deed for the manifest.
type facts struct {
	Body struct {
		Kind     string `json:"kind"`
		DeedID   string `json:"deed_id"`
		Org      string `json:"org"`
		Seq      int64  `json:"seq"`
		PrevHash string `json:"prev_hash"`
		IssuedAt string `json:"issued_at"`
	} `json:"body"`
	BodyHash string `json:"body_hash"`
}

// WriteDeed appends one deed (its stored JCS bytes) and a '\n'. Deeds must come in seq order with
// no gap, all from the header's org.
func (w *Writer) WriteDeed(canonicalEnvelope []byte) error {
	if w.err != nil {
		return w.err
	}
	fail := func(format string, a ...any) error {
		w.err = fmt.Errorf("%w: deed %d: %s", ErrWriter, w.lines+1, fmt.Sprintf(format, a...))
		return w.err
	}
	if len(canonicalEnvelope) == 0 || len(canonicalEnvelope) > deed.MaxEnvelopeBytes || bytes.IndexByte(canonicalEnvelope, '\n') >= 0 {
		return fail("not one canonical envelope")
	}
	var f facts
	if err := json.Unmarshal(canonicalEnvelope, &f); err != nil {
		return fail("not JSON")
	}
	b := f.Body
	want := w.h.FirstSeq + w.lines
	switch {
	case b.Org != w.h.Org:
		return fail("org %s, want %s", b.Org, w.h.Org)
	case b.Seq != want:
		return fail("seq %d, want %d", b.Seq, want)
	case w.lines > 0 && b.PrevHash != w.head.BodyHash:
		return fail("prev_hash does not link to deed %d", w.head.Seq)
	case w.lines == 0 && b.Seq == 1 && b.PrevHash != deed.Genesis(w.h.Org):
		return fail("deed 1 does not link to the org's genesis")
	}
	if _, ok := w.kinds[b.Kind]; !ok {
		return fail("kind %q", b.Kind)
	}
	if w.n+int64(len(canonicalEnvelope))+1 > w.wantBytes || w.lines+1 > w.wantLines {
		return fail("more bytes or lines than NewWriter was told")
	}
	line := append(canonicalEnvelope[:len(canonicalEnvelope):len(canonicalEnvelope)], '\n')
	if _, err := w.tw.Write(line); err != nil {
		w.err = err
		return err
	}
	w.sum.Write(line)
	w.n += int64(len(line))
	if w.lines == 0 {
		w.firstPrev = b.PrevHash
		if b.IssuedAt != w.h.From {
			return fail("issued_at %s, but the header says the range starts at %s", b.IssuedAt, w.h.From)
		}
	}
	w.lines++
	w.kinds[b.Kind]++
	w.head = ChainHead{Seq: b.Seq, DeedID: b.DeedID, BodyHash: f.BodyHash}
	w.lastTime = b.IssuedAt
	return nil
}

// Close finishes deeds.jsonl, writes manifest.json and, when s is not nil, manifest.seal.json
// sealed with s (the exporter's active seal key), then closes the tar and gzip streams. It does not
// close the underlying writer.
func (w *Writer) Close(s deed.Signer) (*Manifest, error) {
	if w.err != nil {
		return nil, w.err
	}
	if w.n != w.wantBytes || w.lines != w.wantLines {
		return nil, fmt.Errorf("%w: wrote %d bytes in %d deeds; NewWriter was told %d in %d", ErrWriter, w.n, w.lines, w.wantBytes, w.wantLines)
	}
	if w.lastTime != w.h.To {
		return nil, fmt.Errorf("%w: the last deed was issued at %s, but the header says %s", ErrWriter, w.lastTime, w.h.To)
	}
	lines := w.lines
	w.files[NameDeeds] = File{Bytes: w.n, SHA256: hex.EncodeToString(w.sum.Sum(nil)), Lines: &lines}
	anchor := ChainAnchor{Kind: "genesis", Seq: 0, BodyHash: w.firstPrev}
	if w.h.FirstSeq > 1 {
		anchor = ChainAnchor{Kind: "deed", Seq: w.h.FirstSeq - 1, BodyHash: w.firstPrev}
	}
	m := &Manifest{Object: "deed_bundle_manifest", V: 1, Org: w.h.Org, Issuer: w.h.Issuer,
		CreatedAt: deed.FormatTime(w.h.CreatedAt), Generator: w.h.Generator,
		Range: Range{FirstSeq: w.h.FirstSeq, LastSeq: w.h.LastSeq, Count: w.lines, From: w.h.From, To: w.h.To},
		Chain: ManifestChain{Anchor: anchor, Head: w.head}, Kinds: w.kinds, Files: w.files}
	mb, err := m.marshal()
	if err != nil {
		return nil, err
	}
	if err := w.file(NameManifest, mb); err != nil {
		return nil, err
	}
	delete(w.files, NameManifest) // the manifest does not list itself
	if s != nil {
		bs, err := deed.SealManifest(s, mb)
		if err != nil {
			return nil, err
		}
		sb, err := deed.StrictCanonical(bs)
		if err != nil {
			return nil, err
		}
		if err := w.file(NameManifestSeal, sb); err != nil {
			return nil, err
		}
		delete(w.files, NameManifestSeal)
	}
	if err := w.tw.Close(); err != nil {
		return nil, err
	}
	if err := w.gz.Close(); err != nil {
		return nil, err
	}
	return m, nil
}
