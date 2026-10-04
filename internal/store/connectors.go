package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Connectors and their credentials (S16, migrations 0004 and 0017). Every function runs inside
// the org transaction its caller opened; RLS keeps each org's rows to itself. Credential rows hold
// only field names, a hint and the AES-256-GCM envelope: never a value.

// Connector is one row of deedglass.connectors.
type Connector struct {
	ID          string
	OrgID       string
	Type        string
	Name        string
	Description string
	Config      json.RawMessage
	ConfigHash  string
	Status      string
	Health      json.RawMessage
	CreatedBy   string
	UpdatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

const connectorCols = `id, org_id, type, name, description, config, COALESCE(config_hash, ''), status, health,
  COALESCE(created_by, ''), COALESCE(updated_by, ''), created_at, updated_at, deleted_at`

func scanConnector(row pgx.Row) (Connector, error) {
	var c Connector
	err := row.Scan(&c.ID, &c.OrgID, &c.Type, &c.Name, &c.Description, &c.Config, &c.ConfigHash, &c.Status, &c.Health,
		&c.CreatedBy, &c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connector{}, ErrNotFound
	}
	return c, err
}

// InsertConnector adds a connector. A live connector with the same name is ErrConflict.
func InsertConnector(ctx context.Context, tx pgx.Tx, c Connector) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	_, err = sp.Exec(ctx, `INSERT INTO deedglass.connectors (id, org_id, type, name, description, config, config_hash, status, created_by, updated_by)
	    VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $9)`,
		c.ID, c.OrgID, c.Type, c.Name, c.Description, string(c.Config), c.ConfigHash, c.Status, c.CreatedBy)
	if SQLState(err) == CodeUniqueViolation {
		_ = sp.Rollback(ctx)
		if e := slackApprovalsTaken(err); e != nil {
			return e
		}
		return ErrConflict
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	return sp.Commit(ctx)
}

// GetConnector returns the live connector with this id, or ErrNotFound. forUpdate locks the row.
func GetConnector(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (Connector, error) {
	q := `SELECT ` + connectorCols + ` FROM deedglass.connectors WHERE id = $1 AND deleted_at IS NULL`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scanConnector(tx.QueryRow(ctx, q, id))
}

// ConnectorByName returns the live connector with this name, or ErrNotFound.
func ConnectorByName(ctx context.Context, tx pgx.Tx, name string, forUpdate bool) (Connector, error) {
	q := `SELECT ` + connectorCols + ` FROM deedglass.connectors WHERE name = $1 AND deleted_at IS NULL`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scanConnector(tx.QueryRow(ctx, q, name))
}

// ConnectorHead is what the Gate reads on every call to a connector: enough to find the cached
// instance (id and config hash) without reading the config.
type ConnectorHead struct {
	ID         string
	Type       string
	Status     string
	ConfigHash string
}

// ConnectorHeadByName returns the live connector named name, or ErrNotFound.
func ConnectorHeadByName(ctx context.Context, tx pgx.Tx, name string) (ConnectorHead, error) {
	var h ConnectorHead
	err := tx.QueryRow(ctx, `SELECT id, type, status, COALESCE(config_hash, '') FROM deedglass.connectors
	    WHERE name = $1 AND deleted_at IS NULL`, name).Scan(&h.ID, &h.Type, &h.Status, &h.ConfigHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, ErrNotFound
	}
	return h, err
}

// ListConnectors returns up to limit live connectors after afterID (id order), and whether more follow.
func ListConnectors(ctx context.Context, tx pgx.Tx, afterID string, limit int) ([]Connector, bool, error) {
	rows, err := tx.Query(ctx, `SELECT `+connectorCols+` FROM deedglass.connectors
	    WHERE deleted_at IS NULL AND ($1 = '' OR id > $1) ORDER BY id LIMIT $2`, afterID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []Connector
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// UpdateConnector writes the mutable fields of c (description, status, config and its hash).
func UpdateConnector(ctx context.Context, tx pgx.Tx, c Connector) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	tag, err := sp.Exec(ctx, `UPDATE deedglass.connectors SET description = $2, status = $3, config = $4::jsonb, config_hash = $5,
	    updated_by = $6 WHERE id = $1 AND deleted_at IS NULL`, c.ID, c.Description, c.Status, string(c.Config), c.ConfigHash, c.UpdatedBy)
	if err != nil {
		_ = sp.Rollback(ctx)
		if e := slackApprovalsTaken(err); e != nil {
			return e
		}
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetConnectorHealth records the last test result.
func SetConnectorHealth(ctx context.Context, tx pgx.Tx, id string, health json.RawMessage) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.connectors SET health = $2::jsonb WHERE id = $1 AND deleted_at IS NULL`, id, string(health))
	return err
}

// DisableConnector turns an active connector off after a safety trip (S30's Stripe breaker): status
// disabled and health {status: fail, reason}. It reports whether this call changed the row, so the
// caller seals connector.disabled once.
func DisableConnector(ctx context.Context, tx pgx.Tx, id, reason string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.connectors SET status = 'disabled',
	    health = jsonb_build_object('status', 'fail', 'reason', $2::text, 'tested_at', to_jsonb(now()))
	    WHERE id = $1 AND deleted_at IS NULL AND status <> 'disabled'`, id, reason)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SoftDeleteConnector marks a connector deleted; its name is free again.
func SoftDeleteConnector(ctx context.Context, tx pgx.Tx, id, by string) error {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.connectors SET deleted_at = pg_catalog.now(), status = 'disabled', updated_by = $2
	    WHERE id = $1 AND deleted_at IS NULL`, id, by)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ConnectorExecuting reports whether an action on the connector named name is executing.
func ConnectorExecuting(ctx context.Context, tx pgx.Tx, name string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deedglass.actions WHERE connector = $1 AND state = 'executing')`, name).Scan(&ok)
	return ok, err
}

// Credential is one row of deedglass.credentials.
type Credential struct {
	OrgID       string
	ConnectorID string
	Version     int
	Kind        string
	FieldNames  []string
	Hint        string
	KEKID       string
	WrappedDEK  []byte
	Ciphertext  []byte
	UpdatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	RotatedAt   *time.Time
}

const credentialCols = `org_id, connector_id, version, kind, field_names, hint, kek_id, wrapped_dek, ciphertext,
  updated_by, created_at, updated_at, rotated_at`

func scanCredential(row pgx.Row) (Credential, error) {
	var c Credential
	err := row.Scan(&c.OrgID, &c.ConnectorID, &c.Version, &c.Kind, &c.FieldNames, &c.Hint, &c.KEKID, &c.WrappedDEK, &c.Ciphertext,
		&c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt, &c.RotatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	return c, err
}

// GetCredential returns the connector's credential row, or ErrNotFound.
func GetCredential(ctx context.Context, tx pgx.Tx, connectorID string) (Credential, error) {
	return scanCredential(tx.QueryRow(ctx, `SELECT `+credentialCols+` FROM deedglass.credentials WHERE connector_id = $1`, connectorID))
}

// CredentialVersion returns the connector's current credential version, or 0 when it has none.
func CredentialVersion(ctx context.Context, tx pgx.Tx, connectorID string) (int, error) {
	var v int
	err := tx.QueryRow(ctx, `SELECT version FROM deedglass.credentials WHERE connector_id = $1`, connectorID).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

// UpsertCredential writes the connector's one credential row. The old wrapped data key is
// overwritten, which crypto-shreds the old secret.
func UpsertCredential(ctx context.Context, tx pgx.Tx, c Credential) error {
	_, err := tx.Exec(ctx, `INSERT INTO deedglass.credentials (org_id, connector_id, version, kind, field_names, hint, kek_id,
	    wrapped_dek, ciphertext, updated_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	  ON CONFLICT (org_id, connector_id) DO UPDATE SET version = EXCLUDED.version, kind = EXCLUDED.kind,
	    field_names = EXCLUDED.field_names, hint = EXCLUDED.hint, kek_id = EXCLUDED.kek_id, wrapped_dek = EXCLUDED.wrapped_dek,
	    ciphertext = EXCLUDED.ciphertext, updated_by = EXCLUDED.updated_by, rotated_at = NULL`,
		c.OrgID, c.ConnectorID, c.Version, c.Kind, c.FieldNames, c.Hint, c.KEKID, c.WrappedDEK, c.Ciphertext, c.UpdatedBy)
	return err
}

// DeleteCredential removes the connector's credential row (crypto-shredding it). It reports
// whether there was one.
func DeleteCredential(ctx context.Context, tx pgx.Tx, connectorID string) (bool, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM deedglass.credentials WHERE connector_id = $1`, connectorID)
	return tag.RowsAffected() > 0, err
}

// CredentialsToRewrap locks up to limit rows not wrapped by activeKEK, skipping rows another
// rotator holds.
func CredentialsToRewrap(ctx context.Context, tx pgx.Tx, activeKEK string, limit int) ([]Credential, error) {
	rows, err := tx.Query(ctx, `SELECT `+credentialCols+` FROM deedglass.credentials WHERE kek_id <> $1
	    ORDER BY connector_id LIMIT $2 FOR UPDATE SKIP LOCKED`, activeKEK, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RewrapCredential replaces a row's wrapped data key, only if it is still wrapped by fromKEK.
func RewrapCredential(ctx context.Context, tx pgx.Tx, connectorID, fromKEK, toKEK string, wrapped []byte) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.credentials SET kek_id = $3, wrapped_dek = $4, rotated_at = pg_catalog.now()
	    WHERE connector_id = $1 AND kek_id = $2`, connectorID, fromKEK, toKEK, wrapped)
	return tag.RowsAffected() == 1, err
}

// CredentialsByKEK counts the org's credentials per key id.
func CredentialsByKEK(ctx context.Context, tx pgx.Tx) (map[string]int, error) {
	rows, err := tx.Query(ctx, `SELECT kek_id, pg_catalog.count(*) FROM deedglass.credentials GROUP BY kek_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// OrgIDs lists every org's id (a SystemTx(maintenance) read of the global orgs table), for
// operator commands that walk orgs one org transaction at a time (deedglass vault rotate).
func (db *DB) OrgIDs(ctx context.Context) ([]string, error) {
	var out []string
	err := db.SystemTx(ctx, ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM deedglass.orgs ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

// Slack approvals routing (migration 0026): a Slack app routes to one live connector with approvals
// on, and an org has at most one.
var (
	// ErrSlackAppTaken: another connector (in any org) already receives this Slack app's approvals.
	ErrSlackAppTaken = errors.New("store: another connector already receives this Slack app's approvals")
	// ErrSlackApprovalsTaken: this org already has a Slack connector with approvals on.
	ErrSlackApprovalsTaken = errors.New("store: this org already has a Slack connector with approvals on")
)

func slackApprovalsTaken(err error) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != CodeUniqueViolation {
		return nil
	}
	switch pe.ConstraintName {
	case "connectors_slack_route_uq":
		return ErrSlackAppTaken
	case "connectors_slack_approvals_org_uq":
		return ErrSlackApprovalsTaken
	}
	return nil
}
