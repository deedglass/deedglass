package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// deedglass approvals open <apr_…> (adoption-v1 F3): the approve link for one approval. In dev it
// mints a fresh one-time link for a member who may countersign (--as, default the owner deedglass
// init set up), directly on the database like approve --as, prints it and opens it. In prod it
// prints the Console's page for the approval: links there go by email only.

var approvalsOpenValues struct {
	as     string
	noOpen bool
}

func approvalsOpenFlags(fs *flag.FlagSet) {
	approvalsOpenValues.as, approvalsOpenValues.noOpen = "", false
	fs.StringVar(&approvalsOpenValues.as, "as", "", "dev only: the member the link is for (default: the owner deedglass init set up)")
	fs.BoolVar(&approvalsOpenValues.noOpen, "no-open", false, "print the link without opening a browser")
}

// openApproveLink opens a URL (a seam for tests).
var openApproveLink = func(u string) { _ = openBrowser(u) }

func runApprovalsOpen(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass approvals open", approvalsOpenFlags, args)
	if err != nil {
		return err
	}
	id, err := oneArg("deedglass approvals open", "an approval id", "deedglass approvals open apr_01j9zw6q3m4n5p6q7r8s9t0v1w", pos)
	if err != nil {
		return err
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if _, perr := ids.Parse[ids.ApprovalKind](id); perr != nil {
		return usageErr(fmt.Errorf("%q isn't an approval id", clean(id)), "Approval ids look like apr_01j9zw6q3m4n5p6q7r8s9t0v1w; deedglass approvals list shows them.")
	}
	link, console, who, err := approveLink(ctx, e, id, approvalsOpenValues.as)
	if err != nil {
		return err
	}
	target := firstNonEmpty(link, console)
	if e.Globals.JSON {
		out := map[string]any{"object": "approval_link", "approval": id, "console_url": console}
		if link != "" {
			out["url"], out["email"] = link, who
		}
		return writeJSON(e, out)
	}
	if link != "" {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Approve link %s %s %s for %s %s one use, dev only", ui.Sep, shortID(id), ui.Sep, who, ui.Sep)))
	} else {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Approval %s %s %s countersign it in the Console", ui.Sep, shortID(id), ui.Sep)))
	}
	fmt.Fprintln(e.Stdout, "  "+target)
	if e.IsTTY && !approvalsOpenValues.noOpen {
		openApproveLink(target)
	}
	return nil
}

// approveLink is the link a person countersigns approval id with. In dev: a fresh one-time link
// for as (default: the local stack's owner), and the email it is for. In prod: only the Console
// page. console is always the Console page for the approval.
func approveLink(ctx context.Context, e *Env, id, as string) (link, console, email string, err error) {
	opts, st, err := stackLoadOptions(e)
	if err != nil {
		return "", "", "", err
	}
	cfg, warnings, err := config.Load(opts)
	var ps *config.Problems
	if errors.As(err, &ps) {
		return "", "", "", configProblems(e, ps, warnings)
	}
	if err != nil {
		return "", "", "", err
	}
	console = strings.TrimSuffix(cfg.BaseURL.String(), "/") + "/app/approvals/" + id
	if cfg.Env == config.Prod {
		return "", console, "", nil
	}
	if as == "" && st != nil {
		as = st.OwnerEmail
	}
	if strings.TrimSpace(as) == "" {
		return "", "", "", usageErr(errors.New("--as names the member the link is for"), "deedglass approvals open "+clean(id)+" --as you@example.com")
	}
	email, err = identity.NormalizeEmail(as)
	if err != nil {
		return "", "", "", failNext(e, ExitUsage, "validation_failed", err.Error(), "")
	}
	_, db, led, _, done, err := openPeopleOpts(ctx, e, opts, store.Options{MaxConns: 2})
	if err != nil {
		return "", "", "", err
	}
	defer done()
	org, err := memberOrg(ctx, e, db, email)
	if err != nil {
		return "", "", "", err
	}
	svc, err := cliApprovals(cfg, db, led)
	if err != nil {
		return "", "", "", err
	}
	link, err = svc.DevLink(ctx, org, id, email)
	switch {
	case errors.Is(err, approve.ErrNotPending):
		return "", "", "", failNext(e, ExitFailure, "approval_not_pending", "That approval isn't waiting any more.", "deedglass approvals show "+clean(id)+" says how it ended.")
	case errors.Is(err, approve.ErrNotEligible):
		return "", "", "", failNext(e, ExitFailure, "not_eligible", fmt.Sprintf("%s can't countersign this approval.", email), "Pass --as with an approver the rule names (deedglass approvals show "+clean(id)+").")
	case err != nil:
		return "", "", "", cliAPIError(e, err)
	}
	return link, console, email, nil
}

// memberOrg is the one active org email belongs to (--org picks one when there are several).
func memberOrg(ctx context.Context, e *Env, db *store.DB, email string) (ids.OrgID, error) {
	var org ids.OrgID
	u, err := db.PreAuth().UserByEmail(ctx, email)
	if err != nil {
		return org, failNext(e, ExitFailure, "not_found", fmt.Sprintf("%s isn't a member of any org here.", email), "Invite them first, or check the address.")
	}
	for _, m := range u.Memberships {
		if m.Status != "active" {
			continue
		}
		if want := strings.TrimSpace(e.Globals.Org); want != "" && m.OrgID.String() != want {
			continue
		}
		if !org.IsZero() {
			return org, failNext(e, ExitUsage, "org_required", fmt.Sprintf("%s is a member of several orgs. Pick one with --org.", email), "")
		}
		org = m.OrgID
	}
	if org.IsZero() {
		return org, failNext(e, ExitFailure, "not_found", fmt.Sprintf("%s isn't an active member of this org.", email), "")
	}
	return org, nil
}

// pendingApprovalLink is undo run's best effort (dev, a local stack): the approve link for the
// approval it just created, or "" when one can't be made here. It never fails the command.
func pendingApprovalLink(ctx context.Context, e *Env, id string) string {
	if t := resolveServer(e); t.From != fromStack {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	quiet := *e
	quiet.Stderr = discard{}
	link, _, _, err := approveLink(ctx, &quiet, id, "")
	if err != nil {
		return ""
	}
	return link
}
