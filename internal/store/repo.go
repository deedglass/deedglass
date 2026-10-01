package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// The repositories here are minimal: they exist so RLS is exercised end to end, and S07/S08 extend
// them. Each runs inside the transaction its caller opened (OrgTx, OrgReadTx or SystemTx) and uses
// schema-qualified, parameterized SQL only.

// Org is one row of deedglass.orgs.
type Org struct {
	ID         ids.OrgID
	Name       string
	Slug       string
	Status     string
	APIVersion string
	Timezone   string
	CreatedAt  time.Time
}

// User is one row of deedglass.users.
type User struct {
	ID          ids.UserID
	Email       string
	DisplayName string
	Status      string
	CreatedAt   time.Time
}

// Membership is one row of deedglass.memberships.
type Membership struct {
	ID     ids.MembershipID
	OrgID  ids.OrgID
	UserID ids.UserID
	Role   string
	Status string
}

// NewOrg is what CreateOrg needs.
type NewOrg struct {
	Name       string
	Slug       string
	OwnerEmail string
	OwnerName  string // optional
}

// CreateOrg creates an org, its first owner (reusing the user when the email exists) and the
// owner's active membership, in one SystemTx(org_create).
func (db *DB) CreateOrg(ctx context.Context, in NewOrg) (Org, User, Membership, error) {
	var (
		o Org
		u User
		m Membership
	)
	err := db.SystemTx(ctx, ReasonOrgCreate, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if u, err = UpsertUserByEmail(ctx, tx, in.OwnerEmail, in.OwnerName); err != nil {
			return err
		}
		id := ids.New[ids.OrgKind]()
		err = tx.QueryRow(ctx, `INSERT INTO deedglass.orgs (id, name, slug) VALUES ($1, $2, $3)
		    RETURNING id, name, slug, status, api_version, timezone, created_at`, id, in.Name, in.Slug).
			Scan(&o.ID, &o.Name, &o.Slug, &o.Status, &o.APIVersion, &o.Timezone, &o.CreatedAt)
		if err != nil {
			return err
		}
		m = Membership{ID: ids.New[ids.MembershipKind](), OrgID: o.ID, UserID: u.ID, Role: "owner", Status: "active"}
		_, err = tx.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at)
		    VALUES ($1, $2, $3, 'owner', 'active', pg_catalog.now())`, m.ID, m.OrgID, m.UserID)
		return err
	})
	return o, u, m, err
}

const orgCols = `id, name, slug, status, api_version, timezone, created_at`

func scanOrg(row pgx.Row) (Org, error) {
	var o Org
	err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.Status, &o.APIVersion, &o.Timezone, &o.CreatedAt)
	return o, notFound(err)
}

// GetOrg returns the org, if the transaction can see it.
func GetOrg(ctx context.Context, tx pgx.Tx, id ids.OrgID) (Org, error) {
	return scanOrg(tx.QueryRow(ctx, `SELECT `+orgCols+` FROM deedglass.orgs WHERE id = $1`, id))
}

// GetOrgBySlug returns the org with this slug, if the transaction can see it.
func GetOrgBySlug(ctx context.Context, tx pgx.Tx, slug string) (Org, error) {
	return scanOrg(tx.QueryRow(ctx, `SELECT `+orgCols+` FROM deedglass.orgs WHERE slug = $1`, slug))
}

// ListOrgs returns every org the transaction can see (all of them under SystemTx), oldest first.
func ListOrgs(ctx context.Context, tx pgx.Tx) ([]Org, error) {
	rows, err := tx.Query(ctx, `SELECT `+orgCols+` FROM deedglass.orgs ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Org, error) { return scanOrg(r) })
}

// UpsertUserByEmail returns the user with this email, creating it when missing. The email is
// lowercased and trimmed. Inserting needs SystemTx (users are global; only a member is visible
// from inside an org).
func UpsertUserByEmail(ctx context.Context, tx pgx.Tx, email, name string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var display *string
	if name = strings.TrimSpace(name); name != "" {
		display = &name
	}
	var u User
	var dn *string
	// ON CONFLICT DO UPDATE with a no-op assignment returns the existing row.
	err := tx.QueryRow(ctx, `INSERT INTO deedglass.users (id, email, display_name) VALUES ($1, $2, $3)
	    ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
	    RETURNING id, email, display_name, status, created_at`, ids.New[ids.UserKind](), email, display).
		Scan(&u.ID, &u.Email, &dn, &u.Status, &u.CreatedAt)
	if dn != nil {
		u.DisplayName = *dn
	}
	return u, err
}

