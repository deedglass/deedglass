package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

// SignatureWindow is how far a request timestamp may be from now (Slack's 5 minutes).
const SignatureWindow = 300 * time.Second

// Errors of VerifyInteraction. Each names only the rule that failed: never the secret, the
// payload or the signature.
var (
	ErrTimestampMissing   = errors.New("slack: X-Slack-Request-Timestamp is missing")
	ErrTimestampInvalid   = errors.New("slack: X-Slack-Request-Timestamp isn't an integer")
	ErrTimestampWindow    = errors.New("slack: X-Slack-Request-Timestamp is more than 300 s from now")
	ErrSignatureMissing   = errors.New("slack: X-Slack-Signature is missing")
	ErrSignatureMalformed = errors.New("slack: X-Slack-Signature isn't v0= and 64 lowercase hex characters")
	ErrSignatureMismatch  = errors.New("slack: X-Slack-Signature doesn't match the request")
	ErrSigningSecret      = errors.New("slack: the connector's signing secret isn't usable")
)

// Sign is Slack's v0 signature: "v0=" + hex(HMAC-SHA256(secret, "v0:"+ts+":"+body)). Exported
// for tests.
func Sign(secret []byte, ts string, body []byte) string {
	sum := mac(secret, ts, body)
	return "v0=" + hex.EncodeToString(sum[:])
}

func mac(secret []byte, ts string, body []byte) [sha256.Size]byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("v0:"))
	m.Write([]byte(ts))
	m.Write([]byte(":"))
	m.Write(body)
	var out [sha256.Size]byte
	m.Sum(out[:0])
	return out
}

// verify is the pure check: the timestamp rule, the signature's shape, then the HMAC compared
// with hmac.Equal. A nil or empty secret never verifies.
func verify(secret []byte, h http.Header, body []byte, now time.Time) error {
	ts := h.Get("X-Slack-Request-Timestamp")
	if ts == "" {
		return ErrTimestampMissing
	}
	if len(ts) > 12 || !allDigits(ts) {
		return ErrTimestampInvalid
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrTimestampInvalid
	}
	d := now.Unix() - n
	if d > int64(SignatureWindow/time.Second) || d < -int64(SignatureWindow/time.Second) {
		return ErrTimestampWindow
	}
	sig := h.Get("X-Slack-Signature")
	if sig == "" {
		return ErrSignatureMissing
	}
	var got [sha256.Size]byte
	if len(sig) != 67 || sig[:3] != "v0=" || !lowerHex(sig[3:]) {
		return ErrSignatureMalformed
	}
	if _, err := hex.Decode(got[:], []byte(sig[3:])); err != nil {
		return ErrSignatureMalformed
	}
	if len(secret) == 0 {
		return ErrSigningSecret
	}
	want := mac(secret, ts, body)
	if !hmac.Equal(got[:], want[:]) {
		return ErrSignatureMismatch
	}
	return nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func lowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// VerifyInteraction checks a signed Slack request (S29 §3.4 steps 2 and 5):
// X-Slack-Request-Timestamp an integer with |now − ts| ≤ 300 s, X-Slack-Signature matching
// ^v0=[0-9a-f]{64}$, then the HMAC recomputed with this connector's signing_secret and compared
// with hmac.Equal. It returns nil or one of the Err… values above. The header rules run before
// the credential is opened; the secret is destroyed after use.
func VerifyInteraction(ctx context.Context, inst *connector.Instance, h http.Header, body []byte, now time.Time) error {
	// Shape first, so a malformed request never opens the vault.
	if err := verify(nil, h, body, now); !errors.Is(err, ErrSigningSecret) {
		return err
	}
	sec, err := inst.Secret(ctx)
	if err != nil {
		if ce := connector.AsError(err); ce != nil {
			return fmt.Errorf("%w (%s)", ErrSigningSecret, ce.Code)
		}
		return ErrSigningSecret
	}
	defer sec.Destroy()
	s := sec.Field(FieldSigningSecret)
	if checkSigningSecret(s) != "" {
		return ErrSigningSecret
	}
	b := []byte(s)
	defer clear(b)
	return verify(b, h, body, now)
}
