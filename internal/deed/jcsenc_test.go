package deed

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gowebpki/jcs"

	"github.com/deedglass/deedglass/internal/intent"
)

// bs builds a JSON backslash escape at run time: bs("u0061") is backslash, u, 0061.
func bs(s string) string { return "\\" + s }

// vectorLines returns every line of every deed vector (valid and invalid) in testdata/deeds.
func vectorLines(t testing.TB) [][]byte {
	t.Helper()
	var out [][]byte
	err := filepath.WalkDir("../../testdata/deeds", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(p, ".json") || strings.HasSuffix(p, ".jsonl")) || strings.Contains(p, "expected") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, l := range bytes.Split(b, []byte("\n")) {
			if len(l) > 0 {
				out = append(out, l)
			}
		}
		return nil
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("read vectors: %d lines, %v", len(out), err)
	}
	return out
}

// checkTree compares canonicalTree with gowebpki/jcs and with canonicalBytes for every input the
// strict profile accepts.
func checkTree(t *testing.T, b []byte) {
	t.Helper()
	tree, err := intent.ParseStrictNonNegative(b)
	if err != nil {
		if _, cerr := canonicalBytes(b); cerr == nil {
			t.Fatalf("canonicalBytes accepted what the strict parser refuses: %q", b)
		}
		return
	}
	got, verr := canonicalTree(tree)
	if verr != nil {
		t.Fatalf("canonicalTree refused a strict tree: %v (%q)", verr, b)
	}
	want, err := jcs.Transform(b)
	if err != nil {
		t.Fatalf("jcs refused a strict tree: %v (%q)", err, b)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("canonicalTree differs from jcs:\n got %q\nwant %q\n  in %q", got, want, b)
	}
	if ref, err := canonicalBytes(b); err != nil || !bytes.Equal(got, ref) {
		t.Fatalf("canonicalTree differs from canonicalBytes: %q %v", ref, err)
	}
}

// checkBody compares canonicalBody with StrictCanonical. Both must accept the same bodies and write
// the same bytes, with one difference: json.Marshal replaces invalid UTF-8 with U+FFFD, so
// StrictCanonical accepts it, while canonicalBody refuses it.
func checkBody(t *testing.T, b *Body) {
	t.Helper()
	got, gerr := canonicalBody(b)
	want, werr := StrictCanonical(b)
	switch {
	case gerr == nil && werr == nil:
		if !bytes.Equal(got, want) {
			t.Fatalf("canonicalBody differs from StrictCanonical:\n got %s\nwant %s", got, want)
		}
	case gerr == nil:
		t.Fatalf("canonicalBody accepted a body StrictCanonical refuses (%v):\n%s", werr, got)
	case CodeOf(gerr) != CodeMalformed:
		t.Fatalf("canonicalBody error is not malformed: %v", gerr)
	case werr == nil:
		if !strings.Contains(gerr.Error(), "not valid UTF-8") || !bodyHasInvalidUTF8(b) {
			t.Fatalf("canonicalBody refused a body StrictCanonical accepts: %v", gerr)
		}
	case CodeOf(werr) != CodeMalformed:
		t.Fatalf("StrictCanonical error is not malformed: %v", werr)
	}
}

// bodyHasInvalidUTF8 reports whether any string in b is invalid UTF-8.
func bodyHasInvalidUTF8(b *Body) bool {
	bad := false
	visitStrings(b, func(p *string) { bad = bad || !utf8.ValidString(*p) })
	return bad
}

// visitStrings calls f with every string field of b (set or not, pointers only when non-nil).
func visitStrings(b *Body, f func(*string)) {
	opt := func(p *string) {
		if p != nil {
			f(p)
		}
	}
	k := string(b.Kind)
	f(&k)
	b.Kind = Kind(k)
	for _, p := range []*string{&b.DeedID, &b.Org, &b.PrevHash, &b.IssuedAt, &b.SealKeyID} {
		f(p)
	}
	opt(b.OnBehalfOf)
	opt(b.Session)
	opt(b.Amends)
	if a := b.Agent; a != nil {
		f(&a.ID)
		f(&a.Name)
	}
	if a := b.Action; a != nil {
		for _, p := range []*string{&a.ID, &a.Tool, &a.Mode, &a.RequestDigest} {
			f(p)
		}
		opt(a.Connector)
		opt(a.IntentHash)
	}
	if p := b.Policy; p != nil {
		f(&p.Mode)
		f(&p.Decision)
		for _, s := range []*string{p.ID, p.VersionID, p.VersionHash, p.RuleID} {
			opt(s)
		}
	}
	if a := b.Approval; a != nil {
		f(&a.ID)
		f(&a.Outcome)
		for i := range a.DecidedBy {
			f(&a.DecidedBy[i])
		}
		opt(a.DecidedAt)
		opt(a.Deed)
	}
	if e := b.Execution; e != nil {
		for _, p := range []*string{&e.Status, &e.StartedAt, &e.FinishedAt} {
			f(p)
		}
		opt(e.ResultDigest)
		opt(e.ErrorCode)
	}
	if w := b.Witness; w != nil {
		for _, p := range []*string{&w.Level, &w.Verdict, &w.Method} {
			f(p)
		}
		opt(w.DetailDigest)
	}
	if c := b.Cost; c != nil {
		f(&c.Currency)
	}
	if a := b.Admin; a != nil {
		for _, p := range []*string{&a.Event, &a.Action, &a.Actor.Type, &a.Actor.ID} {
			f(p)
		}
		if a.Target != nil {
			f(&a.Target.Type)
			f(&a.Target.ID)
		}
		opt(a.Reason)
		opt(a.DataDigest)
	}
}

