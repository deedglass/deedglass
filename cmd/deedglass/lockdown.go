package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/lockdown"
	"github.com/deedglass/deedglass/internal/store"
)

// deedglass lockdown on|off|status|ack (ADR-117, docs/LOCKDOWN.md). Each runs on the server,
// against the database (like org start-all), as the operator; --as names the person, who must be an
// active owner or admin. There is no API route that turns lockdown off.

var lockdownValues struct {
	as, reason  string
	countersign bool
}

func lockdownFlags(fs *flag.FlagSet) {
	lockdownValues = struct {
		as, reason  string
		countersign bool
	}{}
	fs.StringVar(&lockdownValues.as, "as", "", "the owner or admin this is done for (an email); the record names them")
	fs.StringVar(&lockdownValues.reason, "reason", "", "why; it goes into the record")
	fs.BoolVar(&lockdownValues.countersign, "countersign", false, "off: countersign another person's request to turn lockdown off")
}

const lockdownDetail = "Lockdown is the strictest setup in one switch (docs/LOCKDOWN.md): every write waits for a person, high-risk writes need two different people, writes are checked at Proven where the connector reaches it, a Can't check result holds the agent's writes until a person acknowledges it, DROP, TRUNCATE, ALTER and unbounded UPDATE or DELETE never run, each agent reaches one connector (its connector label), FHIR needs PHI mode, and approvals expire within 15 minutes.\n\nIt is on for an org when the org turned it on (deedglass lockdown on) or when the server's config says lockdown: true. These commands run on the server, with its database URL, as the operator.\n\non: turns it on for the org and seals org.updated (and turns PHI mode on when the org has a FHIR connector).\noff: needs two people. One owner or admin asks (--as), and a different one countersigns within 30 minutes (--countersign --as); both are sealed. It is refused while lockdown: true is in the config.\nack: a person has looked at an action that ended Can't check, which releases its agent's writes; sealed as lockdown.acknowledged.\n\nExamples:\n  deedglass lockdown status\n  deedglass lockdown on --reason \"Pilot at the ward\"\n  deedglass lockdown off --as priya@acme.test --reason \"Pilot over\"\n  deedglass lockdown off --countersign --as sam@acme.test\n  deedglass lockdown ack act_01k6w4… --as priya@acme.test --reason \"Read back by hand: the row is right\""

// lockdownSession opens the database, the people service and the org.
func lockdownSession(ctx context.Context, e *Env) (*config.Config, *store.DB, *identity.Service, store.Org, func(), error) {
	cfg, db, _, people, done, err := openPeople(ctx, e)
	if err != nil {
		return nil, nil, nil, store.Org{}, nil, err
	}
	org, err := pickOrg(ctx, e, db)
	if err != nil {
		done()
		return nil, nil, nil, store.Org{}, nil, err
	}
	return cfg, db, people, org, done, nil
}

// lockdownPerson resolves --as to a user id.
func lockdownPerson(ctx context.Context, e *Env, db *store.DB, cmd string) (ids.UserID, string, error) {
	if strings.TrimSpace(lockdownValues.as) == "" {
		return ids.UserID{}, "", usageErr(errors.New("--as names the owner or admin this is done for"), cmd+" --as priya@acme.test")
	}
	email, err := identity.NormalizeEmail(lockdownValues.as)
	if err != nil {
		return ids.UserID{}, "", failNext(e, ExitUsage, "validation_failed", err.Error(), "")
	}
	u, err := db.PreAuth().UserByEmail(ctx, email)
	if err != nil {
		return ids.UserID{}, "", failNext(e, ExitFailure, "not_found", fmt.Sprintf("%s isn't a member of any org here.", email), "Invite them first, or check the address.")
	}
	return u.ID, email, nil
}

func lockdownErr(e *Env, err error) error {
	var le *identity.LockdownError
	if errors.As(err, &le) {
		return failNext(e, ExitFailure, le.Code, le.Message, le.Next)
	}
	return failNext(e, ExitFailure, "lockdown_failed", "Nothing changed: "+lastPart(err)+".", "Check the database, then run it again.")
}

func noLockdownArgs(name string, pos []string) error {
	if len(pos) > 0 {
		return usageErr(fmt.Errorf("deedglass lockdown %s takes flags only (got %q)", name, clean(pos[0])), "deedglass lockdown "+name)
	}
	return nil
}

