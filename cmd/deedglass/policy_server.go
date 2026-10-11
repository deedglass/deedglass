package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// The policy commands that talk to a server (S12 §5): push, activate and evaluate --server. They
// use an org admin key (--api-key or DEEDGLASS_API_KEY) at the configured server, like the agent
// commands (S08). Every message ends with a next step.

func apiPath(parts ...string) string {
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/v1/policies/" + strings.Join(parts, "/")
}

// --- policy push -----------------------------------------------------------------------------------

func runPolicyPush(ctx context.Context, e *Env, args []string) error {
	var notes string
	var jsonOut bool
	paths, err := parseInterleaved("deedglass policy push", args, func(fs *flag.FlagSet) {
		fs.StringVar(&notes, "notes", notes, "what changed and why (up to 2,000 characters)")
		fs.BoolVar(&jsonOut, "json", jsonOut, "print JSON")
	})
	if err != nil {
		return err
	}
	jsonOut = jsonOut || e.Globals.JSON
	if len(paths) != 1 {
		return usageErr(errors.New("deedglass policy push takes one policy file"),
			`For example: deedglass policy push examples/policies/postgres-safe.yaml --notes "Orders DB, first cut"`)
	}
	file := paths[0]
	src, err := readPolicy(e, policyInput{path: file, display: file})
	if err != nil {
		return err
	}
	// Lint first, locally: the server runs the same compiler, and this shows the source excerpt.
	lr := policy.Lint(src, policy.Options{File: file})
	c := lr.Compiled()
	if c == nil {
		if jsonOut {
			_, _ = e.Stdout.Write(lr.JSON())
		} else {
			var buf bytes.Buffer
			policy.RenderText(&buf, lr, src, file, e.Style)
			_, _ = e.Stdout.Write(buf.Bytes())
		}
		return &ExitError{Code: ExitFailure, ErrCode: "policy_invalid", Err: errors.New("the policy was not accepted, so nothing was pushed"), printed: true}
	}
	cl, err := apiClient(e)
	if err != nil {
		return err
	}
	body := map[string]string{"source": string(src)}
	if notes != "" {
		body["notes"] = notes
	}
	var existing policy.Policy
	_, err = cl.Do(ctx, "GET", apiPath(c.Name), nil, &existing)
	var de *deedglass.Error
	var v policy.Version
	created := true
	switch {
	case errors.As(err, &de) && de.Status == http.StatusNotFound:
		var res struct {
			policy.Policy
			Version policy.Version `json:"version"`
		}
		if _, err := cl.Do(ctx, "POST", "/v1/policies", body, &res); err != nil {
			return apiErr(e, cl, err)
		}
		v = res.Version
	case err != nil:
		return apiErr(e, cl, err)
	default:
		resp, err := cl.Do(ctx, "POST", apiPath(existing.ID, "versions"), body, &v)
		if err != nil {
			return apiErr(e, cl, err)
		}
		created = resp.Status == http.StatusCreated
	}
	if jsonOut {
		return writeJSON(e, map[string]any{"object": "policy_push", "created": created, "version": v})
	}
	if !created {
		fmt.Fprintln(e.Stdout, e.Style.UnknownLine(fmt.Sprintf("%s v%d already has this content %s nothing saved", v.Name, v.Version, ui.Sep)))
		fmt.Fprintf(e.Stdout, "  Next: deedglass policy activate %s --version %d --mode shadow --reason \"…\"\n", v.Name, v.Version)
		return nil
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%s v%d saved %s %s %s %s %s %s %s %s", v.Name, v.Version, ui.Sep, v.ID, ui.Sep,
		policy.ShortHash(v.VersionHash), ui.Sep, pluralN(v.Rules, "rule", "rules"), ui.Sep, pluralN(v.Budgets, "budget", "budgets"))))
	for _, w := range v.Warnings {
		fmt.Fprintf(e.Stdout, "  %s %s\n", e.Style.Amber("○"), policy.PlainText(w.Message))
	}
	fmt.Fprintf(e.Stdout, "  Next: deedglass policy test %s\n", file)
	return nil
}

// --- policy activate ---------------------------------------------------------------------------

type activateOpts struct {
	version          string
	mode, reason     string
	yes, clear, json bool
	ack              kvFlag
}

