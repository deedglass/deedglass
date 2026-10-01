package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// The agent commands (S08) talk to a Deedglass server over the API with an org admin key
// (--api-key or DEEDGLASS_API_KEY) at --server, DEEDGLASS_SERVER, DEEDGLASS_BASE_URL or
// http://127.0.0.1:8080. Agent keys are refused with insufficient_scope: no agent key changes any
// agent.

const defaultServer = "http://127.0.0.1:8080"

// apiClient builds the API client from the globals and the environment.
func apiClient(e *Env) (*deedglass.Client, error) {
	server := firstNonEmpty(e.Globals.Server, e.Getenv("DEEDGLASS_SERVER"), e.Getenv("DEEDGLASS_BASE_URL"), defaultServer)
	key := firstNonEmpty(e.Globals.APIKey, e.Getenv("DEEDGLASS_API_KEY"))
	if key == "" {
		return nil, &ExitError{Code: ExitNotAuthed, ErrCode: "authentication_required",
			Err:  errors.New("This command needs an admin API key"),
			Next: "Set DEEDGLASS_API_KEY or pass --api-key. Create one with deedglass org keys create on the server."}
	}
	return &deedglass.Client{BaseURL: server, APIKey: key, UserAgent: "deedglass-cli/" + buildInfo().Version}, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// apiErr turns an API or transport error into the CLI's error: exit 3 for 401, else 1. The API's
// message already ends with a next step.
func apiErr(e *Env, c *deedglass.Client, err error) error {
	var de *deedglass.Error
	if errors.As(err, &de) {
		code := ExitFailure
		if de.Status == http.StatusUnauthorized {
			code = ExitNotAuthed
		}
		msg := de.Message
		if msg == "" {
			msg = fmt.Sprintf("The server answered HTTP %d.", de.Status)
		}
		extra := map[string]any{"type": firstNonEmpty(de.Type, "api_error")}
		if de.RequestID != "" {
			extra["request_id"] = de.RequestID
		}
		return &ExitError{Code: code, ErrCode: firstNonEmpty(de.Code, "api_error"), Err: errors.New(strings.TrimSuffix(msg, ".")), Extra: extra}
	}
	if errors.Is(err, context.Canceled) {
		return &ExitError{Code: ExitFailure, ErrCode: "cancelled", Err: errors.New("Cancelled")}
	}
	return &ExitError{Code: ExitFailure, ErrCode: "server_unreachable", Err: fmt.Errorf("Can't reach the Deedglass server at %s", c.BaseURL),
		Next: "Check --server or DEEDGLASS_SERVER, and that deedglass serve is running."}
}

// mixedArgs parses flags that may come before or after positional arguments.
func mixedArgs(name string, flags func(*flag.FlagSet), args []string) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if flags != nil {
		flags(fs)
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageErr(fmt.Errorf("%s: %v", name, err), "Run deedglass help "+strings.TrimPrefix(name, "deedglass ")+".")
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func oneArg(name, what, example string, pos []string) (string, error) {
	if len(pos) != 1 {
		return "", usageErr(fmt.Errorf("%s needs %s", name, what), example)
	}
	return pos[0], nil
}

// --- shared lookups ---

// resolveAgent finds an agent by id (agt_…) or name.
func resolveAgent(ctx context.Context, e *Env, c *deedglass.Client, ref string) (identity.Agent, error) {
	if strings.HasPrefix(ref, "agt_") {
		var a identity.Agent
		if _, err := c.Do(ctx, "GET", "/v1/agents/"+url.PathEscape(ref), nil, &a); err != nil {
			return a, apiErr(e, c, err)
		}
		return a, nil
	}
	if !identity.ValidAgentName(ref) {
		return identity.Agent{}, usageErr(fmt.Errorf("%q isn't an agent name or id", ref),
			"Agent names use lowercase letters, digits and hyphens, like support-bot; ids look like agt_01k633rrmh3bcz2p39gg4vp46q.")
	}
	var l httpx.List[identity.Agent]
	if _, err := c.Do(ctx, "GET", "/v1/agents?name="+url.QueryEscape(ref), nil, &l); err != nil {
		return identity.Agent{}, apiErr(e, c, err)
	}
	if len(l.Data) == 0 {
		return identity.Agent{}, &ExitError{Code: ExitFailure, ErrCode: "not_found", Err: fmt.Errorf("There's no agent named %s in this org", ref),
			Next: "List agents with deedglass agent list."}
	}
	return l.Data[0], nil
}

func whoami(ctx context.Context, e *Env, c *deedglass.Client) (identity.Whoami, error) {
	var w identity.Whoami
	if _, err := c.Do(ctx, "GET", "/v1/whoami", nil, &w); err != nil {
		return w, apiErr(e, c, err)
	}
	return w, nil
}

// --- agent create ---

type labelFlags map[string]string

func (l labelFlags) String() string { return "" }
func (l labelFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return errors.New("labels are key=value, like env=prod")
	}
	l[k] = val
	return nil
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

var agentCreateValues struct {
	description, owner, currency string
	labels                       labelFlags
	perMinute, perDay, spend     int64
}

func agentCreateFlags(fs *flag.FlagSet) {
	v := &agentCreateValues
	*v = struct {
		description, owner, currency string
		labels                       labelFlags
		perMinute, perDay, spend     int64
	}{labels: labelFlags{}}
	fs.StringVar(&v.description, "description", "", "what the agent does, up to 500 characters")
	fs.Var(v.labels, "label", "a label key=value (repeatable): env=prod")
	fs.Int64Var(&v.perMinute, "calls-per-minute", 0, "budget: calls per minute (S11 enforces it)")
	fs.Int64Var(&v.perDay, "calls-per-day", 0, "budget: calls per day")
	fs.Int64Var(&v.spend, "spend-per-day", 0, "budget: spend per day in minor units (5000 = $50.00), with --currency")
	fs.StringVar(&v.currency, "currency", "usd", "the spend budget's ISO 4217 currency, lowercase")
	fs.StringVar(&v.owner, "owner", "", "the person responsible (usr_…)")
}

func runAgentCreate(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass agent create", agentCreateFlags, args)
	if err != nil {
		return err
	}
	name, err := oneArg("deedglass agent create", "a name", "deedglass agent create support-bot --label env=prod", pos)
	if err != nil {
		return err
	}
	v := agentCreateValues
	in := identity.CreateAgentInput{Name: name, Description: v.description, Labels: map[string]string(v.labels)}
	if v.perMinute != 0 {
		in.Budgets.CallsPerMinute = &v.perMinute
	}
	if v.perDay != 0 {
		in.Budgets.CallsPerDay = &v.perDay
	}
	if v.spend != 0 {
		in.Budgets.SpendPerDay = &httpx.Money{Amount: v.spend, Currency: v.currency}
	}
	if v.owner != "" {
		in.Owner = &v.owner
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var res identity.AgentResult
	if _, err := c.Do(ctx, "POST", "/v1/agents", in, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	s := e.Style
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Agent created %s %s %s %s", ui.Sep, res.Name, ui.Sep, res.ID)))
	if res.Deed != nil {
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Deed sealed %s agent.created %s %s", ui.Sep, ui.Sep, *res.Deed)))
	}
	fmt.Fprintf(e.Stdout, "  Next: create its key with `deedglass agent keys create %s`.\n", res.Name)
	return nil
}

// --- agent list and show ---

var agentListValues struct{ status, label string }

func agentListFlags(fs *flag.FlagSet) {
	agentListValues.status, agentListValues.label = "", ""
	fs.StringVar(&agentListValues.status, "status", "", "active, suspended or stopped")
	fs.StringVar(&agentListValues.label, "label", "", "only agents with this label, key:value (env:prod)")
}

func listAllAgents(ctx context.Context, e *Env, c *deedglass.Client, q url.Values) ([]identity.Agent, error) {
	var all []identity.Agent
	q.Set("limit", "100")
	for {
		var l httpx.List[identity.Agent]
		if _, err := c.Do(ctx, "GET", "/v1/agents?"+q.Encode(), nil, &l); err != nil {
			return nil, apiErr(e, c, err)
		}
		all = append(all, l.Data...)
		if !l.HasMore || l.NextCursor == nil {
			return all, nil
		}
		q.Set("cursor", *l.NextCursor)
	}
}

func runAgentList(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass agent list takes flags only (got %q)", args[0]), "deedglass agent list [--status stopped] [--label env:prod]")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	q := url.Values{}
	if agentListValues.status != "" {
		q.Set("status", agentListValues.status)
	}
	if agentListValues.label != "" {
		q.Set("label", agentListValues.label)
	}
	list, err := listAllAgents(ctx, e, c, q)
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, httpx.NewList(list, false, nil))
	}
	if len(list) == 0 {
		fmt.Fprintln(e.Stdout, "No agents yet. Register your first one with deedglass agent create support-bot.")
		return nil
	}
	now := time.Now()
	rows := [][]string{{"NAME", "STATUS", "KEYS", "LABELS", "LAST USED"}}
	for _, a := range list {
		rows = append(rows, []string{a.Name, string(a.Status), strconv.Itoa(a.ActiveKeys), labelText(a.Labels), ago(now, a.LastUsedAt)})
	}
	printTable(e, rows, func(row, col int, cell string) string {
		if row > 0 && col == 1 && cell == string(identity.AgentStopped) {
			return e.Style.Red(cell)
		}
		return cell
	})
	return nil
}

// printTable writes left-aligned columns separated by three spaces. paint may colour a cell after
// its width is measured.
func printTable(e *Env, rows [][]string, paint func(row, col int, cell string) string) {
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], ui.Width(c))
		}
	}
	for ri, r := range rows {
		var b strings.Builder
		for i, c := range r {
			cell := c
			if paint != nil {
				cell = paint(ri, i, c)
			}
			if i == len(r)-1 {
				b.WriteString(cell)
				break
			}
			b.WriteString(cell + strings.Repeat(" ", widths[i]-ui.Width(c)+3))
		}
		fmt.Fprintln(e.Stdout, strings.TrimRight(b.String(), " "))
	}
}