// GetUser returns the user, if the transaction can see it (a member of the current org, or SystemTx).
func GetUser(ctx context.Context, tx pgx.Tx, id ids.UserID) (User, error) {
	var u User
	var dn *string
	err := tx.QueryRow(ctx, `SELECT id, email, display_name, status, created_at FROM deedglass.users WHERE id = $1`, id).
		Scan(&u.ID, &u.Email, &dn, &u.Status, &u.CreatedAt)
	if dn != nil {
		u.DisplayName = *dn
	}
	return u, notFound(err)
}

// Agent is one row of deedglass.agents.
type Agent struct {
	ID          ids.AgentID
	OrgID       ids.OrgID
	Name        string
	Description string
	Status      string
	Labels      map[string]string
	CreatedAt   time.Time
}

// NewAgent is what CreateAgent needs.
type NewAgent struct {
	Name        string
	Description string
	Labels      map[string]string
	CreatedBy   string // usr_… or "system"
}

// CreateAgent registers an agent in org. Run it inside OrgTx(org).
func CreateAgent(ctx context.Context, tx pgx.Tx, org ids.OrgID, in NewAgent) (Agent, error) {
	if org.IsZero() {
		return Agent{}, ErrNoOrg
	}
	labels := in.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	lb, err := json.Marshal(labels)
	if err != nil {
		return Agent{}, err
	}
	var desc, by *string
	if in.Description != "" {
		desc = &in.Description
	}
	if in.CreatedBy != "" {
		by = &in.CreatedBy
	}
	return scanAgent(tx.QueryRow(ctx, `INSERT INTO deedglass.agents (id, org_id, name, description, labels, created_by)
	    VALUES ($1, $2, $3, $4, $5::jsonb, $6) RETURNING `+agentCols,
		ids.New[ids.AgentKind](), org, in.Name, desc, string(lb), by))
}

const agentCols = `id, org_id, name, description, status, labels, created_at`

func scanAgent(row pgx.Row) (Agent, error) {
	var a Agent
	var desc *string
	var labels []byte
	if err := row.Scan(&a.ID, &a.OrgID, &a.Name, &desc, &a.Status, &labels, &a.CreatedAt); err != nil {
		return Agent{}, notFound(err)
	}
	if desc != nil {
		a.Description = *desc
	}
	if err := json.Unmarshal(labels, &a.Labels); err != nil {
		return Agent{}, fmt.Errorf("store: agent labels: %w", err)
	}
	return a, nil
}

// GetAgent returns the agent, if it is in the transaction's org.
func GetAgent(ctx context.Context, tx pgx.Tx, id ids.AgentID) (Agent, error) {
	return scanAgent(tx.QueryRow(ctx, `SELECT `+agentCols+` FROM deedglass.agents WHERE id = $1`, id))
}

// GetAgentByName returns the agent with this name, if it is in the transaction's org.
func GetAgentByName(ctx context.Context, tx pgx.Tx, org ids.OrgID, name string) (Agent, error) {
	return scanAgent(tx.QueryRow(ctx, `SELECT `+agentCols+` FROM deedglass.agents WHERE org_id = $1 AND name = $2`, org, name))
}

// Page limits.
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// ClampLimit maps a requested page size to 1–100; 0 means the default, 20.
func ClampLimit(n int) int {
	switch {
	case n == 0:
		return DefaultLimit
	case n < 1:
		return 1
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}

// AgentCursor is the keyset position after which a page starts: newest first, ties by id.
type AgentCursor struct {
	CreatedAt time.Time
	ID        ids.AgentID
}

// ListAgents returns one page of org's agents, newest first, and the cursor for the next page (nil
// when there are no more). The org filter is explicit as well as enforced by RLS.
func ListAgents(ctx context.Context, tx pgx.Tx, org ids.OrgID, limit int, after *AgentCursor) ([]Agent, *AgentCursor, error) {
	if org.IsZero() {
		return nil, nil, ErrNoOrg
	}
	limit = ClampLimit(limit)
	var rows pgx.Rows
	var err error
	if after == nil {
		rows, err = tx.Query(ctx, `SELECT `+agentCols+` FROM deedglass.agents WHERE org_id = $1
		    ORDER BY created_at DESC, id DESC LIMIT $2`, org, limit+1)
	} else {
		rows, err = tx.Query(ctx, `SELECT `+agentCols+` FROM deedglass.agents WHERE org_id = $1
		    AND (created_at, id) < ($2, $3::text) ORDER BY created_at DESC, id DESC LIMIT $4`,
			org, after.CreatedAt, after.ID, limit+1)
	}
	if err != nil {
		return nil, nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Agent, error) { return scanAgent(r) })
	if err != nil {
		return nil, nil, err
	}
	if len(out) <= limit {
		return out, nil, nil
	}
	out = out[:limit]
	last := out[len(out)-1]
	return out, &AgentCursor{CreatedAt: last.CreatedAt, ID: last.ID}, nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
