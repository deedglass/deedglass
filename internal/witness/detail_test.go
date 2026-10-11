package witness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gowebpki/jcs"
)

var witnessedAt = time.Date(2026, 10, 7, 4, 41, 22, 418_000_000, time.UTC)

func runOK(t *testing.T, in Input) Outcome {
	t.Helper()
	if in.Now.IsZero() {
		in.Now = witnessedAt
	}
	out, deferred, err := Run(context.Background(), in)
	if err != nil || deferred {
		t.Fatalf("Run: deferred %v, err %v", deferred, err)
	}
	return out
}

func TestRunHonest(t *testing.T) {
	out := runOK(t, Input{Subject: restSubject(shippedArgs, ordersReadBack(), ""), Observer: &fakeObserver{fn: answer(`{"id":"42","status":"shipped"}`)}, Final: true})
	d := out.Decision
	if d.Level != L1 || d.Verdict != Verified || d.Method != "readback.rest.get" {
		t.Fatalf("got %s · %s · %s", d.Level, d.Verdict, d.Method)
	}
	if out.Detail.Summary != `Read back orders.get: status is "shipped".` {
		t.Errorf("summary %q", out.Detail.Summary)
	}
	if len(out.Flags()) != 0 {
		t.Errorf("flags %v", out.Flags())
	}
}