func labelText(l map[string]string) string {
	if len(l) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + l[k]
	}
	return strings.Join(parts, " ")
}

// ago is "2m ago" style relative time, or "never".
func ago(now time.Time, t *httpx.Time) string {
	if t == nil {
		return "never"
	}
	d := now.Sub(time.Time(*t))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func utcMinute(t httpx.Time) string { return time.Time(t).UTC().Format("2006-01-02 15:04") + " UTC" }

func runAgentShow(ctx context.Context, e *Env, args []string) error {
	ref, err := oneArg("deedglass agent show", "an agent name or id", "deedglass agent show support-bot", args)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	a, err := resolveAgent(ctx, e, c, ref)
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, a)
	}
	s := e.Style
	status := string(a.Status)
	if a.Status == identity.AgentStopped {
		status = s.Red(status)
	}
	fmt.Fprintf(e.Stdout, "%s %s %s %s %s\n", s.Bold(a.Name), ui.Sep, a.ID, ui.Sep, status)
	line := func(k, v string) { fmt.Fprintf(e.Stdout, "  %-12s %s\n", k, v) }
	if a.Description != "" {
		line("Description", a.Description)
	}
	line("Labels", labelText(a.Labels))
	line("Budgets", budgetText(a.Budgets))
	if a.Owner != nil {
		line("Owner", *a.Owner)
	}
	line("Keys", fmt.Sprintf("%d active %s last used %s", a.ActiveKeys, ui.Sep, ago(time.Now(), a.LastUsedAt)))
	if a.StatusChangedAt != nil {
		v := string(a.Status) + " since " + utcMinute(*a.StatusChangedAt)
		if a.StatusChangedBy != nil {
			v += " by " + *a.StatusChangedBy
		}
		if a.StatusReason != nil {
			v += fmt.Sprintf(" %s %q", ui.Sep, *a.StatusReason)
		}
		line("Status", v)
	}
	line("Created", utcMinute(a.CreatedAt))
	return nil
}

