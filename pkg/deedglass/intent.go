package deedglass

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/deedglass/deedglass/internal/intent"
	"github.com/deedglass/deedglass/internal/keys"
)

// Signed intents (docs/INTENTS.md). An agent's runtime signs each call with an Ed25519 key that
// never leaves it; Deedglass checks the signature, the tool, the arguments' digest and a one-time
// nonce. This is the reference signer the TypeScript and Python SDKs port against
// testdata/intents/; it shares the wire format code with the server, so the two can't drift.

// Intent limits.
const (
	DefaultIntentTTL = 60 * time.Second
	MaxIntentTTL     = 5 * time.Minute
)

// IntentSigner signs intents for one agent. It is safe for concurrent use.
type IntentSigner struct {
	key   ed25519.PrivateKey
	kid   string
	org   string
	agent string
}

// SignOptions refine one signature.
type SignOptions struct {
	OnBehalfOf string           // the person the agent serves (signed; the header must match or be absent)
	Session    string           // the conversation or run (signed)
	TTL        time.Duration    // default 60 s, at most 5 minutes
	Now        func() time.Time // default time.Now
	Nonce      string           // 32 lowercase hex; default 128 fresh random bits. Tests and vectors only.
}

// GenerateIntentKey returns a new intent key: the private key as PKCS#8 PEM (store it with mode
// 0600 next to the agent), the public key as "ed25519:<base64url>" (register it with Deedglass),
// and its kid.
func GenerateIntentKey() (privPEM []byte, publicKey, kid string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", "", err
	}
	return encodeIntentKey(priv, pub)
}

func encodeIntentKey(priv ed25519.PrivateKey, pub ed25519.PublicKey) ([]byte, string, string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, "", "", err
	}
	kid, err := keys.KID(pub)
	if err != nil {
		return nil, "", "", err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), "ed25519:" + base64.RawURLEncoding.EncodeToString(pub), kid, nil
}

// LoadIntentSigner reads a PKCS#8 PEM Ed25519 private key (what GenerateIntentKey writes, and what
// Node's crypto and Python's cryptography load) for the agent agent of org org.
func LoadIntentSigner(pemBytes []byte, org, agent string) (*IntentSigner, error) {
	priv, err := ParseIntentPrivateKey(pemBytes)
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	if _, err := keys.ParsePublicKey(pub); err != nil {
		return nil, fmt.Errorf("deedglass: the intent key can't be registered: %w", err)
	}
	kid, err := keys.KID(pub)
	if err != nil {
		return nil, err
	}
	return &IntentSigner{key: priv, kid: kid, org: org, agent: agent}, nil
}

