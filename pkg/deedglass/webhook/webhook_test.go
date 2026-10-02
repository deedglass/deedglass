package webhook

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const secret = "whsec_test_0123456789abcdefghijklmnopqrstuv"

func TestSignVerify(t *testing.T) {
	now := time.Unix(1790000000, 0)
	body := []byte(`{"id":"evt_x","type":"approval.requested"}`)
	h := Sign(secret, now, body)
	if !strings.HasPrefix(h, "t=1790000000,v1=") || len(h) != len("t=1790000000,v1=")+64 {
		t.Fatalf("header %q", h)
	}
	if err := VerifyAt(secret, h, body, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAt(secret, h, body, 0, now.Add(299*time.Second)); err != nil {
		t.Errorf("inside the window: %v", err)
	}
	cases := []struct {
		name   string
		secret string
		header string
		body   []byte
		at     time.Time
		want   error
	}{
		{"wrong secret", secret + "x", h, body, now, ErrSignature},
		{"body changed", secret, h, append([]byte{}, append(body, ' ')...), now, ErrSignature},
		{"too late", secret, h, body, now.Add(301 * time.Second), ErrTimestamp},
		{"too early", secret, h, body, now.Add(-301 * time.Second), ErrTimestamp},
		{"no header", secret, "", body, now, ErrNoHeader},
		{"spaces", secret, "   ", body, now, ErrNoHeader},
		{"no t", secret, h[strings.Index(h, "v1="):], body, now, ErrHeader},
		{"no v1", secret, "t=1790000000", body, now, ErrHeader},
		{"t twice", secret, "t=1790000000," + h, body, now, ErrHeader},
		{"t negative", secret, "t=-5," + h[strings.Index(h, "v1="):], body, now, ErrHeader},
		{"t not a number", secret, "t=abc," + h[strings.Index(h, "v1="):], body, now, ErrHeader},
		{"short v1", secret, "t=1790000000,v1=abcd", body, now, ErrHeader},
		{"huge", secret, h + "," + strings.Repeat("x", 5000), body, now, ErrHeader},
	}
	for _, c := range cases {
		if err := VerifyAt(c.secret, c.header, c.body, 0, c.at); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
}

func TestRotationAndFutureSchemes(t *testing.T) {
	now := time.Unix(1790000000, 0)
	body := []byte("{}")
	good := Sign(secret, now, body)
	old := Sign("whsec_old", now, body)
	// Several v1 values (rotation) in any order, plus an unknown v2 scheme and junk parts.
	h := old + "," + good[strings.Index(good, "v1="):] + ",v2=zzz,junk, v1=nothex"
	if err := VerifyAt(secret, h, body, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAt("whsec_old", h, body, 0, now); err != nil {
		t.Fatal(err)
	}
	ts, sigs, err := ParseHeader(h)
	if err != nil || ts != 1790000000 || len(sigs) != 2 {
		t.Fatalf("%d %d %v", ts, len(sigs), err)
	}
	// A custom tolerance.
	if err := VerifyAt(secret, good, body, 10*time.Second, now.Add(11*time.Second)); !errors.Is(err, ErrTimestamp) {
		t.Fatal(err)
	}
	if err := Verify(secret, Sign(secret, time.Now(), body), body, 0); err != nil {
		t.Fatal(err)
	}
}

// FuzzSignatureHeader: any header parses or fails cleanly, never panics, and never verifies
// without the right MAC.
func FuzzSignatureHeader(f *testing.F) {
	now := time.Unix(1790000000, 0)
	for _, s := range []string{Sign(secret, now, []byte("{}")), "", "t=", "v1=", "t=1,v1=00", "t=1790000000,v1=" + strings.Repeat("0", 64),
		"t=99999999999999999999,v1=", ",,,,", "t=1790000000, v1=" + strings.Repeat("A", 64)} {
		f.Add(s, []byte("{}"))
	}
	f.Fuzz(func(t *testing.T, header string, body []byte) {
		ts, sigs, err := ParseHeader(header)
		if err == nil && (ts <= 0 || len(sigs) == 0) {
			t.Fatalf("accepted %q", header)
		}
		if VerifyAt(secret, header, body, 0, now) == nil {
			// Only a header carrying the real MAC for (t, body) can verify.
			if !strings.Contains(header, Sign(secret, time.Unix(ts, 0), body)[len("t="+strconv.FormatInt(ts, 10)+",v1="):]) &&
				!strings.Contains(strings.ToLower(header), Sign(secret, time.Unix(ts, 0), body)[len("t="+strconv.FormatInt(ts, 10)+",v1="):]) {
				t.Fatalf("verified without the MAC: %q", header)
			}
		}
	})
}
