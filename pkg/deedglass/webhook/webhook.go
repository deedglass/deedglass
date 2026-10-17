// Package webhook signs and verifies Deedglass webhook deliveries.
//
// Every delivery carries
//
//	Deedglass-Signature: t=<unix seconds>,v1=<hex hmac-sha256>
//
// where the HMAC key is the UTF-8 bytes of the endpoint's full signing secret (whsec_…) and the
// message is "<t>.<raw body>". A header may carry several v1 values (secret rotation); any one
// matching is enough. Verify refuses a timestamp more than the tolerance away from now (default
// 300 s), so a captured delivery can't be replayed later; dedupe by the Deedglass-Event-Id header
// for anything inside the window.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Header is the signature header's name.
const Header = "Deedglass-Signature"

// DefaultTolerance is how far a delivery's timestamp may be from now.
const DefaultTolerance = 300 * time.Second

// Errors Verify returns. Check them with errors.Is.
var (
	ErrNoHeader  = errors.New("webhook: the Deedglass-Signature header is missing")
	ErrHeader    = errors.New("webhook: the Deedglass-Signature header can't be read")
	ErrTimestamp = errors.New("webhook: the signature's timestamp is outside the tolerance")
	ErrSignature = errors.New("webhook: no signature matches the body and the secret")
)

// Sign returns the header value for body sent at t.
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac(secret, ts, body))
}

func mac(secret, ts string, body []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts))
	m.Write([]byte{'.'})
	m.Write(body)
	return m.Sum(nil)
}

// ParseHeader reads "t=…,v1=…[,v1=…]". Unknown keys are ignored (a future v2 scheme); malformed
// v1 values are skipped.
func ParseHeader(h string) (t int64, sigs [][]byte, err error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, nil, ErrNoHeader
	}
	if len(h) > 4096 {
		return 0, nil, ErrHeader
	}
	seenT := false
	for _, part := range strings.Split(h, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			n, perr := strconv.ParseInt(v, 10, 64)
			if perr != nil || n <= 0 || seenT {
				return 0, nil, ErrHeader
			}
			t, seenT = n, true
		case "v1":
			b, derr := hex.DecodeString(v)
			if derr == nil && len(b) == sha256.Size {
				sigs = append(sigs, b)
			}
		}
	}
	if !seenT || len(sigs) == 0 {
		return 0, nil, ErrHeader
	}
	return t, sigs, nil
}

// Verify checks a delivery's header against its raw body (the exact bytes received, before any
// JSON parsing) at the current time. tolerance ≤ 0 means DefaultTolerance.
func Verify(secret, header string, body []byte, tolerance time.Duration) error {
	return VerifyAt(secret, header, body, tolerance, time.Now())
}

// VerifyAt is Verify at a given time (tests).
func VerifyAt(secret, header string, body []byte, tolerance time.Duration, now time.Time) error {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	t, sigs, err := ParseHeader(header)
	if err != nil {
		return err
	}
	d := now.Sub(time.Unix(t, 0))
	if d > tolerance || d < -tolerance {
		return ErrTimestamp
	}
	want := mac(secret, strconv.FormatInt(t, 10), body)
	for _, s := range sigs {
		if hmac.Equal(s, want) {
			return nil
		}
	}
	return ErrSignature
}