func budgetText(b identity.Budgets) string {
	var parts []string
	if b.CallsPerMinute != nil {
		parts = append(parts, fmt.Sprintf("%s calls/min", groupDigits(*b.CallsPerMinute)))
	}
	if b.CallsPerDay != nil {
		parts = append(parts, fmt.Sprintf("%s calls/day", groupDigits(*b.CallsPerDay)))
	}
	if m := b.SpendPerDay; m != nil {
		parts = append(parts, fmt.Sprintf("%s %s/day (minor units)", groupDigits(m.Amount), m.Currency))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " "+ui.Sep+" ")
}

func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

// --- suspend, resume, stop, start ---

var statusReason string

func reasonFlags(fs *flag.FlagSet) {
	statusReason = ""
	fs.StringVar(&statusReason, "reason", "", "why; it goes into the deed (required for start)")
}

func statusCmd(op identity.StatusOp) func(context.Context, *Env, []string) error {
	return func(ctx context.Context, e *Env, args []string) error {
		name := "deedglass agent " + string(op)
		pos, err := mixedArgs(name, reasonFlags, args)
		if err != nil {
			return err
		}
		ref, err := oneArg(name, "an agent name or id", name+" support-bot", pos)
		if err != nil {
			return err
		}
		if op == identity.OpStart && strings.TrimSpace(statusReason) == "" {
			return usageErr(errors.New(`Starting an agent needs a reason. Add --reason "why it's safe now"; it goes into the deed`), "")
		}
		c, err := apiClient(e)
		if err != nil {
			return err
		}
		a, err := resolveAgent(ctx, e, c, ref)
		if err != nil {
			return err
		}
		var res identity.AgentResult
		body := map[string]string{}
		if statusReason != "" {
			body["reason"] = statusReason
		}
		if _, err := c.Do(ctx, "POST", "/v1/agents/"+a.ID+"/"+string(op), body, &res); err != nil {
			return apiErr(e, c, err)
		}
		if e.Globals.JSON {
			return writeJSON(e, res)
		}
		printStatusResult(e, op, res)
		return nil
	}
}

