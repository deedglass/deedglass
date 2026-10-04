package learnstore

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Store is learn.Store on Postgres. Ledger seals the admin deeds (anomaly.resolved,
// insights.settings_changed) and publishes anomaly.detected; it may be read-only for reads.
type Store struct {
	DB     *store.DB
	Ledger *ledger.Ledger
	Inputs *Inputs

	statsOnce sync.Once
	statsSem  chan struct{}
}

// New returns the store.
func New(db *store.DB, led *ledger.Ledger) *Store {
	return &Store{DB: db, Ledger: led, Inputs: &Inputs{DB: db}}
}

var _ learn.Store = (*Store)(nil)

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return learn.ErrNotFound
	}
	return err
}

// Org reads the org as Insights needs it.
func (s *Store) Org(ctx context.Context, org ids.OrgID) (learn.OrgInfo, error) {
	var (
		o   learn.OrgInfo
		raw []byte
	)
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, name, slug, timezone, settings, COALESCE((settings ->> 'simulated') = 'true', false)
		    FROM deedglass.orgs WHERE id = $1`, org).Scan(&o.ID, &o.Name, &o.Slug, &o.Timezone, &raw, &o.Simulated)
	})
	if err != nil {
		return o, notFound(err)
	}
	o.Settings = learn.ParseSettings(raw)
	return o, nil
}

const versionCols = `SELECT v.id, v.policy_id, p.name, v.version, v.version_hash, v.yaml`

func scanVersion(row pgx.Row) (*learn.ResolvedVersion, error) {
	var (
		r   learn.ResolvedVersion
		src string
	)
	if err := row.Scan(&r.VersionID, &r.PolicyID, &r.Name, &r.Version, &r.VersionHash, &src); err != nil {
		return nil, err
	}
	r.Source, r.Found = []byte(src), true
	return &r, nil
}

// ResolveVersion finds version n of a policy named by id or name.
func (s *Store) ResolveVersion(ctx context.Context, org ids.OrgID, policyRef string, version int) (*learn.ResolvedVersion, error) {
	var out *learn.ResolvedVersion
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		var pid, name string
		// A policy name can't contain "_" (S10), so an id and a name never collide.
		err := tx.QueryRow(ctx, `SELECT id, name FROM deedglass.policies WHERE org_id = $1 AND (id = $2 OR name = $2)`, org, policyRef).Scan(&pid, &name)
		if err != nil {
			return err
		}
		r, err := scanVersion(tx.QueryRow(ctx, versionCols+` FROM deedglass.policy_versions AS v
		    JOIN deedglass.policies AS p ON p.org_id = v.org_id AND p.id = v.policy_id
		    WHERE v.org_id = $1 AND v.policy_id = $2 AND v.version = $3`, org, pid, version))
		if errors.Is(err, pgx.ErrNoRows) {
			r = &learn.ResolvedVersion{VersionInfo: learn.VersionInfo{PolicyID: pid, Name: name}}
			err = nil
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.max(version), 0) FROM deedglass.policy_versions WHERE org_id = $1 AND policy_id = $2`,
			org, pid).Scan(&r.Latest); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

// activeSource is the org's active version in mode, with its source; nil when none.
func (s *Store) activeSource(ctx context.Context, org ids.OrgID, mode string) (*learn.VersionInfo, []byte, error) {
	var (
		out   *learn.ResolvedVersion
		since *time.Time
	)
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, versionCols+`, a.activated_at FROM deedglass.policy_activations AS a
		    JOIN deedglass.policy_versions AS v ON v.org_id = a.org_id AND v.id = a.version_id
		    JOIN deedglass.policies AS p ON p.org_id = v.org_id AND p.id = v.policy_id
		    WHERE a.org_id = $1 AND a.mode = $2 AND a.deactivated_at IS NULL`, org, mode)
		var (
			r   learn.ResolvedVersion
			src string
		)
		if err := row.Scan(&r.VersionID, &r.PolicyID, &r.Name, &r.Version, &r.VersionHash, &src, &since); err != nil {
			return err
		}
		r.Source = []byte(src)
		out = &r
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	v := out.VersionInfo
	v.Live = true
	if since != nil {
		v.Since = since.UTC()
	}
	return &v, out.Source, nil
}

// EnforcedSource is the enforced version and its source.
func (s *Store) EnforcedSource(ctx context.Context, org ids.OrgID) (*learn.VersionInfo, []byte, error) {
	return s.activeSource(ctx, org, "enforce")
}

// ShadowSource is the active shadow version and its source.
func (s *Store) ShadowSource(ctx context.Context, org ids.OrgID) (*learn.VersionInfo, []byte, error) {
	return s.activeSource(ctx, org, "shadow")
}

// LiveShadow is the active shadow version.
func (s *Store) LiveShadow(ctx context.Context, org ids.OrgID) (*learn.VersionInfo, error) {
	v, _, err := s.activeSource(ctx, org, "shadow")
	return v, err
}

// Version names one version (nil when it isn't the org's). Live and Since are set when it is the
// active shadow.
func (s *Store) Version(ctx context.Context, org ids.OrgID, versionID string) (*learn.VersionInfo, error) {
	if _, err := ids.Parse[ids.PolicyVersionKind](versionID); err != nil {
		return nil, nil
	}
	m, err := s.VersionsByID(ctx, org, []string{versionID})
	if err != nil {
		return nil, err
	}
	v, ok := m[versionID]
	if !ok {
		return nil, nil
	}
	if live, err := s.LiveShadow(ctx, org); err == nil && live != nil && live.VersionID == versionID {
		return live, nil
	}
	return &v, nil
}

// VersionsByID names several versions.
func (s *Store) VersionsByID(ctx context.Context, org ids.OrgID, versionIDs []string) (map[string]learn.VersionInfo, error) {
	out := map[string]learn.VersionInfo{}
	if len(versionIDs) == 0 {
		return out, nil
	}
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT v.id, v.policy_id, p.name, v.version FROM deedglass.policy_versions AS v
		    JOIN deedglass.policies AS p ON p.org_id = v.org_id AND p.id = v.policy_id
		    WHERE v.org_id = $1 AND v.id = ANY($2::text[])`, org, versionIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v learn.VersionInfo
			if err := rows.Scan(&v.VersionID, &v.PolicyID, &v.Name, &v.Version); err != nil {
				return err
			}
			out[v.VersionID] = v
		}
		return rows.Err()
	})
	return out, err
}
