package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
	"github.com/deedglass/deedglass/internal/witness"
)

// deedglass approvals list|show (an org admin key, through the API) and deedglass approve|deny
// (S22 §5.1). Approvals need a person: in dev, approve and deny act as a member named by --as,
// directly on the database like org login-link; in prod they print the Console link and exit 2.

// --- approvals list ---

var approvalsListValues struct {
	status, agent string
	limit         int
}

func approvalsListFlags(fs *flag.FlagSet) {
	approvalsListValues = struct {
		status, agent string
		limit         int
	}{}
	fs.StringVar(&approvalsListValues.status, "status", "pending", "pending, approved, denied, expired, cancelled, or a comma-separated list")
	fs.StringVar(&approvalsListValues.agent, "agent", "", "an agent id or name")
	fs.IntVar(&approvalsListValues.limit, "limit", 25, "at most this many (1 to 100)")
}

func runApprovalsList(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass approvals list takes flags only (got %q)", args[0]), "deedglass approvals list --status pending")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	q := url.Values{}
	if approvalsListValues.status != "" {
		q.Set("status", approvalsListValues.status)
	}
	if approvalsListValues.agent != "" {
		q.Set("agent", approvalsListValues.agent)
	}
	q.Set("limit", strconv.Itoa(min(max(approvalsListValues.limit, 1), 100)))
	var l approve.List
	var raw json.RawMessage
	if _, err := c.Do(ctx, "GET", "/v1/approvals?"+q.Encode(), nil, &raw); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(raw, '\n'))
		return err
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		return err
	}
	if len(l.Data) == 0 {
		fmt.Fprintln(e.Stdout, "No approvals waiting. Risky actions appear here for a person to countersign.")
		return nil
	}
	now := time.Now()
	rows := make([][]string, 0, len(l.Data))
	for _, a := range l.Data {
		sym, when := statusSymbol(e, string(a.Status)), ""
		if a.Status == approve.StatusPending {
			when = "expires " + approve.In(time.Time(a.ExpiresAt), now)
		} else if a.DecidedAt != nil {
			when = string(a.Status) + " " + ago(now, a.DecidedAt)
		}
		rows = append(rows, []string{sym, shortID(a.ID), a.Agent.Name, a.Tool, deref(a.RuleID), countersignWords(a), when})
	}
	printColumns(e.Stdout, rows)
	return nil
}

func statusSymbol(e *Env, status string) string {
	s := e.Style
	switch status {
	case "pending":
		return s.Violet(ui.Pending)
	case "approved":
		return s.Teal(ui.OK)
	case "denied":
		return s.Red(ui.Fail)
	}
	return s.Amber(ui.Unknown)
}

func countersignWords(a *approve.Approval) string {
	n := 0
	switch c := a.Countersigned.(type) {
	case []any:
		n = len(c)
	case float64:
		n = int(c)
	}
	return fmt.Sprintf("%d of %d", n, a.Required)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// printColumns pads every column but the last to its widest cell, two spaces apart.
func printColumns(w io.Writer, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	width := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r[:len(r)-1] {
			width[i] = max(width[i], visibleLen(c))
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", width[i]-visibleLen(c)+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// visibleLen is a cell's width without its ANSI colour codes.
func visibleLen(s string) int {
	n, esc := 0, false
	for _, r := range s {
		switch {
		case r == 0x1b:
			esc = true
		case esc && r == 'm':
			esc = false
		case !esc:
			n++
		}
	}
	return n
}

// --- approvals show ---

func runApprovalsShow(ctx context.Context, e *Env, args []string) error {
	id, err := oneArg("deedglass approvals show", "an approval id", "deedglass approvals show apr_01j9zw6q3m4n5p6q7r8s9t0v1w", args)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, "GET", "/v1/approvals/"+url.PathEscape(id), nil, &raw); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(raw, '\n'))
		return err
	}
	var a approve.Approval
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	var snap approve.Snapshot
	_ = json.Unmarshal(a.Context, &snap)
	printApproval(e, &a, &snap, time.Now())
	return nil
}