// fullBody returns a body with every section set, so each member is written.
func fullBody() *Body {
	s := func(v string) *string { return &v }
	return &Body{V: 1, Kind: KindAction, DeedID: "d", Org: "o", Seq: 7, PrevHash: "p", IssuedAt: "i",
		Agent: &AgentRef{ID: "a", Name: "n"}, OnBehalfOf: s("obo"), Session: s("ses"),
		Action:    &ActionSection{ID: "x", Tool: "t", Connector: s("c"), Mode: "m", RequestDigest: "r", IntentHash: s("h"), IntentSigned: true},
		Policy:    &PolicySection{ID: s("p"), VersionID: s("v"), VersionHash: s("vh"), Mode: "m", Decision: "d", RuleID: s("r")},
		Approval:  &ApprovalSection{ID: "a", Outcome: "o", DecidedBy: []string{"u1", "u2"}, DecidedAt: s("t"), Deed: s("d")},
		Execution: &ExecutionSection{Status: "s", StartedAt: "a", FinishedAt: "b", LatencyMS: 12, ResultDigest: s("r"), ErrorCode: s("e")},
		Witness:   &WitnessSection{Level: "L2", Verdict: "V", Method: "m", DetailDigest: s("d")},
		Cost:      &Cost{Amount: MaxSafeInteger, Currency: "inr"},
		Admin:     &AdminSection{Event: "e", Action: "a", Actor: Ref{Type: "t", ID: "i"}, Target: &Ref{Type: "t2", ID: "i2"}, Reason: s("r"), DataDigest: s("d")},
		Amends:    s("am"), SealKeyID: "sk"}
}

// The deed vectors canonicalize the same three ways: StrictCanonical of the parsed body, the body
// encoder and the tree encoder over the strict-parsed body.
func TestCanonicalVectors(t *testing.T) {
	n := 0
	for _, l := range vectorLines(t) {
		checkTree(t, l)
		p, err := parseEnvelope(l)
		if err != nil {
			continue
		}
		checkBody(t, &p.env.Body)
		want, _ := StrictCanonical(&p.env.Body)
		if !bytes.Equal(p.bodyJCS, want) {
			t.Fatalf("parseEnvelope body bytes differ:\n got %s\nwant %s", p.bodyJCS, want)
		}
		n++
	}
	if n < 20 {
		t.Fatalf("only %d valid vectors", n)
	}
}

func TestCanonicalBodyShapes(t *testing.T) {
	full := fullBody()
	checkBody(t, full)
	checkBody(t, &Body{})
	empty := fullBody()
	empty.Approval.DecidedBy = []string{}
	checkBody(t, empty)
	null := fullBody()
	null.Approval.DecidedBy = nil
	null.Admin.Target = nil
	checkBody(t, null)
	// Every string field, one at a time, holding text that needs each kind of escaping.
	hard := "q\" b\\ \b\f\n\r\t \x00\x01\x1f\x7f </script>& é € " + string(rune(0x2028)) + string(rune(0x2029)) + " 😀 " + string(rune(0xFFFF))
	count := 0
	visitStrings(fullBody(), func(*string) { count++ })
	for i := 0; i < count; i++ {
		b := fullBody()
		j := 0
		visitStrings(b, func(p *string) {
			if j == i {
				*p = hard
			}
			j++
		})
		checkBody(t, b)
	}
}