func runLockdownStatus(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass lockdown status", lockdownFlags, args)
	if err != nil {
		return err
	}
	if err := noLockdownArgs("status", pos); err != nil {
		return err
	}
	cfg, _, people, org, done, err := lockdownSession(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	st, err := people.LockdownStatus(ctx, org.ID)
	if err != nil {
		return lockdownErr(e, err)
	}
	return printLockdown(e, cfg, org, st, "")
}

func runLockdownOn(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass lockdown on", lockdownFlags, args)
	if err != nil {
		return err
	}
	if err := noLockdownArgs("on", pos); err != nil {
		return err
	}
	cfg, _, people, org, done, err := lockdownSession(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	st, err := people.LockdownOn(ctx, org.ID, lockdownValues.reason)
	if err != nil {
		return lockdownErr(e, err)
	}
	head := "Lockdown is on for " + org.Name
	if st.ChangedDeed == nil {
		head = "Lockdown was already on for " + org.Name + " " + ui.Sep + " no new record"
	}
	return printLockdown(e, cfg, org, st, head)
}

func runLockdownOff(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass lockdown off", lockdownFlags, args)
	if err != nil {
		return err
	}
	if err := noLockdownArgs("off", pos); err != nil {
		return err
	}
	cfg, db, people, org, done, err := lockdownSession(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	user, email, err := lockdownPerson(ctx, e, db, "deedglass lockdown off")
	if err != nil {
		return err
	}
	st, err := people.LockdownOff(ctx, org.ID, user, email, lockdownValues.countersign, cfg.Lockdown, lockdownValues.reason, time.Now())
	if err != nil {
		return lockdownErr(e, err)
	}
	head := "Lockdown is off for " + org.Name + " " + ui.Sep + " countersigned by " + email
	if !lockdownValues.countersign {
		head = fmt.Sprintf("Asked to turn lockdown off for %s %s waiting for a second person until %s UTC", org.Name, ui.Sep, st.OffRequest.ExpiresAt.Format("15:04"))
	}
	return printLockdown(e, cfg, org, st, head)
}

func runLockdownAck(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass lockdown ack", lockdownFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr(errors.New("deedglass lockdown ack needs one action id"), `deedglass lockdown ack act_… --as priya@acme.test --reason "…"`)
	}
	act, perr := ids.Parse[ids.ActionKind](strings.ToLower(strings.TrimSpace(pos[0])))
	if perr != nil {
		return usageErr(fmt.Errorf("%q isn't an action id", clean(pos[0])), "deedglass lockdown status lists the actions that hold an agent.")
	}
	_, db, people, org, done, err := lockdownSession(ctx, e)
	if err != nil {
		return err
	}
	defer done()
	user, email, err := lockdownPerson(ctx, e, db, "deedglass lockdown ack "+act.String())
	if err != nil {
		return err
	}
	d, err := people.LockdownAck(ctx, org.ID, user, email, act, lockdownValues.reason)
	if err != nil {
		return lockdownErr(e, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "lockdown_ack", "org": org.ID.String(), "action": act.String(), "deed": d})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Acknowledged %s %s its agent's writes can run again %s %s sealed", act, ui.Sep, ui.Sep, deref(d))))
	return nil
}

// printLockdown writes the status: the switch, then what weakens it.
func printLockdown(e *Env, cfg *config.Config, org store.Org, st identity.LockdownStatus, head string) error {
	state := lockdown.State{Config: cfg.Lockdown, Org: st.OrgSetting}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "lockdown", "org": org.ID.String(), "on": state.On(), "source": state.Source(),
			"config": cfg.Lockdown, "status": st, "weakened_by": lockdownWeak(cfg, st)})
	}
	w := e.Stdout
	if head != "" {
		fmt.Fprintln(w, e.Style.OKLine(head))
	}
	switch {
	case state.On():
		fmt.Fprintln(w, e.Style.OKLine(fmt.Sprintf("Lockdown on %s %s %s set by %s", ui.Sep, org.Name, ui.Sep, lockdownSourceText(state))))
	default:
		fmt.Fprintln(w, e.Style.UnknownLine(fmt.Sprintf("Lockdown off %s %s", ui.Sep, org.Name)))
	}
	var rows []ui.Row
	row := func(k, v string) { rows = append(rows, ui.Row{Key: k, Value: v}) }
	if st.ChangedDeed != nil {
		row("record", ui.ShortID(*st.ChangedDeed)+", sealed")
	}
	if r := st.OffRequest; r != nil {
		row("off request", fmt.Sprintf("%s asked; countersign by %s UTC", r.Email, r.ExpiresAt.Format("15:04")))
	}
	row("approvers", fmt.Sprintf("%d %s", st.Approvers, plural(st.Approvers, "owner or admin", "owners or admins")))
	for _, h := range st.Holds {
		row("held", fmt.Sprintf("%s, Can't check, %s", h.Name, h.Action))
	}
	ui.Lines(w, e.Style.KV("  ", e.Width, rows...)...)
	for _, line := range lockdownWeak(cfg, st) {
		fmt.Fprintln(w, "  "+e.Style.Amber(ui.Unknown)+" "+line)
	}
	if !state.On() {
		fmt.Fprintln(w, e.Style.Next("deedglass lockdown on"))
	} else if len(st.Holds) > 0 {
		fmt.Fprintln(w, e.Style.Next("Look at the held action, then deedglass lockdown ack "+st.Holds[0].Action+" --as <email>"))
	}
	return nil
}

func lockdownSourceText(s lockdown.State) string {
	switch s.Source() {
	case lockdown.SourceConfig:
		return "lockdown: true in the config"
	case lockdown.SourceOrg:
		return "the org"
	}
	return "the org and lockdown: true in the config"
}

// lockdownWeak lists what weakens lockdown for this org, each with what to do (doctor shows the
// same list from the API).
func lockdownWeak(cfg *config.Config, st identity.LockdownStatus) []string {
	out := []string{}
	if !cfg.Lockdown && !st.OrgSetting {
		return out
	}
	if cfg.SealPQKeyFile == "" {
		out = append(out, "No ML-DSA-65 seal key: records carry one seal. Run deedglass keys rotate --alg ML-DSA-65.")
	}
	if !cfg.Timestamps.Enabled() {
		out = append(out, "No TSAs configured: no outside timestamps. Set timestamps.tsa_urls (docs/TIMESTAMPS.md).")
	}
	if st.Approvers < 2 {
		out = append(out, "Fewer than two owners or admins: high-risk writes, which need two people, are refused. Invite a second admin.")
	}
	if n := len(st.Unlabelled); n > 0 {
		out = append(out, fmt.Sprintf("%d %s without a connector label, so every call is refused: %s.", n, plural(n, "agent", "agents"), strings.Join(st.Unlabelled, ", ")))
	}
	if st.FHIR > 0 && !st.PHIMode {
		out = append(out, "A FHIR connector and PHI mode off, so its calls are refused. Run deedglass lockdown on again, or turn PHI mode on.")
	}
	return out
}
