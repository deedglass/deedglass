package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/store"
)

// The people commands (S07): org create and org login-link are DB-direct operator commands, like
// migrate. They connect as the runtime role, so row-level security still applies to everything
// but the org_create system transaction. dev mail-preview renders the email templates.

type orgCreateOpts struct {
	name, ownerEmail, slug string
	printLink, adminKey    bool
}

var orgCreateValues orgCreateOpts

func orgCreateFlags(fs *flag.FlagSet) {
	o := &orgCreateValues
	*o = orgCreateOpts{}
	fs.StringVar(&o.name, "name", "", "the org's name, as people see it (required)")
	fs.StringVar(&o.ownerEmail, "owner-email", "", "the first owner's email address (required)")
	fs.StringVar(&o.slug, "slug", "", "the org's short name in URLs (default: from the name)")
	fs.BoolVar(&o.printLink, "print-link", false, "prod: print the owner's sign-in link instead of emailing it")
	fs.BoolVar(&o.adminKey, "admin-key", false, "also create an org admin key and print it once (S08)")
}

type loginLinkOpts struct{ email string }

var loginLinkValues loginLinkOpts

func loginLinkFlags(fs *flag.FlagSet) {
	loginLinkValues = loginLinkOpts{}
	fs.StringVar(&loginLinkValues.email, "email", "", "the member's email address (required); --org picks the org")
}

// openPeople loads the config, connects as the runtime role, loads the seal key and returns the
// sealing Ledger and identity service a DB-direct people command uses.
func openPeople(ctx context.Context, e *Env) (*config.Config, *store.DB, *ledger.Ledger, *identity.Service, func(), error) {
	return openPeopleWith(ctx, e, store.Options{MaxConns: 2})
}

// openPeopleWith is openPeople with the pool's options (dev simulate: more connections, async commit).
func openPeopleWith(ctx context.Context, e *Env, so store.Options) (*config.Config, *store.DB, *ledger.Ledger, *identity.Service, func(), error) {
	return openPeopleOpts(ctx, e, cliLoadOptions(e), so)
}

// openPeopleOpts is openPeopleWith on config options the caller resolved (a local stack's).
func openPeopleOpts(ctx context.Context, e *Env, opts config.LoadOptions, so store.Options) (*config.Config, *store.DB, *ledger.Ledger, *identity.Service, func(), error) {
	cfg, warnings, err := config.Load(opts)
	var ps *config.Problems
	if errors.As(err, &ps) {
		return nil, nil, nil, nil, nil, configProblems(e, ps, warnings)
	}
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if err := cfg.Require(config.NeedDatabase | config.NeedPepper); errors.As(err, &ps) {
		return nil, nil, nil, nil, nil, configProblems(e, ps, warnings)
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, so)
	if err != nil {
		return nil, nil, nil, nil, nil, failNext(e, ExitUsage, "database_unreachable", "Can't reach the database in DEEDGLASS_DATABASE_URL.",
			"Check that Postgres is running and that migrations ran (deedglass migrate up).")
	}
	log := slog.New(slog.DiscardHandler)
	signer, err := loadSealKeyQuiet(ctx, e, cfg, db, log, true)
	if err != nil {
		db.Close()
		return nil, nil, nil, nil, nil, err
	}
	led := newLedger(db, signer, cfg, log)
	people, err := newIdentity(cfg, db, led, nil, log)
	if err != nil {
		db.Close()
		return nil, nil, nil, nil, nil, err
	}
	return cfg, db, led, people, db.Close, nil
}

