package ids

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const goodAgent = "agt_01j9zr3m5k8q2w7x4t6v0b1c9d"

func TestParse(t *testing.T) {
	cases := []struct {
		name, in, reason string // reason "" means ok
	}{
		{"ok", goodAgent, ""},
		{"uppercase", "agt_01J9ZR3M5K8Q2W7X4T6V0B1C9D", "ids are lowercase"},
		{"uppercase prefix", "AGT_01j9zr3m5k8q2w7x4t6v0b1c9d", "ids are lowercase"},
		{"other prefix", "org_01j9zr3m5k8q2w7x4t6v0b1c9d", "expected an agent id (agt_…)"},
		{"no underscore", "agt01j9zr3m5k8q2w7x4t6v0b1c9d", "expected an agent id (agt_…)"},
		{"empty ulid", "agt_", "expected 26 characters after agt_, got 0"},
		{"25 chars", goodAgent[:len(goodAgent)-1], "expected 26 characters after agt_, got 25"},
		{"27 chars", goodAgent + "0", "expected 26 characters after agt_, got 27"},
		{"leading 8", "agt_81j9zr3m5k8q2w7x4t6v0b1c9d", "the first character after the prefix must be 0-7"},
		{"i", "agt_01j9ir3m5k8q2w7x4t6v0b1c9d", `character 'i' is not in the id alphabet`},
		{"l", "agt_01j9lr3m5k8q2w7x4t6v0b1c9d", `character 'l'`},
		{"o", "agt_01j9or3m5k8q2w7x4t6v0b1c9d", `character 'o'`},
		{"u", "agt_01j9ur3m5k8q2w7x4t6v0b1c9d", `character 'u'`},
		{"trailing space", goodAgent + " ", "got 27"},
		{"leading space", " " + goodAgent, "expected an agent id"},
		{"inner space", "agt_01j9zr3m5k8q2w7 4t6v0b1c9d", `character ' '`},
		{"empty", "", "expected an agent id"},
		{"non-ascii", "agt_01j9zr3m5k8q2w7x4t6v0b1c9é", "got 27"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := Parse[AgentKind](tc.in)
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("Parse(%q): %v", tc.in, err)
				}
				if id.String() != tc.in || id.IsZero() {
					t.Fatalf("round trip: %q", id)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse(%q) = %v, want ErrInvalid", tc.in, err)
			}
			if !strings.Contains(err.Error(), tc.reason) || !strings.HasPrefix(err.Error(), "invalid agent id ") {
				t.Fatalf("Parse(%q) error %q, want it to contain %q", tc.in, err, tc.reason)
			}
			if !id.IsZero() {
				t.Fatal("a failed parse must return the zero id")
			}
		})
	}
}

func TestErrorMessages(t *testing.T) {
	_, err := Parse[AgentKind]("agt_01J9ZR3M5K8Q2W7X4T6V0B1C9D")
	if got, want := err.Error(), `invalid agent id "agt_01J9ZR3M5K8Q2W7X4T6V0B1C9D": ids are lowercase`; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	_, err = Parse[OrgKind](goodAgent)
	if got, want := err.Error(), `invalid org id "agt_01j9zr3m5k8q2w7x4t6v0b1c9d": expected an org id (org_…)`; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	_, err = Parse[AgentKind](strings.Repeat("x", 100))
	if !strings.Contains(err.Error(), `"`+strings.Repeat("x", 40)+`…"`) {
		t.Errorf("long input not shortened: %q", err)
	}
	e := &Error{Prefix: "zz", Input: "q", Reason: "r"}
	if e.Error() != `invalid zz id "q": r` {
		t.Errorf("unknown prefix message %q", e)
	}
}

