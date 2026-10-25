package attack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/lockdown"
	"github.com/deedglass/deedglass/internal/policy"
)

// Group M, lockdown mode (ADR-117). M01–M09, M11–M14 run here as library-level cases with plain go
// test, like groups Y and T: the overlay the Gate calls (lockdown.Apply), the config loader, the
// org PATCH decoder and the documented limits. M10 needs the database: TestLockdownLive in
// cmd/deedglass (integration) runs it, and the end-to-end path of M01, M02 and M06 through a real
// serve. Expected outcomes are in attack_meta.json, declared before the Gate applied lockdown.

var mOn = lockdown.State{Org: true}

// mAllowAll is a policy that allows everything: lockdown has to hold without the policy's help.
var mAllowAll = policy.Decision{Outcome: policy.Allow, Source: policy.SourceRule, RuleID: "allow-everything", Verify: policy.L0}

func mInput(tool, ctype, side string, args map[string]any, labels map[string]string) policy.Input {
	b, _ := json.Marshal(args)
	conn, op, _ := strings.Cut(tool, ".")
	if labels == nil {
		labels = map[string]string{lockdown.LabelConnector: conn}
	}
	return policy.Input{V: policy.InputVersion, Args: b,
		Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{ID: "agt_m", Name: "ward-bot", Labels: labels, Status: policy.StatusActive}},
		Tool:  policy.ToolInput{ToolFacts: policy.ToolFacts{Name: tool, Connector: conn, ConnectorType: ctype, SideEffect: side, Op: op}}}
}

func mSQL(q string) policy.Input {
	return mInput("acme.execute", "postgres", "write", map[string]any{"sql": q}, nil)
}

func mDenied(t *testing.T, d policy.Decision, rule string) {
	t.Helper()
	if d.Outcome != policy.Deny || d.Code != policy.CodePolicyDenied || d.RuleID != rule {
		t.Fatalf("got %s %s %s, want DENIED by %s", d.Outcome, d.Code, d.RuleID, rule)
	}
}

func TestM01WriteAllowedByPolicyWaits(t *testing.T) {
	d, det := lockdown.Apply(mOn, mAllowAll, mSQL("UPDATE public.orders SET status = 'shipped' WHERE id = 42"), lockdown.Facts{ProvenAvailable: true})
	if d.Outcome != policy.RequireApproval || d.RuleID != lockdown.RuleWriteNeedsPerson || d.Approval == nil || d.Approval.ExpiresIn > 900 {
		t.Fatalf("BLOCKED expected: %+v %+v", d, det)
	}
}

func TestM02HighRiskNeedsTwoPeople(t *testing.T) {
	one := policy.Decision{Outcome: policy.RequireApproval, RuleID: "deletes", Approval: &policy.ApprovalSpec{Approvers: []string{"role:admin"}, Required: 1, ExpiresIn: 600}}
	d, _ := lockdown.Apply(mOn, one, mSQL("DELETE FROM public.orders WHERE id = 42"), lockdown.Facts{})
	if d.Outcome != policy.RequireApproval || d.Approval.Required != 2 {
		t.Fatalf("four-eyes expected: %+v", d.Approval)
	}
	// One person voting twice is already_countersigned: the approvals decision table (S22,
	// TestDecisionTable) refuses it for any required ≥ 2; here the rule asks for 2.
}

func TestM03DropTruncate(t *testing.T) {
	for _, q := range []string{"DROP TABLE public.orders", "TRUNCATE public.orders", "drop table if exists public.orders cascade"} {
		d, _ := lockdown.Apply(mOn, mAllowAll, mSQL(q), lockdown.Facts{})
		mDenied(t, d, lockdown.RuleNoDropTruncate)
	}
}

func TestM04SchemaChange(t *testing.T) {
	for _, q := range []string{"ALTER TABLE public.orders DROP COLUMN total", "ALTER ROLE deedglass_agent SUPERUSER", "CREATE INDEX ON public.orders (id)", "REVOKE ALL ON public.orders FROM PUBLIC"} {
		d, _ := lockdown.Apply(mOn, mAllowAll, mSQL(q), lockdown.Facts{})
		if d.Outcome != policy.Deny || (d.RuleID != lockdown.RuleNoSchemaChange && d.RuleID != lockdown.RuleNoDropTruncate) {
			t.Fatalf("%s: %s %s", q, d.Outcome, d.RuleID)
		}
	}
}

func TestM05UnboundedWrite(t *testing.T) {
	for _, q := range []string{"UPDATE public.orders SET status = 'void'", "DELETE FROM public.orders"} {
		d, _ := lockdown.Apply(mOn, mAllowAll, mSQL(q), lockdown.Facts{})
		mDenied(t, d, lockdown.RuleNoUnboundedWrite)
	}
}

func TestM06HoldCoversEverySession(t *testing.T) {
	// The Gate reads the hold per agent (store.LockdownHeld takes no session): the Facts carry no
	// session at all, so a new Deedglass-Session header changes nothing. TestLockdownLive sends the
	// header for real.
	f := lockdown.Facts{HeldBy: "act_01k6w4a3d5f7h9k1n3q5s7t9v1"}
	for _, session := range []string{"", "ward-7-morning", "a-brand-new-session"} {
		in := mSQL("UPDATE public.orders SET status = 'x' WHERE id = 1")
		in.Session = policy.SessionFacts{Ref: session}
		d, _ := lockdown.Apply(mOn, mAllowAll, in, f)
		mDenied(t, d, lockdown.RuleCantCheckHold)
		if !strings.Contains(d.Message, f.HeldBy) {
			t.Fatalf("the refusal doesn't name the held action: %s", d.Message)
		}
	}
	read := mInput("acme.query", "postgres", "read", map[string]any{"sql": "SELECT 1"}, nil)
	if d, _ := lockdown.Apply(mOn, mAllowAll, read, f); d.Outcome != policy.Allow {
		t.Fatalf("a read was held: %+v", d)
	}
}

