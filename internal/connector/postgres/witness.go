package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
)

// WitnessTarget is what the decoder (S20) needs to watch one connector's database: the witness
// DSN, parsed with S17's rules (egress dialer, prod TLS), and the connector's witness.* config.
// The credential is opened here, through Instance.Secret, so the decoder never opens the vault
// itself (TestOpenerCallSites).
type WitnessTarget struct {
	Conn      *pgx.ConnConfig // connects as the witness role; holds the password, never printed
	DSNHint   string          // user@host:port/db
	Enabled   bool            // witness.enabled
	Watched   []string        // witness.watched_tables, schema.table
	Sensitive []string        // witness.sensitive_columns, schema.table.column
	Warning   string          // a DSN warning (sslmode=require in prod)

	// Versions is set when the watched database is another server's own (ADR-108: a FHIR server
	// on Postgres): its transactions are bound by the version the server reported, not by a ledger
	// row and marker; Watched lists the tables its publication must hold.
	Versions connector.VersionMap
	// Key keys the leaf digests of resource content (internal/jsonleaf), so a frame never holds a
	// clinical value; derived from the connector's witness credential.
	Key []byte
	// Author is the tag a bound version must carry when the server stores the writer (Medplum's
	// meta.author.reference); "" leaves that cross-check out.
	Author string
	// BindGrace is how long an unbound frame waits for the action that wrote it to be recorded.
	BindGrace time.Duration
}

// ErrWitnessDSNMissing is a postgres connector whose credential has no witness_dsn.
var ErrWitnessDSNMissing = errors.New("postgres: the credential has no witness_dsn")

// WitnessTargetOf returns inst's witness target. It opens the credential once and zeroes it before it
// returns.
func WitnessTargetOf(ctx context.Context, inst *connector.Instance) (*WitnessTarget, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	t := &WitnessTarget{Enabled: st.cfg.Witness.Enabled, Watched: slices.Clone(st.cfg.Witness.WatchedTables),
		Sensitive: slices.Clone(st.cfg.Witness.SensitiveColumns)}
	sec, err := inst.Secret(ctx)
	if err != nil {
		return nil, err
	}
	defer sec.Destroy()
	if sec.Kind() != "dsn" {
		return nil, fmt.Errorf("postgres: the stored credential is %s; a postgres connector needs kind dsn", sec.Kind())
	}
	raw := sec.Field("witness_dsn")
	if raw == "" {
		return nil, ErrWitnessDSNMissing
	}
	d, err := ParseDSN(raw, inst.Env)
	if err != nil {
		return nil, err
	}
	t.Conn, t.DSNHint, t.Warning = d.Config, d.String(), d.Warning
	return t, nil
}