func runOrgCreate(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass org create takes flags only (got %q)", args[0]),
			`deedglass org create --name "Acme" --owner-email priya@acme.com`)
	}
	o := orgCreateValues
	if strings.TrimSpace(o.name) == "" || strings.TrimSpace(o.ownerEmail) == "" {
		return usageErr(errors.New("--name and --owner-email are required"), `deedglass org create --name "Acme" --owner-email priya@acme.com`)
	}
	if _, err := identity.CheckOrgName(o.name); err != nil {
		return failNext(e, ExitUsage, "validation_failed", "The org name must be 1 to 100 characters, without line breaks.", `deedglass org create --name "Acme" …`)
	}
	email, err := identity.NormalizeEmail(o.ownerEmail)
	if err != nil {
		return failNext(e, ExitUsage, "validation_failed", err.Error(), "")
	}
	if o.slug != "" && !identity.ValidSlug(o.slug) {
		return failNext(e, ExitUsage, "validation_failed", fmt.Sprintf("%q isn't a usable slug.", o.slug),
			"Use 1 to 40 lower-case letters, digits and hyphens, starting and ending with a letter or digit.")
	}
	if o.slug == "" && identity.SlugFor(o.name) == "" {
		return failNext(e, ExitUsage, "validation_failed", "The name has no letters or digits to make a slug from.", "Pass --slug acme.")
	}
	cfg, db, led, people, done, err := openPeople(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	prod := cfg.Env == config.Prod
	c, err := identity.CreateOrg(ctx, db, led, o.name, o.slug, email)
	if errors.Is(err, identity.ErrSlugTaken) {
		slug := o.slug
		if slug == "" {
			slug = identity.SlugFor(o.name)
		}
		return failNext(e, ExitFailure, "conflict", fmt.Sprintf("Another org already uses the slug %s.", slug), "Pass a different --slug.")
	}
	if err != nil {
		return failNext(e, ExitFailure, "org_create_failed", "The org wasn't created: "+lastPart(err)+".",
			"Run deedglass migrate up, and check the runtime role (docs/DATABASE_SETUP.md).")
	}
	// The owner's first sign-in: printed in dev (or with --print-link), emailed otherwise.
	var link identity.IssuedLink
	mode := "print"
	if prod && !o.printLink {
		mode = "email"
		if !cfg.ResendAPIKey.IsSet() {
			mode = "none"
		}
	}
	switch mode {
	case "print":
		if link, err = people.IssueLink(ctx, email, c.Org.ID, false); err != nil {
			return failNext(e, ExitFailure, "link_failed", "The org was created, but its sign-in link wasn't: "+lastPart(err)+".",
				fmt.Sprintf("deedglass org login-link --email %s", email))
		}
	case "email":
		// The same sign-in email the API sends (a link and a code), sent now, outside any transaction.
		r, rerr := mailer.NewResend(mailer.ResendOptions{APIKey: cfg.ResendAPIKey.Reveal(), From: cfg.MailFrom, Logger: slog.New(slog.DiscardHandler)})
		if rerr != nil || people.EmailSignIn(ctx, r, email, c.Org.ID) != nil {
			mode = "email_failed"
		}
	}
	var adminKey *identity.KeyResult
	if o.adminKey {
		k, err := people.CreateAdminKey(ctx, identity.Operator, c.Org.ID, identity.CreateKeyInput{Name: "operator"})
		if err != nil {
			return failNext(e, ExitFailure, "key_failed", "The org was created, but its admin key wasn't: "+lastPart(err)+".",
				fmt.Sprintf("deedglass org keys create --org %s", c.Org.Slug))
		}
		adminKey = &k
	}
	if e.Globals.JSON {
		out := map[string]any{"object": "org_created", "org": map[string]any{"object": "org", "id": c.Org.ID.String(), "name": c.Org.Name,
			"slug": c.Org.Slug, "timezone": c.Org.Timezone, "status": c.Org.Status},
			"owner":      map[string]any{"object": "user", "id": c.Owner.ID.String(), "email": c.Owner.Email},
			"membership": c.Membership.String(), "deed": c.Deed, "sign_in": mode}
		if mode == "print" {
			out["link"] = link.Link
			out["link_expires_at"] = link.ExpiresAt.UTC().Format(time.RFC3339)
		}
		if adminKey != nil {
			out["admin_key"] = adminKey
		}
		return writeJSON(e, out)
	}
	s := e.Style
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Org created %s %s %s %s", ui.Sep, c.Org.Name, ui.Sep, c.Org.ID)))
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Owner %s %s %s %s", ui.Sep, c.Owner.Email, ui.Sep, c.Owner.ID)))
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Record sealed %s org.created %s %s", ui.Sep, ui.Sep, c.Deed)))
	fmt.Fprintln(e.Stdout)
	switch mode {
	case "print":
		fmt.Fprintf(e.Stdout, "  Sign-in link for %s (works once, expires in %s):\n", email, ttlWords(link.ExpiresAt.Sub(time.Now())))
		fmt.Fprintf(e.Stdout, "  %s\n\n", link.Link)
	case "email":
		fmt.Fprintf(e.Stdout, "  A sign-in email is on its way to %s.\n\n", email)
	case "email_failed":
		fmt.Fprintf(e.Stdout, "  The sign-in email couldn't be sent. Print a link instead: deedglass org login-link --email %s\n\n", email)
	default:
		fmt.Fprintf(e.Stdout, "  Email isn't set up here (DEEDGLASS_RESEND_API_KEY). Print a link: deedglass org login-link --email %s\n\n", email)
	}
	if adminKey != nil {
		printAdminKey(e, c.Org.Name, *adminKey)
		fmt.Fprintln(e.Stdout)
	}
	fmt.Fprintln(e.Stdout, "  Next: open the link, then register your first agent with `deedglass agent create`.")
	return nil
}

