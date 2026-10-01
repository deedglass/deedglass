package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/policy"
)

// deedglass policy evaluate (S11 §4 step 12): one action against a local policy file, with the
// decision, the message the agent would see and, with --explain, the trace. Exit 0 when an
// evaluation ran (whatever the decision), 1 when the policy doesn't compile, 2 on a usage error.

// knownConnectorTypes are inferred from a tool's prefix (docs/POLICY_LANGUAGE.md §4.4).
var knownConnectorTypes = map[string]bool{"postgres": true, "rest": true, "mcp": true, "mcp_http": true, "sdk": true,
	"github": true, "slack": true, "stripe": true, "gmail": true, "notion": true, "fake": true}

// cliAgentID is the agent id local evaluation uses when --agent names no id.
const cliAgentID = "agt_00000000000000000000000000"

type kvFlag []string

func (k *kvFlag) String() string     { return strings.Join(*k, ",") }
func (k *kvFlag) Set(v string) error { *k = append(*k, v); return nil }

type policyEvalOpts struct {
	policy, input, tool, args, sql, agent, user, at, tz string
	sqlSet                                              bool
	labels, usage                                       kvFlag
	explain, json                                       bool
}

func runPolicyEvaluate(ctx context.Context, e *Env, args []string) error {
	var o policyEvalOpts
	var version string
	rest, err := parseInterleaved("deedglass policy evaluate", args, func(fs *flag.FlagSet) {
		fs.StringVar(&version, "version", version, "with --server and a stored --policy: the version number")
		fs.StringVar(&o.policy, "policy", o.policy, "policy file")
		fs.StringVar(&o.input, "input", o.input, "input JSON file")
		fs.StringVar(&o.tool, "tool", o.tool, "tool name")
		fs.StringVar(&o.args, "args", o.args, "arguments as a JSON object")
		fs.Func("sql", "sets args.sql", func(s string) error { o.sql, o.sqlSet = s, true; return nil })
		fs.StringVar(&o.agent, "agent", o.agent, "agent name or agt_ id")
		fs.StringVar(&o.user, "user", o.user, "on-behalf-of user ref")
		fs.StringVar(&o.at, "time", o.at, "decision time, RFC 3339")
		fs.StringVar(&o.tz, "tz", o.tz, "org time zone (IANA)")
		fs.Var(&o.labels, "label", "agent label k=v (repeatable)")
		fs.Var(&o.usage, "usage", "usage counter k=v (repeatable)")
		fs.BoolVar(&o.explain, "explain", o.explain, "show the trace")
		fs.BoolVar(&o.json, "json", o.json, "print JSON")
	})
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usageErr(fmt.Errorf("unexpected argument %q", clean(rest[0])), "Pass the policy with --policy, for example: deedglass policy evaluate --policy postgres-safe.yaml --tool postgres.query --sql \"SELECT 1\"")
	}
	if e.Globals.ServerFlag {
		return runPolicyEvaluateServer(ctx, e, o, version)
	}
	if o.policy == "" {
		return usageErr(errors.New("deedglass policy evaluate needs --policy <file>, or --server for the active policies"),
			"For example: deedglass policy evaluate --policy postgres-safe.yaml --tool postgres.query --sql \"SELECT 1\"")
	}
	in, err := buildEvalInput(o)
	if err != nil {
		return err
	}
	src, err := readPolicy(e, policyInput{path: o.policy, display: o.policy})
	if err != nil {
		return err
	}
	file := o.policy
	if o.policy == "-" {
		file = ""
	}
	r := policy.Lint(src, policy.Options{File: file})
	c := r.Compiled()
	if c == nil {
		if e.Globals.JSON || o.json {
			enc := json.NewEncoder(e.Stdout)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			_ = enc.Encode(r)
		} else {
			var buf bytes.Buffer
			policy.RenderText(&buf, r, src, o.policy, e.Style)
			_, _ = e.Stdout.Write(buf.Bytes())
		}
		return &ExitError{Code: ExitFailure, ErrCode: "policy_invalid", Err: errors.New("the policy was not accepted"), printed: true}
	}
	in.Usage = policy.FillLocalUsage(c, in.Usage)
	// Local evaluation is interactive, so the backstop is 1 s rather than the gate's 50 ms: a busy
	// laptop shouldn't turn a demo into policy_unavailable. The cost limit is the same.
	d := policy.Evaluate(ctx, c, in, policy.EvalOptions{Explain: o.explain || e.Globals.JSON || o.json, Deadline: time.Second})
	if e.Globals.JSON || o.json {
		return printDecisionJSON(e.Stdout, c, d)
	}
	printDecision(e, c, d, o.explain)
	return nil
}

