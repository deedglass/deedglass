// Package agency is the Agency plan's view (S52 §3.2, ADR-101): the Clients overview across every
// org a person looks after, monthly reports for every client in one run, and the agency's name on
// them.
//
// Separation stays absolute. A client is an org the person belongs to as owner or admin; the
// list of memberships comes from the caller (the same narrow lookup GET /v1/me uses), and every
// number is read inside that client org's own read-only transaction, under its own row-level
// security, after re-checking the membership there. Nothing here opens a system transaction or
// calls a cross-org function (TestNoCrossOrgReads), and no new relationship between orgs exists.
//
// It never reads a licence: serve and the CLI ask cmd/deedglass/licence.go for the agency feature
// and refuse with pro_required before calling in. Each client's own report and the Monday email
// stay free (internal/weekly).
//
// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package agency

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/weekly"
)

// Limits.
const (
	MaxClients     = 200 // one person's client orgs read per load, at most
	DefaultLimit   = 100
	ChainBudget    = 250 * time.Millisecond // per client; one that runs out is "not checked"
	clientsTimeout = "5s"
)

// Membership is one of the person's memberships, from the caller's lookup.
type Membership struct {
	OrgID     ids.OrgID
	Role      string // owner | admin | viewer
	Status    string // active | invited | removed
	OrgStatus string // active | …
}

// Client roles: viewers aren't clients (they can't act for the org).
func clientRole(r string) bool { return r == "owner" || r == "admin" }

// Row is one client org this week.
type Row struct {
	Object       string     `json:"object"` // "client"
	Org          string     `json:"org"`
	Name         string     `json:"name"`
	Slug         string     `json:"slug"`
	Role         string     `json:"role"`
	Actions      int64      `json:"actions"`
	DoesntMatch  int64      `json:"doesnt_match"`
	Caught       int64      `json:"caught"`
	Waiting      int64      `json:"waiting"`
	Stopped      bool       `json:"stopped"`
	Chain        string     `json:"chain"` // intact | broken | not_checked | no_records
	LastActivity *time.Time `json:"last_activity"`
}

// List is GET /v1/clients.
type List struct {
	Object  string        `json:"object"` // "list"
	Period  weekly.Period `json:"period"`
	Data    []Row         `json:"data"`
	HasMore bool          `json:"has_more"`
	Next    *string       `json:"next_cursor"`
	Total   int           `json:"total"` // client orgs after --exclude, all pages
}

// Options are one load's inputs.
type Options struct {
	Exclude []string // org slugs (or ids) to leave out: usually the agency's own org
	Limit   int      // 1..MaxClients; default DefaultLimit
	Cursor  string   // an org id from next_cursor
	Period  weekly.Period
	Now     time.Time
}

// Reader reads one client's row inside that org's own transaction; ok false means the person is
// no longer an owner or admin there (the row disappears on this load).
type Reader interface {
	Row(ctx context.Context, org ids.OrgID, user ids.UserID, p weekly.Period, now time.Time) (Row, bool, error)
}

// ErrCursor is a cursor that isn't one of the person's clients.
var ErrCursor = errors.New("agency: the cursor isn't one of your clients; start again without it")

// Clients loads the person's client orgs, one org-scoped read each.
func Clients(ctx context.Context, r Reader, user ids.UserID, ms []Membership, o Options) (List, error) {
	if o.Limit <= 0 {
		o.Limit = DefaultLimit
	}
	if o.Limit > MaxClients {
		o.Limit = MaxClients
	}
	ex := map[string]bool{}
	for _, e := range o.Exclude {
		if e = strings.TrimSpace(e); e != "" {
			ex[e] = true
		}
	}
	var orgs []ids.OrgID
	seen := map[ids.OrgID]bool{}
	for _, m := range ms {
		if m.Status != "active" || m.OrgStatus != "active" || !clientRole(m.Role) || seen[m.OrgID] || ex[m.OrgID.String()] {
			continue
		}
		seen[m.OrgID] = true
		orgs = append(orgs, m.OrgID)
	}
	sort.Slice(orgs, func(i, j int) bool { return orgs[i].String() < orgs[j].String() })
	if len(orgs) > MaxClients*50 {
		orgs = orgs[:MaxClients*50] // a hard ceiling on one person's memberships walked
	}
	out := List{Object: "list", Period: o.Period, Data: []Row{}}
	start := 0
	if o.Cursor != "" {
		start = -1
		for i, id := range orgs {
			if id.String() == o.Cursor {
				start = i + 1
			}
		}
		if start < 0 {
			return List{}, ErrCursor
		}
	}
	// Excluded slugs are only known after a read (the slug lives in the org's own row), so rows
	// are read in order and an excluded one is skipped.
	for i := start; i < len(orgs); i++ {
		if len(out.Data) == o.Limit {
			out.HasMore = true
			next := out.Data[len(out.Data)-1].Org
			out.Next = &next
			break
		}
		row, ok, err := r.Row(ctx, orgs[i], user, o.Period, o.Now)
		if err != nil {
			return List{}, err
		}
		if !ok || ex[row.Slug] {
			continue
		}
		out.Data = append(out.Data, row)
	}
	out.Total = len(orgs)
	return out, nil
}

