package securitypack

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return testNow.Add(-d) }
func agoP(d time.Duration) *time.Time {
	t := ago(d)
	return &t
}

const daysT = 24 * time.Hour

// goodPolicy denies destructive SQL, holds refunds and undos for two people, allows the rest.
const goodPolicy = `deedglass: policy/v1
name: acme-review
defaults:
  decision: deny
  reason: No rule allows this action.
budgets:
  - id: calls-per-agent
    match: { connector_type: postgres }
    per: agent
    window: minute
    max_calls: 60
rules:
  - id: block-destructive-sql
    match: { connector_type: postgres }
    when: sql.kind in ["drop", "truncate"] || sql.unbounded_write
    decision: deny
    reason: Destructive SQL never runs.
  - id: refunds-need-two
    match: { tool: "*.create_refund" }
    decision: require_approval
    reason: Refunds need two people.
    approval: { approvers: [role:admin], required: 2, expires_in: 30m }
  - id: allow-writes
    match: { connector_type: postgres }
    decision: allow
    verify: L2
`

// allowAllPolicy allows everything.
const allowAllPolicy = `deedglass: policy/v1
name: allow-all
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: allow-all
    decision: allow
`

// oneApproverPolicy holds refunds for one person and allows DROP (to test partial probes).
const oneApproverPolicy = `deedglass: policy/v1
name: one-approver
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: refunds-need-one
    match: { tool: "*.create_refund" }
    decision: require_approval
    reason: Refunds need a person.
    approval: { approvers: [role:admin], required: 1, expires_in: 30m }
  - id: allow-sql
    match: { connector_type: postgres }
    decision: allow
`

func active(name, yaml string) *ActiveVersion {
	return &ActiveVersion{PolicyID: "pol_01k0000000000000000000000a", Name: name, Version: 2, VersionID: "pv_01k0000000000000000000000a",
		ActivationID: "pa_01k0000000000000000000000a", DeedID: "deed_01k0000000000000000000000a", ActivatedBy: "usr_01k0000000000000000000000a",
		ActivatedAt: ago(15 * daysT), YAML: yaml}
}

// goodFacts is an org where every question is In place.
func goodFacts() *Facts {
	return &Facts{
		Org:     Org{ID: "org_01k0000000000000000000000a", Name: "acme"},
		Period:  Period{From: ago(90 * daysT), To: testNow},
		Now:     testNow,
		Version: "0.1.0",
		Agents: []Agent{
			{ID: "agt_01k0000000000000000000000a", Name: "support-bot", Status: "active", HasOwner: true, CreatedAt: ago(40 * daysT)},
			{ID: "agt_01k0000000000000000000000b", Name: "billing-bot", Status: "active", HasOwner: true, CreatedAt: ago(40 * daysT)},
		},
		Keys: []Key{
			{ID: "key_01k0000000000000000000000a", AgentID: "agt_01k0000000000000000000000a", Scopes: []string{"actions:execute"}, CreatedAt: ago(10 * daysT), LastUsedAt: agoP(daysT)},
			{ID: "key_01k0000000000000000000000b", AgentID: "agt_01k0000000000000000000000b", Scopes: []string{"actions:execute"}, CreatedAt: ago(10 * daysT), LastUsedAt: agoP(daysT)},
			{ID: "key_01k0000000000000000000000c", Scopes: []string{"admin"}, CreatedAt: ago(20 * daysT), LastUsedAt: agoP(daysT)},
		},
		Connectors: []Connector{
			{ID: "con_01k0000000000000000000000a", Name: "orders", Type: "postgres", Status: "active", Writes: true, Undo: true, Observe: true,
				Credential: &Credential{Version: 2, Alg: VaultAlg, KEK: "kek:0123456789ab", UpdatedAt: ago(30 * daysT)}},
		},
		Members: Members{OwnersAdmins: 2, AdminKeys: 1},
		Egress:  &Egress{Env: "prod"},
		Rules: Rules{Enforce: active("acme-review", goodPolicy),
			Changes: []RuleChange{{ActivationID: "pa_01k0000000000000000000000a", DeedID: "deed_01k0000000000000000000000a", By: "usr_01k0000000000000000000000a", At: ago(15 * daysT)}}},
		Approvals: Approvals{Total: 4, Approved: 3, Denied: 1, MultiApprover: 2, MedianDecideSeconds: 240},
		Stop:      Stop{InPeriod: 1, LastAt: agoP(5 * daysT), LastDeed: "deed_01k0000000000000000000000b"},
		Actions: Actions{Writes: 100, Checked: 20, Proven: 70, Claimed: 10, Matches: 89, Mismatch: 1, MismatchFollowed: 1,
			Gateway: 150, Finished: 150},
		Sessions: Sessions{Total: 3, Sealed: 3, Caught: 1},
		Undos:    Undos{Actions: 1, Matched: 1},
		Records: Records{ChainIntact: true, ChainHead: "sha256:" + strings.Repeat("a", 64), HeadSeq: 900, Checked: 900,
			Signer: "sk_01k0000000000000000000000a", SealKeys: []SealKey{{ID: "sk_01k0000000000000000000000a", Status: "active", ActivatedAt: ago(60 * daysT)}}},
		Build: BuildFacts{Version: "0.1.0", Attack: &AttackSummary{Version: "0.1.0", RunOn: "2026-10-15", Matched: 70}},
	}
}