func printStatusResult(e *Env, op identity.StatusOp, res identity.AgentResult) {
	s := e.Style
	if res.Deed == nil {
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("%s is already %s %s no new deed", res.Name, res.Status, ui.Sep)))
		return
	}
	verb := map[identity.StatusOp]string{identity.OpSuspend: "Suspended", identity.OpResume: "Resumed", identity.OpStop: "Stopped", identity.OpStart: "Started"}[op]
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("%s %s %s %s sealed", verb, res.Name, ui.Sep, *res.Deed)))
	switch op {
	case identity.OpStop:
		fmt.Fprintf(e.Stdout, "  Every call from %s is refused on every Deedglass instance within 2 seconds.\n", res.Name)
		fmt.Fprintf(e.Stdout, "  Start it again with: deedglass agent start %s --reason \"…\"\n", res.Name)
	case identity.OpSuspend:
		fmt.Fprintf(e.Stdout, "  %s's calls are refused until you resume it: deedglass agent resume %s\n", res.Name, res.Name)
	case identity.OpResume, identity.OpStart:
		fmt.Fprintf(e.Stdout, "  %s can act again.\n", res.Name)
	}
}

// --- agent keys ---

var keyCreateValues struct {
	name, mode string
	scopes     listFlag
}

func keyCreateFlags(fs *flag.FlagSet) {
	keyCreateValues.name, keyCreateValues.mode, keyCreateValues.scopes = "", "live", nil
	fs.StringVar(&keyCreateValues.name, "name", "", "a name for the key, like prod runner")
	fs.StringVar(&keyCreateValues.mode, "mode", "live", "live or test (test keys are recorded as livemode false)")
	fs.Var(&keyCreateValues.scopes, "scope", "a scope (repeatable); default actions:execute, actions:read, deeds:read")
}