// buildEvalInput reads --input (when given) and applies the flags over it.
func buildEvalInput(o policyEvalOpts) (policy.Input, error) {
	var in policy.Input
	if o.input != "" {
		b, err := os.ReadFile(o.input)
		if err != nil {
			return in, usageErr(fmt.Errorf("can't read %s: %s", o.input, plainErr(err)), "Check the path and try again.")
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return in, usageErr(fmt.Errorf("%s is not a policy input: %s", o.input, clean(err.Error())), "See docs/POLICY_LANGUAGE.md §6.2 for the input format.")
		}
	}
	in.V = policy.InputVersion
	if o.tool != "" {
		in.Tool.Name = o.tool
		prefix, _, _ := strings.Cut(o.tool, ".")
		in.Tool.Connector = prefix
		if knownConnectorTypes[prefix] {
			in.Tool.ConnectorType = prefix
		}
	}
	if in.Tool.Name == "" {
		return in, usageErr(errors.New("deedglass policy evaluate needs --tool <name> (or an --input file with one)"),
			"For example: --tool postgres.query")
	}
	if in.Tool.SideEffect == "" {
		in.Tool.SideEffect = "unknown"
	}
	if o.args != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(o.args), &m); err != nil || m == nil {
			return in, usageErr(errors.New("--args must be a JSON object"), `For example: --args '{"amount": 2000}'`)
		}
		in.Args = json.RawMessage(o.args)
	}
	if o.sqlSet {
		var m map[string]json.RawMessage
		if len(in.Args) > 0 {
			if err := json.Unmarshal(in.Args, &m); err != nil {
				return in, usageErr(errors.New("the arguments must be a JSON object"), `For example: --args '{"amount": 2000}'`)
			}
		}
		if m == nil {
			m = map[string]json.RawMessage{}
		}
		q, _ := json.Marshal(o.sql)
		m["sql"] = q
		b, _ := json.Marshal(m)
		in.Args = b
	}
	switch {
	case strings.HasPrefix(o.agent, "agt_"):
		in.Agent.ID = o.agent
	case o.agent != "":
		in.Agent.Name = o.agent
	}
	if in.Agent.ID == "" {
		in.Agent.ID = cliAgentID
	}
	if in.Agent.Name == "" {
		in.Agent.Name = "agent"
	}
	if in.Agent.Status == "" {
		in.Agent.Status = policy.StatusActive
	}
	for _, l := range o.labels {
		k, v, ok := strings.Cut(l, "=")
		if !ok || k == "" {
			return in, usageErr(fmt.Errorf("--label %q is not k=v", clean(l)), "For example: --label env=prod")
		}
		if in.Agent.Labels == nil {
			in.Agent.Labels = map[string]string{}
		}
		in.Agent.Labels[k] = v
	}
	if o.user != "" {
		in.User.Ref = o.user
	}
	if o.tz != "" {
		in.Org.Timezone = o.tz
	}
	if o.at != "" {
		t, err := time.Parse(time.RFC3339, o.at)
		if err != nil {
			return in, usageErr(fmt.Errorf("--time %q is not RFC 3339", clean(o.at)), "For example: --time 2026-10-04T10:00:00+05:30")
		}
		in.Time = t.UTC()
	}
	if in.Time.IsZero() {
		in.Time = time.Now().UTC().Truncate(time.Second)
	}
	if in.Request.Mode == "" {
		in.Request.Mode = "gateway"
	}
	in.Request.Bytes = int64(len(in.Args))
	for _, kv := range o.usage {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return in, usageErr(fmt.Errorf("--usage %q is not k=v", clean(kv)), "For example: --usage calls_minute=60 or --usage refunds-per-day=12000")
		}
		if k == "currency" {
			in.Usage.Currency = v
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return in, usageErr(fmt.Errorf("--usage %s=%s is not a whole number", clean(k), clean(v)), "Counters are whole numbers; money is in minor units (12000 is $120.00).")
		}
		switch k {
		case "calls_minute":
			in.Usage.CallsMinute = n
		case "calls_hour":
			in.Usage.CallsHour = n
		case "calls_today":
			in.Usage.CallsToday = n
		case "spend_today":
			in.Usage.SpendToday = n
		default:
			if in.Usage.Budgets == nil {
				in.Usage.Budgets = map[string]int64{}
			}
			in.Usage.Budgets[k] = n
		}
	}
	return in, nil
}

// decisionJSON is the --json shape: the decision, plus the policy and the evaluation time.
type decisionJSON struct {
	Object string `json:"object"`
	policy.Decision
	Policy        policyRef `json:"policy"`
	EvaluatedInUS int64     `json:"evaluated_in_us"`
}

type policyRef struct {
	Name        string `json:"name"`
	VersionHash string `json:"version_hash"`
}

func printDecisionJSON(w io.Writer, c *policy.Compiled, d policy.Decision) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(decisionJSON{Object: "policy_decision", Decision: d, Policy: policyRef{Name: c.Name, VersionHash: c.VersionHash},
		EvaluatedInUS: d.Elapsed.Microseconds()})
}

func micros(d time.Duration) string {
	us := d.Microseconds()
	if us < 1 {
		us = 1
	}
	return thousandsInt(us) + " µs"
}

func thousandsInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func printDecision(e *Env, c *policy.Compiled, d policy.Decision, explain bool) {
	w := e.Stdout
	s := e.Style
	var head []string
	switch d.Outcome {
	case policy.Deny:
		head = append(head, s.Red("✕")+" deny")
	case policy.RequireApproval:
		head = append(head, s.Violet("◐")+" require_approval")
	default:
		head = append(head, "✓ allow") // neutral on purpose: teal is for VERIFIED (brand §6.3)
	}
	switch d.Source {
	case policy.SourceRule:
		head = append(head, d.RuleID)
	case policy.SourceBudget:
		head = append(head, "budget "+strings.TrimPrefix(d.RuleID, "budget:"))
	case policy.SourceBuiltin:
		head = append(head, d.RuleID)
	default:
		head = append(head, d.Source)
	}
	if d.Outcome == policy.Allow {
		head = append(head, "verify "+string(d.Verify))
	}
	head = append(head, micros(d.Elapsed))
	fmt.Fprintln(w, strings.Join(head, " · "))
	if d.Message != "" {
		fmt.Fprintln(w, "  "+d.Message)
	}
	if d.Outcome == policy.RequireApproval && d.Approval != nil {
		parts := []string{"approvers " + strings.Join(d.Approval.Approvers, ", "), fmt.Sprintf("%d needed", d.Approval.Required),
			"expires in " + durationWords(d.Approval.ExpiresIn)}
		if len(d.Notify) > 0 {
			parts = append(parts, "notify "+strings.Join(d.Notify, ", "))
		}
		parts = append(parts, "verify "+string(d.Verify))
		fmt.Fprintln(w, "  "+strings.Join(parts, " · "))
	}
	if !explain {
		return
	}
	fmt.Fprintln(w)
	label := func(l string) string { return "  " + s.Dim(fmt.Sprintf("%-7s", l)) + " " }
	if f := d.Facts.SQL; f.Present {
		parts := []string{f.Kind, f.Category, pluralN(int(f.Statements), "statement", "statements")}
		if len(f.Tables) > 0 {
			parts = append(parts, strings.Join(f.Tables, ", "))
		}
		if f.Ambiguous {
			parts = append(parts, "ambiguous: "+strings.Join(f.Reasons, ", "))
		}
		fmt.Fprintln(w, label("sql")+strings.Join(parts, " · "))
	}
	var budgets []string
	var rules []policy.Step
	for _, st := range d.Trace {
		switch st.Step {
		case policy.StepStop:
			mark := "✓"
			if st.Result != policy.ResultPass {
				mark = s.Red("✕")
			}
			fmt.Fprintln(w, label("stop")+mark+" "+st.Detail)
		case policy.StepBudget:
			if st.Result == policy.ResultNoMatch {
				continue
			}
			mark := "✓"
			switch st.Result {
			case policy.ResultExceeded, policy.ResultError:
				mark = s.Red("✕")
			}
			budgets = append(budgets, st.ID+" "+mark+" "+strings.Replace(st.Detail, " calls ", " ", 1))
		case policy.StepRule, policy.StepDefault:
			rules = append(rules, st)
		}
	}
	if len(budgets) > 0 {
		fmt.Fprintln(w, label("budget")+strings.Join(budgets, " · "))
	}
	width := 0
	for _, st := range rules {
		width = max(width, len(st.ID))
	}
	for i, st := range rules {
		l := label("")
		if i == 0 {
			l = label("rules")
		}
		var line string
		switch {
		case st.Result == policy.ResultNotEvaluated:
			line = "… " + st.Detail
		case st.Step == policy.StepDefault:
			line = fmt.Sprintf("%-*s   %s", width, "default", st.Detail)
		default:
			result := strings.ReplaceAll(st.Result, "_", " ")
			line = fmt.Sprintf("%-*s   %-8s   %s", width, st.ID, result, st.Detail)
			if st.Result == policy.ResultMatch || st.Result == policy.ResultError {
				line += fmt.Sprintf("      line %d", st.Line)
			}
		}
		fmt.Fprintln(w, l+strings.TrimRight(line, " "))
	}
	for i, er := range d.Errors {
		l := label("")
		if i == 0 {
			l = label("errors")
		}
		fmt.Fprintln(w, l+s.Amber("○")+" "+er)
	}
	pol := []string{c.Name, shortHash(c.VersionHash)}
	if d.SQLGuard != "" {
		pol = append(pol, "sqlguard "+d.SQLGuard)
	}
	fmt.Fprintln(w, label("policy")+strings.Join(pol, " · "))
}

// durationWords renders approval expiry the way policies write it: 30m, 2h, 1d.
func durationWords(sec int64) string {
	switch {
	case sec%86400 == 0:
		return strconv.FormatInt(sec/86400, 10) + "d"
	case sec%3600 == 0:
		return strconv.FormatInt(sec/3600, 10) + "h"
	case sec%60 == 0:
		return strconv.FormatInt(sec/60, 10) + "m"
	}
	return strconv.FormatInt(sec, 10) + "s"
}