func printApproval(e *Env, a *approve.Approval, snap *approve.Snapshot, now time.Time) {
	s := e.Style
	fmt.Fprintf(e.Stdout, "%s %s %s %s %s %s\n", statusSymbol(e, string(a.Status)), a.Status, ui.Sep, a.Tool, ui.Sep, a.Agent.Name)
	field := func(k, v string) {
		if v != "" {
			fmt.Fprintf(e.Stdout, "  %-9s %s\n", k, v)
		}
	}
	summary := strings.TrimSuffix(strings.TrimPrefix(a.Summary, a.Agent.Name+" wants to "), ".")
	if snap.SQL != nil && snap.SQL.Estimate != nil && snap.SQL.Estimate.Rows != nil {
		summary += " (planner estimate)"
	}
	field("summary", summary)
	var args struct {
		SQL string `json:"sql"`
	}
	if json.Unmarshal(snap.Args, &args) == nil {
		field("sql", strings.Join(strings.Fields(args.SQL), " "))
	}
	field("rule", strings.TrimSpace(deref(a.RuleID)+" "+ui.Sep+" "+deref(a.Reason)))
	field("needs", countersignWords(a)+" "+ui.Sep+" "+strings.Join(a.Approvers, ", "))
	if a.Status == approve.StatusPending {
		field("expires", time.Time(a.ExpiresAt).Local().Format("15:04 MST")+" "+ui.Sep+" "+approve.In(time.Time(a.ExpiresAt), now))
	}
	if a.ContextDigest != nil {
		field("context", shortDigest(*a.ContextDigest))
	}
	if a.DecisionReason != nil {
		field("reason", *a.DecisionReason)
	}
	if a.Status == approve.StatusPending {
		fmt.Fprintln(e.Stdout, s.Dim("  Next: countersign with the link in your email, or in Console › Approvals."))
	}
}

func shortDigest(d string) string {
	h := strings.TrimPrefix(d, "sha256:")
	if len(h) < 8 {
		return d
	}
	return "sha256:" + h[:4] + "…" + h[len(h)-4:]
}

// --- approve | deny ---

var decideValues struct{ as, reason string }

func decideFlags(fs *flag.FlagSet) {
	decideValues = struct{ as, reason string }{}
	fs.StringVar(&decideValues.as, "as", "", "dev only: the member who decides (their email)")
	fs.StringVar(&decideValues.reason, "reason", "", "why (required to deny; the agent sees it)")
}

func runApprove(ctx context.Context, e *Env, args []string) error {
	return runDecide(ctx, e, args, approve.VoteApprove)
}
func runDeny(ctx context.Context, e *Env, args []string) error {
	return runDecide(ctx, e, args, approve.VoteDeny)
}