func runAgentKeysCreate(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass agent keys create", keyCreateFlags, args)
	if err != nil {
		return err
	}
	ref, err := oneArg("deedglass agent keys create", "an agent name or id", `deedglass agent keys create support-bot --name "prod runner"`, pos)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	a, err := resolveAgent(ctx, e, c, ref)
	if err != nil {
		return err
	}
	in := identity.CreateKeyInput{Name: keyCreateValues.name, Mode: identity.Mode(keyCreateValues.mode), Scopes: keyCreateValues.scopes}
	var res identity.KeyResult
	if _, err := c.Do(ctx, "POST", "/v1/agents/"+a.ID+"/keys", in, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	printNewKey(e, fmt.Sprintf("Key created %s %s %s %s %s %s", ui.Sep, a.Name, ui.Sep, res.Mode, ui.Sep, res.ID), res)
	fmt.Fprintf(e.Stdout, "  Scopes %s %s\n", ui.Sep, strings.Join(res.Scopes, " "+ui.Sep+" "))
	return nil
}

func printNewKey(e *Env, head string, res identity.KeyResult) {
	fmt.Fprintln(e.Stdout, e.Style.OKLine(head))
	fmt.Fprintln(e.Stdout)
	if res.Secret == nil {
		fmt.Fprintln(e.Stdout, "  The secret was shown to an earlier request with the same Idempotency-Key, and Deedglass can't show it again. Roll or revoke this key.")
		return
	}
	fmt.Fprintf(e.Stdout, "  %s\n\n", *res.Secret)
	fmt.Fprintln(e.Stdout, "  This is the only time Deedglass shows this key. Store it in your secret manager now.")
}

func keyStatus(k identity.APIKey, now time.Time) string {
	switch {
	case k.RevokedAt != nil:
		return "revoked"
	case k.ExpiresAt != nil && !now.Before(time.Time(*k.ExpiresAt)):
		return "expired"
	case k.ExpiresAt != nil:
		return "expires " + utcMinute(*k.ExpiresAt)
	}
	return "active"
}

func printKeys(e *Env, keys []identity.APIKey) {
	if len(keys) == 0 {
		fmt.Fprintln(e.Stdout, "No keys yet. Create one with deedglass agent keys create <name>, or for an org admin key, deedglass org keys create.")
		return
	}
	now := time.Now()
	rows := [][]string{{"ID", "PREFIX", "NAME", "MODE", "STATUS", "LAST USED"}}
	for _, k := range keys {
		rows = append(rows, []string{k.ID, k.Prefix, firstNonEmpty(k.Name, "-"), string(k.Mode), keyStatus(k, now), ago(now, k.LastUsedAt)})
	}
	printTable(e, rows, func(row, col int, cell string) string {
		if row > 0 && col == 4 && (cell == "revoked" || cell == "expired") {
			return e.Style.Dim(cell)
		}
		return cell
	})
}

func runAgentKeysList(ctx context.Context, e *Env, args []string) error {
	ref, err := oneArg("deedglass agent keys list", "an agent name or id", "deedglass agent keys list support-bot", args)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	a, err := resolveAgent(ctx, e, c, ref)
	if err != nil {
		return err
	}
	var l httpx.List[identity.APIKey]
	if _, err := c.Do(ctx, "GET", "/v1/agents/"+a.ID+"/keys", nil, &l); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, l)
	}
	printKeys(e, l.Data)
	return nil
}

var rollOverlap time.Duration

func rollFlags(fs *flag.FlagSet) {
	rollOverlap = identity.DefaultRollWindow
	fs.DurationVar(&rollOverlap, "overlap", identity.DefaultRollWindow, "how long the old key keeps working: 0s to 168h")
}