func runPolicyActivate(ctx context.Context, e *Env, args []string) error {
	var o activateOpts
	pos, err := parseInterleaved("deedglass policy activate", args, func(fs *flag.FlagSet) {
		fs.StringVar(&o.version, "version", o.version, "the version number or pv_ id (default: the latest)")
		fs.StringVar(&o.mode, "mode", o.mode, "enforce or shadow")
		fs.StringVar(&o.reason, "reason", o.reason, "why this change is safe; it goes into the record")
		fs.BoolVar(&o.yes, "yes", o.yes, "skip the typed confirmation for enforce (scripts)")
		fs.BoolVar(&o.clear, "clear", o.clear, "switch off this version's shadow instead")
		fs.Var(&o.ack, "ack", "acknowledge a warning, like default_allow (repeatable)")
		fs.BoolVar(&o.json, "json", o.json, "print JSON")
	})
	if err != nil {
		return err
	}
	jsonOut := o.json || e.Globals.JSON
	example := `deedglass policy activate postgres-safe --version 1 --mode shadow --reason "Watch it for a day before enforcing"`
	if len(pos) != 1 {
		return usageErr(errors.New("deedglass policy activate takes one policy name or pol_ id"), example)
	}
	name := pos[0]
	switch {
	case o.clear && o.mode != "":
		return usageErr(errors.New("--clear switches the shadow off; leave out --mode"), `deedglass policy activate postgres-safe --version 2 --clear --reason "…"`)
	case !o.clear && o.mode != "enforce" && o.mode != "shadow":
		return usageErr(errors.New("--mode must be enforce or shadow"), example)
	case strings.TrimSpace(o.reason) == "":
		return usageErr(errors.New(`a reason is required; it goes into the deed`), example)
	}
	enforce := o.mode == "enforce"
	if enforce && !o.yes && (!e.IsTTY || jsonOut) {
		return usageErr(errors.New("enforcing a policy needs a typed confirmation, and this isn't an interactive terminal"),
			"Run it in a terminal, or add --yes in scripts.")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var p policy.Policy
	if _, err := c.Do(ctx, "GET", apiPath(name), nil, &p); err != nil {
		return apiErr(e, c, err)
	}
	version := o.version
	if version == "" {
		version = strconv.Itoa(p.LatestVersion)
	}
	if o.clear {
		var a policy.Activation
		if _, err := c.Do(ctx, "POST", apiPath(p.ID, "versions", version, "deactivate"), map[string]string{"reason": o.reason}, &a); err != nil {
			return apiErr(e, c, err)
		}
		if jsonOut {
			return writeJSON(e, a)
		}
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%s v%d is no longer the shadow policy %s %s sealed", a.Name, a.Version.Version, ui.Sep, a.Deeds[0])))
		return nil
	}
	if enforce && !o.yes {
		var set policy.ActiveSet
		if _, err := c.Do(ctx, "GET", "/v1/policies/active", nil, &set); err != nil {
			return apiErr(e, c, err)
		}
		w, err := whoami(ctx, e, c)
		if err != nil {
			return err
		}
		replacing := ""
		if set.Enforce != nil {
			replacing = fmt.Sprintf(" (replacing %s v%d)", set.Enforce.Name, set.Enforce.Version)
		}
		fmt.Fprintf(e.Stdout, "  From now on, every agent action in %s is decided by %s v%s%s.\n", w.Org.Slug, p.Name, version, replacing)
		fmt.Fprintln(e.Stdout, "  The change is sealed as a record.")
		fmt.Fprint(e.Stdout, "  Type the policy name to confirm: ")
		line, _ := bufio.NewReader(e.stdin()).ReadString('\n')
		if strings.TrimSpace(line) != p.Name {
			return &ExitError{Code: ExitFailure, ErrCode: "not_confirmed", Err: errors.New("Nothing changed: the confirmation didn't match"),
				Next: "Run it again and type " + p.Name + ", or press Ctrl-C to leave the enforced policy as it is."}
		}
	}
	body := map[string]any{"mode": o.mode, "reason": o.reason}
	if len(o.ack) > 0 {
		body["acknowledge_warnings"] = []string(o.ack)
	}
	var a policy.Activation
	if _, err := c.Do(ctx, "POST", apiPath(p.ID, "versions", version, "activate"), body, &a); err != nil {
		return apiErr(e, c, err)
	}
	if jsonOut {
		return writeJSON(e, a)
	}
	last := a.Deeds[len(a.Deeds)-1]
	if !enforce {
		fmt.Fprintln(e.Stdout, e.Style.Violet("◐")+" "+fmt.Sprintf("%s v%d is now the shadow policy %s %s sealed", a.Name, a.Version.Version, ui.Sep, last))
		var set policy.ActiveSet
		if _, err := c.Do(ctx, "GET", "/v1/policies/active", nil, &set); err == nil && set.Enforce != nil {
			fmt.Fprintf(e.Stdout, "  It decides every action next to %s v%d, and only records what it would have done.\n", set.Enforce.Name, set.Enforce.Version)
		}
		return nil
	}
	line := fmt.Sprintf("%s v%d enforced %s %s sealed", a.Name, a.Version.Version, ui.Sep, last)
	if a.PromotedFromShadow {
		line += " " + ui.Sep + " shadow cleared"
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(line))
	return nil
}

// --- policy evaluate --server -------------------------------------------------------------------

// runPolicyEvaluateServer sends the same flags as a case input to POST /v1/policies/evaluate: the
// active policies by default, a stored version with --policy <name|pol_…> --version N, or a local
// file's source with --policy <file>.
func runPolicyEvaluateServer(ctx context.Context, e *Env, o policyEvalOpts, version string) error {
	if o.input != "" {
		return usageErr(errors.New("--input is for local evaluation"), "On the server, describe the action with --tool, --sql, --args, --agent and --usage.")
	}
	if o.tool == "" {
		return usageErr(errors.New("deedglass policy evaluate --server needs --tool <name>"), `For example: deedglass policy evaluate --server --tool postgres.query --sql "SELECT 1"`)
	}
	in := map[string]any{"tool": o.tool}
	if o.args != "" {
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(o.args))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil || m == nil {
			return usageErr(errors.New("--args must be a JSON object"), `For example: --args '{"amount": 2000}'`)
		}
		in["args"] = m
	}
	if o.sqlSet {
		in["sql"] = o.sql
	}
	agent := map[string]any{}
	switch {
	case strings.HasPrefix(o.agent, "agt_"):
		agent["id"] = o.agent
	case o.agent != "":
		agent["name"] = o.agent
	}
	if len(o.labels) > 0 {
		labels := map[string]any{}
		for _, l := range o.labels {
			k, v, ok := strings.Cut(l, "=")
			if !ok || k == "" {
				return usageErr(fmt.Errorf("--label %q is not k=v", clean(l)), "For example: --label env=prod")
			}
			labels[k] = v
		}
		agent["labels"] = labels
	}
	if len(agent) > 0 {
		in["agent"] = agent
	}
	if o.user != "" {
		in["user"] = map[string]any{"ref": o.user}
	}
	if o.tz != "" {
		in["org"] = map[string]any{"timezone": o.tz}
	}
	if o.at != "" {
		in["time"] = o.at
	}
	if len(o.usage) > 0 {
		u := map[string]any{}
		for _, kv := range o.usage {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return usageErr(fmt.Errorf("--usage %q is not k=v", clean(kv)), "For example: --usage calls_minute=60")
			}
			if k == "currency" {
				u[k] = v
				continue
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return usageErr(fmt.Errorf("--usage %s=%s is not a whole number", clean(k), clean(v)), "Counters are whole numbers; money is in minor units.")
			}
			u[k] = n
		}
		in["usage"] = u
	}
	body := map[string]any{"input": in, "explain": true}
	if o.policy != "" {
		if fi, err := os.Stat(o.policy); err == nil && !fi.IsDir() {
			src, err := readPolicy(e, policyInput{path: o.policy, display: o.policy})
			if err != nil {
				return err
			}
			body["policy"] = map[string]string{"source": string(src)}
		} else {
			n, err := strconv.Atoi(version)
			if err != nil || n < 1 {
				return usageErr(errors.New("--policy names a stored policy here, so it needs --version <n>"), "Or leave --policy out to evaluate the active policies.")
			}
			body["policy"] = map[string]any{"id": o.policy, "version": n}
		}
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var res policy.DryRunResult
	if _, err := c.Do(ctx, "POST", "/v1/policies/evaluate", body, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON || o.json {
		return writeJSON(e, res)
	}
	d := decisionOf(res)
	name := res.Policy.Name
	if res.Policy.Version != nil {
		name = fmt.Sprintf("%s v%d", name, *res.Policy.Version)
	}
	printDecision(e, &policy.Compiled{Name: name, VersionHash: res.Policy.VersionHash}, d, o.explain)
	if res.Shadow != nil && o.explain {
		fmt.Fprintf(e.Stdout, "  shadow  %s %s %s\n", res.Shadow.Policy.Name, ui.Sep, res.Shadow.Decision)
	}
	return nil
}

// decisionOf turns the API's policy_decision back into the evaluator's Decision for printing.
func decisionOf(r policy.DryRunResult) policy.Decision {
	d := policy.Decision{Outcome: r.Decision, Source: r.Source, Message: r.Message, Approval: r.Approval, Notify: r.Notify,
		Budget: r.Budget, Facts: r.Facts, Trace: r.Trace, Errors: r.Errors, SQLGuard: r.SQLGuard,
		Elapsed: time.Duration(r.EvaluatedInUS) * time.Microsecond}
	if r.Code != nil {
		d.Code = policy.Code(*r.Code)
	}
	if r.RuleID != nil {
		d.RuleID = *r.RuleID
	}
	if r.RuleLine != nil {
		d.RuleLine = *r.RuleLine
	}
	if r.Reason != nil {
		d.Reason = *r.Reason
	}
	if r.Verify != nil {
		d.Verify = *r.Verify
	}
	return d
}
