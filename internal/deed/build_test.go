package deed_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

func hexSHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func ptr[T any](v T) *T { return &v }

func TestBuilders(t *testing.T) {
	org := deedtest.Org(1)
	full := deedtest.Snapshot(org, 1)
	bare := full
	bare.OnBehalfOf, bare.Session, bare.Connector, bare.IntentHash = nil, nil, nil, nil
	bare.Policy = deed.PolicySection{Mode: "shadow", Decision: "deny"}
	bare.Approval, bare.Execution, bare.Witness, bare.Cost = nil, nil, nil, nil

	for name, s := range map[string]deed.ActionSnapshot{"every field set": full, "every nullable field null": bare} {
		d, err := deed.ForAction(s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.Body.Kind != deed.KindAction || d.Body.DeedID != "" || d.Body.Seq != 0 {
			t.Fatalf("%s: %+v", name, d.Body)
		}
	}
	d, _ := deed.ForAction(full)
	if d.Body.Execution.LatencyMS != 41 || d.Body.Execution.StartedAt != "2026-10-02T08:59:59.800Z" {
		t.Fatalf("execution %+v", d.Body.Execution)
	}

	bad := []struct {
		field string
		edit  func(s *deed.ActionSnapshot)
	}{
		{"org", func(s *deed.ActionSnapshot) { s.Org = "org_x" }},
		{"agent.id", func(s *deed.ActionSnapshot) { s.Agent.ID = "usr_" + strings.Repeat("0", 26) }},
		{"agent.name", func(s *deed.ActionSnapshot) { s.Agent.Name = strings.Repeat("a", 65) }},
		{"agent.name", func(s *deed.ActionSnapshot) { s.Agent.Name = "tab\there" }},
		{"agent.name", func(s *deed.ActionSnapshot) { s.Agent.Name = "bad \xff utf-8" }},
		{"on_behalf_of", func(s *deed.ActionSnapshot) { s.OnBehalfOf = ptr("priya@example.com and more") }},
		{"action.mode", func(s *deed.ActionSnapshot) { s.Mode = "direct" }},
		{"action.request_digest", func(s *deed.ActionSnapshot) { s.RequestDigest = "sha256:ABC" }},
		{"action.intent_hash", func(s *deed.ActionSnapshot) { s.IntentHash, s.IntentSigned = nil, true }},
		{"policy.decision", func(s *deed.ActionSnapshot) { s.Policy.Decision = "maybe" }},
		{"policy.mode", func(s *deed.ActionSnapshot) { s.Policy.Mode = "audit" }},
		{"policy.version_hash", func(s *deed.ActionSnapshot) { s.Policy.VersionHash = nil }},
		{"execution.status", func(s *deed.ActionSnapshot) { s.Execution.Status = "ok" }},
		{"execution.finished_at", func(s *deed.ActionSnapshot) { s.Execution.FinishedAt = s.Execution.StartedAt.Add(-time.Second) }},
		{"execution.error_code", func(s *deed.ActionSnapshot) { s.Execution.ErrorCode = ptr("Upstream Error") }},
		{"witness.level", func(s *deed.ActionSnapshot) { s.Witness.Level = "L3" }},
		{"witness.method", func(s *deed.ActionSnapshot) { s.Witness.Method = "" }},
		{"cost.currency", func(s *deed.ActionSnapshot) { s.Cost.Currency = "USD" }},
		{"cost.amount", func(s *deed.ActionSnapshot) { s.Cost.Amount = -1 }},
		{"approval.outcome", func(s *deed.ActionSnapshot) {
			s.Approval = &deed.ApprovalSnapshot{ID: deedtest.ID("apr", 1), Outcome: "pending"}
		}},
		{"approval.decided_by", func(s *deed.ActionSnapshot) {
			s.Approval = &deed.ApprovalSnapshot{ID: deedtest.ID("apr", 1), Outcome: "approved"}
		}},
		{"approval.decided_by[1]", func(s *deed.ActionSnapshot) {
			s.Approval = &deed.ApprovalSnapshot{ID: deedtest.ID("apr", 1), Outcome: "approved", DecidedBy: []string{deedtest.ID("usr", 1), deedtest.ID("usr", 1)}}
		}},
	}
	for _, tc := range bad {
		s := deedtest.Snapshot(org, 1)
		tc.edit(&s)
		_, err := deed.ForAction(s)
		var ve *deed.VerifyError
		if !errors.Is(err, deed.ErrDraft) || !errors.As(err, &ve) || ve.Field != tc.field {
			t.Errorf("%s: got %v", tc.field, err)
		}
	}
}

func TestForAdmin(t *testing.T) {
	org := deedtest.Org(1)
	ok := deed.AdminSnapshot{Org: org, Event: deedtest.ID("adm", 1), Action: "ledger.pruned", Actor: deed.Ref{Type: "system", ID: "deedglass"}}
	d, err := deed.ForAdmin(ok)
	if err != nil || d.Body.Agent != nil || d.Body.Admin.Target != nil {
		t.Fatalf("%v %+v", err, d.Body)
	}
	for field, e := range map[string]func(*deed.AdminSnapshot){
		"admin.event":      func(s *deed.AdminSnapshot) { s.Event = deedtest.ID("evt", 1) },
		"admin.action":     func(s *deed.AdminSnapshot) { s.Action = "Stopped" },
		"admin.actor.type": func(s *deed.AdminSnapshot) { s.Actor.Type = "User" },
		"admin.actor.id":   func(s *deed.AdminSnapshot) { s.Actor.ID = "" },
		"admin.target.id":  func(s *deed.AdminSnapshot) { s.Target = &deed.Ref{Type: "agent", ID: "A B"} },
		"admin.reason":     func(s *deed.AdminSnapshot) { s.Reason = ptr(strings.Repeat("r", 281)) },
	} {
		s := ok
		e(&s)
		var ve *deed.VerifyError
		if _, err := deed.ForAdmin(s); !errors.As(err, &ve) || ve.Field != field {
			t.Errorf("%s: got %v", field, err)
		}
	}
	s := ok
	s.Reason = ptr("Two lines\nare fine.")
	if _, err := deed.ForAdmin(s); err != nil {
		t.Errorf("newline in reason: %v", err)
	}
}

func TestForAmendment(t *testing.T) {
	c := deedtest.NewChain(7, deedtest.ChainOpts{})
	orig := &c.Deeds[0].Body
	w := deed.WitnessSection{Level: "L2", Verdict: "MISMATCH", Method: "postgres.pgoutput"}
	d, err := deed.ForAmendment(orig, w)
	if err != nil {
		t.Fatal(err)
	}
	b := d.Body
	if !reflect.DeepEqual(b.Action, orig.Action) || b.Action == orig.Action {
		t.Fatal("action must be an exact copy, not shared")
	}
	if *b.Amends != orig.DeedID || b.Policy != nil || b.Execution != nil || b.Cost != nil || *b.Witness != w || b.Org != orig.Org || *b.OnBehalfOf != *orig.OnBehalfOf {
		t.Fatalf("amendment %+v", b)
	}
	if _, err := deed.ForAmendment(&c.Deeds[6].Body, w); !errors.Is(err, deed.ErrDraft) {
		t.Fatalf("amending an admin deed: %v", err)
	}
	unsealed := deedtest.ActionVerified(deedtest.Org(1), 1)
	if _, err := deed.ForAmendment(&unsealed, w); !errors.Is(err, deed.ErrDraft) {
		t.Fatalf("amending an unsealed body: %v", err)
	}
	if _, err := deed.ForAmendment(nil, w); !errors.Is(err, deed.ErrDraft) {
		t.Fatal("nil original")
	}
}

// Kind × presence: each section set where the kind forbids it, or missing where it requires it.
func TestKindPresence(t *testing.T) {
	c := deedtest.NewChain(7, deedtest.ChainOpts{})
	action, admin := c.Deeds[0].Body, c.Deeds[6].Body
	amend := deedtest.Amendment(&action)
	fill := func(b deed.Body, from deed.Body) deed.Body {
		b.DeedID, b.Seq, b.PrevHash, b.IssuedAt, b.SealKeyID = from.DeedID, from.Seq, from.PrevHash, from.IssuedAt, from.SealKeyID
		return b
	}
	amend = fill(amend, action)
	for _, b := range []deed.Body{action, admin, amend} {
		if err := b.Validate(); err != nil {
			t.Fatalf("%s: %v", b.Kind, err)
		}
	}
	type mut struct {
		field string
		f     func(b *deed.Body)
	}
	set := map[deed.Kind][]mut{
		deed.KindAction: {
			{"agent", func(b *deed.Body) { b.Agent = nil }},
			{"action", func(b *deed.Body) { b.Action = nil }},
			{"policy", func(b *deed.Body) { b.Policy = nil }},
			{"admin", func(b *deed.Body) { b.Admin = admin.Admin }},
			{"amends", func(b *deed.Body) { b.Amends = ptr(action.DeedID) }},
		},
		deed.KindAdmin: {
			{"admin", func(b *deed.Body) { b.Admin = nil }},
			{"on_behalf_of", func(b *deed.Body) { b.OnBehalfOf = ptr("x") }},
			{"session", func(b *deed.Body) { b.Session = ptr("x") }},
			{"action", func(b *deed.Body) { b.Action = action.Action }},
			{"policy", func(b *deed.Body) { b.Policy = action.Policy }},
			{"approval", func(b *deed.Body) {
				b.Approval = &deed.ApprovalSection{ID: deedtest.ID("apr", 1), Outcome: "expired", DecidedBy: []string{}}
			}},
			{"execution", func(b *deed.Body) { b.Execution = action.Execution }},
			{"witness", func(b *deed.Body) { b.Witness = action.Witness }},
			{"cost", func(b *deed.Body) { b.Cost = action.Cost }},
			{"amends", func(b *deed.Body) { b.Amends = ptr(action.DeedID) }},
		},
		deed.KindAmendment: {
			{"agent", func(b *deed.Body) { b.Agent = nil }},
			{"action", func(b *deed.Body) { b.Action = nil }},
			{"policy", func(b *deed.Body) { b.Policy = action.Policy }},
			{"execution", func(b *deed.Body) { b.Execution = action.Execution }},
			{"cost", func(b *deed.Body) { b.Cost = action.Cost }},
			{"witness", func(b *deed.Body) { b.Witness = nil }},
			{"admin", func(b *deed.Body) { b.Admin = admin.Admin }},
			{"amends", func(b *deed.Body) { b.Amends = nil }},
		},
	}
	src := map[deed.Kind]deed.Body{deed.KindAction: action, deed.KindAdmin: admin, deed.KindAmendment: amend}
	for kind, ms := range set {
		for _, m := range ms {
			b := src[kind]
			m.f(&b)
			var ve *deed.VerifyError
			if err := b.Validate(); !errors.As(err, &ve) || ve.Field != m.field {
				t.Errorf("%s with %s changed: got %v", kind, m.field, err)
			}
		}
	}
}

// Privacy: whatever the builders get, a deed carries only the schema's keys.
func TestNoKeysOutsideSchema(t *testing.T) {
	allowed := map[string]bool{}
	for _, k := range strings.Fields(`v kind deed_id org seq prev_hash issued_at agent on_behalf_of session action policy approval execution witness cost admin amends seal_key_id
		id name tool connector mode request_digest intent_hash intent_signed version_id version_hash decision rule_id outcome decided_by decided_at deed
		status started_at finished_at latency_ms result_digest error_code level verdict method detail_digest amount currency event actor target reason data_digest type`) {
		allowed[k] = true
	}
	c := deedtest.NewChain(7, deedtest.ChainOpts{})
	bodies := []deed.Body{c.Deeds[0].Body, c.Deeds[4].Body, c.Deeds[6].Body, deedtest.ActionApproved(c.Org, 3), deedtest.Amendment(&c.Deeds[0].Body)}
	for _, b := range bodies {
		raw, _ := json.Marshal(b)
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				for k, vv := range x {
					if !allowed[k] {
						t.Errorf("%s deed has key %q outside the schema", b.Kind, k)
					}
					walk(vv)
				}
			case []any:
				for _, vv := range x {
					walk(vv)
				}
			}
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		walk(v)
	}
}

