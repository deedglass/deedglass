package main

//deedglass:allow-exec dev agent-script starts this same binary for allow-listed subcommands (argv, never a shell) and make for three allow-listed demo targets

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
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/agentscript"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// deedglass dev agent-script (S37 §3.3): the scripted demo. A YAML file of scenes runs through the
// real Gate as the demo's agent, with allow-listed make targets for the demo database, deedglass
// subcommands as argv, approvals and a one-byte change under .tmp/demo/. Every scene asserts the
// product's real output; the first unmet expectation stops the run with exit 1.

type agentScriptOpts struct {
	step, live bool
	only       string
	keysFile   string
	root       string
	vars       multiFlag
}

var agentScriptValues agentScriptOpts

func agentScriptFlags(fs *flag.FlagSet) {
	o := &agentScriptValues
	*o = agentScriptOpts{}
	fs.BoolVar(&o.step, "step", false, "wait for Enter between scenes and print the narration")
	fs.BoolVar(&o.live, "live", false, "run the live scenes too (Slack, Stripe test mode, a person approving)")
	fs.StringVar(&o.only, "only", "", "run one scene by its id")
	fs.StringVar(&o.keysFile, "keys-file", "examples/demo/.demo-keys", "the name=key file dev seed --demo wrote")
	fs.StringVar(&o.root, "root", ".", "the directory .tmp/demo/ is in")
	fs.Var(&o.vars, "var", "a value a scene reads as ${name}, as name=value (repeatable)")
}

func runDevAgentScript(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass dev agent-script", agentScriptFlags, args)
	if err != nil {
		return err
	}
	file, err := oneArg("deedglass dev agent-script", "a script file", "deedglass dev agent-script examples/demo/launch.yaml", pos)
	if err != nil {
		return err
	}
	o := agentScriptValues
	b, err := os.ReadFile(file)
	if err != nil {
		return failNext(e, ExitUsage, "script_unreadable", fmt.Sprintf("%s couldn't be read: %v", file, errors.Unwrap(err)), "Check the path, for example examples/demo/launch.yaml.")
	}
	s, err := agentscript.Parse(filepath.Base(file), b)
	if err != nil {
		return failNext(e, ExitUsage, "script_invalid", "The script was refused: "+err.Error(), "Fix that step, then run it again. docs/DEMO_SCRIPT.md lists every step kind.")
	}
	keys, err := readKeysFile(o.keysFile)
	if err != nil {
		return failNext(e, ExitUsage, "keys_missing", err.Error(), "Run make demo-up, which writes examples/demo/.demo-keys.")
	}
	for _, need := range []string{s.Agent, "admin"} {
		if keys[need] == "" {
			return failNext(e, ExitUsage, "keys_missing", fmt.Sprintf("%s has no %s= line.", o.keysFile, need), "Run make demo-up (or make demo-reset HARD=1) to write the demo's keys again.")
		}
	}
	root, err := filepath.Abs(o.root)
	if err != nil {
		return usageErr(err, "Pass --root as a directory.")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	server := firstNonEmpty(e.Globals.Server, e.Getenv("DEEDGLASS_SERVER"), e.Getenv("DEEDGLASS_BASE_URL"), defaultServer)
	be := &scriptBackend{e: e, server: server, keys: keys, agent: s.Agent, root: root, self: self}
	opts := agentscript.Options{Live: o.live, Step: o.step, Only: o.only, Root: root, Out: e.Stdout, In: e.stdin(), Style: e.Style}
	if e.Globals.JSON {
		opts.Out = nil
		opts.Step = false
	}
	vars := map[string]string{}
	for _, v := range o.vars {
		k, val, ok := strings.Cut(v, "=")
		if !ok || k == "" {
			return usageErr(fmt.Errorf("--var %q must be name=value", v), "For example --var stripe_charge=ch_….")
		}
		vars[k] = val
	}
	sum, runErr := agentscript.RunWith(ctx, s, be, opts, vars)
	if e.Globals.JSON && sum != nil {
		if err := writeAPIJSON(e, sum); err != nil {
			return err
		}
	}
	if runErr != nil {
		var se *agentscript.StepError
		if errors.As(runErr, &se) {
			return &ExitError{Code: ExitFailure, ErrCode: "script_expectation_failed", Err: runErr, printed: !e.Globals.JSON}
		}
		return &ExitError{Code: ExitFailure, ErrCode: "script_failed", Err: runErr}
	}
	return nil
}

// readKeysFile reads name=key lines (comments and blanks skipped).
func readKeysFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("the demo's keys file %s couldn't be read", path)
	}
	defer f.Close()
	keys := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			keys[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return keys, sc.Err()
}