func ttlWords(d time.Duration) string {
	switch {
	case d > 24*time.Hour:
		n := int((d + 12*time.Hour) / (24 * time.Hour))
		return fmt.Sprintf("%d %s", n, plural(n, "day", "days"))
	default:
		n := max(1, int((d+30*time.Second)/time.Minute))
		return fmt.Sprintf("%d %s", n, plural(n, "minute", "minutes"))
	}
}

func runOrgLoginLink(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass org login-link takes flags only (got %q)", args[0]), "deedglass org login-link --email sam@acme.com")
	}
	if strings.TrimSpace(loginLinkValues.email) == "" {
		return usageErr(errors.New("--email is required"), "deedglass org login-link --email sam@acme.com [--org acme]")
	}
	email, err := identity.NormalizeEmail(loginLinkValues.email)
	if err != nil {
		return failNext(e, ExitUsage, "validation_failed", err.Error(), "")
	}
	_, db, _, people, done, err := openPeople(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	var org store.Org
	if strings.TrimSpace(e.Globals.Org) != "" {
		if org, err = pickOrg(ctx, e, db); err != nil {
			return err
		}
	}
	l, err := people.IssueLink(ctx, email, org.ID, true)
	if errors.Is(err, identity.ErrNoMember) {
		where := "any org here"
		if !org.ID.IsZero() {
			where = org.Name
		}
		return failNext(e, ExitFailure, "not_found", fmt.Sprintf("%s isn't an active or invited member of %s.", email, where),
			"Invite them first (POST /v1/org/members), or check the address.")
	}
	if err != nil {
		return failNext(e, ExitFailure, "link_failed", "The link wasn't created: "+lastPart(err)+".", "Run deedglass migrate up, then try again.")
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "login_link", "purpose": l.Purpose, "email": email, "org": l.OrgID.String(),
			"user": l.UserID.String(), "link": l.Link, "expires_at": l.ExpiresAt.UTC().Format(time.RFC3339), "deed": l.Deed})
	}
	s := e.Style
	what := "Sign-in link issued"
	if l.Purpose == identity.PurposeInvite {
		what = "Invite link issued"
	}
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("%s %s %s %s %s", what, ui.Sep, email, ui.Sep, l.OrgName)))
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Record sealed %s login_link.issued %s %s", ui.Sep, ui.Sep, l.Deed)))
	fmt.Fprintf(e.Stdout, "  %s\n", l.Link)
	fmt.Fprintf(e.Stdout, "  Anyone with this link can sign in as %s for %s. Send it privately.\n", email, ttlWords(time.Until(l.ExpiresAt)))
	return nil
}

// --- dev mail-preview ---

var mailPreviewOut string

func mailPreviewFlags(fs *flag.FlagSet) {
	mailPreviewOut = ""
	fs.StringVar(&mailPreviewOut, "out", "", "write the HTML to this file (default: a temporary file)")
}

// The preview's sample data: the brief's example, so screenshots match the docs.
var previewNow = time.Date(2026, 10, 3, 9, 14, 7, 0, time.UTC)

