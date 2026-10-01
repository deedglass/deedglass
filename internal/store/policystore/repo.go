package policystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// Repo is the Postgres policy.Repo (S12 §3.3): org-scoped transactions through store.OrgTx
// (row-level security), sealed changes through ledger.RecordAdmin, events through ledger.Publish
// and the change feed through store.NotifyPolicy.
type Repo struct {
	db  *store.DB
	led *ledger.Ledger
}

// NewRepo returns the repo. led seals activation deeds; it may be nil for read-only use (the
// registry), where RecordAdmin fails.
func NewRepo(db *store.DB, led *ledger.Ledger) *Repo { return &Repo{db: db, led: led} }

var _ policy.Repo = (*Repo)(nil)

func orgID(org string) (ids.OrgID, error) {
	id, err := ids.Parse[ids.OrgKind](org)
	if err != nil {
		return ids.OrgID{}, store.ErrNoOrg
	}
	return id, nil
}

// InOrgTx implements policy.Repo.
func (r *Repo) InOrgTx(ctx context.Context, org string, fn func(policy.Tx) error) error {
	id, err := orgID(org)
	if err != nil {
		return err
	}
	return r.db.OrgTx(ctx, id, func(ctx context.Context, tx pgx.Tx) error {
		return fn(&Tx{tx: tx, org: org, db: r.db, led: r.led})
	})
}

// InOrgReadTx implements policy.Repo.
func (r *Repo) InOrgReadTx(ctx context.Context, org string, fn func(policy.Tx) error) error {
	id, err := orgID(org)
	if err != nil {
		return err
	}
	return r.db.OrgReadTx(ctx, id, func(ctx context.Context, tx pgx.Tx) error {
		return fn(&Tx{tx: tx, org: org, db: r.db, led: r.led})
	})
}

// TxFor wraps a transaction that is already scoped to org (org creation's SystemTx after
// store.ScopeSystemTxToOrg), for policy.Bootstrap.
func TxFor(tx pgx.Tx, org string, db *store.DB, led *ledger.Ledger) *Tx {
	return &Tx{tx: tx, org: org, db: db, led: led}
}

const loadActiveSQL = `SELECT a.id, a.policy_id, a.version_id, a.mode, COALESCE(a.activated_by, ''), a.activated_at, COALESCE(a.deed_id, ''),
    COALESCE(a.reason, ''), v.version, p.name,
    v.yaml, v.version_hash, v.source_hash, v.lang, v.rules_count, v.budgets_count, v.origin,
    d.envelope, e.data::text
  FROM deedglass.policy_activations AS a
  JOIN deedglass.policy_versions AS v ON v.org_id = a.org_id AND v.id = a.version_id
  JOIN deedglass.policies AS p ON p.org_id = a.org_id AND p.id = a.policy_id
  LEFT JOIN deedglass.deeds AS d ON d.org_id = a.org_id AND d.id = a.deed_id
  LEFT JOIN deedglass.admin_events AS e ON e.org_id = a.org_id AND e.deed_id = a.deed_id
 WHERE a.deactivated_at IS NULL
 ORDER BY a.mode`

