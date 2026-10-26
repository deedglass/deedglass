package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deedglass/deedglass/ee/agency"
	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/setup"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/weekly"
	"github.com/deedglass/deedglass/internal/weekly/pgsource"
)

// deedglass report and deedglass clients (S52 §3.1, §3.2). Both read the server's database
// directly (DB-direct, like deedglass actions list: DEEDGLASS_DATABASE_URL, or .deedglass/stack.json
// after deedglass init). report is free for one org; --all-clients and --brand are the Agency plan,
// and so is clients. Every client is read in its own org transaction.

var reportNow = time.Now // tests pin the clock

var reportValues struct {
	week, month, format, out, as, brand string
	exclude                             string
	allClients                          bool
}

func reportFlags(fs *flag.FlagSet) {
	reportValues = struct {
		week, month, format, out, as, brand string
		exclude                             string
		allClients                          bool
	}{}
	fs.StringVar(&reportValues.week, "week", "", "an ISO week like 2026-W43 (default: the last complete week)")
	fs.StringVar(&reportValues.month, "month", "", "a month like 2026-10")
	fs.StringVar(&reportValues.format, "format", "", "md, html or json (default md; with --all-clients, all three; a list like html,md)")
	fs.StringVar(&reportValues.out, "out", "", "the file to write (default: print it); with --all-clients, the folder")
	fs.BoolVar(&reportValues.allClients, "all-clients", false, "one report for every org you look after as owner or admin")
	fs.StringVar(&reportValues.as, "as", "", "with --all-clients: the person whose client orgs to report on (default: the owner deedglass init set up)")
	fs.StringVar(&reportValues.brand, "brand", "", "brand.yaml with name, contact_line and logo_file: your agency's name on each report")
	fs.StringVar(&reportValues.exclude, "exclude", "", "with --all-clients: org slugs to leave out, comma-separated (usually your own org)")
}

// reportDB is a DB-direct read: the database, the Ledger, and the stack deedglass init wrote.
type reportDB struct {
	db    *store.DB
	led   *ledger.Ledger
	stack *setup.Stack
}

func openReportDB(ctx context.Context, e *Env) (*reportDB, error) {
	opts, st, err := stackLoadOptions(e)
	if err != nil {
		return nil, err
	}
	cfg, warnings, err := config.Load(opts)
	var ps *config.Problems
	if errors.As(err, &ps) {
		return nil, configProblems(e, ps, warnings)
	}
	if err != nil {
		return nil, err
	}
	if err := cfg.Require(config.NeedDatabase); errors.As(err, &ps) {
		return nil, configProblems(e, ps, warnings)
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, store.Options{MaxConns: 4})
	if err != nil {
		return nil, failNext(e, ExitUsage, "database_unreachable", "Can't reach the database in DEEDGLASS_DATABASE_URL.",
			"Check that Postgres is running and that migrations ran (deedglass migrate up).")
	}
	return &reportDB{db: db, led: newLedger(db, nil, cfg, slog.New(slog.DiscardHandler)), stack: st}, nil
}

// reportPeriod reads --week/--month in loc; def is the default (last week or last month).
func reportPeriod(loc *time.Location, monthly bool) (weekly.Period, error) {
	v := reportValues
	switch {
	case v.week != "" && v.month != "":
		return weekly.Period{}, usageErr(errors.New("--week and --month can't be used together"), "Pick one: --week 2026-W43 or --month 2026-10.")
	case v.week != "":
		p, err := weekly.ParsePeriod(v.week, loc)
		if err != nil || p.Kind != weekly.KindWeek {
			return p, usageErr(fmt.Errorf("--week %q isn't an ISO week", clean(v.week)), "For example --week 2026-W43.")
		}
		return p, nil
	case v.month != "":
		p, err := weekly.ParsePeriod(v.month, loc)
		if err != nil || p.Kind != weekly.KindMonth {
			return p, usageErr(fmt.Errorf("--month %q isn't a month", clean(v.month)), "For example --month 2026-10.")
		}
		return p, nil
	case monthly:
		return weekly.LastMonth(reportNow(), loc), nil
	}
	return weekly.LastWeek(reportNow(), loc), nil
}