type fixture struct {
	id   string
	want Status
	mut  func(f *Facts)
}

func noop(*Facts) {}

// fixtures: for every question, each status its rule can give, and Can't tell from a collector error.
var fixtures = []fixture{
	{"agents_listed", InPlace, noop},
	{"agents_listed", Partly, func(f *Facts) { f.Agents[1].HasOwner = false }},
	{"agents_listed", NotSetUp, func(f *Facts) { f.Agents = nil }},
	{"keys_scoped", InPlace, noop},
	{"keys_scoped", Partly, func(f *Facts) { f.Keys[0].LastUsedAt = nil; f.Keys[0].CreatedAt = ago(31 * daysT) }},
	{"keys_scoped", NotSetUp, func(f *Facts) { f.Keys = f.Keys[2:] }},
	{"keys_rotated", InPlace, noop},
	{"keys_rotated", InPlace, func(f *Facts) { f.Keys[0].CreatedAt = ago(90 * daysT) }}, // exactly 90 days: not older
	{"keys_rotated", Partly, func(f *Facts) { f.Keys[0].CreatedAt = ago(90*daysT + time.Second) }},
	{"keys_rotated", NotSetUp, func(f *Facts) {
		for i := range f.Keys {
			f.Keys[i].CreatedAt = ago(120 * daysT)
		}
	}},
	{"keys_rotated", NotApplicable, func(f *Facts) { f.Keys = nil }},
	{"connectors_listed", InPlace, noop},
	{"connectors_listed", Partly, func(f *Facts) { f.Actions.Proxy = 3 }},
	{"connectors_listed", NotSetUp, func(f *Facts) { f.Connectors = nil }},
	{"rules_active", InPlace, noop},
	{"rules_active", Partly, func(f *Facts) {
		f.Rules.Enforce = active(DefaultDenyName, strings.Replace(allowAllPolicy, "allow-all\ndefaults", "default-deny\ndefaults", 1))
	}},
	{"rules_active", NotSetUp, func(f *Facts) { f.Rules.Enforce = nil }},
	{"default_deny", InPlace, noop},
	{"default_deny", Partly, func(f *Facts) { f.Rules.Enforce = active("one-approver", oneApproverPolicy) }},
	{"default_deny", NotSetUp, func(f *Facts) { f.Rules.Enforce = active("allow-all", allowAllPolicy) }},
	{"default_deny", NotSetUp, func(f *Facts) { f.Rules.Enforce = nil }},
	{"limits_set", InPlace, noop},
	{"limits_set", InPlace, func(f *Facts) {
		f.Rules.Enforce = active("one-approver", oneApproverPolicy)
		f.Agents[0].HasBudget, f.Agents[1].HasBudget = true, true
	}},
	{"limits_set", Partly, func(f *Facts) {
		f.Rules.Enforce = active("one-approver", oneApproverPolicy)
		f.Agents[0].HasBudget = true
	}},
	{"limits_set", NotSetUp, func(f *Facts) { f.Rules.Enforce = active("one-approver", oneApproverPolicy) }},
	{"rules_changes_recorded", InPlace, noop},
	{"rules_changes_recorded", Partly, func(f *Facts) {
		f.Rules.Changes = append(f.Rules.Changes, RuleChange{ActivationID: "pa_01k0000000000000000000000b", At: ago(2 * daysT)})
	}},
	{"rules_changes_recorded", NotSetUp, func(f *Facts) { f.Rules.Changes[0].DeedID = "" }},
	{"rules_changes_recorded", NotSetUp, func(f *Facts) { f.Rules.Changes = nil }},
	{"approvals_required", InPlace, noop},
	{"approvals_required", NotSetUp, func(f *Facts) { f.Rules.Enforce = active("allow-all", allowAllPolicy) }},
	{"four_eyes", InPlace, noop},
	{"four_eyes", Partly, func(f *Facts) { f.Rules.Enforce = active("one-approver", oneApproverPolicy) }},
	{"four_eyes", NotApplicable, func(f *Facts) { f.Rules.Enforce = active("allow-all", allowAllPolicy) }},
	{"approvals_expire", InPlace, noop},
	{"approvals_expire", Partly, func(f *Facts) { f.Approvals.PendingOverdue = 1 }},
	{"approvals_expire", NotApplicable, func(f *Facts) {
		f.Rules.Enforce = active("allow-all", allowAllPolicy)
		f.Approvals = Approvals{MedianDecideSeconds: -1}
	}},
	{"stop_all", InPlace, noop},
	{"stop_all", Partly, func(f *Facts) { f.Members.OwnersAdmins = 1 }},
	{"stop_all", NotSetUp, func(f *Facts) { f.Members.OwnersAdmins = 0 }},
	{"stop_used", InPlace, noop},
	{"stop_used", Partly, func(f *Facts) { f.Stop.InPeriod = 0; f.Stop.LastAt = agoP(200 * daysT) }},
	{"stop_used", NotSetUp, func(f *Facts) { f.Stop = Stop{} }},
	{"results_checked", InPlace, noop},
	{"results_checked", InPlace, func(f *Facts) { f.Actions.Checked, f.Actions.Proven = 40, 40 }}, // exactly 80%
	{"results_checked", Partly, func(f *Facts) { f.Actions.Checked, f.Actions.Proven = 40, 39 }},  // 79%
	{"results_checked", Partly, func(f *Facts) { f.Actions.Checked, f.Actions.Proven = 10, 10 }},  // exactly 20%
	{"results_checked", NotSetUp, func(f *Facts) { f.Actions.Checked, f.Actions.Proven = 10, 9 }}, // 19%
	{"results_checked", NotApplicable, func(f *Facts) { f.Actions.Writes, f.Actions.Checked, f.Actions.Proven = 0, 0, 0 }},
	{"mismatches_handled", InPlace, noop},
	{"mismatches_handled", Partly, func(f *Facts) {
		f.Actions.Mismatch = 2
		f.Actions.MismatchOpen = []string{"act_01k0000000000000000000000a"}
	}},
	{"mismatches_handled", NotSetUp, func(f *Facts) {
		f.Actions.MismatchFollowed = 0
		f.Actions.MismatchOpen = []string{"act_01k0000000000000000000000a"}
	}},
	{"mismatches_handled", NotApplicable, func(f *Facts) { f.Actions.Mismatch, f.Actions.MismatchFollowed = 0, 0 }},
	{"watched_sessions", InPlace, noop},
	{"watched_sessions", Partly, func(f *Facts) {
		f.Connectors = append(f.Connectors, Connector{ID: "con_01k0000000000000000000000b", Name: "billing", Type: "postgres", Status: "active", Writes: true, Undo: true})
	}},
	{"watched_sessions", NotSetUp, func(f *Facts) { f.Connectors[0].Observe = false }},
	{"watched_sessions", NotApplicable, func(f *Facts) { f.Connectors = nil }},
	{"undo_available", InPlace, noop},
	{"undo_available", Partly, func(f *Facts) {
		f.Connectors = append(f.Connectors, Connector{ID: "con_01k0000000000000000000000c", Name: "payments", Type: "stripe", Status: "active", Writes: true})
	}},
	{"undo_available", NotSetUp, func(f *Facts) { f.Connectors[0] = Connector{Name: "payments", Type: "stripe", Writes: true} }},
	{"undo_available", NotApplicable, func(f *Facts) { f.Connectors[0].Writes = false }},
	{"undos_done", InPlace, noop},
	{"undos_done", Partly, func(f *Facts) { f.Undos.Actions = 2 }},
	{"undos_done", Partly, func(f *Facts) { f.Undos.Sessions = 1 }},
	{"undos_done", NotApplicable, func(f *Facts) { f.Undos = Undos{} }},
	{"secrets_encrypted", InPlace, noop},
	{"secrets_encrypted", Partly, func(f *Facts) { f.Connectors[0].Credential.UpdatedAt = ago(400 * daysT) }},
	{"secrets_encrypted", InPlace, func(f *Facts) {
		f.Connectors[0].Credential.UpdatedAt = ago(400 * daysT)
		f.Connectors[0].Credential.RotatedAt = agoP(10 * daysT)
	}},
	{"secrets_encrypted", NotApplicable, func(f *Facts) { f.Connectors[0].Credential = nil }},
	{"secrets_encrypted", CantTell, func(f *Facts) { f.Connectors[0].Credential.Alg = "plain" }},
	{"agents_never_see_secrets", InPlace, noop},
	{"agents_never_see_secrets", Partly, func(f *Facts) { f.Actions.Proxy = 5 }},
	{"agents_never_see_secrets", NotSetUp, func(f *Facts) { f.Actions.Gateway, f.Actions.Proxy = 0, 5 }},
	{"agents_never_see_secrets", NotApplicable, func(f *Facts) { f.Actions.Gateway = 0 }},
	{"egress_limited", InPlace, noop},
	{"egress_limited", Partly, func(f *Facts) { f.Egress = &Egress{Env: "dev", AllowPrivate: true} }},
	{"egress_limited", Partly, func(f *Facts) { f.Egress = &Egress{Env: "prod", AllowCIDRs: 2} }},
	{"egress_limited", InPlace, func(f *Facts) { f.Egress = &Egress{Env: "prod", AllowPrivate: true} }}, // the dev override does nothing in prod
	{"egress_limited", CantTell, func(f *Facts) { f.Egress = nil }},
	{"records_sealed", InPlace, noop},
	{"records_sealed", Partly, func(f *Facts) { f.Actions.Unsealed = 2 }},
	{"records_sealed", NotSetUp, func(f *Facts) {
		f.Records.ChainIntact = false
		f.Records.FirstBroken = "deed_01k0000000000000000000000c"
	}},
	{"records_verifiable", InPlace, noop},
	{"records_verifiable", Partly, func(f *Facts) {
		f.Records.SealKeys = append(f.Records.SealKeys, SealKey{ID: "sk_01k0000000000000000000000b", Status: "revoked"})
	}},
	{"records_verifiable", NotSetUp, func(f *Facts) { f.Records.Signer = "" }},
	{"product_tested", InPlace, noop},
	{"product_tested", Partly, func(f *Facts) { f.Build.Version = "0.1.1-dev" }},
	{"product_tested", Partly, func(f *Facts) { f.Build.Attack.Differs = 1 }},
	{"product_tested", NotApplicable, func(f *Facts) { f.Build.Attack = nil }},
}