// Property: random valid bodies of every kind seal, encode and verify, with stable bytes.
func TestRoundTripProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(4, 2))
	ks := deedtest.KeySet()
	b := deedtest.TestKey("b")
	org := deedtest.Org(4)
	for i := range 2000 {
		body := randomBody(r, org, i)
		body.DeedID = deedtest.ID("deed", 500000+i)
		body.Seq = int64(1 + r.IntN(1<<20))
		body.PrevHash = deed.Genesis(org)
		body.IssuedAt = deed.FormatTime(deedtest.BActivated.Add(time.Duration(r.IntN(1<<30)) * time.Millisecond))
		env, err := deed.SealBody(b.Signer, body)
		if err != nil {
			t.Fatalf("%d: %v\n%+v", i, err, body)
		}
		c1, _ := env.MarshalCanonical()
		c2, _ := env.MarshalCanonical()
		if string(c1) != string(c2) {
			t.Fatal("canonical bytes differ between runs")
		}
		res, err := deed.VerifyEnvelope(c1, ks, deed.VerifyOptions{Strict: true})
		if err != nil {
			t.Fatalf("%d: %v\n%s", i, err, c1)
		}
		if !reflect.DeepEqual(res.Envelope.Body, env.Body) {
			t.Fatalf("%d: body changed in the round trip", i)
		}
	}
}

