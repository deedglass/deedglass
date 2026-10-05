package webhook

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// FuzzWebhookSignature (S34): a delivery signed for one body and one secret verifies for exactly
// that pair. Any other body, any other secret, or a timestamp outside the window is refused, and
// nothing panics.
func FuzzWebhookSignature(f *testing.F) {
	for _, s := range []struct {
		body, other []byte
		sec2        string
		skew        int64
	}{
		{[]byte(`{"id":"evt_1"}`), []byte(`{"id":"evt_2"}`), "whsec_other", 0},
		{[]byte{}, []byte{0}, "", 299},
		{[]byte("\xff\xfe"), []byte("\xff\xfe "), secret + " ", -301},
		{[]byte(`{"a":1}`), []byte(`{"a":1}` + "\n"), "WHSEC_TEST_0123456789ABCDEFGHIJKLMNOPQRSTUV", 301},
	} {
		f.Add(s.body, s.other, s.sec2, s.skew)
	}
	now := time.Unix(1790000000, 0)
	f.Fuzz(func(t *testing.T, body, other []byte, secret2 string, skew int64) {
		skew %= 1000
		h := Sign(secret, now, body)
		at := now.Add(time.Duration(skew) * time.Second)
		err := VerifyAt(secret, h, body, 0, at)
		inside := skew <= 300 && skew >= -300
		switch {
		case inside && err != nil:
			t.Fatalf("own signature refused at skew %d: %v", skew, err)
		case !inside && !errors.Is(err, ErrTimestamp):
			t.Fatalf("skew %d: %v", skew, err)
		}
		if string(other) != string(body) && VerifyAt(secret, h, other, 0, now) == nil {
			t.Fatalf("a signature for %q verified %q", body, other)
		}
		// HMAC pads a short key with zeros, so secret+"\x00" is the same key; skip those.
		if strings.TrimRight(secret2, "\x00") != secret && VerifyAt(secret2, h, body, 0, now) == nil {
			t.Fatalf("another secret %q verified", secret2)
		}
	})
}