func runAgentKeysRoll(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass agent keys roll", rollFlags, args)
	if err != nil {
		return err
	}
	id, err := oneArg("deedglass agent keys roll", "a key id", "deedglass agent keys roll key_01k6jz12ptrvg2zn5vts5xc1ay --overlap 24h", pos)
	if err != nil {
		return err
	}
	if rollOverlap < 0 || rollOverlap > identity.MaxRollWindow || rollOverlap%time.Second != 0 {
		return usageErr(errors.New("--overlap must be whole seconds from 0s to 168h"), "deedglass agent keys roll key_… --overlap 24h")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var res identity.KeyResult
	if _, err := c.Do(ctx, "POST", "/v1/keys/"+url.PathEscape(id)+"/roll", map[string]int64{"expires_in": int64(rollOverlap / time.Second)}, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	owner := "org admin key"
	if res.Agent != nil {
		owner = *res.Agent
		if a, err := resolveAgent(ctx, e, c, *res.Agent); err == nil {
			owner = a.Name
		}
	}
	s := e.Style
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Key rolled %s %s %s %s", ui.Sep, owner, ui.Sep, res.ID)))
	if res.Secret != nil {
		fmt.Fprintf(e.Stdout, "  %s\n", *res.Secret)
	}
	oldPrefix := "the old key"
	if res.Previous != nil && res.Previous.Prefix != "" {
		oldPrefix = "the old key " + res.Previous.Prefix
	}
	switch {
	case res.Previous == nil || res.Previous.ExpiresAt == nil:
	case !time.Time(*res.Previous.ExpiresAt).After(time.Now()):
		fmt.Fprintf(e.Stdout, "  %s stopped working now. Deploy the new one.\n", upperFirst(oldPrefix))
	default:
		fmt.Fprintf(e.Stdout, "  %s keeps working until %s. Deploy the new one before then.\n", upperFirst(oldPrefix), utcMinute(*res.Previous.ExpiresAt))
	}
	fmt.Fprintln(e.Stdout, "  This is the only time Deedglass shows the new key. Store it in your secret manager now.")
	return nil
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func runAgentKeysRevoke(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass agent keys revoke", reasonFlags, args)
	if err != nil {
		return err
	}
	id, err := oneArg("deedglass agent keys revoke", "a key id", "deedglass agent keys revoke key_01k6jz12ptrvg2zn5vts5xc1ay", pos)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	body := map[string]string{}
	if statusReason != "" {
		body["reason"] = statusReason
	}
	var res identity.KeyResult
	if _, err := c.Do(ctx, "POST", "/v1/keys/"+url.PathEscape(id)+"/revoke", body, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	printRevoked(e, res)
	return nil
}

func printRevoked(e *Env, res identity.KeyResult) {
	if res.Deed == nil {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Key %s was already revoked %s no new deed", res.Prefix, ui.Sep)))
		return
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Key revoked %s %s %s %s sealed", ui.Sep, res.Prefix, ui.Sep, *res.Deed)))
	fmt.Fprintln(e.Stdout, "  Every Deedglass instance refuses it within 2 seconds.")
}

// --- org stop-all ---

var stopAllValues struct {
	reason string
	yes    bool
}

func stopAllFlags(fs *flag.FlagSet) {
	stopAllValues.reason, stopAllValues.yes = "", false
	fs.StringVar(&stopAllValues.reason, "reason", "", "why; it goes into the deed")
	fs.BoolVar(&stopAllValues.yes, "yes", false, "skip the typed confirmation (scripts)")
}

func runOrgStopAll(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass org stop-all takes flags only (got %q)", args[0]), `deedglass org stop-all --reason "…"`)
	}
	if !stopAllValues.yes && (!e.IsTTY || e.Globals.JSON) {
		return usageErr(errors.New("Stopping all agents needs a typed confirmation, and this isn't an interactive terminal"),
			"Run it in a terminal, or add --yes in scripts.")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	w, err := whoami(ctx, e, c)
	if err != nil {
		return err
	}
	if w.Org.Stopped {
		if e.Globals.JSON {
			return writeJSON(e, map[string]any{"object": "org_stop", "org": w.Org.ID, "stopped": true, "deed": nil})
		}
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("All agents in %s are already stopped %s no new deed", w.Org.Name, ui.Sep)))
		fmt.Fprintln(e.Stdout, "  To start them again: an owner in Console › Settings, or `deedglass org start-all` on the server.")
		return nil
	}
	if !stopAllValues.yes {
		active, err := listAllAgents(ctx, e, c, url.Values{})
		if err != nil {
			return err
		}
		n := 0
		for _, a := range active {
			if a.Status != identity.AgentStopped {
				n++
			}
		}
		fmt.Fprintf(e.Stdout, "Stop all %d %s in %s? Every agent call will be refused within 2 seconds,\nand only an owner can start them again.\n",
			n, plural(n, "agent", "agents"), w.Org.Name)
		fmt.Fprintf(e.Stdout, "Type %s to confirm: ", w.Org.Slug)
		line, _ := bufio.NewReader(e.stdin()).ReadString('\n')
		if strings.TrimSpace(line) != w.Org.Slug {
			return &ExitError{Code: ExitFailure, ErrCode: "not_confirmed", Err: errors.New("Nothing was stopped: the confirmation didn't match"),
				Next: "Run it again and type " + w.Org.Slug + ", or press Ctrl-C to leave things as they are."}
		}
	}
	body := map[string]string{}
	if stopAllValues.reason != "" {
		body["reason"] = stopAllValues.reason
	}
	var res identity.OrgStop
	if _, err := c.Do(ctx, "POST", "/v1/org/stop-all", body, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	deedText := "no new deed"
	if res.Deed != nil {
		deedText = *res.Deed + " sealed"
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("All agents stopped %s %s %s %d %s %s %s", ui.Sep, w.Org.Name, ui.Sep,
		res.AgentsAffected, plural(res.AgentsAffected, "agent", "agents"), ui.Sep, deedText)))
	fmt.Fprintln(e.Stdout, "  To start them again: an owner in Console › Settings, or `deedglass org start-all` on the server.")
	return nil
}

// --- the operator commands (database-direct) ---

func runOrgStartAll(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass org start-all", reasonFlags, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageErr(fmt.Errorf("deedglass org start-all takes flags only (got %q)", pos[0]), `deedglass org start-all --org acme --reason "…"`)
	}
	if strings.TrimSpace(statusReason) == "" {
		return usageErr(errors.New(`Starting all agents needs a reason. Add --reason "why it's safe now"; it goes into the deed`), "")
	}
	_, db, _, people, done, err := openPeople(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	org, err := pickOrg(ctx, e, db)
	if err != nil {
		return err
	}
	res, err := people.StartAll(ctx, identity.Operator, org.ID, statusReason)
	if err != nil {
		if he := httpx.AsError(err); he != nil {
			return failNext(e, ExitUsage, string(he.Code), he.Message, "")
		}
		return failNext(e, ExitFailure, "start_failed", "Nothing was started: "+lastPart(err)+".", "Check the database, then run it again.")
	}
	stopped, _, err := people.ListAgents(ctx, org.ID, identity.AgentFilter{Status: identity.AgentStopped, Limit: 100})
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		names := make([]string, len(stopped))
		for i, a := range stopped {
			names[i] = a.Name
		}
		return writeJSON(e, map[string]any{"object": "org_stop", "org": res.Org, "stopped": res.Stopped, "agents_affected": res.AgentsAffected,
			"deed": res.Deed, "still_stopped": names})
	}
	if res.Deed == nil {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%s wasn't stopped %s no new deed", org.Name, ui.Sep)))
	} else {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("All agents started %s %s %s %s sealed", ui.Sep, org.Name, ui.Sep, *res.Deed)))
	}
	if len(stopped) > 0 {
		names := make([]string, len(stopped))
		for i, a := range stopped {
			names[i] = a.Name
		}
		fmt.Fprintf(e.Stdout, "  Still stopped, because each was stopped on its own: %s. Start each with deedglass agent start <name> --reason \"…\".\n", strings.Join(names, ", "))
	}
	return nil
}