type scriptBackend struct {
	e      *Env
	server string
	keys   map[string]string
	agent  string
	root   string
	self   string
}

func (b *scriptBackend) client(as string) *deedglass.Client {
	k := b.keys[b.agent]
	if as == "admin" {
		k = b.keys["admin"]
	}
	return &deedglass.Client{BaseURL: b.server, APIKey: k, UserAgent: "deedglass-agent-script/" + buildInfo().Version}
}

func (b *scriptBackend) Call(ctx context.Context, as, tool string, args json.RawMessage) (agentscript.Result, error) {
	c := b.client(as)
	body := actBody{Tool: tool, Args: args, Mode: "gateway", Wait: gate.MaxWait.Seconds(),
		Context: map[string]any{"client": map[string]string{"name": "deedglass-agent-script", "version": buildInfo().Version}, "transport": "cli"}}
	var a ledger.Action
	_, err := c.Do(ctx, http.MethodPost, "/v1/actions", body, &a)
	var de *deedglass.Error
	refusal := ""
	if errors.As(err, &de) && len(de.Action) > 0 {
		if jerr := json.Unmarshal(de.Action, &a); jerr != nil {
			return agentscript.Result{}, err
		}
		refusal = de.Code
		if a.Decision != nil && a.Decision.Message == nil && de.Message != "" {
			m := de.Message
			a.Decision.Message = &m
		}
	} else if err != nil {
		return agentscript.Result{}, err
	}
	if a.Status == ledger.StatusProcessing {
		if err := b.follow(ctx, c, &a); err != nil {
			return agentscript.Result{}, err
		}
	}
	r := b.result(ctx, c, &a)
	if refusal != "" {
		r.Error = refusal
	}
	return r, nil
}

// follow waits (up to two minutes) for an action to leave processing or pending approval.
func (b *scriptBackend) follow(ctx context.Context, c *deedglass.Client, a *ledger.Action) error {
	deadline := time.Now().Add(2 * time.Minute)
	for (a.Status == ledger.StatusProcessing || a.Status == ledger.StatusPendingApproval) && time.Now().Before(deadline) {
		var d ledger.Action
		if _, err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/v1/actions/%s?wait=%d", url.PathEscape(a.ID), int(gate.MaxWait.Seconds())), nil, &d); err != nil {
			return err
		}
		d.Execution = keepResult(a.Execution, d.Execution)
		*a = d
	}
	return nil
}

func (b *scriptBackend) result(ctx context.Context, c *deedglass.Client, a *ledger.Action) agentscript.Result {
	raw, _ := json.Marshal(a)
	r := agentscript.Result{ID: a.ID, Tool: a.Tool, Status: a.Status, Raw: string(raw)}
	if d := a.Decision; d != nil {
		r.Decision = d.Outcome
		if d.RuleID != nil {
			r.Rule = *d.RuleID
		}
		if d.Message != nil {
			r.Message = clean(*d.Message)
		}
	}
	if a.Status == ledger.StatusPendingApproval {
		r.Message = clean(gate.PendingMessage(&gate.Action{Action: *a}, time.Local))
	}
	if a.Approval != nil {
		r.Approval = a.Approval.ID
	}
	if x := a.Execution; x != nil && x.ErrorCode != nil {
		r.Error = *x.ErrorCode
	}
	if w := a.Witness; w != nil {
		r.Level, r.Verdict = w.Level, w.Verdict
		if w.Summary != nil {
			r.Summary = clean(*w.Summary)
		}
		if w.Verdict == gate.VerdictMismatch && w.Level == "L2" {
			if detail := actDetail(ctx, c, a.ID); detail != nil {
				pe := *b.e
				pe.Width = 120
				for _, l := range l2Lines(&pe, detail, "", false) {
					t := strings.TrimSpace(l)
					if strings.HasPrefix(stripANSI(t), "expected") || strings.HasPrefix(stripANSI(t), "observed") {
						r.Diff = append(r.Diff, t)
					}
				}
			}
		}
	}
	if a.Deed != nil {
		r.Deed = *a.Deed
		r.DeedSeq = deedSeq(ctx, c, *a.Deed)
	}
	return r
}