func runMailPreview(_ context.Context, e *Env, args []string) error {
	// The kind comes first, and flags may follow it: dev mail-preview login --out /tmp/login.html.
	if len(args) > 1 {
		fs := flag.NewFlagSet("dev mail-preview", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.StringVar(&mailPreviewOut, "out", mailPreviewOut, "")
		if err := fs.Parse(args[1:]); err != nil {
			return usageErr(err, "deedglass dev mail-preview login --out /tmp/login.html")
		}
		args = append(args[:1], fs.Args()...)
	}
	if len(args) != 1 || (args[0] != "login" && args[0] != "invite" && args[0] != "approval" && args[0] != "approval-foureyes") {
		return usageErr(errors.New("say which email: login, invite, approval or approval-foureyes"), "deedglass dev mail-preview approval --out /tmp/approval.html")
	}
	var r mailer.Rendered
	var err error
	switch args[0] {
	case "approval", "approval-foureyes":
		// S22's example: claude-desktop ships order 42; four-eyes adds Priya's earlier countersign.
		loc, _ := time.LoadLocation("Asia/Kolkata")
		d := mailer.ApprovalData{AgentName: "claude-desktop", OrgName: "Acme Demo", Tool: "postgres.execute", RuleID: "orders-writes-need-approval",
			Reason: "Writes to the orders database need a person to countersign.", Summary: "UPDATE about 1 row in public.orders", Estimated: true,
			Required: 1, ExpiresAt: previewNow.Add(27 * time.Minute), Location: loc, ApprovalID: "apr_01m3y2z9q4hk7c8vd5w3e6f2ab",
			Email: "arjun@acme.com", Link: "http://127.0.0.1:8080/approve#t=OEVkbr-q18qimgaDkbcE5mnZqQ2a1zGG7_TgYq8PKFc"}
		if args[0] == "approval-foureyes" {
			d.Required = 2
			d.CountersignedBy = []mailer.ApprovalCountersign{{Name: "Priya Raman", At: previewNow.Add(-4 * time.Minute)}}
		}
		r, err = mailer.RenderApproval(d, previewNow)
	case "login":
		r, err = mailer.RenderLogin(mailer.LoginData{OrgName: "Acme", Email: "priya@acme.com",
			Link: "http://127.0.0.1:8080/auth/confirm#t=OEVkbr-q18qimgaDkbcE5mnZqQ2a1zGG7_TgYq8PKFc", Code: "482913",
			RequestedFrom: "Chrome on macOS", RequestedAt: previewNow, ExpiresIn: identity.LoginTTL})
	default:
		r, err = mailer.RenderInvite(mailer.InviteData{InviterName: "Priya Raman", InviterEmail: "priya@acme.com", OrgName: "Acme",
			Role: "viewer", Email: "sam@acme.com", Link: "http://127.0.0.1:8080/auth/confirm#t=OEVkbr-q18qimgaDkbcE5mnZqQ2a1zGG7_TgYq8PKFc",
			ExpiresAt: previewNow.Add(identity.InviteTTL), Now: previewNow})
	}
	if err != nil {
		return err
	}
	out := mailPreviewOut
	if out == "" {
		out = filepath.Join(os.TempDir(), "deedglass-mail-"+args[0]+".html")
	}
	if err := os.WriteFile(out, []byte(r.HTML), 0o644); err != nil {
		return failNext(e, ExitFailure, "write_failed", fmt.Sprintf("Can't write %s.", out), "Pick another path with --out.")
	}
	txt := strings.TrimSuffix(out, filepath.Ext(out)) + ".txt"
	if err := os.WriteFile(txt, []byte(r.Text), 0o644); err != nil {
		return failNext(e, ExitFailure, "write_failed", fmt.Sprintf("Can't write %s.", txt), "Pick another path with --out.")
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "mail_preview", "kind": args[0], "subject": r.Subject, "html": out, "text": txt})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%s %s %s", r.Subject, ui.Sep, out)))
	fmt.Fprintf(e.Stdout, "  Plain text: %s\n  Open the HTML file in a browser to see it.\n", txt)
	return nil
}
