package stamp

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/intent"
)

// Object names and limits of timestamps.json (spec/v2/BUNDLE_FORMAT.md §9).
const (
	ObjectHead  = "chain_head"
	ObjectFile  = "head_timestamps"
	FileName    = "timestamps.json"
	MaxTokens   = 8       // RFC 3161 tokens per head
	MaxServices = 4       // transparency-service registrations per head
	MaxHeads    = 100_000 // heads per file
	maxURL      = 512
	maxCOSE     = 64 << 10
)

// MaxBundleBytes is timestamps.json's limit inside an export bundle; MaxFileBytes outside one
// (verify --timestamps reads a file that grows by one head per run).
const (
	MaxBundleBytes = 1 << 20
	MaxFileBytes   = 256 << 20
)

var (
	b64          = base64.RawURLEncoding.Strict()
	reOrg        = regexp.MustCompile(deed.IDPattern("org"))
	reDeedID     = regexp.MustCompile(deed.IDPattern("deed"))
	reBodyHash   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reB64URL     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	reJWKCoord   = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	errNotCanon  = errors.New("not canonical JSON (write it with deedglass, or as JCS, RFC 8785)")
	errEmptyHead = errors.New("a head with no timestamp and no registration")
)

// Head is the statement that gets timestamped: one sealed deed of one org's chain. Every field is
// copied from that deed; nothing in it is secret.
type Head struct {
	Object   string `json:"object"` // chain_head
	V        int64  `json:"v"`      // 1
	Org      string `json:"org"`
	Seq      int64  `json:"seq"`
	DeedID   string `json:"deed_id"`
	BodyHash string `json:"body_hash"`
	IssuedAt string `json:"issued_at"`
}

// NewHead returns the head statement for one deed.
func NewHead(org string, seq int64, deedID, bodyHash, issuedAt string) Head {
	return Head{Object: ObjectHead, V: 1, Org: org, Seq: seq, DeedID: deedID, BodyHash: bodyHash, IssuedAt: issuedAt}
}

// Bytes is JCS(head): the bytes an RFC 3161 imprint hashes and a Signed Statement carries.
func (h Head) Bytes() ([]byte, error) {
	if err := h.validate(); err != nil {
		return nil, err
	}
	return deed.StrictCanonical(h)
}

// Imprint is SHA-256(JCS(head)), the RFC 3161 message imprint.
func (h Head) Imprint() ([]byte, error) {
	b, err := h.Bytes()
	if err != nil {
		return nil, err
	}
	s := sha256.Sum256(b)
	return s[:], nil
}

func (h Head) validate() error {
	switch {
	case h.Object != ObjectHead:
		return fmt.Errorf("head object %q", h.Object)
	case h.V != 1:
		return fmt.Errorf("head version %d", h.V)
	case !reOrg.MatchString(h.Org):
		return errors.New("head org")
	case h.Seq < 1 || h.Seq > deed.MaxSafeInteger:
		return errors.New("head seq")
	case !reDeedID.MatchString(h.DeedID):
		return errors.New("head deed_id")
	case !reBodyHash.MatchString(h.BodyHash):
		return errors.New("head body_hash")
	}
	if _, err := deed.ParseTime(h.IssuedAt); err != nil {
		return errors.New("head issued_at")
	}
	return nil
}

// Token is one RFC 3161 time-stamp token: the TSA's URL (as configured, for people) and the DER
// ContentInfo, base64url without padding.
type Token struct {
	TSA   string `json:"tsa"`
	Token string `json:"token"`
}

// Registration is one transparency-service registration (RFC 9943): the Signed Statement the
// issuer registered (COSE_Sign1 over JCS(head), sealed with the issuer's Ed25519 seal key), the
// service's signed inclusion proof for it (an RFC 9942 COSE_Sign1 with an RFC9162_SHA256 inclusion
// proof), and the service's public key as a JWK. All three are base64url without padding except
// the key.
type Registration struct {
	Service    string `json:"service"`
	Statement  string `json:"statement"`
	Inclusion  string `json:"inclusion"`
	ServiceKey JWK    `json:"service_key"`
}

// Entry is one head with its evidence.
type Entry struct {
	Head    Head           `json:"head"`
	RFC3161 []Token        `json:"rfc3161"`
	SCITT   []Registration `json:"scitt"`
}

// File is timestamps.json: one org's timestamped heads, seq ascending.
type File struct {
	Object string  `json:"object"` // head_timestamps
	V      int64   `json:"v"`      // 1
	Org    string  `json:"org"`
	Heads  []Entry `json:"heads"`
}

// NewFile returns an empty file for org.
func NewFile(org string) *File { return &File{Object: ObjectFile, V: 1, Org: org, Heads: []Entry{}} }

// Marshal returns JCS(f) after validating it.
func (f *File) Marshal() ([]byte, error) {
	f.normalize()
	if err := f.validate(); err != nil {
		return nil, err
	}
	return deed.StrictCanonical(f)
}

