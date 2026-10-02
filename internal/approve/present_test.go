package approve

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

func errCode(err error) httpx.Code {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func ist(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestAgentTexts(t *testing.T) {
	loc := ist(t)
	at := time.Date(2026, 10, 2, 5, 11, 0, 0, time.UTC)
	cases := []struct{ got, want string }{
		{AgentDeniedWithReason(""), "A person denied this action. Nothing was run."},
		{AgentDeniedWithReason("   "), "A person denied this action. Nothing was run."},
		{AgentDeniedWithReason(`Order 42 hasn't been "paid" yet`), `A person denied this action. Nothing was run. Reason: "Order 42 hasn't been 'paid' yet."`},
		{AgentDeniedWithReason("Is this right?"), `A person denied this action. Nothing was run. Reason: "Is this right?"`},
		{AgentDeniedWithReason("one\nline\u202Eflip\tdone."), `A person denied this action. Nothing was run. Reason: "onelineflipdone."`},
		{AgentStillPending("apr_x", at, loc), "Still waiting for approval apr_x. It expires at 10:41 IST. Nothing has run yet. Check again in a minute."},
		{AgentExpired(at, nil), "Nobody approved this before 05:11 UTC, so it expired. Nothing was run. Ask again if it's still needed."},
		{MsgDecided("approved", "Priya Raman", at, loc), "This approval was already decided: approved by Priya Raman at 10:41 IST."},
		{MsgDecided("expired", "", at, loc), "This approval was already decided: expired at 10:41 IST."},
		{MsgExpired(at, loc), "This approval expired at 10:41 IST. Nothing ran. Ask the agent to try again if it's still needed."},
		{MsgAlreadyCountersigned(1), "You already countersigned this. It needs one more person."},
		{MsgAlreadyCountersigned(0), "You already countersigned this. It needs one more person."},
		{MsgAlreadyCountersigned(3), "You already countersigned this. It needs 3 more people."},
		{MsgNotEligible("r1", []string{"role:owner", "user:a@b.test"}), `Rule "r1" needs role:owner or user:a@b.test to countersign. Ask one of them.`},
		{MsgNotEligible("", nil), "This approval needs role:admin to countersign. Ask one of them."},
		{MsgUnsatisfiable(2, 1), "No one in this org can approve this: the rule needs 2 people and only 1 is eligible. Nothing was run. Invite another admin, or allow self-countersign in Console › Settings › Approvals."},
		{MsgUnsatisfiable(1, 0), "No one in this org can approve this: the rule needs 1 person and only 0 are eligible. Nothing was run. Invite another admin, or allow self-countersign in Console › Settings › Approvals."},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("%d:\n got %q\nwant %q", i, c.got, c.want)
		}
	}
}

func TestSummaries(t *testing.T) {
	cases := []struct {
		f    SummaryFacts
		want string
	}{
		{SummaryFacts{Tool: "stripe.create_refund"}, "support-bot wants to run stripe.create_refund."},
		{SummaryFacts{Tool: "postgres.execute", SQLKind: "update", Tables: []string{"public.orders"}, Rows: 1, Estimated: true}, "support-bot wants to UPDATE about 1 row in public.orders."},
		{SummaryFacts{Tool: "postgres.execute", SQLKind: "delete", Tables: []string{"public.a", "public.b"}, Rows: 300, Estimated: true}, "support-bot wants to DELETE about 300 rows in public.a, public.b."},
		{SummaryFacts{Tool: "postgres.execute", SQLKind: "insert", Tables: []string{"public.orders"}}, "support-bot wants to INSERT rows in public.orders."},
		{SummaryFacts{Tool: "postgres.query", SQLKind: "select", Tables: []string{"public.orders"}, Rows: 0, Estimated: true}, "support-bot wants to read about 0 rows from public.orders."},
		{SummaryFacts{Tool: "postgres.execute", SQLKind: "update"}, "support-bot wants to UPDATE rows."},
	}
	for _, c := range cases {
		if got := Summary("support-bot", c.f); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestClockAndIn(t *testing.T) {
	now := time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC)
	if got := Clock(time.Time{}, nil); got != "an unknown time" {
		t.Error(got)
	}
	if got := Clock(now, ist(t)); got != "10:30 IST" {
		t.Error(got)
	}
	for d, want := range map[time.Duration]string{
		-time.Second: "now", 0: "now", 30 * time.Second: "in 30 s", 27 * time.Minute: "in 27 min",
		time.Hour: "in 1 h", 112 * time.Minute: "in 1 h 52 min", 24 * time.Hour: "in 24 h",
	} {
		if got := In(now.Add(d), now); got != want {
			t.Errorf("%v: got %q want %q", d, got, want)
		}
	}
}

func TestCopyRules(t *testing.T) {
	loc := ist(t)
	at := time.Now()
	all := []string{AgentApprovedRan, AgentApprovedStopped, AgentDenied, AgentUnsatisfiable, MsgReasonRequired, MsgReasonTooLong,
		MsgReasonChars, MsgDigestRequired, MsgVoteInvalid, MsgStatusFilter, MsgApprovalID, AgentStillPending("apr_x", at, loc),
		AgentExpired(at, loc), MsgDecided("denied", "A", at, loc), MsgExpired(at, loc), MsgAlreadyCountersigned(2),
		MsgNotEligible("r", nil), MsgUnsatisfiable(2, 1)}
	for _, s := range all {
		if strings.ContainsAny(s, "!—–") {
			t.Errorf("exclamation mark or dash: %q", s)
		}
		if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, ".\"") {
			t.Errorf("no full stop: %q", s)
		}
		// Banned words: make banned-words scans these strings in the source.
	}
}

func TestCleanText(t *testing.T) {
	in := "a\u0000b\u202Ac\u2066d\u200Ee\u061Cf\x7fg\nh"
	if got := cleanText(in); got != "abcdefgh" {
		t.Errorf("cleanText %q", got)
	}
	for _, s := range []string{"\u202E", "a\tb", "\u2069", string([]byte{0xff})} {
		if !hasForbidden(s) {
			t.Errorf("hasForbidden(%q) = false", s)
		}
	}
	if hasForbidden("Order 42 isn't paid · ok ✓ 日本") {
		t.Error("plain text refused")
	}
}

func TestCheckDecideInput(t *testing.T) {
	ok := DecideInput{Vote: VoteApprove, ContextDigest: "sha256:x"}
	cases := []struct {
		in    DecideInput
		param string
	}{
		{DecideInput{Vote: "maybe", ContextDigest: "sha256:x"}, "vote"},
		{DecideInput{Vote: VoteDeny, ContextDigest: "sha256:x"}, "reason"},
		{DecideInput{Vote: VoteDeny, Reason: "   ", ContextDigest: "sha256:x"}, "reason"},
		{DecideInput{Vote: VoteDeny, Reason: strings.Repeat("é", MaxReason+1), ContextDigest: "sha256:x"}, "reason"},
		{DecideInput{Vote: VoteApprove, Reason: "ok\u202E", ContextDigest: "sha256:x"}, "reason"},
		{DecideInput{Vote: VoteApprove}, "context_digest"},
	}
	if err := CheckDecideInput(ok); err != nil {
		t.Fatal(err)
	}
	if err := CheckDecideInput(DecideInput{Vote: VoteDeny, Reason: strings.Repeat("é", MaxReason), ContextDigest: "sha256:x"}); err != nil {
		t.Fatalf("280 runes: %v", err)
	}
	for i, c := range cases {
		err := CheckDecideInput(c.in)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Param != c.param {
			t.Errorf("%d: %v", i, err)
		}
	}
}

func TestParseStatuses(t *testing.T) {
	got, err := ParseStatuses("pending, approved,denied,expired,cancelled")
	if err != nil || len(got) != 5 {
		t.Fatal(got, err)
	}
	if got, err := ParseStatuses(""); got != nil || err != nil {
		t.Fatal(got, err)
	}
	for _, s := range []string{"pending,", "Pending", "open", ",", "approved;denied"} {
		if _, err := ParseStatuses(s); errCode(err) != httpx.CodeValidationFailed {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestEligibility(t *testing.T) {
	owner := Person{UserID: ids.New[ids.UserKind](), Email: "Owner@acme.test", Role: "owner"}
	admin := Person{UserID: ids.New[ids.UserKind](), Email: "admin@acme.test", Role: "admin"}
	viewer := Person{UserID: ids.New[ids.UserKind](), Email: "viewer@acme.test", Role: "viewer"}
	cases := []struct {
		spec []string
		p    Person
		want bool
	}{
		{[]string{"role:admin"}, owner, true},
		{[]string{"role:admin"}, admin, true},
		{[]string{"role:admin"}, viewer, false},
		{[]string{"role:owner"}, admin, false},
		{[]string{"role:owner"}, owner, true},
		{[]string{"user:viewer@ACME.test"}, viewer, true},
		{[]string{"user:someone@acme.test"}, admin, false},
		{[]string{"role:viewer"}, viewer, false}, // not a valid approver kind
		{[]string{"agent:claude"}, admin, false},
		{nil, admin, false},
	}
	for i, c := range cases {
		if got := (Spec{Approvers: c.spec}).Eligible(c.p); got != c.want {
			t.Errorf("%d: %v %s → %v", i, c.spec, c.p.Role, got)
		}
	}
	if s := parseSpec(nil); len(s.Approvers) != 1 || s.Approvers[0] != "role:admin" || s.Required != 1 {
		t.Errorf("defaults %+v", s)
	}
	if s := parseSpec([]byte(`{"approvers":["role:owner"],"required":2,"expires_in":60}`)); s.Required != 2 || s.Approvers[0] != "role:owner" {
		t.Errorf("%+v", s)
	}

	if !IsSelf(" "+admin.UserID.String()+" ", admin) || !IsSelf("ADMIN@acme.test", admin) || IsSelf("", admin) || IsSelf("owner@acme.test", admin) {
		t.Error("IsSelf")
	}
	all := []Person{owner, admin, viewer}
	people, n := eligiblePeople(all, Spec{Approvers: []string{"role:admin"}}, admin.Email, false)
	if len(people) != 2 || n != 1 {
		t.Errorf("self excluded: %d %d", len(people), n)
	}
	if _, n := eligiblePeople(all, Spec{Approvers: []string{"role:admin"}}, admin.Email, true); n != 2 {
		t.Errorf("self allowed: %d", n)
	}
	if people, n := eligiblePeople(all, Spec{Approvers: []string{"role:owner"}}, "", false); len(people) != 1 || n != 1 {
		t.Errorf("owners: %d %d", len(people), n)
	}
}

func TestLinkTokens(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		raw, h, err := newLinkToken()
		if err != nil {
			t.Fatal(err)
		}
		if !wellFormed(raw) || len(raw) != tokenLen || !bytes.Equal(h, hashLink(raw)) || seen[raw] {
			t.Fatalf("token %q", raw)
		}
		seen[raw] = true
	}
	raw, _, _ := newLinkToken()
	for _, bad := range []string{"", raw[:42], raw + "A", strings.Repeat("A", 42) + "=", strings.Repeat("+", 43), strings.Repeat("A", 42) + "B"} {
		if wellFormed(bad) {
			t.Errorf("wellFormed(%q)", bad)
		}
	}
	// The hash is domain-separated: it is not sha256 of the bare token.
	if bytes.Equal(hashLink(raw), hashLink(raw+" ")) || LinkHash(raw) == "" || strings.Contains(LinkHash(raw), raw) {
		t.Error("hash")
	}
}

func TestRedactArgs(t *testing.T) {
	if b, note := redactArgs(nil); string(b) != "null" || note != nil {
		t.Error("nil args")
	}
	if b, _ := redactArgs(json.RawMessage(`{not json`)); string(b) != "null" {
		t.Error("bad json")
	}
	b, note := redactArgs(json.RawMessage(`{"sql":"UPDATE orders SET status = 'shipped' WHERE id = 42","n":[1,2]}`))
	if note != nil || !strings.Contains(string(b), "UPDATE orders") {
		t.Errorf("%s", b)
	}
	secret := "sk_live_" + strings.Repeat("a1B2", 8)
	b, _ = redactArgs(json.RawMessage(`{"key":"` + secret + `","list":["` + secret + `"]}`))
	if strings.Contains(string(b), secret) {
		t.Errorf("secret kept: %s", b)
	}
	big := `{"sql":"` + strings.Repeat("é", 9000) + `"}`
	b, note = redactArgs(json.RawMessage(big))
	if note == nil || !strings.HasPrefix(*note, "Showing the first 8 KiB of ") || !json.Valid(b) {
		t.Fatalf("big: %v", note)
	}
	var s string
	if json.Unmarshal(b, &s) != nil || !utf8.ValidString(s) || len(s) > MaxArgsBytes {
		t.Errorf("cut %d", len(s))
	}
}

func TestFitContext(t *testing.T) {
	recent := make([]SnapRecent, MaxRecent)
	for i := range recent {
		recent[i] = SnapRecent{Action: "act_x", Tool: strings.Repeat("t", 200), Status: "executed", At: "2026-10-02T05:00:00.000Z"}
	}
	s := &Snapshot{V: 1, Agent: SnapAgent{Recent: recent}, Args: json.RawMessage(`{"a":"` + strings.Repeat("x", 4000) + `"}`),
		Sections: map[string]json.RawMessage{"undo": json.RawMessage(`"` + strings.Repeat("u", 3000) + `"`)}}
	b, err := fitContext(s, 6000)
	if err != nil || len(b) > 6000 || len(s.Agent.Recent) != 0 || len(s.Sections) != 0 || s.ArgsNote != nil {
		t.Fatalf("step 2: %d %v recent=%d sections=%d", len(b), err, len(s.Agent.Recent), len(s.Sections))
	}
	b, err = fitContext(s, 1000)
	if err != nil || len(b) > 1000 || s.ArgsNote == nil || string(s.Args) != "null" {
		t.Fatalf("step 3: %d %v", len(b), err)
	}
	s.Summary = strings.Repeat("s", 2000)
	if _, err := fitContext(s, 1000); err == nil {
		t.Fatal("too large accepted")
	}
	if b, err := fitContext(&Snapshot{V: 1}, 0); err != nil || len(b) == 0 {
		t.Fatal("default max")
	}
}

func TestSnapshotHelpers(t *testing.T) {
	if s, err := parseSnapshot(nil); s != nil || err != nil {
		t.Fatal("empty")
	}
	if _, err := parseSnapshot([]byte("{")); err == nil {
		t.Fatal("bad json accepted")
	}
	rows := int64(3)
	s := &Snapshot{Action: SnapAction{Tool: "postgres.execute"}, Args: json.RawMessage(`{"sql":"  DELETE FROM t  "}`),
		SQL: &SnapSQL{Kind: "delete", Tables: []string{"public.t"}, Estimate: &SnapEstimate{Rows: &rows, Method: "explain"}}}
	if f := s.summaryFacts(); f.SQLKind != "delete" || f.Rows != 3 || !f.Estimated {
		t.Errorf("%+v", f)
	}
	if s.sqlText() != "DELETE FROM t" {
		t.Error(s.sqlText())
	}
	s.SQL.Kind = "unknown"
	if f := s.summaryFacts(); f.SQLKind != "" {
		t.Errorf("unknown kind %+v", f)
	}
	s.Args = json.RawMessage(`[1]`)
	if s.sqlText() != "" {
		t.Error("array args")
	}
}

func TestSections(t *testing.T) {
	RegisterSection("zz-test", func(tool, _ string) bool { return tool == "x.y" }, func(in gate.ApprovalInput) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})
	RegisterSection("zz-test-bad", func(string, string) bool { return true }, func(gate.ApprovalInput) (json.RawMessage, error) {
		return json.RawMessage(`{bad`), nil
	})
	RegisterSection("zz-test-big", func(string, string) bool { return true }, func(gate.ApprovalInput) (json.RawMessage, error) {
		return json.RawMessage(`"` + strings.Repeat("b", MaxSectionBytes) + `"`), nil
	})
	got := buildSections(gate.ApprovalInput{Tool: "x.y"})
	if len(got) != 1 || string(got["zz-test"]) != `{"ok":true}` {
		t.Errorf("%v", got)
	}
	if got := buildSections(gate.ApprovalInput{Tool: "other"}); len(got) != 0 {
		t.Errorf("%v", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("a second registration didn't panic")
		}
	}()
	RegisterSection("zz-test", func(string, string) bool { return false }, nil)
}

// FuzzApprovalLinkBody: the three public link routes decode any body without a panic, and every
// refusal is an envelope error (S22 §8).
func FuzzApprovalLinkBody(f *testing.F) {
	for _, s := range []string{`{"token":"x"}`, `{"token":"` + strings.Repeat("A", 43) + `","vote":"approve","context_digest":"sha256:x"}`,
		`{"token":1}`, `[]`, `{"token":"a","extra":1}`, ``, `{"token":"a","token":"b"}`, "{\"token\":\"\u202e\"}"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		r := httptest.NewRequest("POST", "/v1/approval-links/preview", bytes.NewReader(body))
		var b tokenBody
		err := httpx.DecodeJSON(r, &b)
		if err != nil && errCode(err) == "" {
			t.Fatalf("not an envelope error: %v", err)
		}
		if err == nil && !wellFormed(strings.TrimSpace(b.Token)) {
			// A malformed token is looked up as dummyHash: same cost, same answer.
			if bytes.Equal(dummyHash, hashLink(b.Token)) && b.Token != strings.Repeat("A", 43) {
				t.Fatal("dummy hash collision")
			}
		}
	})
}