func TestULIDSpecVector(t *testing.T) {
	// github.com/ulid/spec: 1469918176385 encodes to 01ARYZ6S41 (lowercase here).
	var dst [ulidLen]byte
	var rnd [10]byte
	encode(dst[:], 1469918176385, &rnd)
	if got := string(dst[:10]); got != "01aryz6s41" {
		t.Fatalf("time part %q, want 01aryz6s41", got)
	}
	ms, _, _ := decode(string(dst[:]))
	if ms != 1469918176385 {
		t.Fatalf("decode ms %d", ms)
	}
	// Largest valid value.
	for i := range rnd {
		rnd[i] = 0xff
	}
	encode(dst[:], maxTime, &rnd)
	if got := string(dst[:]); got != "7zzzzzzzzzzzzzzzzzzzzzzzzz" {
		t.Fatalf("max %q", got)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 10000 {
		ms := r.Uint64() & maxTime
		var rnd [10]byte
		for i := range rnd {
			rnd[i] = byte(r.Uint32())
		}
		var dst [ulidLen]byte
		encode(dst[:], ms, &rnd)
		gotMs, hi, lo := decode(string(dst[:]))
		if gotMs != ms || byte(hi>>8) != rnd[0] || byte(hi) != rnd[1] || byte(lo) != rnd[9] || byte(lo>>56) != rnd[2] {
			t.Fatalf("round trip ms=%d rnd=%x got ms=%d hi=%x lo=%x", ms, rnd, gotMs, hi, lo)
		}
		if err := check("act", "act_"+string(dst[:])); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMonotonicOneGoroutine(t *testing.T) {
	n := 1_000_000
	if testing.Short() {
		n = 100_000
	}
	prev := New[ActionKind]().String()
	for range n {
		cur := New[ActionKind]().String()
		if cur <= prev {
			t.Fatalf("not increasing: %s then %s", prev, cur)
		}
		prev = cur
	}
}

func TestUniqueConcurrent(t *testing.T) {
	const workers, each = 64, 10_000
	out := make([][]string, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := make([]string, each)
			for i := range s {
				s[i] = New[DeedKind]().String()
			}
			out[w] = s
		}()
	}
	wg.Wait()
	seen := make(map[string]struct{}, workers*each)
	for _, s := range out {
		for i, id := range s {
			if _, dup := seen[id]; dup {
				t.Fatalf("duplicate id %s", id)
			}
			seen[id] = struct{}{}
			if i > 0 && id <= s[i-1] {
				t.Fatalf("not increasing within a goroutine: %s then %s", s[i-1], id)
			}
		}
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time        { return c.t }
func (c *fakeClock) sleep(d time.Duration) { c.t = c.t.Add(d) }
func (c *fakeClock) step(d time.Duration)  { c.t = c.t.Add(d) }

type repeatReader byte

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestClockBackwards(t *testing.T) {
	c := &fakeClock{t: time.UnixMilli(1_790_000_000_000)}
	g := &generator{now: c.now, rand: repeatReader(0x10), sleep: c.sleep}
	a := g.next("act")
	c.step(-5 * time.Second)
	b := g.next("act")
	c2 := g.next("act")
	if !(a < b && b < c2) {
		t.Fatalf("not increasing after the clock stepped back: %s %s %s", a, b, c2)
	}
	if MustParse[ActionKind](b).Time() != MustParse[ActionKind](a).Time() {
		t.Fatal("the timestamp must stay at the last value while the clock is behind")
	}
	c.step(6 * time.Second)
	d := g.next("act")
	if d <= c2 || !MustParse[ActionKind](d).Time().Equal(c.t.UTC()) {
		t.Fatalf("after recovery: %s (time %v, clock %v)", d, MustParse[ActionKind](d).Time(), c.t)
	}
}

func TestRandomOverflowWaitsForNextMillisecond(t *testing.T) {
	c := &fakeClock{t: time.UnixMilli(1_790_000_000_000)}
	g := &generator{now: c.now, rand: repeatReader(0xff), sleep: c.sleep}
	a := g.next("act") // random part is all ones: the next increment overflows
	b := g.next("act")
	if b <= a {
		t.Fatalf("not increasing across overflow: %s then %s", a, b)
	}
	ta, tb := MustParse[ActionKind](a).Time(), MustParse[ActionKind](b).Time()
	if tb.Sub(ta) != time.Millisecond {
		t.Fatalf("overflow should wait exactly one millisecond on this clock, waited %v", tb.Sub(ta))
	}
	if !strings.HasSuffix(a, "zzzzzzzzzzzzzzzz") {
		t.Fatalf("injected randomness not used: %s", a)
	}
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

func TestRandFailurePanics(t *testing.T) {
	g := &generator{now: time.Now, rand: failReader{}, sleep: time.Sleep}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "crypto/rand failed") {
			t.Fatalf("want a panic, got %v", r)
		}
	}()
	g.next("act")
}

func TestClockOutOfRangePanics(t *testing.T) {
	g := &generator{now: func() time.Time { return time.UnixMilli(maxTime + 1) }, rand: repeatReader(1), sleep: time.Sleep}
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic")
		}
	}()
	g.next("act")
}

func TestNegativeClockClampsToZero(t *testing.T) {
	g := &generator{now: func() time.Time { return time.UnixMilli(-5) }, rand: repeatReader(1), sleep: time.Sleep}
	s := g.next("act")
	if !MustParse[ActionKind](s).Time().Equal(time.UnixMilli(0).UTC()) {
		t.Fatalf("got %s", s)
	}
}

func TestTimeAndRoundTrips(t *testing.T) {
	before := time.Now().Truncate(time.Millisecond)
	id := New[AgentKind]()
	after := time.Now()
	if tm := id.Time(); tm.Before(before) || tm.After(after) || tm.Location() != time.UTC {
		t.Fatalf("Time() %v not in [%v, %v] UTC", tm, before, after)
	}
	if !(AgentID{}).Time().IsZero() {
		t.Fatal("zero id must have the zero time")
	}

	// Text.
	b, _ := id.MarshalText()
	var back AgentID
	if err := back.UnmarshalText(b); err != nil || back != id {
		t.Fatalf("text round trip: %v %v", back, err)
	}
	// JSON, inside a struct.
	type doc struct {
		Agent AgentID `json:"agent"`
		Org   OrgID   `json:"org"`
	}
	j, err := json.Marshal(doc{Agent: id})
	if err != nil {
		t.Fatal(err)
	}
	if string(j) != `{"agent":"`+id.String()+`","org":""}` {
		t.Fatalf("json %s", j)
	}
	var d doc
	if err := json.Unmarshal(j, &d); err != nil || d.Agent != id || !d.Org.IsZero() {
		t.Fatalf("json round trip %+v %v", d, err)
	}
	if err := json.Unmarshal([]byte(`{"agent":"org_01j9zr3m5k8q2w7x4t6v0b1c9d"}`), &d); !errors.Is(err, ErrInvalid) {
		t.Fatalf("json with the wrong prefix: %v", err)
	}
	// SQL.
	v, _ := id.Value()
	var s AgentID
	if err := s.Scan(v); err != nil || s != id {
		t.Fatalf("scan string: %v %v", s, err)
	}
	if err := s.Scan([]byte(id.String())); err != nil || s != id {
		t.Fatalf("scan bytes: %v %v", s, err)
	}
	if err := s.Scan(nil); err != nil || !s.IsZero() {
		t.Fatalf("scan nil: %v %v", s, err)
	}
	if err := s.Scan(42); err == nil || !strings.Contains(err.Error(), "agent id") {
		t.Fatalf("scan int: %v", err)
	}
	if v, err := (AgentID{}).Value(); v != nil || err != nil {
		t.Fatalf("zero value: %v %v", v, err)
	}
	// slog.
	var lb bytes.Buffer
	slog.New(slog.NewJSONHandler(&lb, nil)).Info("x", "agent_id", id)
	if !strings.Contains(lb.String(), `"agent_id":"`+id.String()+`"`) {
		t.Fatalf("slog %s", lb.String())
	}
}

func TestPatternAgreesWithParse(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	chars := alphabet + "ILOUilou_ ABZ89é"
	for _, p := range Registry() {
		re := regexp.MustCompile(Pattern(p.Prefix))
		if Pattern(p.Prefix) != "^"+p.Prefix+"_[0-7][0-9a-hjkmnp-tv-z]{25}$" {
			t.Fatalf("pattern %s", Pattern(p.Prefix))
		}
		fresh, err := NewString(p.Prefix)
		if err != nil || !re.MatchString(fresh) || Check(p.Prefix, fresh) != nil {
			t.Fatalf("%s: fresh id %q err %v", p.Prefix, fresh, err)
		}
		for range 10000 {
			var sb strings.Builder
			switch r.IntN(4) {
			case 0: // a mutation of a valid id
				b := []byte(fresh)
				b[r.IntN(len(b))] = chars[r.IntN(len(chars))]
				sb.Write(b)
			case 1: // the right prefix and random tail
				sb.WriteString(p.Prefix + "_")
				for range 24 + r.IntN(4) {
					sb.WriteByte(chars[r.IntN(len(chars))])
				}
			case 2: // another prefix
				q := registry[r.IntN(len(registry))].Prefix
				sb.WriteString(q + fresh[len(p.Prefix):])
			default: // a fresh valid id
				sb.WriteString(fresh)
			}
			s := sb.String()
			if got, want := Check(p.Prefix, s) == nil, re.MatchString(s); got != want {
				t.Fatalf("%s: Check=%v regexp=%v for %q", p.Prefix, got, want, s)
			}
		}
	}
}

func TestRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Registry() {
		if seen[p.Prefix] {
			t.Errorf("duplicate prefix %s", p.Prefix)
		}
		seen[p.Prefix] = true
		if !regexp.MustCompile(`^[a-z]{2,4}$`).MatchString(p.Prefix) || p.Name == "" || p.Noun == "" || p.Description == "" {
			t.Errorf("incomplete entry %+v", p)
		}
		if p.Source != "contract §3" && p.Source != "ADR-014" && p.Source != "ADR-094" {
			t.Errorf("%s: source %q", p.Prefix, p.Source)
		}
	}
	if len(seen) != 29 {
		t.Errorf("want 29 prefixes (18 contract + 10 additive + S50's obs), got %d", len(seen))
	}
	// Every kind maps to a registered prefix.
	kinds := []string{Prefix[OrgKind](), Prefix[UserKind](), Prefix[MembershipKind](), Prefix[SessionKind](),
		Prefix[AgentKind](), Prefix[APIKeyKind](), Prefix[PolicyKind](), Prefix[PolicyVersionKind](),
		Prefix[ConnectorKind](), Prefix[ActionKind](), Prefix[ActionEventKind](), Prefix[ApprovalKind](),
		Prefix[DeedKind](), Prefix[SealKeyKind](), Prefix[SuggestionKind](), Prefix[AnomalyKind](),
		Prefix[ReplayKind](), Prefix[WebhookKind](), Prefix[RequestKind](), Prefix[LoginTokenKind](),
		Prefix[PolicyActivationKind](), Prefix[CredentialKind](), Prefix[ApprovalTokenKind](),
		Prefix[DecodedTxKind](), Prefix[WebhookDeliveryKind](), Prefix[AdminEventKind](),
		Prefix[WaitlistKind](), Prefix[IdempotencyKind](), Prefix[ObserveSessionKind]()}
	for _, k := range kinds {
		if !seen[k] {
			t.Errorf("kind prefix %q is not registered", k)
		}
		delete(seen, k)
	}
	if len(seen) != 0 {
		t.Errorf("prefixes without a kind: %v", seen)
	}
	if _, err := NewString("nope"); err == nil {
		t.Error("NewString must refuse an unknown prefix")
	}
	if Known("nope") || !Known("agt") {
		t.Error("Known")
	}
	r := Registry()
	r[0].Prefix = "changed"
	if Registry()[0].Prefix != Org {
		t.Error("Registry must return a copy")
	}
}

