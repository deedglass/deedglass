package licence

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
)

const (
	// Tag is prepended to body_hash to form the signature input (contract §13.4).
	Tag = "deedglass/licence/v1\n"
	// ObjectToken is the token's object field.
	ObjectToken = "licence_token"
	// SigPrefix starts every signature value.
	SigPrefix = "ed25519:"
	// TimeLayout is the token's time format (the deed format: milliseconds, Z).
	TimeLayout = "2006-01-02T15:04:05.000Z"
)

// Plans. "report" is the $29 full scan report (a 30-day Pro pass), "founding" the $99 founding
// partner place (Pro for six months), "pro" the subscription.
const (
	PlanReport   = "report"
	PlanFounding = "founding"
	PlanPro      = "pro"
)

// Features a token can carry. Only new, paid features: nothing free ever needs one.
const (
	FeatureScanFull      = "scan_full"
	FeatureScanFix       = "scan_fix"
	FeaturePacksPro      = "packs_pro"
	FeatureActionPrivate = "action_private"
)

// PlanFeatures is what each plan unlocks; the site signs the same lists (functions/_lib/commerce).
var PlanFeatures = map[string][]string{
	PlanReport:   {FeatureScanFull, FeatureScanFix, FeaturePacksPro},
	PlanFounding: {FeatureActionPrivate, FeatureScanFull, FeatureScanFix, FeaturePacksPro},
	PlanPro:      {FeatureActionPrivate, FeatureScanFull, FeatureScanFix, FeaturePacksPro},
}

var (
	reLic   = regexp.MustCompile(ids.Pattern("lic"))
	reLK    = regexp.MustCompile(ids.Pattern("lk"))
	reHash  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reSig   = regexp.MustCompile(`^ed25519:[A-Za-z0-9_-]{86}$`)
	reInst  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	b64     = base64.RawURLEncoding.Strict()
	errBody = errors.New("licence: the token is not a valid licence_token")
)

// Body is every signed field of a token.
type Body struct {
	Object      string   `json:"object"`
	V           int64    `json:"v"`
	ID          string   `json:"id"`
	Plan        string   `json:"plan"`
	Features    []string `json:"features"`
	KeySHA256   string   `json:"key_sha256"`
	Instance    string   `json:"instance"`
	ValidatedAt string   `json:"validated_at"`
	GraceDays   int64    `json:"grace_days"`
	PeriodEnd   *string  `json:"period_end"`
	KeyID       string   `json:"key_id"`
	Livemode    bool     `json:"livemode"`
}

// Token is the signed licence token written to licence.json.
type Token struct {
	Body
	BodyHash string `json:"body_hash"`
	Sig      string `json:"sig"`
}

// KeyHash is "sha256:" + hex(sha256(key)), the only form of a licence key we keep.
func KeyHash(key string) string {
	s := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(s[:])
}

// BodyHash returns "sha256:" + hex(sha256(JCS(body))) with the deeds' strict JCS profile.
func BodyHash(b Body) (string, error) {
	c, err := deed.StrictCanonical(b)
	if err != nil {
		return "", fmt.Errorf("licence: %w", err)
	}
	return deed.Digest(c), nil
}

// SigningInput is the exact byte string a token signature covers.
func SigningInput(bodyHash string) []byte { return []byte(Tag + bodyHash) }

// Sign seals b with an lk_ key. The site signs production tokens (WebCrypto); Go signs only the
// cross-language goldens and tests.
func Sign(keyID string, priv ed25519.PrivateKey, b Body) (Token, error) {
	if !reLK.MatchString(keyID) {
		return Token{}, fmt.Errorf("licence: %q is not an lk_ key id; licence tokens are never signed with a seal key", keyID)
	}
	b.KeyID = keyID
	if err := b.check(); err != nil {
		return Token{}, err
	}
	h, err := BodyHash(b)
	if err != nil {
		return Token{}, err
	}
	sig := ed25519.Sign(priv, SigningInput(h))
	return Token{Body: b, BodyHash: h, Sig: SigPrefix + b64.EncodeToString(sig)}, nil
}

func (b Body) check() error {
	bad := func(field, why string) error { return fmt.Errorf("%w: %s %s", errBody, field, why) }
	switch {
	case b.Object != ObjectToken:
		return bad("object", `must be "licence_token"`)
	case b.V != 1:
		return bad("v", "must be 1")
	case !reLic.MatchString(b.ID):
		return bad("id", "must be a lic_ id")
	case PlanFeatures[b.Plan] == nil:
		return bad("plan", "must be report, founding or pro")
	case !reHash.MatchString(b.KeySHA256):
		return bad("key_sha256", "must be sha256:<64 hex>")
	case !reInst.MatchString(b.Instance):
		return bad("instance", "must be a Dodo licence instance id")
	case !reLK.MatchString(b.KeyID):
		return bad("key_id", "must be an lk_ id")
	case b.GraceDays < 0 || b.GraceDays > 30:
		return bad("grace_days", "must be 0 to 30")
	}
	if _, err := time.Parse(TimeLayout, b.ValidatedAt); err != nil {
		return bad("validated_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
	}
	if b.PeriodEnd != nil {
		if _, err := time.Parse(TimeLayout, *b.PeriodEnd); err != nil {
			return bad("period_end", "must be null or YYYY-MM-DDTHH:MM:SS.mmmZ")
		}
	}
	if len(b.Features) == 0 || len(b.Features) > 8 {
		return bad("features", "must list 1 to 8 features")
	}
	for _, f := range b.Features {
		if !slices.Contains([]string{FeatureScanFull, FeatureScanFix, FeaturePacksPro, FeatureActionPrivate}, f) {
			return bad("features", "names an unknown feature")
		}
	}
	return nil
}

// ErrSignature means the token was changed after it was signed, or was signed by a key this build
// doesn't know.
var ErrSignature = errors.New("licence: the signature doesn't match")

// Parse reads a token strictly: unknown or duplicate fields, fractions and anything outside the
// shape are refused.
func Parse(raw []byte) (Token, error) {
	if _, err := deed.StrictCanonical(json.RawMessage(raw)); err != nil {
		return Token{}, fmt.Errorf("%w: %v", errBody, err)
	}
	var t Token
	dec := json.NewDecoder(bytesReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return Token{}, fmt.Errorf("%w: %v", errBody, err)
	}
	if err := t.check(); err != nil {
		return Token{}, err
	}
	return t, nil
}

// Verify checks the body hash and the signature against the key set.
func (t Token) Verify(ks KeySet) error {
	if err := t.check(); err != nil {
		return err
	}
	h, err := BodyHash(t.Body)
	if err != nil {
		return err
	}
	if h != t.BodyHash { //deedglass:allow-compare body_hash is a public digest of the public token body
		return ErrSignature
	}
	pub, ok := ks.lookup(t.KeyID)
	if !ok || !reSig.MatchString(t.Sig) {
		return ErrSignature
	}
	sig, err := b64.DecodeString(t.Sig[len(SigPrefix):])
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, SigningInput(h), sig) {
		return ErrSignature
	}
	return nil
}

// Has reports whether the token carries feature f.
func (t Token) Has(f string) bool { return slices.Contains(t.Features, f) }

// Marshal is the token's file form: JCS, so it is stable byte for byte.
func (t Token) Marshal() ([]byte, error) {
	return deed.StrictCanonical(t)
}
