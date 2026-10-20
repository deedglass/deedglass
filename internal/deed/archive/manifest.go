package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/intent"
)

// File names, in bundle order.
const (
	NameREADME       = "README.txt"
	NameKeys         = "keys.json"
	NameTimestamps   = "timestamps.json" // optional (TY-03, spec/v2/BUNDLE_FORMAT.md §9)
	NameDeeds        = "deeds.jsonl"
	NameManifest     = "manifest.json"
	NameManifestSeal = "manifest.seal.json"
)

// order is the only allowed sequence of entries (timestamps.json and the seal are optional).
var order = []string{NameREADME, NameKeys, NameTimestamps, NameDeeds, NameManifest, NameManifestSeal}

// Limits bounds each file and, by their sum, the whole bundle once decompressed.
type Limits struct {
	README, Keys, Timestamps, Deeds, Manifest, Seal int64
}

// DefaultLimits are the v1 limits (BUNDLE_FORMAT.md §2), with edition 2's timestamps.json (1 MiB).
// verify --max-bytes changes Deeds.
func DefaultLimits() Limits {
	return Limits{README: 64 << 10, Keys: 1 << 20, Timestamps: 1 << 20, Deeds: 8 << 30, Manifest: 64 << 10, Seal: 4 << 10}
}

func (l Limits) of(name string) int64 {
	switch name {
	case NameREADME:
		return l.README
	case NameKeys:
		return l.Keys
	case NameTimestamps:
		return l.Timestamps
	case NameDeeds:
		return l.Deeds
	case NameManifest:
		return l.Manifest
	}
	return l.Seal
}

// total is the most a bundle may expand to: every file at its limit plus tar headers and padding.
func (l Limits) total() int64 {
	return l.README + l.Keys + l.Timestamps + l.Deeds + l.Manifest + l.Seal + 6*(2*512) + 1024 + 8*512
}

// Manifest is manifest.json.
type Manifest struct {
	Object    string           `json:"object"` // "deed_bundle_manifest"
	V         int64            `json:"v"`      // 1
	Org       string           `json:"org"`
	Issuer    string           `json:"issuer"`
	CreatedAt string           `json:"created_at"`
	Generator Generator        `json:"generator"`
	Range     Range            `json:"range"`
	Chain     ManifestChain    `json:"chain"`
	Kinds     map[string]int64 `json:"kinds"`
	Files     map[string]File  `json:"files"`
}

// Generator names the software that wrote the bundle.
type Generator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Range is the deeds in the bundle.
type Range struct {
	FirstSeq int64  `json:"first_seq"`
	LastSeq  int64  `json:"last_seq"`
	Count    int64  `json:"count"`
	From     string `json:"from"`
	To       string `json:"to"`
}

// ManifestChain is where the deeds start and end.
type ManifestChain struct {
	Anchor ChainAnchor `json:"anchor"`
	Head   ChainHead   `json:"head"`
}

// ChainAnchor is what the first deed links to: genesis (seq 0) or the deed before it.
type ChainAnchor struct {
	Kind     string `json:"kind"` // genesis | deed
	Seq      int64  `json:"seq"`
	BodyHash string `json:"body_hash"`
}

// ChainHead is the last deed in the bundle.
type ChainHead struct {
	Seq      int64  `json:"seq"`
	DeedID   string `json:"deed_id"`
	BodyHash string `json:"body_hash"`
}

// File is one file's size and hash; Lines is set for deeds.jsonl only.
type File struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"` // lowercase hex, no prefix
	Lines  *int64 `json:"lines,omitempty"`
}

// FileName is the bundle's file name: deedglass-<org>-<first_seq>-<last_seq>.tar.gz.
func FileName(org string, firstSeq, lastSeq int64) string {
	return "deedglass-" + org + "-" + strconv.FormatInt(firstSeq, 10) + "-" + strconv.FormatInt(lastSeq, 10) + ".tar.gz"
}

// Observed is what a verifier saw in deeds.jsonl, for Manifest.Check.
type Observed struct {
	Count         int64
	FirstSeq      int64
	LastSeq       int64
	From, To      string
	FirstPrevHash string
	Kinds         map[string]int64
	Head          ChainHead
}

// Check compares the manifest's claims with what was verified.
func (m *Manifest) Check(o Observed) error {
	mismatch := func(what string) error {
		return invalid("This bundle's manifest does not match its deeds (%s).", what)
	}
	switch {
	case m.Range.Count != o.Count:
		return mismatch("count")
	case m.Range.FirstSeq != o.FirstSeq || m.Range.LastSeq != o.LastSeq:
		return mismatch("range")
	case m.Range.From != o.From || m.Range.To != o.To:
		return mismatch("dates")
	case m.Chain.Head != o.Head:
		return mismatch("head")
	case m.Chain.Anchor.BodyHash != o.FirstPrevHash: //deedglass:allow-compare a deed's public body hash
		return mismatch("anchor")
	}
	for _, k := range []string{"action", "admin", "amendment"} {
		if m.Kinds[k] != o.Kinds[k] {
			return mismatch("kinds")
		}
	}
	return nil
}

// marshal returns JCS(m).
func (m *Manifest) marshal() ([]byte, error) { return deed.StrictCanonical(m) }

// parseManifest reads manifest.json: strict JSON, canonical bytes, a closed schema and consistent
// values.
func parseManifest(raw []byte) (*Manifest, error) {
	bad := func(why string) error { return invalid("This bundle's manifest is not valid (%s).", why) }
	if _, err := intent.ParseStrictNonNegative(raw); err != nil {
		return nil, bad("not strict JSON")
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, bad("unknown or mistyped field")
	}
	if c, err := m.marshal(); err != nil || !bytes.Equal(c, raw) {
		return nil, bad("not canonical JSON")
	}
	switch {
	case m.Object != "deed_bundle_manifest":
		return nil, bad("object")
	case m.V != 1:
		return nil, bad(fmt.Sprintf("version %d", m.V))
	case m.Range.Count < 1 || m.Range.FirstSeq < 1 || m.Range.LastSeq-m.Range.FirstSeq+1 != m.Range.Count:
		return nil, bad("range")
	case m.Chain.Head.Seq != m.Range.LastSeq:
		return nil, bad("head")
	case len(m.Files) != 3 && len(m.Files) != 4:
		return nil, bad("files")
	}
	if _, err := time.Parse(deed.TimeFormat, m.CreatedAt); err != nil {
		return nil, bad("created_at")
	}
	names := []string{NameREADME, NameKeys, NameDeeds}
	if len(m.Files) == 4 {
		names = append(names, NameTimestamps)
	}
	for _, n := range names {
		f, ok := m.Files[n]
		if !ok || len(f.SHA256) != 64 || (n == NameDeeds) != (f.Lines != nil) {
			return nil, bad("files")
		}
	}
	return &m, nil
}

// invalid is a bundle_invalid error whose detail is one sentence.
func invalid(format string, a ...any) error {
	return &deed.VerifyError{Code: deed.CodeBundleInvalid, Detail: fmt.Sprintf(format, a...)}
}
