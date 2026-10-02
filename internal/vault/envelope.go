package vault

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/vault/keyring"
)

// Alg is the envelope format: AES-256-GCM, one fresh data key (DEK) per credential version,
// wrapped by the ring's active key-encryption key (KEK). Each GCM key encrypts one message with a
// random 96-bit nonce prepended (cipher.NewGCMWithRandomNonce's layout, 28 bytes of overhead).
const Alg = "A256GCM-ENV-1"

// Limits.
const (
	MaxPlaintext   = 16 << 10 // the JSON document {"v":1,"kind":…,"fields":{…}}
	WrappedDEKLen  = 12 + 32 + 16
	MinFieldLength = 6 // a shorter value can't be redacted reliably; prod refuses it
	MaxFields      = 16
)

// Kinds of credentials (contract §4 credentials.kind).
const (
	KindBearer = "bearer"
	KindHeader = "header"
	KindBasic  = "basic"
	KindQuery  = "query"
	KindDSN    = "dsn"
	KindFields = "fields"
)

// Kinds lists every credential kind.
var Kinds = []string{KindBearer, KindHeader, KindBasic, KindQuery, KindDSN, KindFields}

// Errors. Messages are for logs and admins; connectors map them to error codes.
var (
	// ErrNotFound: the connector has no credentials.
	ErrNotFound = errors.New("vault: no credentials for this connector")
	// ErrIntegrity: a ciphertext or wrapped key didn't authenticate (a flipped byte, or a row
	// moved to another org, connector, version or kind). It raises the vault_integrity alert.
	ErrIntegrity = errors.New("vault: the credential didn't authenticate (vault_integrity)")
	// ErrNoVault: DEEDGLASS_VAULT_KEY isn't set in this process.
	ErrNoVault = errors.New("vault: no key ring (DEEDGLASS_VAULT_KEY isn't set)")
)

// UnknownKEKError: a row is wrapped by a key that isn't in the ring.
type UnknownKEKError struct {
	KEKID string
	Rows  int // how many rows use it, when known (rotation and status count them)
}

