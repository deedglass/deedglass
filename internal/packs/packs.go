// Package packs reads, checks and signs Deedglass policy packs (S47 §3.8): a signed index of
// packs, each a policy, its test cases, pack.yaml and a README. The binary carries a snapshot
// (Embedded); `deedglass policy install` can also read a local copy or the policy-packs release.
//
// Trust: index.json is JCS; index.seal.json is an Ed25519 seal over
// "deedglass/packs/v1\n" + sha256(index.json) by a key in pack-keys.json. Every file a pack lists
// is checked against its sha256 in the index before anything is compiled or written. A pack from
// outside the binary must be sealed by a key this build knows; the embedded snapshot is checked by
// its seal when it has one, and by its hashes always.
//
// The package never reads a licence. A Pro pack asks the caller's Entitlements for FeaturePro;
// the CLI answers through cmd/deedglass/licence.go, the one place that reads a licence.
package packs

import (
	"bytes"
	"crypto/ed25519"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/keys"
	"go.yaml.in/yaml/v3"
)

// Tag is the seal's domain tag (contract §7).
const Tag = "deedglass/packs/v1\n"

const (
	ObjectIndex  = "pack_index"
	ObjectKeySet = "pack_key_set"
	IndexFile    = "index.json"
	SealFile     = "index.seal.json"
	MetaFile     = "pack.yaml"
	ReadmeFile   = "README.md"
	TierFree     = "free"
	TierPro      = "pro"
	// FeaturePro is the licence feature a Pro pack needs (licence.FeaturePacksPro; the string is
	// repeated here so this package never imports the licence code).
	FeaturePro = "packs_pro"
	// MinCases is the fewest test cases a pack may carry.
	MinCases = 12
	// Limits for any source: a file, the whole source, and the number of files.
	MaxFileBytes  = 512 << 10
	MaxTotalBytes = 4 << 20
	MaxFiles      = 256
)

// Entitlements answers whether a licence feature is on. A nil Entitlements has none.
type Entitlements interface{ Has(feature string) bool }

// None is the Entitlements of a machine without a licence.
type None struct{}

func (None) Has(string) bool { return false }

var (
	reName    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
	reVersion = regexp.MustCompile(`^(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})$`)
	rePackID  = regexp.MustCompile(`^pck_[0-9a-hjkmnp-tv-z]{26}$`)
	reKeyID   = regexp.MustCompile(`^sk_[0-9a-hjkmnp-tv-z]{26}$`)
	reHash    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reSeal    = regexp.MustCompile(`^ed25519:[A-Za-z0-9_-]{86}$`)
	b64       = base64.RawURLEncoding.Strict()
)

// Meta is pack.yaml.
type Meta struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	Version       string `yaml:"version"`
	Tier          string `yaml:"tier"`
	Summary       string `yaml:"summary"`
	For           string `yaml:"for"`
	SourceStarter string `yaml:"source_starter"`
}

// Entry is one pack in the index. Files maps each file name (inside the pack's directory) to its
// sha256; SHA256 is the digest of JCS(Files), so one value names the whole pack.
type Entry struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Tier    string            `json:"tier"`
	Summary string            `json:"summary"`
	SHA256  string            `json:"sha256"`
	Cases   int               `json:"cases"`
	Files   map[string]string `json:"files"`
}

// Policy is the pack's policy file name; CasesFile its cases.
func (e Entry) Policy() string    { return e.Name + ".yaml" }
func (e Entry) CasesFile() string { return e.Name + ".cases.yaml" }

// Index is index.json.
type Index struct {
	Object   string  `json:"object"`
	V        int     `json:"v"`
	Packs    []Entry `json:"packs"`
	SignedAt string  `json:"signed_at,omitempty"`
	KeyID    string  `json:"key_id,omitempty"`
}

// Find returns the pack named name, at version when version isn't empty.
func (ix *Index) Find(name, version string) (Entry, bool) {
	for _, e := range ix.Packs {
		if e.Name == name && (version == "" || e.Version == version) {
			return e, true
		}
	}
	return Entry{}, false
}