// TestRunLying checks the detail against S19 §3.8's example, field for field.
func TestRunLying(t *testing.T) {
	out := runOK(t, Input{Subject: restSubject(shippedArgs, ordersReadBack(), ""), Observer: &fakeObserver{fn: answer(`{"id":"42","status":"pending"}`)}, Final: true})
	if out.Decision.Level != L1 || out.Decision.Verdict != Mismatch {
		t.Fatalf("got %s · %s", out.Decision.Level, out.Decision.Verdict)
	}
	const want = `{
  "object": "witness_detail",
  "version": 1,
  "claim": { "level": "L1", "verdict": "MISMATCH", "method": "readback.rest.get", "required_level": "L1" },
  "summary": "orders.update_status reported success, but orders.get says status is \"pending\", not \"shipped\".",
  "levels": {
    "L0": { "verdict": "VERIFIED", "method": "reported" },
    "L1": { "verdict": "MISMATCH", "method": "readback.rest.get",
            "readback": { "tool": "orders.get", "target": "id \"42\"", "request_digest": "sha256:5d1c", "status": 200,
                          "response_digest": "sha256:a07e", "attempts": 1, "latency_ms": 12 } },
    "L2": null
  },
  "reasons": [
    { "code": "tool_reported_success", "class": "pass", "level": "L0", "check": "status",
      "message": "The tool reported success." },
    { "code": "postcondition_failed", "class": "violation", "level": "L1", "check": "readback.expect",
      "message": "orders.update_status reported success, but orders.get says status is \"pending\", not \"shipped\".",
      "diff": [ { "path": "body.status", "expected": "shipped", "observed": "pending" } ] }
  ],
  "witnessed_at": "2026-10-07T04:41:22.418Z"
}`
	var got, exp any
	if err := json.Unmarshal(out.JSON, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &exp); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, exp) {
		t.Errorf("detail differs:\n got %s\nwant %s", out.JSON, want)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`).MatchString(out.Detail.WitnessedAt) {
		t.Errorf("witnessed_at %q", out.Detail.WitnessedAt)
	}

	// The digest is sha256 over JCS of the detail, and the stored bytes are already JCS.
	again, err := json.Marshal(out.Detail)
	if err != nil {
		t.Fatal(err)
	}
	canon, err := jcs.Transform(again)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canon)
	if want := "sha256:" + hex.EncodeToString(sum[:]); out.Digest != want {
		t.Errorf("digest %s, want %s", out.Digest, want)
	}
	if string(canon) != string(out.JSON) {
		t.Errorf("stored bytes aren't JCS:\n%s\n%s", out.JSON, canon)
	}
	// Another time, another digest; the same time, the same digest.
	same := runOK(t, Input{Subject: restSubject(shippedArgs, ordersReadBack(), ""), Observer: &fakeObserver{fn: answer(`{"id":"42","status":"pending"}`)}, Final: true})
	later := runOK(t, Input{Subject: restSubject(shippedArgs, ordersReadBack(), ""), Observer: &fakeObserver{fn: answer(`{"id":"42","status":"pending"}`)}, Final: true, Now: witnessedAt.Add(time.Second)})
	if same.Digest != out.Digest || later.Digest == out.Digest {
		t.Error("the digest isn't a function of the detail")
	}

	// ParseDetail reads it back.
	pd, ok := ParseDetail(out.JSON)
	if !ok || pd.Claim != out.Detail.Claim || len(pd.Reasons) != 2 || pd.Levels.L2 != nil || pd.Levels.L1.Readback.Attempts != 1 {
		t.Errorf("ParseDetail: %v %+v", ok, pd)
	}
}

func TestRunReadbackFails(t *testing.T) {
	obs := &fakeObserver{fn: fail(&ReadError{Code: "upstream_error"})} // a 500 on the read-back
	s := restSubject(shippedArgs, ordersReadBack(), "")
	s.Required = L0
	out := runOK(t, Input{Subject: s, Observer: obs, Final: true})
	if out.Decision.Level != L0 || out.Decision.Verdict != Verified {
		t.Fatalf("required L0: %s · %s", out.Decision.Level, out.Decision.Verdict)
	}
	if !slices.ContainsFunc(out.Detail.Reasons, func(r Reason) bool { return r.Code == CodeReadbackFailed && r.Level == L1 }) {
		t.Error("the L1 gap isn't listed")
	}
	if out.Detail.Levels.L1 == nil || out.Detail.Levels.L1.Verdict != Unverifiable || out.Detail.Levels.L1.Readback == nil {
		t.Errorf("levels.L1 %+v", out.Detail.Levels.L1)
	}
	if out.Detail.Summary != "The tool reported success." {
		t.Errorf("summary %q", out.Detail.Summary)
	}

	s.Required = L1
	out = runOK(t, Input{Subject: s, Observer: obs, Final: true})
	if out.Decision.Level != L0 || out.Decision.Verdict != Unverifiable || !slices.Equal(out.Flags(), []string{"level_below_required"}) {
		t.Fatalf("required L1: %s · %s %v", out.Decision.Level, out.Decision.Verdict, out.Flags())
	}
	if out.Detail.Summary != "The policy asks for L1. The strongest clean evidence was L0." {
		t.Errorf("summary %q", out.Detail.Summary)
	}
	if out.Detail.Claim.RequiredLevel != L1 {
		t.Errorf("claim %+v", out.Detail.Claim)
	}
}

func TestRunGaveUp(t *testing.T) {
	s := restSubject(shippedArgs, nil, "") // L0 only
	s.Required = L0
	out := runOK(t, Input{Subject: s, Extra: []Reason{GaveUp(L1, 8, "the target is unreachable.")}, Final: true})
	if out.Decision.Level != L0 || out.Decision.Verdict != Verified {
		t.Fatalf("got %s · %s", out.Decision.Level, out.Decision.Verdict)
	}
	i := slices.IndexFunc(out.Detail.Reasons, func(r Reason) bool { return r.Code == CodeWitnessGaveUp })
	if i < 0 || out.Detail.Reasons[i].Message != "The witness stopped after 8 attempts: the target is unreachable." {
		t.Fatalf("reasons %+v", out.Detail.Reasons)
	}
	if out.Detail.Levels.L1 == nil || out.Detail.Levels.L1.Method != "readback" || out.Detail.Levels.L1.Verdict != Unverifiable {
		t.Errorf("levels.L1 %+v", out.Detail.Levels.L1)
	}
	if r := GaveUp(L0, 3, "  "); !strings.Contains(r.Message, "see the server log") {
		t.Errorf("empty last error: %q", r.Message)
	}
}

func TestRunDeferredAndSkipped(t *testing.T) {
	slow := func(ctx context.Context, _ int) (Observation, error) { <-ctx.Done(); return Observation{}, ctx.Err() }
	ctx, cancel := WithBudget(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, deferred, err := Run(ctx, Input{Subject: restSubject(shippedArgs, ordersReadBack(), ""), Observer: &fakeObserver{fn: slow}})
	if err != nil || !deferred {
		t.Fatalf("deferred %v err %v", deferred, err)
	}

	// A Postgres statement the row read-back can't check: L0 with the note, the default time.
	s := pgSubject(`UPDATE public.orders SET status = 'x' WHERE id IN (1, 2)`)
	s.Required = L0
	out, _, err := Run(context.Background(), Input{Subject: s})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Level != L0 || out.Decision.Verdict != Verified {
		t.Fatalf("got %s · %s", out.Decision.Level, out.Decision.Verdict)
	}
	if !slices.ContainsFunc(out.Detail.Reasons, func(r Reason) bool { return r.Code == CodeReadbackSkipped }) {
		t.Errorf("no readback_skipped: %+v", out.Detail.Reasons)
	}
	// A level with only a note ran no check: it stays null in levels (the note is in reasons).
	if out.Detail.WitnessedAt == "" || out.Detail.Levels.L1 != nil {
		t.Errorf("detail %+v", out.Detail)
	}
}

func TestFinishCap(t *testing.T) {
	var diffs []FieldDiff
	for i := range 2000 {
		diffs = append(diffs, FieldDiff{Path: "body.items[" + strings.Repeat("9", 3) + "]", Expected: strings.Repeat("e", 40), Observed: i})
	}
	r := R(L1, CodePostconditionFailed, "readback.expect", nil).WithDiff(diffs)
	d := Decide(L1, map[Level][]Reason{L0: {R(L0, CodeToolReportedSuccess, "status", nil)}, L1: {r}}, nil)
	out, err := Finish(d, nil, nil, witnessedAt, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.JSON) > DefaultMaxDetailBytes {
		t.Errorf("%d bytes over the cap", len(out.JSON))
	}
	i := slices.IndexFunc(out.Detail.Reasons, func(r Reason) bool { return r.Code == CodePostconditionFailed })
	if got := len(out.Detail.Reasons[i].Diff); got != maxDiffsKept {
		t.Errorf("%d diffs kept", got)
	}
	if out.Detail.Truncated != 2000-maxDiffsKept {
		t.Errorf("truncated %d", out.Detail.Truncated)
	}
	if len(out.Decision.Reasons[i].Diff) != 2000 {
		t.Error("the decision's own reasons were cut")
	}
	var m map[string]any
	_ = json.Unmarshal(out.JSON, &m)
	if m["truncated"] != float64(2000-maxDiffsKept) {
		t.Errorf("truncated in JSON: %v", m["truncated"])
	}

	// Still too large after the diffs: evidence and the remaining diffs go too.
	ev := R(L1, CodeReadbackMatched, "readback", map[string]any{"readback": "orders.get", "fields": "x"}).WithEvidence(map[string]any{"k": strings.Repeat("v", 4000)})
	d = Decide(L1, map[Level][]Reason{L1: {r, ev}}, nil)
	out, err = Finish(d, nil, nil, witnessedAt, 2500)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Detail.Reasons {
		if r.Diff != nil || r.Evidence != nil {
			t.Errorf("%s kept diff or evidence", r.Code)
		}
	}
	if out.Detail.Truncated != 2000+1 {
		t.Errorf("truncated %d", out.Detail.Truncated)
	}
	// Under the cap: untouched.
	out, _ = Finish(d, nil, nil, witnessedAt, 10<<20)
	if out.Detail.Truncated != 0 {
		t.Errorf("truncated %d under the cap", out.Detail.Truncated)
	}
}

func TestWithEvidence(t *testing.T) {
	r := R(L0, CodeToolReportedSuccess, "status", nil)
	if r.WithEvidence(nil).Evidence != nil {
		t.Error("empty evidence set")
	}
	big := r.WithEvidence(map[string]any{"x": strings.Repeat("a", MaxEvidenceBytes)})
	if !reflect.DeepEqual(big.Evidence, map[string]any{"truncated": true}) {
		t.Errorf("big evidence %v", big.Evidence)
	}
	bad := r.WithEvidence(map[string]any{"f": func() {}})
	if !reflect.DeepEqual(bad.Evidence, map[string]any{"truncated": true}) {
		t.Errorf("unencodable evidence %v", bad.Evidence)
	}
}

func TestParseDetail(t *testing.T) {
	d, ok := ParseDetail([]byte(`{"reasons":["outcome_unknown"]}`))
	if !ok || len(d.Reasons) != 1 {
		t.Fatalf("legacy: %v %+v", ok, d)
	}
	r := d.Reasons[0]
	if r.Code != CodeOutcomeUnknown || r.Class != Gap || r.Level != L0 || r.Evidence["cause"] != "outcome_unknown" {
		t.Errorf("legacy reason %+v", r)
	}
	// S13's short causes become outcome_unknown with the cause in the evidence.
	d, ok = ParseDetail([]byte(`{"reasons":["timeout","edge_lost","client_cancelled","witness_gave_up","something_else"]}`))
	if !ok || len(d.Reasons) != 5 {
		t.Fatalf("legacy causes: %v %+v", ok, d)
	}
	for i, cause := range []string{"timeout", "edge_lost", "client_cancelled"} {
		r := d.Reasons[i]
		if r.Code != CodeOutcomeUnknown || r.Class != Gap || r.Evidence["cause"] != cause || !strings.Contains(r.Message, "("+cause+")") {
			t.Errorf("%s: %+v", cause, r)
		}
	}
	if r := d.Reasons[3]; r.Code != CodeWitnessGaveUp || r.Class != Gap || r.Evidence != nil || !strings.Contains(r.Message, "see the server log") {
		t.Errorf("witness_gave_up: %+v", r)
	}
	if r := d.Reasons[4]; r.Code != "something_else" || r.Class != Gap || !strings.Contains(r.Message, "(something_else)") {
		t.Errorf("unknown legacy code: %+v", r)
	}
	if d, ok := ParseDetail([]byte(`{"object":"witness_detail","version":1,"claim":{"level":"L0","verdict":"UNVERIFIABLE","method":"reported","required_level":"L0"}}`)); !ok || d.Reasons == nil || d.Version != 1 {
		t.Errorf("v1 without reasons: %v %+v", ok, d)
	}
	for _, bad := range []string{``, `  `, `{`, `[1]`, `{"reasons":[1]}`, `{"object":"witness_detail","version":"x"}`} {
		if _, ok := ParseDetail([]byte(bad)); ok {
			t.Errorf("ParseDetail(%q) accepted", bad)
		}
	}
}

func TestSummary(t *testing.T) {
	p0 := R(L0, CodeToolReportedSuccess, "status", nil)
	p0s := R(L0, CodeResponseSchemaValid, "response.schema", nil)
	g0 := R(L0, CodeOutcomeUnknown, "status", map[string]any{"cause": "timeout"})
	v1 := R(L1, CodePostconditionFailed, "readback.expect", map[string]any{"tool": "t", "readback": "r", "path": "p", "observed": 1, "expected": 2})
	v2 := R(L2, CodeValueMismatch, "row.value", nil)
	g1 := R(L1, CodeReadbackFailed, "readback", map[string]any{"readback": "orders.get", "error_code": "upstream_error"})
	cases := []struct {
		name     string
		required Level
		in       map[Level][]Reason
		want     string
	}{
		{"verified prefers the schema check", L0, map[Level][]Reason{L0: {p0, p0s}}, p0s.Message},
		{"mismatch at the claimed level", L0, map[Level][]Reason{L0: {p0}, L1: {v1}, L2: {v2}}, v2.Message},
		{"mismatch", L0, map[Level][]Reason{L1: {v1}}, v1.Message},
		{"below required", L1, map[Level][]Reason{L0: {p0}, L1: {g1}}, "The policy asks for L1. The strongest clean evidence was L0."},
		{"no checks", L0, nil, "No check could run for this action."},
		{"gap", L0, map[Level][]Reason{L0: {g0}}, g0.Message},
		{"gap at another level", L1, map[Level][]Reason{L0: {g0}, L1: {g1}}, g1.Message},
	}
	for _, c := range cases {
		if got := Summary(Decide(c.required, c.in, nil)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// Hand-built decisions: a pass the list doesn't name, a violation at another level, a gap elsewhere.
	other := R(L1, CodePostconditionHeld, "postcondition", map[string]any{"expr": "x"})
	if got := Summary(Decision{Level: L1, Verdict: Verified, Reasons: []Reason{{Code: "s20_pass", Class: Pass, Level: L1, Message: "m."}, other}}); got != other.Message {
		t.Errorf("held: %q", got)
	}
	if got := Summary(Decision{Level: L1, Verdict: Verified, Reasons: []Reason{{Code: "s20_pass", Class: Pass, Level: L1, Message: "m."}}}); got != "m." {
		t.Errorf("unnamed pass: %q", got)
	}
	if got := Summary(Decision{Level: L2, Verdict: Mismatch, Reasons: []Reason{v1}}); got != v1.Message {
		t.Errorf("violation elsewhere: %q", got)
	}
	if got := Summary(Decision{Level: L2, Verdict: Unverifiable, Reasons: []Reason{g0}}); got != g0.Message {
		t.Errorf("gap elsewhere: %q", got)
	}
}

func TestPresent(t *testing.T) {
	for l, want := range map[Level]string{L0: TooltipL0, L1: TooltipL1, L2: TooltipL2, "": TooltipL0} {
		if Tooltip(l) != want {
			t.Errorf("Tooltip(%q)", l)
		}
	}
	for l, want := range map[Level]string{L0: "Claimed (L0)", L1: "Checked (L1)", L2: "Proven (L2)", "L7": "L7"} {
		if l.Title() != want {
			t.Errorf("Title(%q) = %q", l, l.Title())
		}
	}
	for l, want := range map[Level]string{L0: "Claimed", L1: "Checked", L2: "Proven", "L7": "L7"} {
		if l.Plain() != want || PlainLevel(string(l)) != want {
			t.Errorf("Plain(%q) = %q", l, l.Plain())
		}
	}
	for v, want := range map[string]string{"VERIFIED": "Matches", "MISMATCH": "Doesn't match", "UNVERIFIABLE": "Can't check", "simulated": "simulated"} {
		if got := PlainVerdict(v); got != want {
			t.Errorf("PlainVerdict(%q) = %q", v, got)
		}
	}
	for in, want := range map[string]Level{"": L0, "L0": L0, "L1": L1, "L2": L2} {
		if got, ok := ParseLevel(in); !ok || got != want {
			t.Errorf("ParseLevel(%q) = %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"l1", "L3", "1"} {
		if _, ok := ParseLevel(bad); ok {
			t.Errorf("ParseLevel(%q) accepted", bad)
		}
	}
	if Level("x").Rank() != -1 || Level("x").Valid() {
		t.Error("an unknown level ranks")
	}
	if !strings.Contains(PanelNoReadBack, "%s › %s") {
		t.Error("PanelNoReadBack lost its placeholders")
	}
}