func TestM07OneConnector(t *testing.T) {
	d, _ := lockdown.Apply(mOn, mAllowAll, mInput("billing.update", "rest", "write", nil, map[string]string{lockdown.LabelConnector: "acme"}), lockdown.Facts{})
	mDenied(t, d, lockdown.RuleOneConnector)
	d, _ = lockdown.Apply(mOn, mAllowAll, mInput("acme.query", "postgres", "read", map[string]any{"sql": "SELECT 1"}, map[string]string{}), lockdown.Facts{})
	mDenied(t, d, lockdown.RuleOneConnector)
}

func TestM08FHIRNeedsPHIMode(t *testing.T) {
	d, _ := lockdown.Apply(mOn, mAllowAll, mInput("ehr.read", "fhir", "read", nil, nil), lockdown.Facts{PHIMode: false})
	mDenied(t, d, lockdown.RulePHIMode)
}

func TestM09LongApprovalAndLowVerify(t *testing.T) {
	long := policy.Decision{Outcome: policy.RequireApproval, Verify: policy.L0, Approval: &policy.ApprovalSpec{Approvers: []string{"role:admin"}, Required: 1, ExpiresIn: 86400}}
	d, det := lockdown.Apply(mOn, long, mSQL("UPDATE public.orders SET status = 'x' WHERE id = 1"), lockdown.Facts{ProvenAvailable: true})
	if d.Approval.ExpiresIn != 900 || d.Verify != policy.L2 || !hasAll(det.Flags, lockdown.FlagExpiryShortened, lockdown.FlagVerifyRaised) {
		t.Fatalf("%+v %+v", d, det)
	}
}

func hasAll(have []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			found = found || h == w
		}
		if !found {
			return false
		}
	}
	return true
}

func TestM11OffPinnedByConfig(t *testing.T) {
	var s identity.Service // the pinned refusal comes before any database access
	org, _ := ids.Parse[ids.OrgKind]("org_01k6w4a3d5f7h9k1n3q5s7t9v1")
	_, err := s.LockdownOff(context.Background(), org, ids.UserID{}, "priya@acme.test", true, true, "", time.Now())
	var le *identity.LockdownError
	if !errors.As(err, &le) || le.Code != identity.CodeLockdownPinned {
		t.Fatalf("REJECTED lockdown_pinned_by_config expected: %v", err)
	}
}

func TestM12ProdConfigNeedsDualSealAndTimestamps(t *testing.T) {
	base := map[string]string{"DEEDGLASS_ENV": "prod", "DEEDGLASS_BASE_URL": "https://deedglass.example.com",
		"DEEDGLASS_VAULT_KEY": "q83vEjRWeJq83vEjRWeJq83vEjRWeJq83vEjRWeJq80=", "DEEDGLASS_API_KEY_PEPPER": "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4v",
		"DEEDGLASS_LOCKDOWN": "true"}
	_, _, err := config.Load(config.LoadOptions{Env: base, FS: fstest.MapFS{}})
	if err == nil || !strings.Contains(err.Error(), "DEEDGLASS_SEAL_PQ_KEY_FILE") {
		t.Fatalf("no ML-DSA-65 key: %v", err)
	}
	base["DEEDGLASS_SEAL_PQ_KEY_FILE"] = "/keys/pq.json"
	_, _, err = config.Load(config.LoadOptions{Env: base, FS: fstest.MapFS{"deedglass.yaml": {Data: []byte("timestamps:\n  at_export: false\n")}}})
	if err == nil || !strings.Contains(err.Error(), "timestamps.at_export") {
		t.Fatalf("at_export false: %v", err)
	}
	if _, _, err = config.Load(config.LoadOptions{Env: base, FS: fstest.MapFS{}}); err != nil {
		t.Fatalf("a complete lockdown config refused: %v", err)
	}
}

func TestM13NoAPIRouteTurnsLockdownOff(t *testing.T) {
	for _, body := range []string{`{"settings":{"lockdown":false}}`, `{"settings":{"lockdown_off":null}}`} {
		var in identity.OrgPatch
		err := httpx.DecodeJSON(httptest.NewRequest(http.MethodPatch, "/v1/org", strings.NewReader(body)), &in)
		var e *httpx.Error
		if !errors.As(err, &e) || e.Code != httpx.CodeValidationFailed || !strings.HasPrefix(e.Param, "settings.lockdown") {
			t.Fatalf("%s: %#v", body, err)
		}
	}
}

func TestM14OneShellUserIsOutsideScope(t *testing.T) {
	// OUTSIDE SCOPE: the limit is written down where users read it.
	for _, f := range []string{"../docs/LOCKDOWN.md", "../docs/LIMITATIONS.md"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "can't tell who is typing") {
			t.Errorf("%s doesn't state that the server CLI can't tell who is typing", f)
		}
	}
}
