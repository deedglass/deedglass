package securitypack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/deed"
)

// The pack folder's files. aliases.private.json is never sealed and never sent.
const (
	FileIndex    = "index.html"
	FileAnswers  = "answers.json"
	FileMarkdown = "answers.md"
	FileRecords  = "records.tar.gz"
	FileSeal     = "pack.seal.json"
	FileReadme   = "README.txt"
	FileAliases  = "aliases.private.json"
)

// MediaTypes is each sealed file's media type (ledger.FileMediaTypes must hold all of them).
var MediaTypes = map[string]string{
	FileIndex:    "text/html",
	FileAnswers:  "application/json",
	FileMarkdown: "text/markdown",
	FileRecords:  "application/gzip",
	FileReadme:   "text/plain",
}

// SealedOrder is the order files are sealed and listed in pack.seal.json.
var SealedOrder = []string{FileReadme, FileIndex, FileMarkdown, FileAnswers, FileRecords}

// ObjectSeal is pack.seal.json's object name.
const ObjectSeal = "security_pack_seal"

// SealedFile is one file's sealed record: what was sealed and the file.sealed deed that seals it.
type SealedFile struct {
	Path      string          `json:"path"`
	SHA256    string          `json:"sha256"`
	Bytes     int64           `json:"bytes"`
	MediaType string          `json:"media_type"`
	Label     string          `json:"label"`
	Deed      json.RawMessage `json:"deed"`
}

// SealFile is pack.seal.json: the sealed records over the folder's files and the public seal keys
// that check them, so `deedglass security-pack verify` works offline without an account.
type SealFile struct {
	Object string          `json:"object"`
	V      int             `json:"v"`
	Org    string          `json:"org"`
	Files  []SealedFile    `json:"files"`
	Keys   json.RawMessage `json:"keys"`
}

// FileLabel is the label sealed with a pack file: the file name and the org, never a name.
func FileLabel(name, org string) string { return "Security review pack · " + name + " · " + org }

// fileSealedData mirrors ledger.FileSealedData: the deed's data_digest is the digest of its JCS
// (a test keeps the two equal; the core doesn't import the ledger).
type fileSealedData struct {
	Bytes     int64  `json:"bytes"`
	Label     string `json:"label"`
	MediaType string `json:"media_type"`
	SHA256    string `json:"sha256"`
}

// HashBytes is sha256:<hex> of b.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return deed.DigestPrefix + hex.EncodeToString(s[:])
}

// HashFile streams a file through SHA-256.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return deed.DigestPrefix + hex.EncodeToString(h.Sum(nil)), n, nil
}