var orgKeyValues struct{ name, mode string }

func orgKeyFlags(fs *flag.FlagSet) {
	orgKeyValues.name, orgKeyValues.mode = "", "live"
	fs.StringVar(&orgKeyValues.name, "name", "", "a name for the key, like ci")
	fs.StringVar(&orgKeyValues.mode, "mode", "live", "live or test")
}

func runOrgKeysCreate(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass org keys create takes flags only (got %q)", args[0]), "deedglass org keys create --org acme --name ci")
	}
	_, db, _, people, done, err := openPeople(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	org, err := pickOrg(ctx, e, db)
	if err != nil {
		return err
	}
	res, err := people.CreateAdminKey(ctx, identity.Operator, org.ID, identity.CreateKeyInput{Name: orgKeyValues.name, Mode: identity.Mode(orgKeyValues.mode)})
	if err != nil {
		if he := httpx.AsError(err); he != nil {
			return failNext(e, ExitUsage, string(he.Code), he.Message, "")
		}
		return failNext(e, ExitFailure, "key_failed", "The key wasn't created: "+lastPart(err)+".", "Run deedglass migrate up, then try again.")
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	printAdminKey(e, org.Name, res)
	return nil
}

func printAdminKey(e *Env, orgName string, res identity.KeyResult) {
	printNewKey(e, fmt.Sprintf("Admin key created %s %s %s %s %s %s", ui.Sep, orgName, ui.Sep, res.Mode, ui.Sep, res.ID), res)
	if res.Deed != nil {
		fmt.Fprintf(e.Stdout, "  Deed sealed %s api_key.created %s %s\n", ui.Sep, ui.Sep, *res.Deed)
	}
	fmt.Fprintf(e.Stdout, "  It acts as Admin in %s: it manages agents and keys, and can't start all agents or create admin keys.\n", orgName)
	fmt.Fprintln(e.Stdout, "  Use it with the CLI by setting DEEDGLASS_API_KEY.")
}

func runOrgKeysList(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass org keys list takes flags only (got %q)", args[0]), "deedglass org keys list --org acme")
	}
	_, db, _, people, done, err := openPeople(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	org, err := pickOrg(ctx, e, db)
	if err != nil {
		return err
	}
	keys, err := people.ListAdminKeys(ctx, org.ID)
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, httpx.NewList(keys, false, nil))
	}
	printKeys(e, keys)
	return nil
}

func runOrgKeysRevoke(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass org keys revoke", reasonFlags, args)
	if err != nil {
		return err
	}
	id, err := oneArg("deedglass org keys revoke", "a key id", "deedglass org keys revoke key_01k6jz12ptrvg2zn5vts5xc1ay --org acme", pos)
	if err != nil {
		return err
	}
	_, db, _, people, done, err := openPeople(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	org, err := pickOrg(ctx, e, db)
	if err != nil {
		return err
	}
	k, err := people.GetKey(ctx, org.ID, id)
	if err != nil || k.Agent != nil {
		return failNext(e, ExitFailure, "not_found", fmt.Sprintf("There's no admin key %s in %s.", id, org.Name), "List them with deedglass org keys list.")
	}
	res, err := people.RevokeKey(ctx, identity.Operator, org.ID, id, statusReason)
	if err != nil {
		if he := httpx.AsError(err); he != nil {
			return failNext(e, ExitUsage, string(he.Code), he.Message, "")
		}
		return failNext(e, ExitFailure, "revoke_failed", "The key wasn't revoked: "+lastPart(err)+".", "Check the database, then try again.")
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	printRevoked(e, res)
	return nil
}