func (f *File) normalize() {
	if f.Heads == nil {
		f.Heads = []Entry{}
	}
	for i := range f.Heads {
		if f.Heads[i].RFC3161 == nil {
			f.Heads[i].RFC3161 = []Token{}
		}
		if f.Heads[i].SCITT == nil {
			f.Heads[i].SCITT = []Registration{}
		}
	}
}

// Add inserts e in seq order. A head already present gains e's evidence (an identical head only:
// two heads at one seq with different hashes are refused, since one of them is false).
func (f *File) Add(e Entry) error {
	if e.Head.Org != f.Org {
		return errors.New("stamp: the head belongs to another org")
	}
	for i := range f.Heads {
		h := &f.Heads[i]
		switch {
		case h.Head.Seq == e.Head.Seq && h.Head != e.Head:
			return fmt.Errorf("stamp: a different head at seq %d is already in the file", e.Head.Seq)
		case h.Head.Seq == e.Head.Seq:
			h.RFC3161 = append(h.RFC3161, e.RFC3161...)
			h.SCITT = append(h.SCITT, e.SCITT...)
			return nil
		case h.Head.Seq > e.Head.Seq:
			f.Heads = append(f.Heads[:i], append([]Entry{e}, f.Heads[i:]...)...)
			return nil
		}
	}
	f.Heads = append(f.Heads, e)
	return nil
}

// ParseFile reads timestamps.json strictly: strict JSON, canonical bytes, a closed schema and
// consistent values. It checks shapes only; Check verifies the evidence.
func ParseFile(raw []byte, max int64) (*File, error) {
	if int64(len(raw)) > max {
		return nil, fmt.Errorf("larger than %d bytes", max)
	}
	if _, err := intent.ParseStrictNonNegative(raw); err != nil {
		return nil, errors.New("not strict JSON")
	}
	var f File
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, errors.New("an unknown or mistyped field")
	}
	if dec.More() {
		return nil, errors.New("data after the object")
	}
	c, err := deed.StrictCanonical(&f)
	if err != nil || !bytes.Equal(c, raw) {
		return nil, errNotCanon
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

func (f *File) validate() error {
	switch {
	case f.Object != ObjectFile:
		return fmt.Errorf("object %q", f.Object)
	case f.V != 1:
		return fmt.Errorf("version %d", f.V)
	case !reOrg.MatchString(f.Org):
		return errors.New("org")
	case len(f.Heads) > MaxHeads:
		return errors.New("more than 100,000 heads")
	}
	var prev int64
	for i, e := range f.Heads {
		where := func(what string) error { return fmt.Errorf("heads[%d]: %s", i, what) }
		if err := e.Head.validate(); err != nil {
			return where(err.Error())
		}
		switch {
		case e.Head.Org != f.Org:
			return where("the head's org is not the file's org")
		case e.Head.Seq <= prev:
			return where("heads must be in ascending seq order, one per seq")
		case len(e.RFC3161) == 0 && len(e.SCITT) == 0:
			return where(errEmptyHead.Error())
		case len(e.RFC3161) > MaxTokens:
			return where("more than 8 RFC 3161 tokens")
		case len(e.SCITT) > MaxServices:
			return where("more than 4 transparency-service registrations")
		}
		prev = e.Head.Seq
		for j, t := range e.RFC3161 {
			if err := checkURL(t.TSA); err != nil {
				return where(fmt.Sprintf("rfc3161[%d].tsa: %v", j, err))
			}
			if err := checkB64(t.Token, MaxTokenBytes); err != nil {
				return where(fmt.Sprintf("rfc3161[%d].token: %v", j, err))
			}
		}
		for j, r := range e.SCITT {
			if err := checkURL(r.Service); err != nil {
				return where(fmt.Sprintf("scitt[%d].service: %v", j, err))
			}
			if err := checkB64(r.Statement, maxCOSE); err != nil {
				return where(fmt.Sprintf("scitt[%d].statement: %v", j, err))
			}
			if err := checkB64(r.Inclusion, maxCOSE); err != nil {
				return where(fmt.Sprintf("scitt[%d].inclusion: %v", j, err))
			}
			if err := r.ServiceKey.validate(); err != nil {
				return where(fmt.Sprintf("scitt[%d].service_key: %v", j, err))
			}
		}
	}
	return nil
}

// EncodeB64 is base64url without padding, the encoding of every binary member.
func EncodeB64(b []byte) string { return b64.EncodeToString(b) }

func checkURL(s string) error {
	if len(s) == 0 || len(s) > maxURL {
		return errors.New("1 to 512 characters")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return errors.New("an http(s) URL with no user info")
	}
	return nil
}

func checkB64(s string, max int) error {
	if s == "" || !reB64URL.MatchString(s) {
		return errors.New("base64url without padding")
	}
	b, err := b64.DecodeString(s)
	if err != nil {
		return errors.New("base64url without padding")
	}
	if len(b) > max {
		return fmt.Errorf("larger than %d bytes", max)
	}
	return nil
}
