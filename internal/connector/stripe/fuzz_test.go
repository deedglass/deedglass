package stripe

import (
	"net/url"
	"strings"
	"testing"
)

func FuzzStripeForm(f *testing.F) {
	f.Add("metadata", "deedglass_action_id", "act_01j9zq7c4m8x2v6t0r3n5k1h7d")
	f.Add("a&b", "c=d", "e f[g]")
	f.Add("", "", "")
	f.Fuzz(func(t *testing.T, k, sub, v string) {
		if k == "" || strings.ContainsAny(k+sub, "[]") {
			return
		}
		form, err := Form(map[string]any{k: map[string]any{sub: v}, "amount": int64(2000)})
		if err != nil {
			t.Fatal(err)
		}
		back, err := url.ParseQuery(form.Encode())
		if err != nil {
			t.Fatalf("round trip: %v", err)
		}
		if back.Get(k+"["+sub+"]") != v || back.Get("amount") != "2000" {
			t.Fatalf("lost %q[%q]=%q in %s", k, sub, v, form.Encode())
		}
	})
}

func FuzzLivemodeScan(f *testing.F) {
	f.Add(`{"livemode":false}`, 0)
	f.Add(`{"data":[{"x":1}]}`, 2)
	f.Add(`[]`, 1)
	f.Fuzz(func(t *testing.T, doc string, where int) {
		if !Livemode([]byte(`{"a":` + `{"livemode":true}` + `}`)) {
			t.Fatal("nested true missed")
		}
		// Splice a livemode:true object into doc at a value position: it must always trip.
		body := `{"wrap":[` + jsonString(doc) + `,{"livemode":true}],"n":` + itoa(where) + `}`
		if !Livemode([]byte(body)) {
			t.Fatalf("missed in %s", body)
		}
		// A document that never says livemode true never trips.
		if !strings.Contains(doc, "livemode") && Livemode([]byte(doc)) {
			t.Fatalf("tripped without livemode: %q", doc)
		}
	})
}

func jsonString(s string) string {
	b := strings.Builder{}
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			b.WriteString(" ")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func itoa(n int) string {
	if n < 0 {
		n = -n
	}
	return strings.TrimLeft(strings.Repeat("0", 1)+string(rune('0'+n%10)), "")
}