func TestCanonicalBodyRefuses(t *testing.T) {
	cases := []struct {
		name, want string
		edit       func(*Body)
	}{
		{"negative seq", "negative numbers and -0 are not allowed at seq", func(b *Body) { b.Seq = -1 }},
		{"large seq", "integer magnitude exceeds 2^53-1 at seq", func(b *Body) { b.Seq = MaxSafeInteger + 1 }},
		{"negative v", "negative numbers and -0 are not allowed at v", func(b *Body) { b.V = -1 }},
		{"large cost", "integer magnitude exceeds 2^53-1 at cost.amount", func(b *Body) { b.Cost.Amount = 1 << 62 }},
		{"negative latency", "negative numbers and -0 are not allowed at execution.latency_ms", func(b *Body) { b.Execution.LatencyMS = -5 }},
		{"invalid UTF-8", "input is not valid UTF-8 at agent.name", func(b *Body) { b.Agent.Name = "a\xffb" }},
		{"surrogate bytes", "input is not valid UTF-8 at admin.reason", func(b *Body) { *b.Admin.Reason = "\xed\xa0\x80" }},
		{"invalid UTF-8 in list", "input is not valid UTF-8 at approval.decided_by[1]", func(b *Body) { b.Approval.DecidedBy[1] = "\xc3" }},
	}
	for _, tc := range cases {
		b := fullBody()
		tc.edit(b)
		_, err := canonicalBody(b)
		if CodeOf(err) != CodeMalformed || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want malformed %q", tc.name, err, tc.want)
		}
		checkBody(t, b)
	}
}

// RFC 8785 §3.2.3 orders keys by UTF-16 code units, so U+E000..U+FFFF sorts after a surrogate pair.
func TestCompareUTF16(t *testing.T) {
	r := func(c rune) string { return string(c) }
	keys := []string{r(0x20ac), "\r", r(0xfb33), "1", r(0x1f600), r(0x80), r(0xf6), r(0xe000), r(0xffff), r(0x10ffff), "", "a", "ab", r(0x10000), r(0xd7ff) + "x"}
	got := slices.Clone(keys)
	slices.SortFunc(got, compareUTF16)
	want := slices.Clone(keys)
	slices.SortFunc(want, func(a, b string) int { return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b))) })
	if !slices.Equal(got, want) {
		t.Fatalf("order %q, want %q", got, want)
	}
	for _, a := range keys {
		for _, b := range keys {
			w := slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
			if g := compareUTF16(a, b); (g < 0) != (w < 0) || (g == 0) != (w == 0) {
				t.Fatalf("compare(%q, %q) = %d, want sign of %d", a, b, g, w)
			}
		}
	}
}

// Every ASCII byte and the RFC 8785 test inputs that pass the strict profile.
func TestCanonicalTreeReference(t *testing.T) {
	for c := 0; c < 0x80; c++ {
		checkTree(t, []byte(`{"k`+string(rune(c))+`":"`+string(rune(c))+`"}`))
		checkTree(t, []byte(`["`+bs("u00")+string("0123456789abcdef"[c>>4])+string("0123456789abcdef"[c&15])+`"]`))
	}
	checkTree(t, []byte(`{"`+bs("ud83d")+bs("ude00")+`":1,"`+bs("ue000")+`":2,"`+bs("/")+`":[true,false,null,{}],"":9007199254740991}`))
	ins, _ := filepath.Glob("../../testdata/jcs/input/*.json")
	if len(ins) == 0 {
		t.Fatal("no RFC 8785 inputs")
	}
	for _, p := range ins {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		checkTree(t, b)
	}
}

// FuzzCanonical is the differential test of the direct encoder. data is checked as a tree (against
// gowebpki/jcs and canonicalBytes) and, read loosely into a Body with s and n placed in the field
// sel picks, as a body (against StrictCanonical).
func FuzzCanonical(f *testing.F) {
	for _, l := range vectorLines(f) {
		f.Add(l, "", int64(0), uint8(0))
		if p, err := parseEnvelope(l); err == nil {
			bj, _ := json.Marshal(&p.env.Body)
			f.Add(bj, "Priya’s agent — ☕", int64(1), uint8(3))
		}
	}
	fb, _ := json.Marshal(fullBody())
	f.Add(fb, "a\xffb", int64(-1), uint8(7))
	f.Add(fb, "\x00\x1f\x7f\"\\"+string(rune(0x2028)), MaxSafeInteger+1, uint8(200))
	f.Add([]byte(`{"`+bs("ud83d")+bs("ude00")+`":1,"`+bs("ue000")+`":[0,"`+bs("u001f")+`"]}`), "", int64(0), uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, s string, n int64, sel uint8) {
		checkTree(t, data)
		b := fullBody()
		if sel&1 == 0 {
			b = &Body{}
		}
		if err := json.Unmarshal(data, b); err != nil {
			return
		}
		count := 0
		visitStrings(b, func(*string) { count++ })
		if count > 0 {
			j, at := 0, int(sel>>1)%count
			visitStrings(b, func(p *string) {
				if j == at {
					*p = s
				}
				j++
			})
		}
		switch sel % 5 {
		case 0:
			b.Seq = n
		case 1:
			b.V = n
		case 2:
			if b.Cost != nil {
				b.Cost.Amount = n
			}
		case 3:
			if b.Execution != nil {
				b.Execution.LatencyMS = n
			}
		}
		checkBody(t, b)
	})
}