func TestNormalizeAndMustParse(t *testing.T) {
	if got := Normalize("  AGT_01J9ZR3M5K8Q2W7X4T6V0B1C9D\n"); got != goodAgent {
		t.Fatalf("Normalize %q", got)
	}
	if MustParse[AgentKind](goodAgent).String() != goodAgent {
		t.Fatal("MustParse")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustParse must panic on an invalid id")
		}
	}()
	MustParse[AgentKind]("nope")
}

func TestUnmarshalEmptyIsZero(t *testing.T) {
	id := New[AgentKind]()
	if err := id.UnmarshalText(nil); err != nil || !id.IsZero() {
		t.Fatalf("%v %v", id, err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(goodAgent)
	f.Add("agt_01J9ZR3M5K8Q2W7X4T6V0B1C9D")
	f.Add("agt_7zzzzzzzzzzzzzzzzzzzzzzzzz")
	f.Add("agt_")
	f.Add("")
	f.Add("org_01j9zr3m5k8q2w7x4t6v0b1c9d")
	f.Fuzz(func(t *testing.T, s string) {
		id, err := Parse[AgentKind](s)
		if err != nil {
			if !errors.Is(err, ErrInvalid) || !id.IsZero() {
				t.Fatalf("bad failure for %q: %v", s, err)
			}
			return
		}
		if id.String() != s {
			t.Fatalf("accepted %q but returned %q", s, id)
		}
		b, _ := id.MarshalText()
		var back AgentID
		if back.UnmarshalText(b) != nil || back != id {
			t.Fatalf("round trip of %q", s)
		}
		// Re-encoding the decoded value gives the same text.
		ms, hi, lo := decode(s[4:])
		var rnd [10]byte
		rnd[0], rnd[1] = byte(hi>>8), byte(hi)
		for i := 0; i < 8; i++ {
			rnd[2+i] = byte(lo >> (56 - 8*i))
		}
		var dst [ulidLen]byte
		encode(dst[:], ms, &rnd)
		if "agt_"+string(dst[:]) != s {
			t.Fatalf("re-encode of %q gave %q", s, dst)
		}
	})
}

func BenchmarkNew(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = New[ActionKind]()
	}
}

func BenchmarkNewParallel(b *testing.B) {
	b.ReportAllocs()
	b.SetParallelism(1) // GOMAXPROCS goroutines; run with -cpu 8 for the §3.9 target
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = New[ActionKind]()
		}
	})
}

