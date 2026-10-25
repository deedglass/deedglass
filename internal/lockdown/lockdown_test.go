package lockdown

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/policy"
)

// in builds a policy input for one tool call.
func in(tool, ctype, side, connector string, args map[string]any, labels map[string]string) policy.Input {
	b, _ := json.Marshal(args)
	if args == nil {
		b = []byte("{}")
	}
	if labels == nil {
		labels = map[string]string{LabelConnector: connector}
	}
	conn, op, _ := cut(tool)
	if connector == "" {
		connector = conn
	}
	return policy.Input{V: policy.InputVersion, Args: b,
		Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{ID: "agt_x", Name: "bot", Labels: labels, Status: policy.StatusActive}},
		Tool:  policy.ToolInput{ToolFacts: policy.ToolFacts{Name: tool, Connector: connector, ConnectorType: ctype, SideEffect: side, Op: op}}}
}

func cut(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func sql(q string) map[string]any { return map[string]any{"sql": q} }

var (
	on    = State{Org: true}
	allow = policy.Decision{Outcome: policy.Allow, Source: policy.SourceRule, RuleID: "allow-everything", Verify: policy.L0}
)

func pgWrite(q string) policy.Input {
	return in("db.execute", "postgres", "write", "db", sql(q), nil)
}

func TestOffChangesNothing(t *testing.T) {
	d, det := Apply(State{}, allow, pgWrite("DROP TABLE public.orders"), Facts{})
	if det != nil || d.Outcome != policy.Allow || d.RuleID != "allow-everything" {
		t.Fatalf("off: %+v %+v", d, det)
	}
}

func TestStateSource(t *testing.T) {
	for st, want := range map[State]string{{}: "", {Config: true}: "config", {Org: true}: "org", {Config: true, Org: true}: "config+org"} {
		if st.Source() != want || st.On() != (want != "") {
			t.Errorf("%+v: %q %v", st, st.Source(), st.On())
		}
	}
}

func TestDenyStaysDeny(t *testing.T) {
	deny := policy.Decision{Outcome: policy.Deny, Code: policy.CodePolicyDenied, RuleID: "no", Source: policy.SourceRule}
	d, det := Apply(on, deny, pgWrite("UPDATE public.orders SET x = 1 WHERE id = 1"), Facts{})
	if d.Outcome != policy.Deny || d.RuleID != "no" || det == nil || det.Source != "org" {
		t.Fatalf("%+v %+v", d, det)
	}
}

// Rule: every write waits for a person (M01).
func TestWriteNeedsPerson(t *testing.T) {
	d, det := Apply(on, allow, pgWrite("UPDATE public.orders SET status = 'shipped' WHERE id = 42"), Facts{ProvenAvailable: true})
	if d.Outcome != policy.RequireApproval || d.RuleID != RuleWriteNeedsPerson || d.Code != policy.CodeApprovalRequired || d.Source != policy.SourceBuiltin {
		t.Fatalf("decision %+v", d)
	}
	if a := d.Approval; a == nil || a.Required != 1 || a.ExpiresIn != 900 || !slices.Equal(a.Approvers, []string{"role:admin"}) {
		t.Fatalf("approval %+v", d.Approval)
	}
	if d.Verify != policy.L2 || det.Strongest != policy.L2 || !slices.Contains(det.Flags, FlagApprovalAdded) || !slices.Contains(det.Flags, FlagProvenRequired) {
		t.Fatalf("verify %s detail %+v", d.Verify, det)
	}
	if d.Message != `Waiting for approval under policy "lockdown": `+ReasonWrite {
		t.Fatalf("message %q", d.Message)
	}
}

// Rule: a high-risk write needs two different people (M02).
func TestFourEyes(t *testing.T) {
	cases := map[string]policy.Input{
		"sql delete":    pgWrite("DELETE FROM public.orders WHERE id = 42"),
		"function call": in("db.call", "postgres", "write", "db", map[string]any{"function": "refund"}, nil),
		"undo":          in("db.undo", "postgres", "write", "db", nil, nil),
		"destructive":   in("files.remove", "rest", "destructive", "files", nil, nil),
		"stripe":        in("pay.create_refund", "stripe", "write", "pay", nil, nil),
		"fhir delete": func() policy.Input {
			i := in("ehr.delete", "fhir", "write", "ehr", nil, nil)
			i.Request.FHIR.Interaction = "delete"
			return i
		}(),
		"http delete": func() policy.Input {
			i := in("api.remove", "rest", "write", "api", nil, nil)
			i.Request.HTTP.Method = "DELETE"
			return i
		}(),
		"undo session":  in("db.undo_session", "postgres", "write", "db", nil, nil),
		"policy 1 of 1": pgWrite("DELETE FROM public.orders WHERE id = 7"),
	}
	for name, i := range cases {
		f := Facts{PHIMode: true}
		d, det := Apply(on, allow, i, f)
		if d.Outcome != policy.RequireApproval || d.Approval == nil || d.Approval.Required != 2 || d.Reason != ReasonFour || !slices.Contains(det.Flags, FlagFourEyes) {
			t.Errorf("%s: %+v %+v", name, d, det)
		}
	}
	// A policy approval for one person is raised to two, keeping its approvers.
	req := policy.Decision{Outcome: policy.RequireApproval, RuleID: "deletes", Approval: &policy.ApprovalSpec{Approvers: []string{"user:a@x.test", "user:b@x.test"}, Required: 1, ExpiresIn: 600}}
	d, det := Apply(on, req, pgWrite("DELETE FROM public.orders WHERE id = 7"), Facts{})
	if d.RuleID != "deletes" || d.Approval.Required != 2 || d.Approval.ExpiresIn != 600 || len(d.Approval.Approvers) != 2 || !slices.Contains(det.Flags, FlagFourEyes) {
		t.Fatalf("%+v %+v", d.Approval, det)
	}
	if req.Approval.Required != 1 {
		t.Fatal("Apply changed the caller's approval spec")
	}
	// Ordinary writes stay at one person; a policy's two stay two.
	two := policy.Decision{Outcome: policy.RequireApproval, Approval: &policy.ApprovalSpec{Approvers: []string{"role:admin"}, Required: 2, ExpiresIn: 60}}
	if d, _ := Apply(on, two, pgWrite("UPDATE public.t SET a = 1 WHERE id = 1"), Facts{}); d.Approval.Required != 2 {
		t.Fatalf("two lowered: %+v", d.Approval)
	}
}

// Rule: approvals expire within 15 minutes (M09).
func TestExpiryCapped(t *testing.T) {
	for _, exp := range []int64{0, 901, 86400} {
		req := policy.Decision{Outcome: policy.RequireApproval, Approval: &policy.ApprovalSpec{Approvers: []string{"role:admin"}, Required: 1, ExpiresIn: exp}}
		d, det := Apply(on, req, pgWrite("UPDATE public.t SET a = 1 WHERE id = 1"), Facts{})
		if d.Approval.ExpiresIn != int64(ApprovalExpiry/time.Second) || !slices.Contains(det.Flags, FlagExpiryShortened) {
			t.Errorf("%d: %+v %+v", exp, d.Approval, det)
		}
	}
	req := policy.Decision{Outcome: policy.RequireApproval, Approval: &policy.ApprovalSpec{Approvers: []string{"role:admin"}, Required: 1, ExpiresIn: 300}}
	if d, det := Apply(on, req, pgWrite("UPDATE public.t SET a = 1 WHERE id = 1"), Facts{}); d.Approval.ExpiresIn != 300 || slices.Contains(det.Flags, FlagExpiryShortened) {
		t.Fatalf("a short expiry changed: %+v", d.Approval)
	}
	// A require_approval without a spec gets one.
	if d, _ := Apply(on, policy.Decision{Outcome: policy.RequireApproval}, pgWrite("UPDATE public.t SET a = 1 WHERE id = 1"), Facts{}); d.Approval == nil || d.Approval.ExpiresIn != 900 {
		t.Fatalf("%+v", d.Approval)
	}
}

// Rule: the strongest level the connector reaches (M09).
func TestVerifyLevels(t *testing.T) {
	w := in("orders.update", "rest", "write", "orders", nil, nil)
	for _, c := range []struct {
		f      Facts
		policy policy.Level
		want   policy.Level
		flags  []string
	}{
		{Facts{ProvenAvailable: true}, policy.L0, policy.L2, []string{FlagProvenRequired, FlagVerifyRaised}},
		{Facts{ToolLevels: []string{"L0", "L1"}}, policy.L0, policy.L1, []string{FlagProvenUnavailable, FlagVerifyRaised}},
		{Facts{ToolLevels: []string{"L0"}}, policy.L0, policy.L0, []string{FlagProvenUnavailable, FlagClaimedOnly}},
		{Facts{ToolLevels: []string{"L0", "L1"}}, policy.L2, policy.L2, []string{FlagProvenUnavailable}}, // never lowered
	} {
		a := allow
		a.Verify = c.policy
		d, det := Apply(on, a, w, c.f)
		if d.Verify != c.want {
			t.Errorf("%+v: verify %s, want %s", c.f, d.Verify, c.want)
		}
		for _, fl := range c.flags {
			if !slices.Contains(det.Flags, fl) {
				t.Errorf("%+v: flags %v lack %s", c.f, det.Flags, fl)
			}
		}
	}
}

// Rules: the SQL refusals (M03, M04, M05), even under a policy that allows everything.
func TestSQLRefusals(t *testing.T) {
	for q, rule := range map[string]string{
		"DROP TABLE public.orders":                      RuleNoDropTruncate,
		"TRUNCATE public.orders":                        RuleNoDropTruncate,
		"ALTER TABLE public.orders ADD COLUMN x int":    RuleNoSchemaChange,
		"CREATE TABLE public.x (id int)":                RuleNoSchemaChange,
		"GRANT ALL ON public.orders TO PUBLIC":          RuleNoSchemaChange,
		"UPDATE public.orders SET status = 'x'":         RuleNoUnboundedWrite,
		"DELETE FROM public.orders":                     RuleNoUnboundedWrite,
		"SELECT 1; DELETE FROM public.orders":           RuleUnclearSQL,
		"SELECT pg_catalog.pg_read_file('/etc/passwd')": RuleNoServerCode,
	} {
		for _, tool := range []string{"db.execute", "db.query"} {
			d, _ := Apply(on, allow, in(tool, "postgres", "write", "db", sql(q), nil), Facts{})
			if d.Outcome != policy.Deny || d.Code != policy.CodePolicyDenied || d.RuleID != rule || d.Source != policy.SourceBuiltin || d.Message == "" {
				t.Errorf("%s via %s: %s %s, want deny %s", q, tool, d.Outcome, d.RuleID, rule)
			}
		}
	}
	// A bounded write and a read are not refused.
	if d, _ := Apply(on, allow, pgWrite("UPDATE public.orders SET status = 'x' WHERE id = 1"), Facts{}); d.Outcome == policy.Deny {
		t.Fatalf("bounded write refused: %+v", d)
	}
	if d, _ := Apply(on, allow, in("db.query", "postgres", "read", "db", sql("SELECT * FROM public.orders WHERE id = 1"), nil), Facts{}); d.Outcome != policy.Allow {
		t.Fatalf("read changed: %+v", d)
	}
}

// The SQL rules use the policy's facts when it derived them, and their own analysis when not.
func TestSQLFactsFromPolicy(t *testing.T) {
	a := allow
	a.Facts.SQL = policy.SQLFacts{Present: true, Kind: "drop", Category: "ddl"}
	if d, _ := Apply(on, a, in("db.execute", "postgres", "write", "db", sql("SELECT 1"), nil), Facts{}); d.RuleID != RuleNoDropTruncate {
		t.Fatalf("%+v", d)
	}
	// Another connector type's "sql" argument isn't analysed (the person approving sees it).
	if f := SQLFacts(allow, in("x.run", "mcp_http", "unknown", "x", sql("DROP TABLE t"), nil)); f.Present {
		t.Fatal("analysed a non-postgres tool")
	}
}

// Rule: a Can't check result holds the agent's writes (M06); reads still run.
func TestCantCheckHold(t *testing.T) {
	f := Facts{HeldBy: "act_01k6w4a3d5f7h9k1n3q5s7t9v1"}
	d, det := Apply(on, allow, pgWrite("UPDATE public.t SET a = 1 WHERE id = 1"), f)
	if d.Outcome != policy.Deny || d.RuleID != RuleCantCheckHold || det.HeldBy != f.HeldBy {
		t.Fatalf("%+v %+v", d, det)
	}
	if d, _ := Apply(on, allow, in("db.query", "postgres", "read", "db", sql("SELECT 1"), nil), f); d.Outcome != policy.Allow {
		t.Fatalf("read held: %+v", d)
	}
	f.Person = true // a person's own undo isn't held
	if d, _ := Apply(on, allow, in("db.undo", "postgres", "write", "db", nil, map[string]string{}), f); d.Outcome != policy.RequireApproval {
		t.Fatalf("person held: %+v", d)
	}
}

// Rule: one connector per agent (M07).
func TestOneConnector(t *testing.T) {
	other := in("crm.update", "rest", "write", "crm", nil, map[string]string{LabelConnector: "db"})
	if d, _ := Apply(on, allow, other, Facts{}); d.RuleID != RuleOneConnector {
		t.Fatalf("other connector: %+v", d)
	}
	none := in("db.query", "postgres", "read", "db", sql("SELECT 1"), map[string]string{"env": "prod"})
	if d, _ := Apply(on, allow, none, Facts{}); d.RuleID != RuleOneConnector {
		t.Fatalf("no label: %+v", d)
	}
	same := in("db.query", "postgres", "read", "db", sql("SELECT 1"), map[string]string{LabelConnector: "DB"})
	if d, _ := Apply(on, allow, same, Facts{}); d.Outcome != policy.Allow {
		t.Fatalf("same connector: %+v", d)
	}
}

// Rule: FHIR needs PHI mode (M08).
func TestPHIMode(t *testing.T) {
	read := in("ehr.read", "fhir", "read", "ehr", nil, nil)
	if d, _ := Apply(on, allow, read, Facts{}); d.RuleID != RulePHIMode {
		t.Fatalf("%+v", d)
	}
	if d, _ := Apply(on, allow, read, Facts{PHIMode: true}); d.Outcome != policy.Allow {
		t.Fatalf("%+v", d)
	}
}

// Unknown side effects (MCP tools) count as writes.
func TestUnknownIsWrite(t *testing.T) {
	for _, side := range []string{"unknown", "", "write"} {
		if d, _ := Apply(on, allow, in("fs.write_file", "mcp_http", side, "fs", nil, nil), Facts{}); d.Outcome != policy.RequireApproval {
			t.Errorf("%q: %+v", side, d)
		}
	}
}

// Apply never loosens: across every outcome and fact combination, a deny stays a deny, required
// never drops, expiry never grows past the policy's, verify never drops.
func TestNeverLoosens(t *testing.T) {
	inputs := []policy.Input{pgWrite("UPDATE public.t SET a = 1 WHERE id = 1"), pgWrite("DELETE FROM public.t WHERE id = 1"),
		in("db.query", "postgres", "read", "db", sql("SELECT 1"), nil), in("x.y", "rest", "write", "x", nil, nil),
		in("ehr.update", "fhir", "write", "ehr", nil, nil), in("db.execute", "postgres", "write", "db", sql("DROP TABLE t"), nil)}
	decisions := []policy.Decision{allow, {Outcome: policy.Deny, RuleID: "d"},
		{Outcome: policy.RequireApproval, Verify: policy.L1, Approval: &policy.ApprovalSpec{Approvers: []string{"role:owner"}, Required: 3, ExpiresIn: 120}}}
	for _, st := range []State{{Config: true}, {Org: true}} {
		for _, i := range inputs {
			for _, d0 := range decisions {
				for _, f := range []Facts{{}, {ProvenAvailable: true, PHIMode: true}, {HeldBy: "act_x"}, {Person: true, ToolLevels: []string{"L0", "L1"}}} {
					d, det := Apply(st, d0, i, f)
					if det == nil {
						t.Fatal("no detail while on")
					}
					if d0.Outcome == policy.Deny && d.Outcome != policy.Deny {
						t.Errorf("deny loosened")
					}
					if d.Outcome == policy.Allow && IsWrite(i, SQLFacts(d0, i)) {
						t.Errorf("a write allowed without a person: %+v", i.Tool)
					}
					if d0.Outcome == policy.RequireApproval && d.Outcome == policy.RequireApproval {
						if d.Approval.Required < d0.Approval.Required || d.Approval.ExpiresIn > d0.Approval.ExpiresIn || rank(d.Verify) < rank(d0.Verify) {
							t.Errorf("approval loosened: %+v → %+v", d0.Approval, d.Approval)
						}
						if !slices.Equal(d.Approval.Approvers, d0.Approval.Approvers) {
							t.Errorf("approvers changed")
						}
					}
				}
			}
		}
	}
}

func TestDetailJSON(t *testing.T) {
	_, det := Apply(on, allow, pgWrite("UPDATE public.t SET a = 1 WHERE id = 1"), Facts{ProvenAvailable: true})
	var back map[string]any
	if err := json.Unmarshal(det.JSON(), &back); err != nil || back["source"] != "org" || back["strongest_level"] != "L2" {
		t.Fatalf("%s %v", det.JSON(), err)
	}
}