func reportFormats(def []string) ([]string, error) {
	if reportValues.format == "" {
		return def, nil
	}
	var out []string
	for _, f := range strings.Split(reportValues.format, ",") {
		f = strings.TrimSpace(f)
		if f != agency.FormatMD && f != agency.FormatHTML && f != agency.FormatJSON {
			return nil, usageErr(fmt.Errorf("--format %q isn't md, html or json", clean(f)), "For example --format html.")
		}
		out = append(out, f)
	}
	return out, nil
}

func runReport(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass report", reportFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageErr(fmt.Errorf("deedglass report takes no arguments (got %q)", clean(pos[0])), "deedglass report [--week 2026-W43 | --month 2026-10] [--format md]")
	}
	if reportValues.allClients || reportValues.brand != "" {
		if err := agencyAllowed(ctx, e); err != nil {
			return err
		}
	}
	if reportValues.allClients {
		return runReportAllClients(ctx, e)
	}
	if reportValues.as != "" || reportValues.exclude != "" {
		return usageErr(errors.New("--as and --exclude work only with --all-clients"), "deedglass report --all-clients --month 2026-10 --exclude <your-org>")
	}
	def := []string{agency.FormatMD}
	if e.Globals.JSON && reportValues.out == "" {
		def = []string{agency.FormatJSON} // --json on stdout is the report as JSON (QA-1)
	}
	formats, err := reportFormats(def)
	if err != nil {
		return err
	}
	if len(formats) != 1 {
		return usageErr(errors.New("one org's report takes one --format"), "For example --format html --out report.html.")
	}
	var brand *weekly.Brand
	if reportValues.brand != "" {
		if brand, err = agency.LoadBrand(reportValues.brand); err != nil {
			return usageErrCode("brand_invalid", err, "Fix the brand file and run the command again.")
		}
	}
	r, err := openReportDB(ctx, e)
	if err != nil {
		return err
	}
	defer r.db.Close()
	org, err := pickOrg(ctx, e, r.db)
	if err != nil {
		return err
	}
	p, err := reportPeriod(weekly.LoadLocation(org.Timezone), false)
	if err != nil {
		return err
	}
	src := &pgsource.Source{DB: r.db, Ledger: r.led}
	s, err := src.Summarize(ctx, org.ID, p, reportNow().UTC())
	if err != nil {
		return failNext(e, ExitFailure, "database_error", "Can't read this org's records for the report.", "Run deedglass doctor, then try again.")
	}
	opts := weekly.Options{Brand: brand}
	body, err := renderReport(s, formats[0], opts)
	if err != nil {
		return err
	}
	if reportValues.out == "" {
		_, err := e.Stdout.Write(body)
		return err
	}
	if err := writeReportFile(reportValues.out, body); err != nil {
		return err
	}
	if !e.Globals.JSON {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%s %s %s %s %s", reportValues.out, ui.Sep, clean(org.Slug), ui.Sep, p.Label)))
		fmt.Fprintln(e.Stdout, "  Built from this org's own records; numbers, ids and names only.")
	}
	return nil
}

func renderReport(s weekly.Summary, format string, o weekly.Options) ([]byte, error) {
	switch format {
	case agency.FormatHTML:
		h, err := weekly.HTML(s, o)
		return []byte(h), err
	case agency.FormatJSON:
		return weekly.JSON(s)
	}
	return []byte(weekly.Markdown(s, o)), nil
}

