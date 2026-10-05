package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The dry run (POST /v1/policies/evaluate) and the server-side test runner (POST
// /v1/policies/test). Neither reserves usage nor writes anything: the dry run reads usage as zero
// unless the input gives it, exactly like the CLI.

// DryRunRequest is POST /v1/policies/evaluate.
type DryRunRequest struct {
	Policy  json.RawMessage // "active" (the default) | {"id": "pol_…", "version": 3} | {"source": "<yaml>"}
	Input   any             // the case-input value (§3.8), decoded from JSON
	Explain bool
}

// DryRunPolicyRef names the policy that decided.
type DryRunPolicyRef struct {
	ID          *string `json:"id"`
	Name        string  `json:"name"`
	Version     *int    `json:"version"`
	VersionID   *string `json:"version_id"`
	VersionHash string  `json:"version_hash"`
	Mode        Mode    `json:"mode"`
}

// DryRunResult is the policy_decision object.
type DryRunResult struct {
	Object        string          `json:"object"` // "policy_decision"
	Decision      Outcome         `json:"decision"`
	Code          *string         `json:"code"`
	Source        string          `json:"source"`
	RuleID        *string         `json:"rule_id"`
	RuleLine      *int            `json:"rule_line"`
	Reason        *string         `json:"reason"`
	Message       string          `json:"message"`
	Approval      *ApprovalSpec   `json:"approval"`
	Verify        *Level          `json:"verify"`
	Notify        []string        `json:"notify"`
	Budget        *BudgetHit      `json:"budget"`
	Policy        DryRunPolicyRef `json:"policy"`
	Shadow        *DryRunResult   `json:"shadow"`
	Facts         DerivedFacts    `json:"facts"`
	Trace         []Step          `json:"trace"`
	Errors        []string        `json:"errors"`
	SQLGuard      string          `json:"sqlguard_version"`
	EvaluatedInUS int64           `json:"evaluated_in_us"`
}

type dryPolicy struct {
	Active  bool
	ID      string
	Version int
	Source  string
}

func parseDryPolicy(raw json.RawMessage) (dryPolicy, *Error) {
	if len(raw) == 0 || string(raw) == "null" {
		return dryPolicy{Active: true}, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "active" {
			return dryPolicy{Active: true}, nil
		}
		return dryPolicy{}, errf(KindValidation, "policy", msgDryRunPolicy)
	}
	var o struct {
		ID      *string `json:"id"`
		Version *int    `json:"version"`
		Source  *string `json:"source"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return dryPolicy{}, errf(KindValidation, "policy", msgDryRunPolicy)
	}
	switch {
	case o.Source != nil && o.ID == nil && o.Version == nil:
		return dryPolicy{Source: *o.Source}, nil
	case o.ID != nil && o.Source == nil:
		v := 0
		if o.Version != nil {
			v = *o.Version
		}
		if v < 1 {
			return dryPolicy{}, errf(KindValidation, "policy.version", "policy.version is required with policy.id: a version number like 3.")
		}
		return dryPolicy{ID: *o.ID, Version: v}, nil
	}
	return dryPolicy{}, errf(KindValidation, "policy", msgDryRunPolicy)
}

// Evaluate is the dry run: it evaluates one case input against the active policies, a stored
// version or a source, with the trace. It never reserves usage.
func (s *Service) Evaluate(ctx context.Context, caller Caller, req DryRunRequest) (*DryRunResult, error) {
	dp, e := parseDryPolicy(req.Policy)
	if e != nil {
		return nil, e
	}
	in, err := s.dryInput(ctx, caller, req.Input)
	if err != nil {
		return nil, err
	}
	var enforce, shadow *Loaded
	switch {
	case dp.Active:
		r := s.registry
		if r == nil {
			r = NewRegistry(s.repo, nil)
		}
		a, err := r.Active(ctx, caller.Org)
		if err != nil {
			return nil, err
		}
		enforce, shadow = a.Enforce, a.Shadow
	case dp.Source != "":
		c, list := Compile([]byte(dp.Source), Options{})
		if c == nil {
			e := invalidPolicy(list)
			e.Param = "policy.source"
			return nil, e
		}
		enforce = &Loaded{Compiled: c, Name: c.Name, VersionHash: c.VersionHash, Mode: ModeEnforce}
	default:
		l, err := s.loadVersion(ctx, caller, dp.ID, VersionRef{Number: dp.Version})
		if err != nil {
			return nil, err
		}
		enforce = l
	}
	in.Usage = FillLocalUsage(enforce.Policy(), in.Usage)
	opts := EvalOptions{Explain: true, Deadline: time.Second}
	var d Decision
	var sd *Decision
	if shadow != nil {
		d, sd = EvaluatePair(ctx, enforce.Policy(), shadow.Policy(), in, opts)
	} else {
		d = Evaluate(ctx, enforce.Policy(), in, opts)
	}
	if enforce.Unavailable != nil && len(d.Errors) == 0 {
		d.Errors = []string{"the enforced policy failed its load checks"}
	}
	out := dryResult(d, enforce, req.Explain)
	if sd != nil {
		out.Shadow = dryResult(*sd, shadow, req.Explain)
	}
	return out, nil
}

// loadVersion compiles a stored version for a dry run or a test.
func (s *Service) loadVersion(ctx context.Context, caller Caller, policyRef string, ref VersionRef) (*Loaded, error) {
	var l *Loaded
	err := s.repo.InOrgReadTx(ctx, caller.Org, func(tx Tx) error {
		p, err := getPolicy(ctx, tx, policyRef)
		if err != nil {
			return err
		}
		v, err := getVersion(ctx, tx, p, ref)
		if err != nil {
			return err
		}
		c, _ := Compile(v.Source, Options{})
		if c == nil || c.VersionHash != v.VersionHash { //deedglass:allow-compare a policy version's public digest
			return errf(KindConflict, "", msgVersionTampered, p.Name, v.Version)
		}
		l = &Loaded{Compiled: c, PolicyID: p.ID, Name: p.Name, VersionID: v.ID, VersionHash: v.VersionHash, Version: v.Version, Mode: ModeEnforce}
		return nil
	})
	return l, err
}

// dryInput builds the input: the case-input format, the org's real facts, a real agent when
// agent.id names one, and the current time when the input names none.
func (s *Service) dryInput(ctx context.Context, caller Caller, v any) (Input, error) {
	v = NormalizeJSONValue(v)
	in, p := BuildInput(v)
	if p != nil {
		param := "input"
		if p.Path != "" {
			param = "input." + p.Path
		}
		msg := p.Message
		if p.Hint != "" {
			msg += " " + p.Hint
		}
		return in, &Error{Kind: KindValidation, Param: param, Message: msg}
	}
	m, _ := v.(map[string]any)
	if _, set := m["time"]; !set {
		in.Time = s.now().UTC().Truncate(time.Millisecond)
	}
	var orgSet map[string]any
	if o, ok := m["org"].(map[string]any); ok {
		orgSet = o
	}
	err := s.repo.InOrgReadTx(ctx, caller.Org, func(tx Tx) error {
		o, err := tx.LoadOrg(ctx)
		if err != nil {
			return err
		}
		in.Org.ID = o.ID
		if _, set := orgSet["timezone"]; !set {
			in.Org.Timezone = o.Timezone
		}
		if _, set := orgSet["stopped"]; !set {
			in.Org.Stopped = o.Stopped
		}
		if in.Agent.ID == LocalAgentID {
			return nil
		}
		a, err := tx.LoadAgent(ctx, in.Agent.ID)
		if errors.Is(err, ErrNotFound) {
			return &Error{Kind: KindNotFound, Param: "input.agent.id", Message: fmt.Sprintf(msgAgentNotFound, in.Agent.ID)}
		}
		if err != nil {
			return err
		}
		in.Agent = *a
		return nil
	})
	return in, err
}

func dryResult(d Decision, l *Loaded, explain bool) *DryRunResult {
	out := &DryRunResult{Object: "policy_decision", Decision: d.Outcome, Source: d.Source, Message: d.Message, Approval: d.Approval,
		Notify: d.Notify, Budget: d.Budget, Facts: d.Facts, Errors: d.Errors, SQLGuard: d.SQLGuard, EvaluatedInUS: d.Elapsed.Microseconds()}
	if d.Code != CodeNone {
		c := string(d.Code)
		out.Code = &c
	}
	if d.RuleID != "" {
		r := d.RuleID
		out.RuleID = &r
	}
	if d.RuleLine > 0 {
		n := d.RuleLine
		out.RuleLine = &n
	}
	if d.Reason != "" {
		r := d.Reason
		out.Reason = &r
	}
	if d.Verify != "" {
		v := d.Verify
		out.Verify = &v
	}
	if out.Notify == nil {
		out.Notify = []string{}
	}
	if out.Errors == nil {
		out.Errors = []string{}
	}
	if explain {
		out.Trace = d.Trace
	}
	if out.Trace == nil {
		out.Trace = []Step{}
	}
	if out.EvaluatedInUS < 1 {
		out.EvaluatedInUS = 1
	}
	ref := DryRunPolicyRef{Name: l.Name, VersionHash: l.VersionHash, Mode: l.Mode}
	if l.Unavailable != nil {
		ref.Name = "unavailable"
	}
	if l.PolicyID != "" {
		id, vid, n := l.PolicyID, l.VersionID, l.Version
		ref.ID, ref.VersionID, ref.Version = &id, &vid, &n
	}
	out.Policy = ref
	return out
}

// TestRequest is POST /v1/policies/test.
type TestRequest struct {
	Source    string     // a policy source, or
	PolicyRef string     // a stored policy (with Version)
	Version   VersionRef //
	Cases     any        // a cases file as a YAML string, or a list of cases (or an object with cases) as JSON
}

// Test runs cases against a source or a stored version.
func (s *Service) Test(ctx context.Context, caller Caller, req TestRequest) (*TestResult, error) {
	var c *Compiled
	switch {
	case req.Source != "" && req.PolicyRef == "":
		var list []Diagnostic
		if c, list = Compile([]byte(req.Source), Options{}); c == nil {
			return nil, invalidPolicy(list)
		}
	case req.PolicyRef != "" && req.Source == "":
		l, err := s.loadVersion(ctx, caller, req.PolicyRef, req.Version)
		if err != nil {
			return nil, err
		}
		c = l.Compiled
	default:
		return nil, errf(KindValidation, "source", "Send source (the policy YAML), or policy and version, but not both.")
	}
	var cf *CasesFile
	var list []Diagnostic
	switch x := req.Cases.(type) {
	case string:
		cf, list = ParseCasesYAML([]byte(x), "cases")
	case nil:
		return nil, errf(KindValidation, "cases", "cases is required: a cases file as a string, or a list of cases.")
	default:
		cf, list = ParseCasesValue(x, "cases")
	}
	if cf == nil {
		e := &Error{Kind: KindValidation, Param: "cases", Message: "The cases were not accepted."}
		for _, d := range list {
			if d.Severity == SevError {
				where := ""
				if d.Line > 0 {
					where = fmt.Sprintf(" (line %d)", d.Line)
				}
				if d.Path != "" {
					e.Param = d.Path
				}
				e.Message = strings.ReplaceAll(d.Message, "`", "") + where
				if d.Hint != "" {
					e.Message += " " + strings.ReplaceAll(d.Hint, "`", "")
				}
				break
			}
		}
		return nil, e
	}
	return RunCases(ctx, c, cf, TestOptions{}), nil
}
