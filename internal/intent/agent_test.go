package intent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The args profile (docs/INTENTS.md §3): fractions canonicalize as IEEE-754 doubles, pinned so the
// TypeScript and Python signers match (gowebpki/jcs v1.0.2, checked 2026-10-01).
func TestArgsFloats(t *testing.T) {
	for _, c := range []struct{ in, canon string }{
		{`{"x":12.5}`, `{"x":12.5}`},
		{`{"x":1e21}`, `{"x":1e+21}`},
		{`{"x":1E21}`, `{"x":1e+21}`},
		{`{"x":0.0000001}`, `{"x":1e-7}`},
		{`{"x":1e-7}`, `{"x":1e-7}`},
		{`{"x":0.1}`, `{"x":0.1}`},
		{`{"x":-0}`, `{"x":0}`},
		{`{"x":-0.0}`, `{"x":0}`},
		{`{"x":100.0}`, `{"x":100}`},
		{`{"x":1e20}`, `{"x":100000000000000000000}`},
		{`{"x":5e-324}`, `{"x":5e-324}`},
		{`{"x":9007199254740991}`, `{"x":9007199254740991}`},
		{`{"x":-9007199254740991}`, `{"x":-9007199254740991}`},
		// A fraction token is a double: this one rounds, which is why the docs say send big values as strings.
		{`{"x":9007199254740993.0}`, `{"x":9007199254740992}`},
	} {
		got, err := CanonicalArgs([]byte(c.in))
		if err != nil || string(got) != c.canon {
			t.Errorf("%s: %s %v, want %s", c.in, got, err, c.canon)
		}
	}
}

func TestArgsRefusals(t *testing.T) {
	deep := strings.Repeat("[", MaxArgsDepth+1) + strings.Repeat("]", MaxArgsDepth+1)
	ok32 := strings.Repeat("[", MaxArgsDepth) + strings.Repeat("]", MaxArgsDepth)
	big := `{"x":"` + strings.Repeat("a", MaxArgsBytes) + `"}`
	for _, c := range []struct {
		name, in, reason string
	}{
		{"2^53", `{"x":9007199254740992}`, WhyBigInt},
		{"-2^53", `{"x":-9007199254740992}`, WhyBigInt},
		{"huge integer", `{"x":123456789012345678901234567890}`, WhyBigInt},
		{"duplicate key", `{"a":1,"a":2}`, WhyArgsInvalid},
		{"too deep", deep, WhyArgsInvalid},
		{"beyond double", `{"x":1e400}`, WhyArgsInvalid},
		{"invalid UTF-8", "{\"x\":\"\xff\"}", WhyArgsInvalid},
		{"lone surrogate", `{"x":"\ud800"}`, WhyArgsInvalid},
		{"trailing data", `{} {}`, WhyArgsInvalid},
		{"bad fraction", `{"x":1.}`, WhyArgsInvalid},
		{"bad exponent", `{"x":1e}`, WhyArgsInvalid},
		{"too large", big, WhyArgsInvalid},
	} {
		_, err := ArgsDigest([]byte(c.in))
		var ae *AgentError
		if !errors.As(err, &ae) || ae.Reason != c.reason {
			t.Errorf("%s: %v, want %s", c.name, err, c.reason)
		}
	}
	if _, err := ArgsDigest([]byte(ok32)); err != nil {
		t.Errorf("depth 32: %v", err)
	}
}

// The digest depends only on the JSON value: not on key order, whitespace or escapes.
func TestArgsDigestInvariance(t *testing.T) {
	a, err := ArgsDigest([]byte(`{"charge":"ch_3QxZ8f2L","amount":1200}`))
	if err != nil || a != "sha256:f7411a7cf4706333c9ba80c85aab7d8b92363ad3077a288dadc26bef42d89d36" {
		t.Fatalf("%s %v", a, err)
	}
	for _, v := range []string{
		`{"amount":1200,"charge":"ch_3QxZ8f2L"}`,
		" {\n \"amount\" : 1200 ,\t\"charge\":\"ch_3QxZ8f2L\" } ",
		`{"amount":1200,"charge":"ch_3QxZ8f2L"}`,
		`{"amount":12e2,"charge":"ch_3QxZ8f2L"}`,
	} {
		if b, err := ArgsDigest([]byte(v)); err != nil || b != a {
			t.Errorf("%s: %s %v", v, b, err)
		}
	}
}