func randomBody(r *rand.Rand, org string, i int) deed.Body {
	maybe := func(s string) *string {
		if r.IntN(2) == 0 {
			return nil
		}
		return &s
	}
	switch r.IntN(3) {
	case 0:
		s := deedtest.Snapshot(org, 1+r.IntN(50))
		s.OnBehalfOf, s.Session, s.Connector = maybe("u:"+deedtest.ID("usr", i)[4:12]), maybe("s-1"), maybe(deedtest.ID("con", i))
		s.Agent.Name = []string{"a", "billing-agent", "Priya’s agent — ☕", "\"quoted\" \\ back", "日本語のエージェント", "emoji 🙂 ok"}[r.IntN(6)]
		s.Cost = &deed.Cost{Amount: r.Int64N(deed.MaxSafeInteger + 1), Currency: []string{"usd", "inr", "eur"}[r.IntN(3)]}
		if r.IntN(3) == 0 {
			s.Execution, s.Witness, s.Cost = nil, nil, nil
			s.Policy.Decision = "deny"
		}
		if r.IntN(4) == 0 {
			s.Approval = &deed.ApprovalSnapshot{ID: deedtest.ID("apr", i), Outcome: "expired", DecidedBy: nil}
		}
		d, err := deed.ForAction(s)
		if err != nil {
			panic(err)
		}
		return d.Body
	case 1:
		d, err := deed.ForAdmin(deed.AdminSnapshot{Org: org, Event: deedtest.ID("adm", i), Action: "member.invited",
			Actor: deed.Ref{Type: "user", ID: deedtest.ID("usr", i)}, Reason: maybe("Line one\nline two — with “quotes”")})
		if err != nil {
			panic(err)
		}
		return d.Body
	}
	orig := deedtest.ActionVerified(org, 1+r.IntN(50))
	orig.DeedID = deedtest.ID("deed", i)
	return deedtest.Amendment(&orig)
}