// Seal is index.seal.json.
type Seal struct {
	Seal  string `json:"seal"`
	KeyID string `json:"key_id"`
}

// SealInput is the exact byte string a seal signs.
func SealInput(indexJSON []byte) []byte { return []byte(Tag + deed.Digest(indexJSON)) }

// Source is a set of files read from the binary, a directory or an archive, with their paths
// relative to the index ("postgres-safe/pack.yaml").
type Source struct {
	Origin string // "embedded", a path, or a URL: for messages
	Files  map[string][]byte
}

// Errors a caller turns into words.
var (
	ErrNoIndex    = errors.New("packs: no index.json")
	ErrUnsigned   = errors.New("packs: the index has no seal")
	ErrKeyUnknown = errors.New("packs: the index is sealed by a key this build doesn't know")
	ErrSeal       = errors.New("packs: the seal doesn't match the index")
	ErrNotFound   = errors.New("packs: no such pack")
)

// MismatchError names the first file that doesn't match the index.
type MismatchError struct{ File, Why string }

func (e *MismatchError) Error() string { return "packs: " + e.File + ": " + e.Why }

// FormatError is an index, seal or pack.yaml that isn't well formed.
type FormatError struct{ File, Why string }

func (e *FormatError) Error() string { return "packs: " + e.File + ": " + e.Why }

//go:embed all:snapshot
var snapshot embed.FS

//go:embed pack-keys.json
var embeddedKeys []byte

// Embedded is the snapshot this binary carries.
func Embedded() *Source {
	sub, err := fs.Sub(snapshot, "snapshot")
	if err != nil {
		panic(err)
	}
	src, err := FromFS(sub, "embedded")
	if err != nil {
		panic(err)
	}
	return src
}

// FromFS reads every regular file of fsys within the limits.
func FromFS(fsys fs.FS, origin string) (*Source, error) {
	s := &Source{Origin: origin, Files: map[string][]byte{}}
	total := 0
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return &FormatError{File: p, Why: "is not a regular file"}
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		return s.add(p, b, &total)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Source) add(p string, b []byte, total *int) error {
	if len(b) > MaxFileBytes {
		return &FormatError{File: p, Why: fmt.Sprintf("is larger than %d KiB", MaxFileBytes>>10)}
	}
	*total += len(b)
	if *total > MaxTotalBytes {
		return &FormatError{File: p, Why: fmt.Sprintf("takes the source over %d MiB", MaxTotalBytes>>20)}
	}
	if len(s.Files) >= MaxFiles {
		return &FormatError{File: p, Why: fmt.Sprintf("is past the limit of %d files", MaxFiles)}
	}
	if _, dup := s.Files[p]; dup {
		return &FormatError{File: p, Why: "appears twice"}
	}
	s.Files[p] = b
	return nil
}

// KeySet is the set of pack keys a build trusts.
type KeySet struct{ keys map[string]ed25519.PublicKey }

// KeyRecord is one key in pack-keys.json.
type KeyRecord struct {
	ID         string `json:"id"`
	Alg        string `json:"alg"`
	PublicKey  string `json:"public_key"`
	Thumbprint string `json:"thumbprint"`
}

type keySetFile struct {
	Object string      `json:"object"`
	Keys   []KeyRecord `json:"keys"`
}

// Record builds the key-set row for a public key.
func Record(id string, pub ed25519.PublicKey) KeyRecord {
	return KeyRecord{ID: id, Alg: "Ed25519", PublicKey: b64.EncodeToString(pub), Thumbprint: deed.Thumbprint(pub)}
}

// KeySetJSON renders pack-keys.json.
func KeySetJSON(recs []KeyRecord) ([]byte, error) {
	if recs == nil {
		recs = []KeyRecord{}
	}
	b, err := json.MarshalIndent(keySetFile{Object: ObjectKeySet, Keys: recs}, "", "  ")
	return append(b, '\n'), err
}