func writeReportFile(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return usageErrCode("report_exists", fmt.Errorf("%s already exists", clean(path)), "Choose another --out, or move the old report first.")
	}
	if err != nil {
		return err
	}
	_, werr := f.Write(body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// person resolves --as: the person whose client orgs to read.
func (r *reportDB) person(ctx context.Context, e *Env, as string) (ids.UserID, []agency.Membership, error) {
	email := strings.TrimSpace(as)
	if email == "" && r.stack != nil {
		email = r.stack.OwnerEmail
	}
	if email == "" {
		return ids.UserID{}, nil, usageErrCode("person_required", errors.New("name the person whose client orgs to list"), "Add --as you@agency.com.")
	}
	u, err := r.db.PreAuth().UserByEmail(ctx, strings.ToLower(email))
	if errors.Is(err, store.ErrNotFound) {
		return ids.UserID{}, nil, usageErrCode("person_not_found", fmt.Errorf("no person %s on this server", clean(email)), "Check the address, or sign in to the Console once.")
	}
	if err != nil {
		return ids.UserID{}, nil, err
	}
	var ms []agency.Membership
	for _, m := range u.Memberships {
		ms = append(ms, agency.Membership{OrgID: m.OrgID, Role: m.Role, Status: m.Status, OrgStatus: m.OrgStatus})
	}
	return u.ID, ms, nil
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func runReportAllClients(ctx context.Context, e *Env) error {
	if reportValues.out == "" {
		return usageErr(errors.New("--all-clients writes a folder: name it with --out"), "For example: deedglass report --all-clients --month 2026-10 --out reports/")
	}
	formats, err := reportFormats(nil)
	if err != nil {
		return err
	}
	var brand *weekly.Brand
	if reportValues.brand != "" {
		if brand, err = agency.LoadBrand(reportValues.brand); err != nil {
			return usageErrCode("brand_invalid", err, "Fix the brand file and run the command again.")
		}
	}
	r, err := openReportDB(ctx, e)
	if err != nil {
		return err
	}
	defer r.db.Close()
	user, ms, err := r.person(ctx, e, reportValues.as)
	if err != nil {
		return err
	}
	key, err := reportPeriod(time.UTC, true)
	if err != nil {
		return err
	}
	pg := agency.PG{DB: r.db, Ledger: r.led}
	var clients []agency.Row
	cursor := ""
	for {
		l, err := agency.Clients(ctx, pg, user, ms, agency.Options{Exclude: splitList(reportValues.exclude), Limit: agency.MaxClients, Cursor: cursor,
			Period: key, Now: reportNow().UTC()})
		if err != nil {
			return err
		}
		clients = append(clients, l.Data...)
		if !l.HasMore {
			break
		}
		cursor = *l.Next
	}
	if len(clients) == 0 {
		return failNext(e, ExitFailure, "no_clients", "You look after no client orgs on this server.", "Create one with deedglass org create, then invite yourself.")
	}
	src := &pgsource.Source{DB: r.db, Ledger: r.led}
	sum := func(ctx context.Context, c agency.Row) (weekly.Summary, error) {
		org, err := ids.Parse[ids.OrgKind](c.Org)
		if err != nil {
			return weekly.Summary{}, err
		}
		_, tz, err := src.Org(ctx, org)
		if err != nil {
			return weekly.Summary{}, err
		}
		p, err := weekly.ParsePeriod(key.Key, weekly.LoadLocation(tz))
		if err != nil {
			return weekly.Summary{}, err
		}
		return src.Summarize(ctx, org, p, reportNow().UTC())
	}
	res, err := agency.WriteReports(ctx, reportValues.out, clients, key, formats, brand, sum, weekly.Options{})
	if errors.Is(err, agency.ErrExists) {
		return usageErrCode("report_exists", err, "Choose an empty folder for --out.")
	}
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, map[string]any{"object": "client_reports", "folder": reportValues.out, "period": key.Key, "clients": len(clients),
			"files": res.Files, "index": res.Index, "failed": res.Failed})
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%d client %s %s %s %s %d files in %s", len(clients), plural(len(clients), "report", "reports"),
		ui.Sep, key.Label, ui.Sep, len(res.Files)+1, filepath.Clean(reportValues.out))))
	if brand != nil {
		fmt.Fprintln(e.Stdout, "  Prepared by "+brand.Name+" with Deedglass. Each report holds one client's own records only.")
	} else {
		fmt.Fprintln(e.Stdout, "  Each report holds one client's own records only. Start with index.md.")
	}
	for _, f := range res.Failed {
		fmt.Fprintln(e.Stdout, e.Style.UnknownLine("Couldn't build "+f+": run deedglass report --org "+f))
	}
	return nil
}