func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

// Run starts this binary with argv (never a shell). Paths under .tmp/demo/ resolve against --root;
// the child keeps this process's environment and working directory, plus the key for as.
func (b *scriptBackend) Run(ctx context.Context, as string, argv []string) (agentscript.RunOutput, error) {
	args := []string{"--no-color"}
	for _, a := range argv {
		if strings.HasPrefix(a, agentscript.DemoDir+"/") {
			a = filepath.Join(b.root, filepath.FromSlash(a))
		}
		args = append(args, a)
	}
	key := b.keys[b.agent]
	if as == "admin" {
		key = b.keys["admin"]
	}
	cmd := exec.CommandContext(ctx, b.self, args...)
	cmd.Env = append(scrubEnv(os.Environ()), "DEEDGLASS_API_KEY="+key, "DEEDGLASS_SERVER="+b.server, "NO_COLOR=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return agentscript.RunOutput{Text: out.String(), Exit: ee.ExitCode()}, nil
	}
	if err != nil {
		return agentscript.RunOutput{}, err
	}
	return agentscript.RunOutput{Text: out.String()}, nil
}

func scrubEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "DEEDGLASS_API_KEY=") || strings.HasPrefix(kv, "DEEDGLASS_SERVER=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Make runs one of the three allow-listed demo targets in --root.
func (b *scriptBackend) Make(ctx context.Context, target string) error {
	cmd := exec.CommandContext(ctx, "make", "-s", target)
	cmd.Dir = b.root
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *scriptBackend) Countersign(ctx context.Context, approval, as, reason string) error {
	argv := []string{"approve", approval, "--as", as}
	if reason != "" {
		argv = append(argv, "--reason", reason)
	}
	out, err := b.Run(ctx, "admin", argv)
	if err != nil {
		return err
	}
	if out.Exit != 0 {
		return errors.New(strings.TrimSpace(out.Text))
	}
	return nil
}

// WaitDecided polls the approval until a person decides it.
func (b *scriptBackend) WaitDecided(ctx context.Context, approval string, timeout time.Duration) (string, error) {
	c := b.client("admin")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var ap map[string]any
		if _, err := c.Do(ctx, http.MethodGet, "/v1/approvals/"+url.PathEscape(approval), nil, &ap); err != nil {
			return "", err
		}
		switch ap["status"] {
		case "approved":
			if who := firstEmail(ap["votes"]); who != "" {
				return who, nil
			}
			return "a person", nil
		case "denied", "expired", "cancelled":
			return "", fmt.Errorf("the approval was %v", ap["status"])
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return "", errors.New("two minutes passed")
}

func firstEmail(v any) string {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x["email"].(string); ok && strings.Contains(s, "@") {
			return s
		}
		for _, y := range x {
			if s := firstEmail(y); s != "" {
				return s
			}
		}
	case []any:
		for _, y := range x {
			if s := firstEmail(y); s != "" {
				return s
			}
		}
	}
	return ""
}

func (b *scriptBackend) Await(ctx context.Context, id string) (agentscript.Result, error) {
	c := b.client("admin")
	a := ledger.Action{ID: id, Status: ledger.StatusProcessing}
	if err := b.follow(ctx, c, &a); err != nil {
		return agentscript.Result{}, err
	}
	return b.result(ctx, c, &a), nil
}