func build(t *testing.T, f *Facts) *Pack {
	t.Helper()
	p, err := Build(context.Background(), f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func find(p *Pack, id string) Answer {
	for _, a := range p.Answers {
		if a.ID == id {
			return a
		}
	}
	return Answer{}
}

func TestRules(t *testing.T) {
	b, err := Questions()
	if err != nil {
		t.Fatal(err)
	}
	reached := map[string]map[Status]bool{}
	for i, fx := range fixtures {
		f := goodFacts()
		fx.mut(f)
		a := find(build(t, f), fx.id)
		if a.Status != fx.want {
			t.Errorf("fixture %d %s: status %s, want %s (answer %q, evidence %v)", i, fx.id, a.Status, fx.want, a.Answer, a.Evidence)
		}
		if strings.Contains(a.Answer, "{") || strings.Contains(a.Answer, "}") || a.Answer == "" {
			t.Errorf("fixture %d %s: unfilled answer %q", i, fx.id, a.Answer)
		}
		if reached[fx.id] == nil {
			reached[fx.id] = map[Status]bool{}
		}
		reached[fx.id][a.Status] = true
	}
	// Every answer and fix in the bank is reached by a fixture, and every question has a Can't tell.
	for _, q := range b.Questions {
		for st := range q.Answer {
			if !reached[q.ID][st] {
				t.Errorf("%s: no fixture reaches its %s answer", q.ID, st)
			}
		}
		if len(reached[q.ID]) < 2 {
			t.Errorf("%s: fewer than two statuses tested", q.ID)
		}
		f := goodFacts()
		f.Fail(q.Needs[len(q.Needs)-1], "the database didn't answer")
		a := find(build(t, f), q.ID)
		if a.Status != CantTell || !strings.Contains(a.Answer, "the database didn't answer") {
			t.Errorf("%s with %s unreadable: %s %q", q.ID, q.Needs[len(q.Needs)-1], a.Status, a.Answer)
		}
	}
}

func TestAllInPlace(t *testing.T) {
	p := build(t, goodFacts())
	for _, a := range p.Answers {
		if a.Status != InPlace {
			t.Errorf("%s: %s (%s)", a.ID, a.Status, a.Answer)
		}
	}
	if p.Summary.InPlace != 24 || len(p.Answers) != 24 {
		t.Fatalf("summary %+v, %d answers", p.Summary, len(p.Answers))
	}
	if got := p.Summary.Line(); got != "24 in place" {
		t.Fatalf("line %q", got)
	}
}

func TestFixesFill(t *testing.T) {
	for i, fx := range fixtures {
		f := goodFacts()
		fx.mut(f)
		a := find(build(t, f), fx.id)
		fix, err := Fix(a)
		if err != nil {
			t.Fatalf("fixture %d %s: %v", i, fx.id, err)
		}
		needs := a.Status == Partly || a.Status == NotSetUp || a.Status == CantTell
		if needs != (fix != "") {
			t.Errorf("fixture %d %s (%s): fix %q", i, fx.id, a.Status, fix)
		}
		if strings.ContainsAny(fix, "{}") {
			t.Errorf("fixture %d %s: unfilled fix %q", i, fx.id, fix)
		}
	}
}

func TestDefaultDenyFollowsEvaluation(t *testing.T) {
	// A version that allows DROP is Not set up even though its name sounds safe.
	f := goodFacts()
	f.Rules.Enforce = active("postgres-safe", strings.Replace(allowAllPolicy, "name: allow-all", "name: postgres-safe", 1))
	a := find(build(t, f), "default_deny")
	if a.Status != NotSetUp || !strings.Contains(a.Answer, "DROP TABLE") {
		t.Fatalf("%s %q", a.Status, a.Answer)
	}
	// A stored version whose hash doesn't match its source can't be judged.
	f = goodFacts()
	f.Rules.Enforce.VersionHash = "sha256:" + strings.Repeat("0", 64)
	if a := find(build(t, f), "default_deny"); a.Status != CantTell {
		t.Fatalf("hash mismatch: %s", a.Status)
	}
	// Source that doesn't compile can't be judged either.
	f = goodFacts()
	f.Rules.Enforce.YAML = "deedglass: policy/v1\nname: x\nrules: 3\n"
	if a := find(build(t, f), "rules_active"); a.Status != CantTell {
		t.Fatalf("broken source: %s", a.Status)
	}
}

func TestProbesUseOrgToolNames(t *testing.T) {
	f := goodFacts()
	f.Connectors = append(f.Connectors, Connector{Name: "payments", Type: "stripe"})
	if got := toolName(f, "postgres", "execute"); got != "orders.execute" {
		t.Fatal(got)
	}
	if got := toolName(f, "stripe", "create_refund"); got != "payments.create_refund" {
		t.Fatal(got)
	}
	if got := toolName(&Facts{}, "stripe", "create_refund"); got != "stripe.create_refund" {
		t.Fatal(got)
	}
}

func TestBank(t *testing.T) {
	b, err := Questions()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Questions) != 24 || len(b.Groups) != 8 || len(rules) != 24 {
		t.Fatalf("%d questions, %d groups, %d rules", len(b.Questions), len(b.Groups), len(rules))
	}
	for _, g := range b.Groups {
		n := 0
		for _, q := range b.Questions {
			if q.Group == g.ID {
				n++
			}
		}
		if n == 0 {
			t.Errorf("group %s has no question", g.ID)
		}
	}
	for _, q := range b.Questions {
		if !strings.HasSuffix(q.Question, "?") {
			t.Errorf("%s: a question ends with a question mark", q.ID)
		}
		for _, r := range q.Refs {
			if r.Framework != "OWASP LLM 2025" && r.Framework != "NIST AI RMF 1.0" {
				t.Errorf("%s: framework %q", q.ID, r.Framework)
			}
			if r.Framework == "OWASP LLM 2025" && !owaspTitles[r.ID+" "+r.Title] {
				t.Errorf("%s: OWASP ref %s %q isn't one of the verified ids", q.ID, r.ID, r.Title)
			}
			if r.Framework == "NIST AI RMF 1.0" && !nistIDs[r.ID] {
				t.Errorf("%s: NIST ref %s isn't one of the verified ids", q.ID, r.ID)
			}
		}
	}
}

// The refs verified on 2026-10-15 (genai.owasp.org/llm-top-10, airc.nist.gov AI RMF Core).
var owaspTitles = map[string]bool{"LLM02 Sensitive Information Disclosure": true, "LLM06 Excessive Agency": true, "LLM10 Unbounded Consumption": true}
var nistIDs = map[string]bool{"GOVERN 1.4": true, "GOVERN 1.6": true, "MAP 3.5": true, "MEASURE 1.3": true, "MEASURE 2.4": true,
	"MEASURE 2.6": true, "MEASURE 2.7": true, "MEASURE 2.8": true, "MANAGE 1.3": true, "MANAGE 2.3": true, "MANAGE 2.4": true, "MANAGE 4.3": true}

func TestParseBankRefusals(t *testing.T) {
	ok := string(questionsYAML)
	for name, src := range map[string]string{
		"unknown field": strings.Replace(ok, "v: 1\n", "v: 1\nextra: 2\n", 1),
		"version":       strings.Replace(ok, "v: 1\n", "v: 2\n", 1),
		"group":         strings.Replace(ok, "group: inventory\n", "group: nowhere\n", 1),
		"no rule":       strings.Replace(ok, "id: agents_listed\n", "id: agents_missing\n", 1),
		"collector":     strings.Replace(ok, "needs: [agents]\n", "needs: [nothing]\n", 1),
		"long question": strings.Replace(ok, "question: Are API keys replaced at least every 90 days?", "question: "+strings.Repeat("word ", 21)+"?", 1),
		"fix status":    strings.Replace(ok, "    fix:\n      partly: \"Set an owner", "    fix:\n      in_place: \"Set an owner", 1),
		"answer status": strings.Replace(ok, "      in_place: \"{agents:agent is", "      cant_tell: \"{agents:agent is", 1),
		"duplicate":     strings.Replace(ok, "id: keys_scoped\n", "id: agents_listed\n", 1),
		"ref":           strings.Replace(ok, "{framework: NIST AI RMF 1.0, id: GOVERN 1.6}", "{framework: NIST AI RMF 1.0}", 1),
		"no check":      strings.Replace(ok, "check: \"deedglass agent list, or Console › Agents\"", "check: \"\"", 1),
		"bad yaml":      "v: [",
	} {
		if src == ok && name != "bad yaml" {
			t.Fatalf("%s: the replacement didn't apply", name)
		}
		if _, err := ParseBank([]byte(src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTemplates(t *testing.T) {
	ev := Evidence{"n": 1, "m": 3, "s": "x", "d_on": "2026-10-03", "b": true, "big": 4212}
	for tpl, want := range map[string]string{
		"{n:key|keys}":             "1 key",
		"{m:key|keys}":             "3 keys",
		"{m?is|are}":               "are",
		"{n?is|are}":               "is",
		"{m:the key|all {m} keys}": "all 3 keys",
		"{n:the key|all {m} keys}": "the key",
		"{d_on}":                   "3 Oct 2026",
		"{big} rows":               "4,212 rows",
		"{b}":                      "yes",
		"plain":                    "plain",
	} {
		got, err := fill(tpl, ev)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", tpl, got, err, want)
		}
	}
	for _, bad := range []string{"{missing}", "{s:a|b}", "{n:onlyone}", "{n", "{m:a|{missing}}"} {
		if _, err := fill(bad, ev); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
	if display("x", 1.5) != "1.5" || display("x", false) != "no" || display("x_on", "not a date") != "not a date" {
		t.Error("display")
	}
	if formatCount(-1234567) != "-1,234,567" {
		t.Error(formatCount(-1234567))
	}
}

func TestStatusWords(t *testing.T) {
	want := []string{"In place", "Partly", "Not set up", "Not applicable", "Can't tell"}
	for i, s := range Statuses {
		if s.Word() != want[i] {
			t.Errorf("%s: %q", s, s.Word())
		}
	}
	if Status("odd").Word() != "odd" {
		t.Error("unknown status word")
	}
	s := Summary{InPlace: 17, Partly: 4, NotSetUp: 2, NotApplicable: 1, CantTell: 1}
	if s.Line() != "17 in place · 4 partly · 2 not set up · 1 not applicable · 1 can't tell" {
		t.Error(s.Line())
	}
}

func TestReadiness(t *testing.T) {
	f := goodFacts()
	f.Keys[0].CreatedAt = ago(100 * daysT)
	f.Keys[1].CreatedAt = ago(100 * daysT)
	r, err := ReadinessOf(build(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if r.Object != ObjectReadiness || len(r.Rows) != 24 || r.Summary.Partly != 1 {
		t.Fatalf("%+v", r.Summary)
	}
	for _, row := range r.Rows {
		if row.ID == "keys_rotated" && (row.Status != Partly || !strings.HasPrefix(row.Fix, "Roll the 2 keys older than 90 days")) {
			t.Fatalf("%+v", row)
		}
		if row.GroupTitle == "" {
			t.Errorf("%s: no group title", row.ID)
		}
	}
	// No evidence in the free view.
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "evidence") || strings.Contains(string(b), "key_01k") {
		t.Fatalf("readiness carries evidence: %s", b)
	}
}

func TestParsePack(t *testing.T) {
	p := build(t, goodFacts())
	raw, err := MarshalPack(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePack(strings.NewReader(string(raw))); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(string) string{
		"unknown field": func(s string) string { return strings.Replace(s, `"v": 1,`, `"v": 1, "x": 1,`, 1) },
		"object":        func(s string) string { return strings.Replace(s, `"security_pack"`, `"other"`, 1) },
		"summary":       func(s string) string { return strings.Replace(s, `"in_place": 24`, `"in_place": 23`, 1) },
		"status":        func(s string) string { return strings.Replace(s, `"status": "in_place"`, `"status": "fine"`, 1) },
		"trailing":      func(s string) string { return s + "{}" },
		"answer field":  func(s string) string { return strings.Replace(s, `"check":`, `"extra": 1, "check":`, 1) },
	} {
		if _, err := ParsePack(strings.NewReader(mut(string(raw)))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAttackSummaryCurrent(t *testing.T) {
	raw, err := os.ReadFile("../../attack/results/latest.json")
	if err != nil {
		t.Fatal(err)
	}
	var latest struct {
		Version, Commit, Postgres string
		StartedAt                 string `json:"started_at"`
		Summary                   struct{ Matched, Differs, Waiting int }
	}
	if err := json.Unmarshal(raw, &latest); err != nil {
		t.Fatal(err)
	}
	a := EmbeddedAttack()
	if a == nil || "v"+a.Version != latest.Version || a.Commit != latest.Commit || a.RunOn != latest.StartedAt[:10] ||
		a.Matched != latest.Summary.Matched || a.Differs != latest.Summary.Differs || a.Waiting != latest.Summary.Waiting {
		t.Fatalf("internal/securitypack/attack_summary.json is stale; regenerate it from attack/results/latest.json:\n" +
			`jq '{version, commit, run_on: (.started_at[0:10]), postgres, matched: .summary.matched, differs: .summary.differs, waiting: .summary.waiting}' attack/results/latest.json`)
	}
	if b := BuildFactsFor("v0.1.0", "abc"); b.Version != "0.1.0" || b.Attack == nil {
		t.Fatal(b)
	}
}

// The words no pack string may carry (S51 §5): every question, answer, fix and check in the bank.
var forbidden = regexp.MustCompile(`(?i)complian|certif|audit-ready|guarante|\bproves?\b|\bproof\b|soc ?2|attest|receipt|tamper-proof|—`)

func TestForbiddenWords(t *testing.T) {
	if m := forbidden.FindString(string(questionsYAML)); m != "" {
		t.Fatalf("questions.yaml says %q", m)
	}
	for i, fx := range fixtures {
		f := goodFacts()
		fx.mut(f)
		for _, a := range build(t, f).Answers {
			fix, _ := Fix(a)
			if m := forbidden.FindString(a.Answer + " " + fix); m != "" {
				t.Fatalf("fixture %d %s says %q", i, a.ID, m)
			}
		}
	}
	if m := forbidden.FindString(CantTellAnswer("rules", "x")); m != "" {
		t.Fatal(m)
	}
}

func TestAliasesAndPresent(t *testing.T) {
	f := goodFacts()
	f.Rules.Shadow = active("acme-next", goodPolicy)
	f.Rules.Shadow.PolicyID = "pol_01k0000000000000000000000b"
	a := NewAliases(f)
	if a.Name("agent", "support-bot") != "agent-1" || a.Name("agent", "billing-bot") != "agent-2" || a.Name("connector", "orders") != "connector-1" ||
		a.Name("rules", "acme-review") != "rules-1" || a.Name("rules", "acme-next") != "rules-2" || a.Name("agent", "unknown") != "unknown" {
		t.Fatalf("%+v", a.Entries)
	}
	var none *Aliases
	if none.Name("agent", "x") != "x" {
		t.Fatal("nil aliases")
	}
	// default-deny is Deedglass's name: never aliased, so rules_active still reads it.
	f2 := goodFacts()
	f2.Rules.Enforce = active(DefaultDenyName, strings.Replace(allowAllPolicy, "allow-all\ndefaults", "default-deny\ndefaults", 1))
	p, err := Build(context.Background(), f2, Options{Aliases: NewAliases(f2)})
	if err != nil || find(p, "rules_active").Status != Partly || f2.Agents[0].Name != "agent-1" {
		t.Fatalf("%v %s", err, find(p, "rules_active").Status)
	}
	p = build(t, goodFacts())
	gs := p.Groups()
	if len(gs) != 8 || gs[0].Number != 1 || gs[0].Title != "Inventory" || len(gs[0].Answers) != 4 {
		t.Fatalf("%+v", gs[0])
	}
	if l := EvidenceLine(find(p, "keys_rotated")); l != "live keys 3 · older than 90 days 0 · oldest 28 Sep 2026" {
		t.Fatal(l)
	}
	ct := goodFacts()
	ct.Fail(CollectorKeys, "x")
	if l := EvidenceLine(find(build(t, ct), "keys_rotated")); l != "unreadable keys" {
		t.Fatal(l)
	}
	if r := RefLine([]Ref{{Framework: "OWASP LLM 2025", ID: "LLM06", Title: "Excessive Agency"}, {Framework: "NIST AI RMF 1.0", ID: "MANAGE 2.4"}, {Framework: "X", ID: "1"}}); r != "OWASP LLM06 Excessive Agency · NIST AI RMF MANAGE 2.4 · X 1" {
		t.Fatal(r)
	}
	items := Summary{InPlace: 3, NotSetUp: 1}.Items()
	if len(items) != 2 || items[1].Status != NotSetUp || Count(1234) != "1,234" || Short("sha256:"+strings.Repeat("ab", 32)) != "abababababab" {
		t.Fatalf("%+v", items)
	}
	for in, want := range map[PeriodJSON]string{
		{From: "2026-07-20T00:00:00Z", To: "2026-10-18T00:00:00Z"}: "20 July to 18 October 2026",
		{From: "2025-12-01T00:00:00Z", To: "2026-01-05T00:00:00Z"}: "1 December 2025 to 5 January 2026",
		{From: "x", To: "y"}: "x to y",
	} {
		if got := PeriodWords(in); got != want {
			t.Errorf("%v: %q", in, got)
		}
	}
	if FileLabel("index.html", "org_x") != "Security review pack · index.html · org_x" || ErrNoRecords.Error() == "" || Status("x").Word() != "x" {
		t.Fatal("labels")
	}
	b, _ := Questions()
	if b.GroupTitle("rules") != "Rules" || b.GroupTitle("nope") != "" {
		t.Fatal("group titles")
	}
	if probeLabel("other") != "other" {
		t.Fatal("probe label")
	}
	if _, err := Fix(Answer{ID: "nope", Status: Partly}); err == nil {
		t.Fatal("fix for an unknown question")
	}
}

func TestRuleEdges(t *testing.T) {
	f := goodFacts()
	f.Agents = append(f.Agents, Agent{ID: "agt_01k0000000000000000000000c", Name: "old-bot", Status: "stopped"})
	if a := find(build(t, f), "agents_listed"); a.Status != InPlace || a.Evidence["stopped"] != 1 {
		t.Fatalf("a stopped agent without an owner: %s %v", a.Status, a.Evidence)
	}
	f = goodFacts()
	f.Actions.Mismatch, f.Actions.MismatchFollowed = 9, 0
	f.Actions.MismatchOpen = []string{"act_b", "act_a"}
	if a := find(build(t, f), "mismatches_handled"); !strings.Contains(a.Answer, "act_a, act_b and 7 more") {
		t.Fatal(a.Answer)
	}
	if got, err := fill("{n:one {x}|many {x}}", Evidence{"n": 2, "x": "y"}); err != nil || got != "many y" {
		t.Fatal(got, err)
	}
	if _, err := fill("{n:a", Evidence{"n": 1}); err == nil {
		t.Fatal("unclosed")
	}
}

func TestFolderReadErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/"+FileSeal, []byte(strings.Repeat(" ", MaxSealBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := VerifyFolder(dir, nil); r.OK || !strings.Contains(r.Problems[0], "larger than") {
		t.Fatal(r.Problems)
	}
	if _, _, err := HashFile(dir + "/missing"); err == nil || reasonOf(err) != "it doesn't exist" {
		t.Fatal(err)
	}
	if reasonOf(os.ErrPermission) != "permission denied" || reasonOf(errors.New("odd.")) != "odd" {
		t.Fatal("reasons")
	}
	if _, err := readPack(dir + "/missing.json"); err == nil {
		t.Fatal("read a missing pack")
	}
}