// MarshalSeal is pack.seal.json's bytes: compact (the deeds stay byte-exact JCS) plus a newline.
func MarshalSeal(s *SealFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// FileResult is one file's check.
type FileResult struct {
	Path    string `json:"path"`
	OK      bool   `json:"ok"`
	Deed    string `json:"deed,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Bytes   int64  `json:"bytes"`
	Problem string `json:"problem,omitempty"`
}

// FolderReport is a pack folder's check (without the records bundle's own chain check, which the
// CLI runs with deedglass verify's code on Records).
type FolderReport struct {
	OK       bool         `json:"ok"`
	Org      string       `json:"org"`
	Files    []FileResult `json:"files"`
	Problems []string     `json:"problems"`
	Warnings []string     `json:"warnings"`
	// Pack is answers.json, parsed strictly (nil when it couldn't be).
	Pack *Pack `json:"-"`
	// Keys are the seal keys the deeds were checked with.
	Keys *deed.KeySet `json:"-"`
	// SealKeys are the key ids that sealed the files (the records bundle must carry them).
	SealKeys []string `json:"seal_keys"`
	// Records is the records bundle's path when the pack carries one and it matched its seal.
	Records string `json:"records,omitempty"`
}

func (r *FolderReport) fail(format string, a ...any) {
	r.OK = false
	r.Problems = append(r.Problems, fmt.Sprintf(format, a...))
}

// MaxSealBytes bounds pack.seal.json.
const MaxSealBytes = 1 << 20

var rePlainName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)

// VerifyFolder checks a pack folder offline: pack.seal.json is strict, every sealed file's bytes
// match its sealed digest and size, every file.sealed deed verifies with the keys (trusted when
// given, else the ones inside the pack) and belongs to the pack's org, answers.json is a valid
// closed security_pack v1 of the same org, and every file the pack needs is sealed. It never
// writes and never reaches the network.
func VerifyFolder(dir string, trusted *deed.KeySet) *FolderReport {
	r := &FolderReport{OK: true, Problems: []string{}, Warnings: []string{}, Files: []FileResult{}, SealKeys: []string{}}
	raw, err := readCapped(filepath.Join(dir, FileSeal), MaxSealBytes)
	if err != nil {
		r.fail("%s can't be read: %s. Every pack folder has one.", FileSeal, reasonOf(err))
		return r
	}
	var s SealFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil || dec.More() || s.Object != ObjectSeal || s.V != 1 {
		r.fail("%s isn't a security_pack_seal v1.", FileSeal)
		return r
	}
	r.Org = s.Org
	ks := trusted
	if ks == nil {
		ks, err = deed.ParseKeySet(s.Keys)
		if err != nil {
			r.fail("%s carries keys that can't be used: %s", FileSeal, reasonOf(err))
			return r
		}
	}
	r.Keys = ks
	sealed := map[string]SealedFile{}
	for _, f := range s.Files {
		if !rePlainName.MatchString(f.Path) || MediaTypes[f.Path] == "" {
			r.fail("%s seals %q, which isn't a pack file.", FileSeal, f.Path)
			continue
		}
		if _, dup := sealed[f.Path]; dup {
			r.fail("%s seals %s twice.", FileSeal, f.Path)
			continue
		}
		sealed[f.Path] = f
		r.Files = append(r.Files, checkSealed(dir, f, s.Org, ks, r))
	}
	// answers.json: strict, the same org, and it says whether records belong in the folder.
	if _, ok := sealed[FileAnswers]; ok && fileOK(r, FileAnswers) {
		p, err := readPack(filepath.Join(dir, FileAnswers))
		switch {
		case err != nil:
			r.fail("%s: %s", FileAnswers, reasonOf(err))
		case p.Org != s.Org:
			r.fail("%s is about %s but the seals are %s's.", FileAnswers, p.Org, s.Org)
		default:
			r.Pack = p
			if p.Records != nil && p.Records.SHA256 != sealed[FileRecords].SHA256 {
				r.fail("%s names records %s, but %s seals %s.", FileAnswers, short(p.Records.SHA256), FileSeal, short(sealed[FileRecords].SHA256))
			}
		}
	}
	need := []string{FileReadme, FileIndex, FileMarkdown, FileAnswers}
	if r.Pack != nil && r.Pack.Records != nil {
		need = append(need, FileRecords)
	}
	for _, n := range need {
		if _, ok := sealed[n]; !ok {
			r.fail("%s isn't sealed in %s, so it can't be checked.", n, FileSeal)
		}
	}
	if _, ok := sealed[FileRecords]; ok && fileOK(r, FileRecords) {
		r.Records = filepath.Join(dir, FileRecords)
	}
	// Anything else in the folder isn't covered by the seal: say so, without failing.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if _, ok := sealed[n]; ok || n == FileSeal || n == FileAliases {
			continue
		}
		r.Warnings = append(r.Warnings, n+" isn't sealed; it isn't part of what was checked.")
	}
	if _, err := os.Stat(filepath.Join(dir, FileAliases)); err == nil {
		r.Warnings = append(r.Warnings, FileAliases+" is in the folder. It maps aliases to real names: don't send it.")
	}
	keys := map[string]bool{}
	for _, f := range s.Files {
		if env, err := deed.ParseEnvelope(f.Deed); err == nil {
			keys[env.SealKeyID] = true
		}
	}
	for k := range keys {
		r.SealKeys = append(r.SealKeys, k)
	}
	sort.Strings(r.SealKeys)
	return r
}

func fileOK(r *FolderReport, name string) bool {
	for _, f := range r.Files {
		if f.Path == name {
			return f.OK
		}
	}
	return false
}

// checkSealed compares one file with its sealed record and checks the record itself.
func checkSealed(dir string, f SealedFile, org string, ks *deed.KeySet, r *FolderReport) FileResult {
	fr := FileResult{Path: f.Path, Bytes: f.Bytes}
	bad := func(msg string) FileResult {
		fr.Problem = msg
		r.fail("%s: %s", f.Path, msg)
		return fr
	}
	res, err := deed.VerifyEnvelope(f.Deed, ks, deed.VerifyOptions{Strict: true})
	if err != nil {
		var ve *deed.VerifyError
		if errors.As(err, &ve) {
			msg, _ := deed.Message(ve)
			return bad("its sealed record doesn't check: " + msg)
		}
		return bad("its sealed record doesn't check.")
	}
	fr.Deed = res.DeedID
	b := res.Envelope.Body
	if b.Org != org {
		return bad(fmt.Sprintf("its sealed record belongs to %s, not %s.", b.Org, org))
	}
	if b.Kind != "admin" || b.Admin == nil || b.Admin.Action != "file.sealed" || b.Admin.Reason == nil || b.Admin.DataDigest == nil {
		return bad("its record isn't a sealed file.")
	}
	want, err := deed.DigestValue(fileSealedData{Bytes: f.Bytes, Label: f.Label, MediaType: f.MediaType, SHA256: f.SHA256})
	if err != nil || want != *b.Admin.DataDigest || *b.Admin.Reason != f.Label || f.MediaType != MediaTypes[f.Path] {
		return bad("its listing in " + FileSeal + " doesn't match its sealed record.")
	}
	sum, n, err := HashFile(filepath.Join(dir, f.Path))
	if err != nil {
		return bad("it can't be read: " + reasonOf(err) + ".")
	}
	fr.SHA256 = sum
	if sum != f.SHA256 || n != f.Bytes {
		return bad(fmt.Sprintf("it was changed after it was sealed. Its sha256 is %s (%s bytes); the sealed file's is %s (%s bytes).",
			short(sum), formatCount(n), short(f.SHA256), formatCount(f.Bytes)))
	}
	fr.OK = true
	return fr
}

func readPack(path string) (*Pack, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParsePack(f)
}

func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("larger than %s bytes", formatCount(max))
	}
	return b, nil
}

func reasonOf(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "it doesn't exist"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	}
	return strings.TrimSuffix(err.Error(), ".")
}

// short is the first 12 hex digits of a sha256:… digest.
func short(d string) string {
	h := strings.TrimPrefix(d, deed.DigestPrefix)
	if len(h) > 12 {
		h = h[:12]
	}
	return h
}