// LoadActive implements policy.Repo: the live activations with their versions, deeds and event
// data in one read-only transaction, and the deployment's seal keys.
func (r *Repo) LoadActive(ctx context.Context, org string) (*policy.ActiveRows, error) {
	out := &policy.ActiveRows{}
	err := r.InOrgReadTx(ctx, org, func(t policy.Tx) error {
		tx := t.(*Tx).tx
		rows, err := tx.Query(ctx, loadActiveSQL)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var lr policy.LoadedRow
			a, v := &lr.Activation, &lr.Version
			var mode, origin string
			var data *string
			if err := rows.Scan(&a.ID, &a.PolicyID, &a.VersionID, &mode, &a.ActivatedBy, &a.ActivatedAt, &a.DeedID, &a.Reason,
				&a.Version, &a.PolicyName, &v.Source, &v.VersionHash, &v.SourceHash, &v.Lang, &v.Rules, &v.Budgets, &origin,
				&lr.Deed, &data); err != nil {
				return err
			}
			a.Mode, v.Origin = policy.Mode(mode), policy.Origin(origin)
			v.ID, v.PolicyID, v.Version, v.Name = a.VersionID, a.PolicyID, a.Version, a.PolicyName
			if data != nil {
				lr.DeedData = []byte(*data)
			}
			out.Rows = append(out.Rows, lr)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	keys, err := r.db.SealKeys(ctx)
	if err != nil {
		return nil, err
	}
	recs := make([]deed.KeyRecord, 0, len(keys))
	for _, k := range keys {
		recs = append(recs, k.Record())
	}
	if len(recs) > 0 {
		if out.Keys, err = deed.NewKeySet("", recs); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// LiveActivationIDs implements policy.Repo.
func (r *Repo) LiveActivationIDs(ctx context.Context, org string) ([]string, error) {
	var out []string
	err := r.InOrgReadTx(ctx, org, func(t policy.Tx) error {
		rows, err := t.(*Tx).tx.Query(ctx, `SELECT id FROM deedglass.policy_activations WHERE deactivated_at IS NULL`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return out, err
}

// Tx is one org-scoped transaction (policy.Tx).
type Tx struct {
	tx  pgx.Tx
	org string
	db  *store.DB
	led *ledger.Ledger
}

var _ policy.Tx = (*Tx)(nil)

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return policy.ErrNotFound
	}
	return err
}

// NewID implements policy.Tx.
func (t *Tx) NewID(prefix string) string {
	switch prefix {
	case ids.Policy:
		return ids.New[ids.PolicyKind]().String()
	case ids.PolicyVersion:
		return ids.New[ids.PolicyVersionKind]().String()
	case ids.PolicyActivation:
		return ids.New[ids.PolicyActivationKind]().String()
	}
	panic("policystore: no id kind for prefix " + prefix)
}

// LockOrgPolicies implements policy.Tx: one policy change per org at a time.
func (t *Tx) LockOrgPolicies(ctx context.Context) error {
	_, err := t.tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('deedglass/policy/' || $1::text, 0))`, t.org)
	return err
}

// InsertPolicy implements policy.Tx.
func (t *Tx) InsertPolicy(ctx context.Context, p policy.PolicyRow) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO deedglass.policies (id, org_id, name, description, created_by) VALUES ($1, $2, $3, $4, $5)`,
		p.ID, t.org, p.Name, nullable(p.Description), nullable(p.CreatedBy))
	return err
}

// TouchPolicy implements policy.Tx (the trigger sets updated_at).
func (t *Tx) TouchPolicy(ctx context.Context, id, description string) error {
	_, err := t.tx.Exec(ctx, `UPDATE deedglass.policies SET description = $2 WHERE id = $1`, id, nullable(description))
	return err
}

const policyCols = `p.id, p.name, COALESCE(p.description, ''), COALESCE(p.created_by, ''), p.created_at, p.updated_at,
    (SELECT COALESCE(pg_catalog.max(v.version), 0) FROM deedglass.policy_versions AS v WHERE v.org_id = p.org_id AND v.policy_id = p.id)`

func scanPolicy(row pgx.Row) (*policy.PolicyRow, error) {
	var p policy.PolicyRow
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.Latest); err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

// GetPolicy implements policy.Tx: by pol_ id or by name.
func (t *Tx) GetPolicy(ctx context.Context, ref string) (*policy.PolicyRow, error) {
	if strings.HasPrefix(ref, "pol_") {
		if ids.Check(ids.Policy, ref) != nil {
			return nil, policy.ErrNotFound
		}
		return scanPolicy(t.tx.QueryRow(ctx, `SELECT `+policyCols+` FROM deedglass.policies AS p WHERE p.id = $1 AND p.archived_at IS NULL`, ref))
	}
	return scanPolicy(t.tx.QueryRow(ctx, `SELECT `+policyCols+` FROM deedglass.policies AS p WHERE p.name = $1 AND p.archived_at IS NULL`, ref))
}

// ListPolicies implements policy.Tx.
func (t *Tx) ListPolicies(ctx context.Context, after string, limit int) ([]policy.PolicyRow, error) {
	rows, err := t.tx.Query(ctx, `SELECT `+policyCols+` FROM deedglass.policies AS p
	  WHERE p.archived_at IS NULL AND p.name > $1 ORDER BY p.name LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (policy.PolicyRow, error) {
		p, err := scanPolicy(r)
		if err != nil {
			return policy.PolicyRow{}, err
		}
		return *p, nil
	})
}

// NextVersion implements policy.Tx: it locks the policy row, then reads the highest version.
func (t *Tx) NextVersion(ctx context.Context, id string) (int, error) {
	if _, err := t.tx.Exec(ctx, `SELECT 1 FROM deedglass.policies WHERE id = $1 FOR UPDATE`, id); err != nil {
		return 0, err
	}
	var n int
	err := t.tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.max(version), 0) + 1 FROM deedglass.policy_versions WHERE policy_id = $1`, id).Scan(&n)
	return n, err
}

func author(r deed.Ref) (typ string, id, userID *string) {
	switch r.Type {
	case "user":
		return "user", &r.ID, &r.ID
	case "api_key":
		return "api_key", &r.ID, nil
	}
	return "system", nil, nil
}

// InsertVersion implements policy.Tx.
func (t *Tx) InsertVersion(ctx context.Context, v policy.VersionRow) error {
	warnings, err := json.Marshal(v.Warnings)
	if err != nil {
		return err
	}
	if v.Warnings == nil {
		warnings = []byte("[]")
	}
	typ, aid, uid := author(v.Author)
	_, err = t.tx.Exec(ctx, `INSERT INTO deedglass.policy_versions (id, org_id, policy_id, version, yaml, version_hash, source_hash, lang,
	    rules_count, budgets_count, warnings, origin, suggestion_id, author_type, author_id, author_user_id, notes)
	  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13, $14, $15, $16, $17)`,
		v.ID, t.org, v.PolicyID, v.Version, string(v.Source), v.VersionHash, v.SourceHash, v.Lang, v.Rules, v.Budgets, string(warnings),
		string(v.Origin), nullable(v.SuggestionID), typ, aid, uid, nullable(v.Notes))
	return err
}

const versionCols = `v.id, v.policy_id, v.version, %s, v.version_hash, v.source_hash, v.lang, v.rules_count, v.budgets_count,
    v.warnings::text, v.origin, COALESCE(v.suggestion_id, ''), v.author_type, COALESCE(v.author_id, ''), COALESCE(v.notes, ''), v.created_at`

var (
	versionColsFull = fmt.Sprintf(versionCols, "v.yaml")
	versionColsList = fmt.Sprintf(versionCols, "''")
)

func scanVersion(row pgx.Row) (*policy.VersionRow, error) {
	var v policy.VersionRow
	var src, warnings, origin, atype, aid string
	if err := row.Scan(&v.ID, &v.PolicyID, &v.Version, &src, &v.VersionHash, &v.SourceHash, &v.Lang, &v.Rules, &v.Budgets,
		&warnings, &origin, &v.SuggestionID, &atype, &aid, &v.Notes, &v.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	if src != "" {
		v.Source = []byte(src)
	}
	v.Origin = policy.Origin(origin)
	v.Author = deed.Ref{Type: atype, ID: aid}
	if atype == "system" {
		v.Author = policy.SystemActor
	}
	if err := json.Unmarshal([]byte(warnings), &v.Warnings); err != nil {
		return nil, err
	}
	return &v, nil
}

// The version lookups: fixed statements, chosen by which part of the reference is set.
var getVersionSQL = map[string]string{
	"number": `SELECT ` + versionColsFull + ` FROM deedglass.policy_versions AS v WHERE v.policy_id = $1 AND v.version = $2`,
	"id":     `SELECT ` + versionColsFull + ` FROM deedglass.policy_versions AS v WHERE v.policy_id = $1 AND v.id = $2`,
	"hash":   `SELECT ` + versionColsFull + ` FROM deedglass.policy_versions AS v WHERE v.policy_id = $1 AND v.version_hash = $2`,
	"any-id": `SELECT ` + versionColsFull + ` FROM deedglass.policy_versions AS v WHERE v.id = $1`,
}

// GetVersion implements policy.Tx.
func (t *Tx) GetVersion(ctx context.Context, policyID string, ref policy.VersionRef) (*policy.VersionRow, error) {
	switch {
	case ref.ID != "" && ids.Check(ids.PolicyVersion, ref.ID) != nil:
		return nil, policy.ErrNotFound
	case policyID == "" && ref.ID != "":
		return scanVersion(t.tx.QueryRow(ctx, getVersionSQL["any-id"], ref.ID))
	case policyID == "":
		return nil, policy.ErrNotFound
	case ref.Number > 0:
		return scanVersion(t.tx.QueryRow(ctx, getVersionSQL["number"], policyID, ref.Number))
	case ref.ID != "":
		return scanVersion(t.tx.QueryRow(ctx, getVersionSQL["id"], policyID, ref.ID))
	case ref.Hash != "":
		return scanVersion(t.tx.QueryRow(ctx, getVersionSQL["hash"], policyID, ref.Hash))
	}
	return nil, policy.ErrNotFound
}

// LatestVersion implements policy.Tx.
func (t *Tx) LatestVersion(ctx context.Context, policyID string) (*policy.VersionRow, error) {
	return scanVersion(t.tx.QueryRow(ctx, `SELECT `+versionColsFull+` FROM deedglass.policy_versions AS v
	  WHERE v.policy_id = $1 ORDER BY v.version DESC LIMIT 1`, policyID))
}

// ListVersions implements policy.Tx: newest first, without the source.
func (t *Tx) ListVersions(ctx context.Context, policyID string, below, limit int) ([]policy.VersionRow, error) {
	if below <= 0 {
		below = 1 << 30
	}
	rows, err := t.tx.Query(ctx, `SELECT `+versionColsList+` FROM deedglass.policy_versions AS v
	  WHERE v.policy_id = $1 AND v.version < $2 ORDER BY v.version DESC LIMIT $3`, policyID, below, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (policy.VersionRow, error) {
		v, err := scanVersion(r)
		if err != nil {
			return policy.VersionRow{}, err
		}
		return *v, nil
	})
}

const activationCols = `a.id, a.policy_id, a.version_id, a.mode, COALESCE(a.activated_by, ''), a.activated_at, a.deactivated_at,
    COALESCE(a.deactivated_by, ''), COALESCE(a.deactivation_deed_id, ''), COALESCE(a.deed_id, ''), COALESCE(a.reason, ''), v.version, p.name`

const activationFrom = ` FROM deedglass.policy_activations AS a
  JOIN deedglass.policy_versions AS v ON v.org_id = a.org_id AND v.id = a.version_id
  JOIN deedglass.policies AS p ON p.org_id = a.org_id AND p.id = a.policy_id`

func scanActivation(row pgx.Row) (policy.ActivationRow, error) {
	var a policy.ActivationRow
	var mode string
	err := row.Scan(&a.ID, &a.PolicyID, &a.VersionID, &mode, &a.ActivatedBy, &a.ActivatedAt, &a.DeactivatedAt, &a.DeactivatedBy,
		&a.DeactivationDeedID, &a.DeedID, &a.Reason, &a.Version, &a.PolicyName)
	a.Mode = policy.Mode(mode)
	return a, err
}

// LiveActivations implements policy.Tx (enforce first).
func (t *Tx) LiveActivations(ctx context.Context) ([]policy.ActivationRow, error) {
	rows, err := t.tx.Query(ctx, `SELECT `+activationCols+activationFrom+` WHERE a.deactivated_at IS NULL ORDER BY a.mode`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (policy.ActivationRow, error) { return scanActivation(r) })
}

// GetActivation implements policy.Tx.
func (t *Tx) GetActivation(ctx context.Context, id string) (*policy.ActivationRow, error) {
	a, err := scanActivation(t.tx.QueryRow(ctx, `SELECT `+activationCols+activationFrom+` WHERE a.id = $1`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

func by(r deed.Ref) string {
	if r.Type == "system" {
		return "system"
	}
	return r.ID
}

// EndActivation implements policy.Tx.
func (t *Tx) EndActivation(ctx context.Context, id string, actor deed.Ref, deedID string) (time.Time, error) {
	var at time.Time
	err := t.tx.QueryRow(ctx, `UPDATE deedglass.policy_activations
	  SET deactivated_at = pg_catalog.clock_timestamp(), deactivated_by = $2, deactivation_deed_id = $3
	  WHERE id = $1 AND deactivated_at IS NULL RETURNING deactivated_at`, id, by(actor), deedID).Scan(&at)
	return at, notFound(err)
}

// InsertActivation implements policy.Tx; activated_at is the database clock.
func (t *Tx) InsertActivation(ctx context.Context, a *policy.ActivationRow) error {
	return t.tx.QueryRow(ctx, `INSERT INTO deedglass.policy_activations (id, org_id, policy_id, version_id, mode, activated_by, activated_at, deed_id, reason)
	  VALUES ($1, $2, $3, $4, $5, $6, pg_catalog.clock_timestamp(), $7, $8) RETURNING activated_at`,
		a.ID, t.org, a.PolicyID, a.VersionID, string(a.Mode), a.ActivatedBy, a.DeedID, nullable(a.Reason)).Scan(&a.ActivatedAt)
}

// RecordAdmin implements policy.Tx through ledger.RecordAdmin (admin_events + the sealed deed).
func (t *Tx) RecordAdmin(ctx context.Context, c policy.AdminChange) (string, error) {
	if t.led == nil {
		return "", errors.New("policystore: no ledger to seal with")
	}
	env, err := t.led.RecordAdmin(ctx, t.tx, ledger.AdminEvent{Action: c.Action, Actor: c.Actor,
		Target: &deed.Ref{Type: "policy_version", ID: c.VersionID}, Reason: c.Reason, Data: c.Data, RequestID: c.RequestID})
	if err != nil {
		return "", err
	}
	return env.ID, nil
}

// Publish implements policy.Tx through ledger.Publish.
func (t *Tx) Publish(ctx context.Context, ev policy.PolicyEvent) error {
	if t.led == nil {
		return errors.New("policystore: no ledger to publish with")
	}
	mode, d := string(ev.Mode), ev.DeedID
	return t.led.Publish(ctx, t.tx, ledger.Event{Type: ev.Type, Org: t.org, ID: ev.ActivationID, State: &mode, Deed: &d})
}

// NotifyPolicyChanged implements policy.Tx.
func (t *Tx) NotifyPolicyChanged(ctx context.Context) error {
	return store.NotifyPolicy(ctx, t.tx, t.org)
}

// LoadAgent implements policy.Tx.
func (t *Tx) LoadAgent(ctx context.Context, id string) (*policy.AgentInput, error) {
	if ids.Check(ids.Agent, id) != nil {
		return nil, policy.ErrNotFound
	}
	var a policy.AgentInput
	var labels, budgets []byte
	err := t.tx.QueryRow(ctx, `SELECT id, name, status, labels, budgets FROM deedglass.agents WHERE id = $1`, id).
		Scan(&a.ID, &a.Name, &a.Status, &labels, &budgets)
	if err != nil {
		return nil, notFound(err)
	}
	if err := json.Unmarshal(labels, &a.Labels); err != nil {
		return nil, err
	}
	var b policy.AgentBudgets
	if err := json.Unmarshal(budgets, &b); err != nil {
		return nil, err
	}
	if b.CallsPerMinute != nil || b.CallsPerDay != nil || b.SpendPerDay != nil {
		a.Budgets = &b
	}
	return &a, nil
}

// LoadOrg implements policy.Tx.
func (t *Tx) LoadOrg(ctx context.Context) (*policy.OrgInput, error) {
	var o policy.OrgInput
	var stopped *time.Time
	err := t.tx.QueryRow(ctx, `SELECT id, timezone, stopped_at FROM deedglass.orgs WHERE id = $1`, t.org).Scan(&o.ID, &o.Timezone, &stopped)
	if err != nil {
		return nil, notFound(err)
	}
	o.Stopped = stopped != nil
	return &o, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