// ParseIntentPrivateKey reads a PKCS#8 PEM Ed25519 private key.
func ParseIntentPrivateKey(pemBytes []byte) (ed25519.PrivateKey, error) {
	b, _ := pem.Decode(pemBytes)
	if b == nil || b.Type != "PRIVATE KEY" {
		return nil, errors.New("deedglass: the intent key isn't a PKCS#8 PEM block (BEGIN PRIVATE KEY)")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("deedglass: the intent key can't be read: %w", err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("deedglass: the intent key isn't an Ed25519 key")
	}
	return priv, nil
}

// KID is the signer's key id: hex of the first 16 bytes of SHA-256(public key).
func (s *IntentSigner) KID() string { return s.kid }

// PublicKey is the signer's public key as Deedglass registers it ("ed25519:<base64url>").
func (s *IntentSigner) PublicKey() string {
	return "ed25519:" + base64.RawURLEncoding.EncodeToString(s.key.Public().(ed25519.PublicKey))
}

// Signed is one signed intent, with the parts --explain shows.
type Signed struct {
	Header     string // the Deedglass-Intent value
	IntentHash string
	Intent     []byte // the exact JCS bytes
	ArgsDigest string
	Args       []byte // JCS(args)
	Nonce      string
	ExpiresAt  time.Time
}

// Sign signs one call: tool is the canonical connector.tool name and args the exact JSON
// arguments sent with it. It returns the Deedglass-Intent header and the intent_hash.
func (s *IntentSigner) Sign(tool string, args json.RawMessage, o SignOptions) (header, intentHash string, err error) {
	sg, err := s.SignDetailed(tool, args, o)
	if err != nil {
		return "", "", err
	}
	return sg.Header, sg.IntentHash, nil
}

// SignDetailed is Sign, returning every intermediate value.
func (s *IntentSigner) SignDetailed(tool string, args json.RawMessage, o SignOptions) (*Signed, error) {
	if s == nil || s.key == nil {
		return nil, errors.New("deedglass: no intent key")
	}
	ttl := o.TTL
	if ttl == 0 {
		ttl = DefaultIntentTTL
	}
	if ttl < time.Millisecond || ttl > MaxIntentTTL {
		return nil, errors.New("deedglass: an intent lasts more than 0 and at most 5 minutes")
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	nonce := o.Nonce
	if nonce == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		nonce = hex.EncodeToString(b)
	}
	if !intent.ValidTool(tool) {
		return nil, fmt.Errorf("deedglass: %q isn't a connector.tool name, like stripe.refund", tool)
	}
	digest, err := intent.ArgsDigest(args)
	if err != nil {
		var ae *intent.AgentError
		if errors.As(err, &ae) && ae.Reason == intent.WhyBigInt {
			return nil, fmt.Errorf("deedglass: the arguments contain an integer larger than 9007199254740991, which JSON can't carry exactly; send it as a string: %w", err)
		}
		return nil, fmt.Errorf("deedglass: the arguments can't be canonicalized with RFC 8785 (unique keys, at most 32 levels deep, at most 256 KiB): %w", err)
	}
	canon, err := intent.CanonicalArgs(args)
	if err != nil {
		return nil, err
	}
	iat := now().UnixMilli()
	in := &intent.AgentIntent{V: intent.AgentVersion, Org: s.org, Agent: s.agent, KID: s.kid, Tool: tool, ArgsDigest: digest,
		Nonce: nonce, Iat: iat, Exp: iat + ttl.Milliseconds(), OnBehalfOf: o.OnBehalfOf, Session: o.Session}
	b, err := intent.EncodeAgent(in)
	if err != nil {
		return nil, fmt.Errorf("deedglass: the intent isn't valid: %w", err)
	}
	hash := intent.Digest(b)
	sig := ed25519.Sign(s.key, intent.SigningInput(hash))
	return &Signed{Header: intent.JoinHeader(b, sig), IntentHash: hash, Intent: b, ArgsDigest: digest, Args: canon, Nonce: nonce,
		ExpiresAt: time.UnixMilli(in.Exp).UTC()}, nil
}

// ArgsDigest is "sha256:" + hex(SHA-256(JCS(args))) under the strict args profile: unique keys,
// depth at most 32, at most 256 KiB, integers at most 2^53−1 in magnitude (send larger ones as
// strings), fractions as IEEE-754 doubles.
func ArgsDigest(args json.RawMessage) (string, error) { return intent.ArgsDigest(args) }

// IntentCheck is POST /v1/intents/verify's answer.
type IntentCheck struct {
	Object        string     `json:"object"` // "intent_check"
	Signed        bool       `json:"signed"`
	IntentHash    *string    `json:"intent_hash"`
	KID           *string    `json:"kid"`
	Agent         string     `json:"agent"`
	Tool          string     `json:"tool"`
	ArgsDigest    string     `json:"args_digest"`
	OnBehalfOf    *string    `json:"on_behalf_of"`
	Session       *string    `json:"session"`
	ExpiresAt     *time.Time `json:"expires_at"`
	NonceConsumed bool       `json:"nonce_consumed"`
	IntentMode    string     `json:"intent_mode"`
}

// VerifyIntent sends one signed intent to POST /v1/intents/verify, which checks it exactly as the
// Gate will and consumes its nonce (so a header can be checked once). Refusals are *Error with code
// intent_invalid, intent_replay or intent_required.
func (c *Client) VerifyIntent(ctx context.Context, tool string, args json.RawMessage, header string, opts ...Option) (*IntentCheck, error) {
	body := struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}{tool, args}
	var out IntentCheck
	opts = append([]Option{func(r *http.Request) { r.Header.Set("Deedglass-Intent", header) }}, opts...)
	if _, err := c.Do(ctx, http.MethodPost, "/v1/intents/verify", body, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// WithOnBehalfOf sets Deedglass-On-Behalf-Of on one request.
func WithOnBehalfOf(ref string) Option {
	return func(r *http.Request) { r.Header.Set("Deedglass-On-Behalf-Of", ref) }
}

// WithSession sets Deedglass-Session on one request.
func WithSession(ref string) Option {
	return func(r *http.Request) { r.Header.Set("Deedglass-Session", ref) }
}
