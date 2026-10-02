package identity

import (
	"bytes"
	"crypto/sha256"
	mrand "math/rand/v2"
	"strings"
	"testing"
)

func TestNewToken(t *testing.T) {
	seen := make(map[string]bool, 10000)
	for i := 0; i < 10000; i++ {
		raw, h, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) != TokenLen || seen[raw] {
			t.Fatalf("token %d: %q (len %d, dup %v)", i, raw, len(raw), seen[raw])
		}
		seen[raw] = true
		h2, err := HashToken(raw)
		if err != nil || h2 != h || h != sha256.Sum256([]byte(raw)) {
			t.Fatalf("HashToken disagrees with NewToken: %v", err)
		}
	}
}

func TestHashTokenRefusesMalformed(t *testing.T) {
	good, _, _ := NewToken()
	for name, raw := range map[string]string{
		"empty":         "",
		"42 chars":      good[:42],
		"44 chars":      good + "A",
		"plus":          "+" + good[1:],
		"slash":         good[:42] + "/",
		"padding":       good[:42] + "=",
		"space":         " " + good[1:],
		"newline":       good[:42] + "\n",
		"non-canonical": good[:42] + "B", // the last character carries 2 spare bits; most changes break strict decoding
		"utf8":          "é" + good[2:],
	} {
		if name == "non-canonical" {
			// Only some last characters are non-canonical; find one that is.
			for _, c := range "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" {
				cand := good[:42] + string(c)
				if _, err := HashToken(cand); err != nil {
					raw = cand
					break
				}
			}
		}
		if _, err := HashToken(raw); err != ErrMalformedToken {
			t.Errorf("%s: %q accepted (%v)", name, raw, err)
		}
	}
}

func FuzzHashToken(f *testing.F) {
	good, _, _ := NewToken()
	for _, s := range []string{"", good, good[:42] + "=", strings.Repeat("A", 43), strings.Repeat("_", 43), "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		h, err := HashToken(s)
		if err == nil && (len(s) != TokenLen || h != sha256.Sum256([]byte(s))) {
			t.Fatalf("accepted %q", s)
		}
	})
}

// Six positions × ten digits over 1,000,000 codes: each position's digit counts must look uniform
// (chi-square with 9 degrees of freedom below 27.88, which is p > 0.001).
// TestNewCodeUniform checks the sampling (crypto/rand.Int's rejection over 10^6) with a seeded
// ChaCha8 stream in place of crypto/rand, so the chi-square statistic is the same on every run and
// the test can't flake (DL-034: it failed once in ~1,000 runs at p < 0.001 on crypto/rand).
// TestNewCodeFormat covers the crypto/rand path itself.
func TestNewCodeUniform(t *testing.T) {
	n := 1_000_000
	if testing.Short() {
		n = 100_000
	}
	src := mrand.NewChaCha8([32]byte{'d', 'e', 'e', 'd', 'g', 'l', 'a', 's', 's'})
	var counts [CodeDigits][10]int
	leadingZero := false
	for i := 0; i < n; i++ {
		c, err := newCodeFrom(src)
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != CodeDigits {
			t.Fatalf("code %q", c)
		}
		if c[0] == '0' {
			leadingZero = true
		}
		for p := 0; p < CodeDigits; p++ {
			counts[p][c[p]-'0']++
		}
	}
	if !leadingZero {
		t.Fatal("no code started with 0: leading zeros must be kept")
	}
	want := float64(n) / 10
	for p := 0; p < CodeDigits; p++ {
		chi := 0.0
		for d := 0; d < 10; d++ {
			x := float64(counts[p][d]) - want
			chi += x * x / want
		}
		if chi >= 27.88 {
			t.Errorf("position %d: chi-square %.2f ≥ 27.88 (p < 0.001): %v", p, chi, counts[p])
		}
	}
}

func TestNewCodeFormat(t *testing.T) {
	for i := 0; i < 2_000; i++ {
		c, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := NormalizeCode(c); !ok || len(c) != CodeDigits {
			t.Fatalf("code %q", c)
		}
	}
}

func TestHashCode(t *testing.T) {
	pepper := bytes.Repeat([]byte{7}, 32)
	a := HashCode(pepper, "ltk_a", "482913")
	if a != HashCode(pepper, "ltk_a", "482913") {
		t.Fatal("not deterministic")
	}
	for name, b := range map[string][32]byte{
		"another code":   HashCode(pepper, "ltk_a", "482914"),
		"another token":  HashCode(pepper, "ltk_b", "482913"),
		"another pepper": HashCode(bytes.Repeat([]byte{8}, 32), "ltk_a", "482913"),
		"no pepper":      HashCode(nil, "ltk_a", "482913"),
		"shifted":        HashCode(pepper, "ltk_a4", "82913"),
	} {
		if Equal(a, b) {
			t.Errorf("%s collides", name)
		}
	}
	if !equalBytes(a[:], a) || equalBytes(a[:31], a) || equalBytes(nil, a) {
		t.Fatal("equalBytes")
	}
}

func TestNormalizeCode(t *testing.T) {
	for in, want := range map[string]string{"482913": "482913", "482 913": "482913", " 004817 ": "004817"} {
		if got, ok := NormalizeCode(in); !ok || got != want {
			t.Errorf("%q → %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "48291", "4829134", "48291a", "482-913", "４８２９１３"} {
		if _, ok := NormalizeCode(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}