// ParseKeySet reads pack-keys.json strictly; any bad key refuses the whole set.
func ParseKeySet(raw []byte) (KeySet, error) {
	var f keySetFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return KeySet{}, fmt.Errorf("packs: the key set isn't valid JSON: %w", err)
	}
	if f.Object != ObjectKeySet {
		return KeySet{}, errors.New(`packs: the key set's object must be "pack_key_set"`)
	}
	ks := KeySet{keys: map[string]ed25519.PublicKey{}}
	for _, k := range f.Keys {
		if !reKeyID.MatchString(k.ID) {
			return KeySet{}, fmt.Errorf("packs: key %q is not an sk_ id", k.ID)
		}
		if k.Alg != "Ed25519" {
			return KeySet{}, fmt.Errorf("packs: key %s has alg %q; only Ed25519", k.ID, k.Alg)
		}
		raw, err := b64.DecodeString(k.PublicKey)
		if err != nil {
			return KeySet{}, fmt.Errorf("packs: key %s isn't base64url", k.ID)
		}
		pub, err := keys.ParsePublicKey(raw)
		if err != nil {
			return KeySet{}, fmt.Errorf("packs: key %s: %w", k.ID, err)
		}
		if deed.Thumbprint(pub) != k.Thumbprint {
			return KeySet{}, fmt.Errorf("packs: key %s: the thumbprint doesn't match the key", k.ID)
		}
		if _, dup := ks.keys[k.ID]; dup {
			return KeySet{}, fmt.Errorf("packs: key %s is listed twice", k.ID)
		}
		ks.keys[k.ID] = pub
	}
	return ks, nil
}

// Len is the number of keys.
func (ks KeySet) Len() int { return len(ks.keys) }

// EmbeddedKeys is the key set this build trusts. It is empty until the founder generates the pack
// key offline (deedglass keys gen --purpose packs) and commits its public half.
func EmbeddedKeys() (KeySet, error) { return ParseKeySet(embeddedKeys) }

// Checked is a source whose index (and seal, when present) checked out.
type Checked struct {
	Source *Source
	Index  *Index
	Sealed bool // a known key sealed the index
	KeyID  string
}