func TestSplitHeader(t *testing.T) {
	sig := strings.Repeat("A", 85) + "A" // 86 chars = 64 bytes (last char's low bits zero)
	for _, c := range []struct {
		h  string
		ok bool
	}{
		{"e30." + sig, true},
		{"e30", false},
		{"e30." + sig + ".x", false},
		{"." + sig, false},
		{"e30.", false},
		{"e30=." + sig, false},           // padding
		{"e3+." + sig, false},            // standard alphabet
		{"e30." + sig[:85], false},       // 63 bytes and a bit
		{"e30." + sig[:84], false},       // 63 bytes
		{"e30." + sig[:85] + "B", false}, // non-zero trailing bits: a second encoding of the same bytes
	} {
		_, _, err := SplitHeader(c.h)
		if (err == nil) != c.ok {
			t.Errorf("%q: %v", c.h, err)
		}
	}
}

func TestParseAgentOrder(t *testing.T) {
	good := map[string]any{"v": AgentVersion, "org": "org_01k6h7kp6msmzrhdzwtcr3jnkp", "agent": "agt_01k633rrmh3bcz2p39gg4vp46q",
		"kid": "21fe31dfa154a261626bf854046fd227", "tool": "stripe.refund",
		"args_digest": "sha256:f7411a7cf4706333c9ba80c85aab7d8b92363ad3077a288dadc26bef42d89d36",
		"nonce":       "05c18be68a24c056cd6c7c8910dc0729", "iat": 1791019800000, "exp": 1791019860000}
	enc := func(f map[string]any) []byte { b, _ := json.Marshal(f); return b }
	if _, err := ParseAgent(enc(good)); err != nil {
		t.Fatal(err)
	}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range good {
			m[a] = b
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		name   string
		b      []byte
		reason string
		field  string
	}{
		{"tool upper-case connector", enc(with("tool", "Stripe.refund")), WhySchema, "tool"},
		{"tool with dashes and dots", enc(with("tool", "my-pg.public.Orders-v2")), "", ""},
		{"tool too long", enc(with("tool", "a1."+strings.Repeat("b", 129))), WhySchema, "tool"},
		{"iat zero", enc(with("iat", 0)), WhySchema, "iat"},
		{"iat string", enc(with("iat", "1")), WhySchema, "iat"},
		{"obo with a space", enc(with("on_behalf_of", "a b")), WhySchema, "on_behalf_of"},
		{"session ok", enc(with("session", "conv_9f2c")), "", ""},
		{"an array", []byte(`[]`), WhySchema, ""},
		{"v2 with v1 fields", enc(with("v", "deedglass.intent/2")), WhyVersion, "v"},
		{"fraction", []byte(strings.Replace(string(enc(good)), `"exp":1791019860000`, `"exp":1791019860000.5`, 1)), WhyStrict, "exp"},
	} {
		_, err := ParseAgent(c.b)
		var ae *AgentError
		switch {
		case c.reason == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.reason != "" && (!errors.As(err, &ae) || ae.Reason != c.reason || ae.Field != c.field):
			t.Errorf("%s: %v, want %s %s", c.name, err, c.reason, c.field)
		}
	}
}

// EncodeAgent refuses what ParseAgent would refuse, so a signer can't produce a refused intent.
func TestEncodeAgentRefuses(t *testing.T) {
	if _, err := EncodeAgent(&AgentIntent{V: AgentVersion, Org: "org_x"}); err == nil {
		t.Fatal("encoded an invalid intent")
	}
}

// FuzzArgsDigest: no panic, and the digest never depends on key order or whitespace.
func FuzzArgsDigest(f *testing.F) {
	for _, s := range []string{`{"charge":"ch_3QxZ8f2L","amount":1200}`, `{"a":[1,2.5,{"b":null}],"c":"é"}`, `{}`, `[1e21,-0]`, `"x"`, `{"a":1,"a":2}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ArgsDigest([]byte(s))
		if err != nil {
			var ae *AgentError
			if !errors.As(err, &ae) || ae.Reason == "" {
				t.Fatalf("an error without a reason: %v", err)
			}
			return
		}
		var v any
		if json.Unmarshal([]byte(s), &v) != nil {
			return
		}
		re, err := json.MarshalIndent(v, " ", "\t") // Go sorts keys and adds whitespace
		if err != nil {
			return
		}
		if d2, err := ArgsDigest(re); err == nil && d2 != d {
			t.Fatalf("digest changed under reformatting: %s → %s\n%s\n%s", d, d2, s, re)
		}
	})
}