func runDecide(ctx context.Context, e *Env, args []string, v approve.Vote) error {
	name := "deedglass " + string(v)
	pos, err := mixedArgs(name, decideFlags, args)
	if err != nil {
		return err
	}
	id, err := oneArg(name, "an approval id", name+` apr_01j9zw6q3m4n5p6q7r8s9t0v1w --as founder@example.test --reason "Customer confirmed by phone"`, pos)
	if err != nil {
		return err
	}
	if v == approve.VoteDeny && strings.TrimSpace(decideValues.reason) == "" {
		return failNext(e, ExitUsage, "validation_failed", `A reason is required to deny. The agent sees it. Add --reason "…".`, "")
	}
	cfg, warnings, err := config.Load(e.LoadOptions())
	var ps *config.Problems
	if errors.As(err, &ps) {
		return configProblems(e, ps, warnings)
	}
	if err != nil {
		return err
	}
	if cfg.Env == config.Prod || strings.TrimSpace(decideValues.as) == "" {
		base := strings.TrimSuffix(cfg.BaseURL.String(), "/")
		return failNext(e, ExitUsage, "approval_needs_person",
			fmt.Sprintf("Approvals need a person. Open %s/app/approvals/%s or use the link in your email.", base, id), "")
	}
	email, err := identity.NormalizeEmail(decideValues.as)
	if err != nil {
		return failNext(e, ExitUsage, "validation_failed", err.Error(), "")
	}
	_, db, led, _, done, err := openPeople(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	u, err := db.PreAuth().UserByEmail(ctx, email)
	if err != nil {
		return failNext(e, ExitFailure, "not_found", fmt.Sprintf("%s isn't a member of any org here.", email), "Invite them first, or check the address.")
	}
	var org ids.OrgID
	for _, m := range u.Memberships {
		if m.Status != "active" {
			continue
		}
		if want := strings.TrimSpace(e.Globals.Org); want != "" && m.OrgID.String() != want {
			continue
		}
		if !org.IsZero() {
			return failNext(e, ExitUsage, "org_required", fmt.Sprintf("%s is a member of several orgs. Pick one with --org.", email), "")
		}
		org = m.OrgID
	}
	if org.IsZero() {
		return failNext(e, ExitFailure, "not_found", fmt.Sprintf("%s isn't an active member of this org.", email), "")
	}
	svc, err := cliApprovals(cfg, db, led)
	if err != nil {
		return err
	}
	admin := approve.Viewer{Kind: httpx.KindAdminKey, Org: org}
	cur, err := svc.Get(ctx, admin, id)
	if err != nil {
		return cliAPIError(e, err)
	}
	digest := ""
	if cur.ContextDigest != nil {
		digest = *cur.ContextDigest
	}
	a, err := svc.Decide(ctx, approve.DecideInput{Org: org, ApprovalID: id, Vote: v, Reason: decideValues.reason, ContextDigest: digest,
		Approver: approve.Approver{UserID: u.ID, Via: approve.ViaCLIDev}})
	if err != nil {
		return cliAPIError(e, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, a)
	}
	s := e.Style
	cs, _ := a.Countersigned.([]approve.Countersign)
	n := len(cs)
	if v == approve.VoteDeny {
		fmt.Fprintln(e.Stdout, s.FailLine(fmt.Sprintf("Denied %s %s %s %s", ui.Sep, shortID(a.ID), ui.Sep, email)))
	} else {
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Countersigned %s %s %s %s %s %d of %d", ui.Sep, shortID(a.ID), ui.Sep, email, ui.Sep, n, a.Required)))
	}
	action := ledger.AdminApprovalCountersigned
	if a.Status != approve.StatusPending {
		action = ledger.AdminApprovalDecided
	}
	if d := deref(a.Deed); d != "" || len(cs) > 0 {
		if d == "" {
			d = cs[len(cs)-1].Deed
		}
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Record sealed %s %s %s %s", ui.Sep, action, ui.Sep, shortID(d))))
	}
	switch a.Status {
	case approve.StatusPending:
		fmt.Fprintf(e.Stdout, "  It runs when %d more %s.\n", a.Required-n, map[bool]string{true: "person approves", false: "people approve"}[a.Required-n == 1])
		return nil
	case approve.StatusDenied:
		fmt.Fprintln(e.Stdout, "  Nothing ran. The agent sees your reason.")
		return nil
	}
	// Approved: the server's worker runs it. Follow the action until it is sealed (30 s).
	deadline := time.Now().Add(30 * time.Second)
	for {
		act, err := led.ReadActionView(ctx, org, a.Action)
		if err == nil && act.State == "sealed" {
			printDecidedAction(e, act)
			return nil
		}
		if err == nil && act.Status == ledger.StatusAuthorized {
			fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("authorized %s %s %s the agent's edge runs it on its next deedglass_check_approval", ui.Sep, act.Tool, ui.Sep)))
			return nil
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(e.Stdout, s.Violet(ui.Pending)+" processing "+ui.Sep+" a running deedglass serve runs it; follow it with deedglass actions show "+a.Action)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func printDecidedAction(e *Env, a *ledger.Action) {
	s := e.Style
	line := fmt.Sprintf("%s %s %s", a.Status, ui.Sep, a.Tool)
	if a.Execution != nil && a.Execution.LatencyMS != nil {
		line += fmt.Sprintf(" %s %d ms", ui.Sep, *a.Execution.LatencyMS)
	}
	if a.Status == ledger.StatusExecuted {
		fmt.Fprintln(e.Stdout, s.OKLine(line))
	} else {
		fmt.Fprintln(e.Stdout, s.FailLine(line))
	}
	if w := a.Witness; w != nil {
		t := witness.PlainLevel(w.Level) + " " + ui.Sep + " " + witness.PlainVerdict(w.Verdict)
		if w.Summary != nil && *w.Summary != "" {
			t += " " + ui.Sep + " " + strings.TrimSuffix(*w.Summary, ".")
		}
		fmt.Fprintf(e.Stdout, "  check    %s\n", t)
	}
	if a.Deed != nil {
		fmt.Fprintf(e.Stdout, "  record   %s %s sealed\n", shortID(*a.Deed), ui.Sep)
	}
}

// cliAPIError prints a service error (an *httpx.Error) the CLI way.
func cliAPIError(e *Env, err error) error {
	if he := httpx.AsError(err); he != nil {
		code := ExitFailure
		if he.Status == 400 {
			code = ExitUsage
		}
		return failNext(e, code, string(he.Code), he.Message, "")
	}
	return err
}

// cliApprovals is the approvals service for the DB-direct commands: a Gate that can conclude a
// denied action in the decision's transaction, and no notifiers (the server's worker sends mail).
func cliApprovals(cfg *config.Config, db *store.DB, led *ledger.Ledger) (*approve.Service, error) {
	log := slog.New(slog.DiscardHandler)
	svc, err := approve.New(approve.Options{DB: db, Ledger: led, BaseURL: cfg.BaseURL, Logger: log})
	if err != nil {
		return nil, err
	}
	gp, err := gate.New(gate.Options{DB: db, Ledger: led, Policies: noPolicies{}, Usage: policystore.NewUsage(db),
		Resolver: connector.NewRegistry(), Approvals: svc, Logger: log})
	if err != nil {
		return nil, err
	}
	svc.Attach(gp)
	return svc, nil
}

// noPolicies is the policy cache of a pipeline that never evaluates (the CLI's).
type noPolicies struct{}

func (noPolicies) Active(context.Context, string) (*policy.Active, error) {
	return nil, errors.New("deedglass: this command doesn't evaluate policies")
}