func (e *UnknownKEKError) Error() string {
	n := e.Rows
	if n <= 0 {
		n = 1
	}
	return fmt.Sprintf("%d %s %s, which isn't in DEEDGLASS_VAULT_KEY. Add it back as the second key, restart, then run deedglass vault rotate.",
		n, plural(n, "credential uses", "credentials use"), e.KEKID)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Ref names the connector a credential belongs to.
type Ref struct {
	Org       ids.OrgID
	Connector ids.ConnectorID
	// Webhook binds a webhook's signing secret instead of a connector credential (S22): exactly
	// one of Connector and Webhook is set, and the envelope's AAD names it.
	Webhook ids.WebhookID
}

// subject is the id the envelope's AAD binds: the connector, or the webhook (S22).
func (r Ref) subject() string {
	if !r.Webhook.IsZero() {
		return r.Webhook.String()
	}
	return r.Connector.String()
}

// KEKProvider wraps and unwraps data keys. Ring implements it with keys from the environment; a
// KMS implementation is the v2 seam (docs/VAULT.md, "Using a KMS").
type KEKProvider interface {
	ActiveID() string
	Wrap(ctx context.Context, dek, aad []byte) (kekID string, wrapped []byte, err error)
	Unwrap(ctx context.Context, kekID string, wrapped, aad []byte) ([]byte, error)
}

// Ring is the KEKProvider over DEEDGLASS_VAULT_KEY.
type Ring struct {
	keys []keyring.Key
	rand io.Reader // nonce source; nil = crypto/rand through NewGCMWithRandomNonce
	mu   sync.Mutex
}

// NewRing parses a DEEDGLASS_VAULT_KEY value.
func NewRing(s string) (*Ring, error) {
	ks, err := keyring.Parse(s)
	if err != nil {
		return nil, err
	}
	return &Ring{keys: ks}, nil
}

// WithRand makes the ring's nonces come from r (golden vectors only).
func (r *Ring) WithRand(rd io.Reader) *Ring { r.rand = rd; return r }

// ActiveID is the id of the key every new data key is wrapped with.
func (r *Ring) ActiveID() string { return r.keys[0].ID }

// IDs lists the ring's key ids, active first.
func (r *Ring) IDs() []string { return keyring.IDs(r.keys) }

// Has reports whether id is in the ring.
func (r *Ring) Has(id string) bool {
	return slices.ContainsFunc(r.keys, func(k keyring.Key) bool { return k.ID == id })
}

func (r *Ring) key(id string) ([]byte, bool) {
	for _, k := range r.keys {
		if k.ID == id {
			return k.Bytes, true
		}
	}
	return nil, false
}

// Wrap implements KEKProvider: GCM(active KEK).Seal(dek, aad). The aad must already name the
// active key id (aadDEK), so callers build it with ActiveID first.
func (r *Ring) Wrap(_ context.Context, dek, aad []byte) (string, []byte, error) {
	k := r.keys[0]
	w, err := r.seal(k.Bytes, dek, aad)
	return k.ID, w, err
}

// Unwrap implements KEKProvider.
func (r *Ring) Unwrap(_ context.Context, kekID string, wrapped, aad []byte) ([]byte, error) {
	k, ok := r.key(kekID)
	if !ok {
		return nil, &UnknownKEKError{KEKID: kekID}
	}
	if len(wrapped) != WrappedDEKLen {
		return nil, ErrIntegrity
	}
	dek, err := open(k, wrapped, aad)
	if err != nil || len(dek) != 32 {
		return nil, ErrIntegrity
	}
	return dek, nil
}

func (r *Ring) seal(key, pt, aad []byte) ([]byte, error) {
	r.mu.Lock()
	rd := r.rand
	r.mu.Unlock()
	return sealWith(rd, key, pt, aad)
}

// sealWith encrypts pt under key with a fresh random nonce, prepended. With rd nil it is
// cipher.NewGCMWithRandomNonce; with a reader (golden vectors) the same layout from rd's bytes.
func sealWith(rd io.Reader, key, pt, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if rd == nil {
		g, err := cipher.NewGCMWithRandomNonce(block)
		if err != nil {
			return nil, err
		}
		return g.Seal(nil, nil, pt, aad), nil
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rd, nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, pt, aad), nil
}

func open(key, ct, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return g.Open(nil, nil, ct, aad)
}

// The associated data: each ciphertext is bound to its org, connector, version and kind, and each
// wrapped key to its org, connector and KEK.
func aadDEK(ref Ref, kekID string) []byte {
	return []byte("deedglass/vault/dek/v1\n" + ref.Org.String() + "\n" + ref.subject() + "\n" + kekID)
}

func aadSecret(ref Ref, version int, kind string) []byte {
	return []byte("deedglass/vault/secret/v1\n" + ref.Org.String() + "\n" + ref.subject() + "\n" + strconv.Itoa(version) + "\n" + kind)
}

// Sealed is one stored credential version: what the credentials row holds besides its names.
type Sealed struct {
	Version    int
	Kind       string
	KEKID      string
	WrappedDEK []byte
	Ciphertext []byte
}

// document is the plaintext: {"v":1,"kind":"bearer","fields":{"token":"…"}}.
type document struct {
	V      int               `json:"v"`
	Kind   string            `json:"kind"`
	Fields map[string]string `json:"fields"`
}

// Seal encrypts fields as one credential version with a fresh data key, which it zeroes after use.
func Seal(ctx context.Context, kp KEKProvider, rd io.Reader, ref Ref, version int, kind string, fields map[string][]byte) (Sealed, error) {
	if !slices.Contains(Kinds, kind) {
		return Sealed{}, fmt.Errorf("vault: unknown kind %q", kind)
	}
	doc := document{V: 1, Kind: kind, Fields: make(map[string]string, len(fields))}
	for k, v := range fields {
		if !utf8.Valid(v) {
			return Sealed{}, fmt.Errorf("vault: field %q isn't UTF-8 text", k)
		}
		doc.Fields[k] = string(v)
	}
	pt, err := json.Marshal(doc)
	if err != nil {
		return Sealed{}, err
	}
	defer zero(pt)
	if len(pt) > MaxPlaintext {
		return Sealed{}, fmt.Errorf("vault: the credential is %d bytes; the limit is %d", len(pt), MaxPlaintext)
	}
	dek := make([]byte, 32)
	defer zero(dek)
	src := rd
	if src == nil {
		src = rand.Reader
	}
	if _, err := io.ReadFull(src, dek); err != nil {
		return Sealed{}, err
	}
	ct, err := sealWith(rd, dek, pt, aadSecret(ref, version, kind))
	if err != nil {
		return Sealed{}, err
	}
	kek := kp.ActiveID()
	gotKEK, wrapped, err := kp.Wrap(ctx, dek, aadDEK(ref, kek))
	if err != nil {
		return Sealed{}, err
	}
	if gotKEK != kek {
		return Sealed{}, errors.New("vault: the key provider wrapped with a key other than its active one")
	}
	return Sealed{Version: version, Kind: kind, KEKID: kek, WrappedDEK: wrapped, Ciphertext: ct}, nil
}

// Open decrypts a stored credential. Any authentication failure is ErrIntegrity; a key that isn't
// in the ring is *UnknownKEKError. Nothing is returned on failure.
func Open(ctx context.Context, kp KEKProvider, ref Ref, s Sealed) (*Secret, error) {
	dek, err := kp.Unwrap(ctx, s.KEKID, s.WrappedDEK, aadDEK(ref, s.KEKID))
	if err != nil {
		return nil, err
	}
	defer zero(dek)
	pt, err := open(dek, s.Ciphertext, aadSecret(ref, s.Version, s.Kind))
	if err != nil {
		return nil, ErrIntegrity
	}
	defer zero(pt)
	var doc document
	dec := json.NewDecoder(bytes.NewReader(pt))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil || doc.V != 1 || doc.Kind != s.Kind {
		return nil, ErrIntegrity
	}
	fs := make(map[string][]byte, len(doc.Fields))
	for k, v := range doc.Fields {
		fs[k] = []byte(v)
	}
	return newSecret(s.Kind, fs), nil
}

// Rewrap moves a stored credential to the active KEK without touching its ciphertext.
func Rewrap(ctx context.Context, kp KEKProvider, ref Ref, s Sealed) (Sealed, error) {
	dek, err := kp.Unwrap(ctx, s.KEKID, s.WrappedDEK, aadDEK(ref, s.KEKID))
	if err != nil {
		return Sealed{}, err
	}
	defer zero(dek)
	kek := kp.ActiveID()
	gotKEK, wrapped, err := kp.Wrap(ctx, dek, aadDEK(ref, kek))
	if err != nil {
		return Sealed{}, err
	}
	if gotKEK != kek {
		return Sealed{}, errors.New("vault: the key provider wrapped with a key other than its active one")
	}
	out := s
	out.KEKID, out.WrappedDEK = kek, wrapped
	return out, nil
}

func zero(b []byte) { clear(b) }