func BenchmarkParse(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse[AgentKind](goodAgent); err != nil {
			b.Fatal(err)
		}
	}
}

var update = flag.Bool("update", false, "rewrite the prefix table in docs/IDS.md")

// TestDocsTable keeps the prefix table in docs/IDS.md in step with Registry.
func TestDocsTable(t *testing.T) {
	const path = "../../docs/IDS.md"
	const begin, end = "<!-- prefixes:begin (generated from internal/ids; go test ./internal/ids -run TestDocsTable -update) -->\n", "<!-- prefixes:end -->"
	var b strings.Builder
	b.WriteString("| Prefix | Go type | What it names | In API responses | Source |\n|---|---|---|---|---|\n")
	for _, p := range Registry() {
		pub := "no"
		if p.Public {
			pub = "yes"
		}
		fmt.Fprintf(&b, "| `%s_` | `ids.%sID` | %s | %s | %s |\n", p.Prefix, p.Name, p.Description, pub, p.Source)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	i, j := strings.Index(doc, begin), strings.Index(doc, end)
	if i < 0 || j < i {
		t.Fatalf("docs/IDS.md needs the markers %q and %q", begin, end)
	}
	if doc[i+len(begin):j] == b.String() {
		return
	}
	if !*update {
		t.Fatal("docs/IDS.md prefix table drifted from Registry. Run: go test ./internal/ids -run TestDocsTable -update")
	}
	if err := os.WriteFile(path, []byte(doc[:i+len(begin)]+b.String()+doc[j:]), 0o644); err != nil {
		t.Fatal(err)
	}
}