// Verify checks src: the index is well formed and canonical; the seal, when present (and always
// when requireSeal), was made by a key in ks; every pack's files match their hashes and its
// pack.yaml agrees with the index.
func Verify(src *Source, ks KeySet, requireSeal bool) (*Checked, error) {
	raw, ok := src.Files[IndexFile]
	if !ok {
		return nil, ErrNoIndex
	}
	ix, err := ParseIndex(raw)
	if err != nil {
		return nil, err
	}
	c := &Checked{Source: src, Index: ix}
	sealRaw, sealed := src.Files[SealFile]
	switch {
	case !sealed && requireSeal:
		return nil, ErrUnsigned
	case sealed:
		var s Seal
		dec := json.NewDecoder(bytes.NewReader(sealRaw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil || !reSeal.MatchString(s.Seal) || !reKeyID.MatchString(s.KeyID) {
			return nil, &FormatError{File: SealFile, Why: "isn't a pack seal"}
		}
		if ix.KeyID != s.KeyID {
			return nil, &FormatError{File: SealFile, Why: "names a different key than index.json"}
		}
		pub, ok := ks.keys[s.KeyID]
		if !ok {
			return nil, ErrKeyUnknown
		}
		sig, err := b64.DecodeString(strings.TrimPrefix(s.Seal, deed.SealPrefix))
		if err != nil || !ed25519.Verify(pub, SealInput(raw), sig) {
			return nil, ErrSeal
		}
		c.Sealed, c.KeyID = true, s.KeyID
	}
	for _, e := range ix.Packs {
		if err := checkEntry(src, e); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// ParseIndex reads index.json: strict JSON, canonical (bytes == JCS(bytes)), well-formed entries.
func ParseIndex(raw []byte) (*Index, error) {
	var ix Index
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ix); err != nil {
		return nil, &FormatError{File: IndexFile, Why: "isn't valid JSON"}
	}
	canon, err := deed.StrictCanonical(json.RawMessage(raw))
	if err != nil || !bytes.Equal(canon, raw) {
		return nil, &FormatError{File: IndexFile, Why: "isn't in canonical (JCS) form"}
	}
	if ix.Object != ObjectIndex || ix.V != 1 {
		return nil, &FormatError{File: IndexFile, Why: `isn't a pack_index, version 1`}
	}
	if (ix.KeyID == "") != (ix.SignedAt == "") || (ix.KeyID != "" && !reKeyID.MatchString(ix.KeyID)) {
		return nil, &FormatError{File: IndexFile, Why: "has a bad key_id or signed_at"}
	}
	seen := map[string]bool{}
	for _, e := range ix.Packs {
		switch {
		case !reName.MatchString(e.Name):
			return nil, &FormatError{File: IndexFile, Why: fmt.Sprintf("pack name %q isn't allowed", e.Name)}
		case !rePackID.MatchString(e.ID), !reVersion.MatchString(e.Version), e.Tier != TierFree && e.Tier != TierPro,
			!reHash.MatchString(e.SHA256), e.Cases < MinCases:
			return nil, &FormatError{File: IndexFile, Why: "pack " + e.Name + " has a bad id, version, tier, sha256 or case count"}
		case seen[e.Name+"@"+e.Version]:
			return nil, &FormatError{File: IndexFile, Why: "pack " + e.Name + "@" + e.Version + " is listed twice"}
		}
		seen[e.Name+"@"+e.Version] = true
		want := []string{ReadmeFile, e.CasesFile(), e.Policy(), MetaFile}
		got := slices.Sorted(maps.Keys(e.Files))
		sort.Strings(want)
		if !slices.Equal(got, want) {
			return nil, &FormatError{File: IndexFile, Why: "pack " + e.Name + " must list exactly " + strings.Join(want, ", ")}
		}
		for f, h := range e.Files {
			if !reHash.MatchString(h) {
				return nil, &FormatError{File: IndexFile, Why: "pack " + e.Name + " has a bad hash for " + f}
			}
		}
	}
	return &ix, nil
}

// FilesDigest is an entry's SHA256: the digest of JCS(files).
func FilesDigest(files map[string]string) string {
	m := make(map[string]any, len(files))
	for k, v := range files {
		m[k] = v
	}
	b, err := deed.StrictCanonical(m)
	if err != nil {
		panic(err) // a map of strings always canonicalizes
	}
	return deed.Digest(b)
}

func checkEntry(src *Source, e Entry) error {
	if FilesDigest(e.Files) != e.SHA256 {
		return &MismatchError{File: e.Name, Why: "the pack's sha256 doesn't match its files"}
	}
	for _, f := range slices.Sorted(maps.Keys(e.Files)) {
		b, ok := src.Files[e.Name+"/"+f]
		if !ok {
			return &MismatchError{File: f, Why: "is missing"}
		}
		if deed.Digest(b) != e.Files[f] {
			return &MismatchError{File: f, Why: "doesn't match its sha256"}
		}
	}
	m, err := ParseMeta(src.Files[e.Name+"/"+MetaFile])
	if err != nil {
		return err
	}
	if m.ID != e.ID || m.Name != e.Name || m.Version != e.Version || m.Tier != e.Tier || m.Summary != e.Summary {
		return &MismatchError{File: MetaFile, Why: "doesn't agree with the index for " + e.Name}
	}
	return nil
}

// ParseMeta reads pack.yaml strictly.
func ParseMeta(raw []byte) (Meta, error) {
	var m Meta
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return Meta{}, &FormatError{File: MetaFile, Why: "isn't valid: " + firstLine(err.Error())}
	}
	if !reName.MatchString(m.Name) || !rePackID.MatchString(m.ID) || !reVersion.MatchString(m.Version) ||
		(m.Tier != TierFree && m.Tier != TierPro) || strings.TrimSpace(m.Summary) == "" {
		return Meta{}, &FormatError{File: MetaFile, Why: "needs id (pck_…), name, version (x.y.z), tier (free or pro) and summary"}
	}
	return m, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