// --- the Postgres reader ---

// OrgReader is the one store method the reader uses: a read-only transaction scoped to one org
// (RLS on). A test wraps it to see every read stay inside its own org.
type OrgReader interface {
	OrgReadTx(ctx context.Context, org ids.OrgID, fn store.TxFunc) error
}

// PG reads client rows from Deedglass's own database.
type PG struct {
	DB     OrgReader
	Ledger *ledger.Ledger // the chain check; nil: "not checked"
}

// inFlight are the action states that haven't finished.
const inFlight = `'received', 'authenticated', 'evaluated', 'pending_approval', 'executing', 'witnessing'`

// Row reads one client inside its own read-only transaction: the membership again, the org, and
// this period's counts.
func (p PG) Row(ctx context.Context, org ids.OrgID, user ids.UserID, per weekly.Period, now time.Time) (Row, bool, error) {
	row := Row{Object: "client", Org: org.String(), Chain: weekly.ChainNotChecked}
	member := false
	err := p.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout', '`+clientsTimeout+`', true)`); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT m.role FROM deedglass.memberships AS m JOIN deedglass.orgs AS o ON o.id = m.org_id
		    WHERE m.org_id = $1 AND m.user_id = $2 AND m.status = 'active' AND o.status = 'active'`, org, user).Scan(&row.Role)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !clientRole(row.Role) {
			return nil
		}
		member = true
		var stopped *time.Time
		if err := tx.QueryRow(ctx, `SELECT name, slug, stopped_at FROM deedglass.orgs WHERE id = $1`, org).Scan(&row.Name, &row.Slug, &stopped); err != nil {
			return err
		}
		row.Stopped = stopped != nil
		var last *time.Time
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FILTER (WHERE a.created_at >= $2 AND a.created_at < $3),
		    pg_catalog.count(*) FILTER (WHERE a.created_at >= $2 AND a.created_at < $3 AND a.witness_verdict = 'MISMATCH'),
		    pg_catalog.count(*) FILTER (WHERE a.created_at >= $2 AND a.created_at < $3 AND a.witness_verdict = 'MISMATCH' AND a.witness_level IN ('L1', 'L2')),
		    pg_catalog.max(a.created_at)
		  FROM deedglass.actions AS a JOIN deedglass.agents AS g ON g.org_id = a.org_id AND g.id = a.agent_id
		  WHERE a.org_id = $1 AND g.kind <> 'system' AND a.created_at >= $2 - interval '400 days'`, org, per.From, per.To).
			Scan(&row.Actions, &row.DoesntMatch, &row.Caught, &last); err != nil {
			return err
		}
		row.LastActivity = last
		var undone, sessions int64
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.actions WHERE org_id = $1 AND undoes_action_id IS NOT NULL
		    AND created_at >= $2 AND created_at < $3 AND state NOT IN (`+inFlight+`, 'denied', 'expired', 'cancelled', 'failed')`,
			org, per.From, per.To).Scan(&undone); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.observe_sessions WHERE org_id = $1
		    AND opened_at >= $2 AND opened_at < $3 AND 'caught' = ANY (flags)`, org, per.From, per.To).Scan(&sessions); err != nil {
			return err
		}
		row.Caught += undone + sessions
		return tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.approvals WHERE org_id = $1 AND status = 'pending'`, org).Scan(&row.Waiting)
	})
	if err != nil || !member {
		return Row{}, false, err
	}
	row.Chain = p.chain(ctx, org, per)
	return row, true, nil
}

func (p PG) chain(ctx context.Context, org ids.OrgID, per weekly.Period) string {
	if p.Ledger == nil {
		return weekly.ChainNotChecked
	}
	from, to := per.From, per.To
	cc, err := p.Ledger.CheckChain(ctx, org, ledger.Range{IssuedGTE: &from, IssuedLT: &to}, ledger.CheckOptions{Budget: ChainBudget})
	if errors.Is(err, ledger.ErrEmptyRange) {
		head, herr := p.Ledger.HeadSeq(ctx, org)
		if herr != nil || head == 0 {
			return weekly.ChainNoRecords
		}
		if cc, err = p.Ledger.CheckChain(ctx, org, ledger.Range{FromSeq: head}, ledger.CheckOptions{Budget: ChainBudget}); err != nil {
			return weekly.ChainNotChecked
		}
	} else if err != nil {
		return weekly.ChainNotChecked
	}
	switch cc.Status {
	case ledger.CheckIntact:
		return weekly.ChainIntact
	case ledger.CheckBroken:
		return weekly.ChainBroken
	}
	return weekly.ChainNotChecked
}