// --- clients ---

var clientsValues struct {
	as, exclude, week string
}

func clientsFlags(fs *flag.FlagSet) {
	clientsValues = struct{ as, exclude, week string }{}
	fs.StringVar(&clientsValues.as, "as", "", "the person whose client orgs to list (default: the owner deedglass init set up)")
	fs.StringVar(&clientsValues.exclude, "exclude", "", "org slugs to leave out, comma-separated (usually your own org)")
	fs.StringVar(&clientsValues.week, "week", "", "an ISO week like 2026-W43 (default: this week so far)")
}

func runClients(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass clients", clientsFlags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageErr(fmt.Errorf("deedglass clients takes no arguments (got %q)", clean(pos[0])), "deedglass clients [--json] [--exclude <your-org>]")
	}
	if err := agencyAllowed(ctx, e); err != nil {
		return err
	}
	p, err := agency.PeriodParam(clientsValues.week, reportNow().UTC())
	if err != nil || p.Kind != weekly.KindWeek {
		return usageErr(fmt.Errorf("--week %q isn't an ISO week", clean(clientsValues.week)), "For example --week 2026-W43.")
	}
	r, err := openReportDB(ctx, e)
	if err != nil {
		return err
	}
	defer r.db.Close()
	user, ms, err := r.person(ctx, e, clientsValues.as)
	if err != nil {
		return err
	}
	l, err := agency.Clients(ctx, agency.PG{DB: r.db, Ledger: r.led}, user, ms, agency.Options{Exclude: splitList(clientsValues.exclude),
		Limit: agency.MaxClients, Period: p, Now: reportNow().UTC()})
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, l)
	}
	printClients(e, l, clientsValues.week == "")
	return nil
}

func printClients(e *Env, l agency.List, current bool) {
	when := "this week"
	if !current {
		when = l.Period.Label
	}
	fmt.Fprintf(e.Stdout, "Clients %s %d %s %s\n", ui.Sep, len(l.Data), ui.Sep, when)
	if len(l.Data) == 0 {
		fmt.Fprintln(e.Stdout, "You look after no client orgs. Add a client by creating its org: deedglass org create …, then invite yourself.")
		return
	}
	w := 0
	for _, c := range l.Data {
		w = max(w, len(clean(c.Slug)))
	}
	for _, c := range l.Data {
		chain := chainWords(c.Chain)
		line := ui.Pad(clean(c.Slug), w+2)
		if c.Actions == 0 {
			line += fmt.Sprintf("%5d actions   %s", 0, ui.Pad("no activity "+when, 51))
		} else {
			line += fmt.Sprintf("%5s actions   %s don't match   %s caught   %s waiting", weekly.N(c.Actions), weekly.N(c.DoesntMatch), weekly.N(c.Caught), weekly.N(c.Waiting))
			line += "   "
		}
		if c.Stopped {
			line += e.Style.Red("stopped") + "   "
		}
		line += chain
		fmt.Fprintln(e.Stdout, line)
	}
	if l.HasMore {
		fmt.Fprintln(e.Stdout, "More clients: deedglass clients --json lists them all.")
	}
	month := weekly.LastMonth(reportNow(), time.UTC).Key
	fmt.Fprintln(e.Stdout, "Monthly reports for all: deedglass report --all-clients --month "+month+" --brand brand.yaml --out reports/")
}

func chainWords(s string) string {
	switch s {
	case weekly.ChainIntact:
		return "chain intact"
	case weekly.ChainBroken:
		return "chain broken"
	case weekly.ChainNoRecords:
		return "no records yet"
	}
	return "chain not checked"
}
