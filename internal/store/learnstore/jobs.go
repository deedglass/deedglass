package learnstore

//deedglass:allow-sql-build the list query appends constant WHERE fragments and a LIMIT placeholder number; every value is a parameter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/sqlguard"
)

// replayCols reads a replay row with its candidate's names.
const replayCols = `SELECT j.id, j.status, j.purpose, j.policy_id, COALESCE(j.version_id, ''), COALESCE(p.name, ''), COALESCE(v.version, 0),
    COALESCE(v.version_hash, ''), COALESCE(j.source_hash, ''), j.baseline, j.from_ts, j.to_ts, j.processed, j.total,
    j.requested_by_type, j.requested_by_id, j.attempts, j.created_at, j.started_at, j.finished_at, j.error, j.result
  FROM deedglass.replay_jobs AS j
  LEFT JOIN deedglass.policies AS p ON p.org_id = j.org_id AND p.id = j.policy_id
  LEFT JOIN deedglass.policy_versions AS v ON v.org_id = j.org_id AND v.id = j.version_id`

func scanReplay(row pgx.Row) (*learn.Replay, error) {
	var (
		r                 learn.Replay
		baseline, result  []byte
		from, to, created time.Time
		started, finished *time.Time
		total             *int64
		errText           *string
	)
	if err := row.Scan(&r.ID, &r.Status, &r.Purpose, &r.Candidate.PolicyID, &r.Candidate.VersionID, &r.Candidate.Name, &r.Candidate.Version,
		&r.Candidate.VersionHash, &r.Candidate.SourceHash, &baseline, &from, &to, &r.Progress.Processed, &total,
		&r.RequestedBy.Type, &r.RequestedBy.ID, &r.Attempts, &created, &started, &finished, &errText, &result); err != nil {
		return nil, err
	}
	r.Object = "replay"
	r.Window = learn.Window{From: from.UTC(), To: to.UTC()}
	r.CreatedAt = created.UTC()
	if started != nil {
		t := started.UTC()
		r.StartedAt = &t
	}
	if finished != nil {
		t := finished.UTC()
		r.FinishedAt = &t
	}
	_ = json.Unmarshal(baseline, &r.Baseline)
	if r.Baseline.Type == "" {
		r.Baseline.Type = learn.BaselineRecorded
	}
	r.Progress = learn.ProgressOf(r.Progress.Processed, total)
	if errText != nil {
		var e learn.ReplayError
		if json.Unmarshal([]byte(*errText), &e) == nil && e.Code != "" {
			r.Error = &e
		} else {
			r.Error = &learn.ReplayError{Code: "internal_error", Message: *errText}
		}
	}
	if r.Status == learn.ReplaySucceeded && len(result) > 0 {
		var res learn.ReplayResult
		if err := json.Unmarshal(result, &res); err != nil {
			return nil, fmt.Errorf("learnstore: replay %s's stored result doesn't decode: %w", r.ID, err)
		}
		r.WithResult(&res)
	}
	return &r, nil
}

// CreateReplay inserts a queued replay.
func (s *Store) CreateReplay(ctx context.Context, org ids.OrgID, n learn.NewReplay) (*learn.Replay, error) {
	id := ids.New[ids.ReplayKind]().String()
	base, err := json.Marshal(n.Baseline)
	if err != nil {
		return nil, err
	}
	var src, srcHash, vid *string
	if n.Source != nil {
		x := string(n.Source)
		src, srcHash = &x, &n.SourceHash
	} else {
		vid = &n.VersionID
	}
	purpose := n.Purpose
	if purpose == "" {
		purpose = "manual"
	}
	var out *learn.Replay
	err = s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO deedglass.replay_jobs (id, org_id, policy_id, version_id, source, source_hash, baseline, purpose,
		      from_ts, to_ts, total, requested_by, requested_by_type, requested_by_id)
		    VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13, $12)`,
			id, org, n.PolicyID, vid, src, srcHash, string(base), purpose, n.Window.From, n.Window.To, n.Total, n.RequestedBy.ID, n.RequestedBy.Type); err != nil {
			return err
		}
		r, err := scanReplay(tx.QueryRow(ctx, replayCols+` WHERE j.org_id = $1 AND j.id = $2`, org, id))
		out = r
		return err
	})
	return out, err
}

// GetReplay reads one replay.
func (s *Store) GetReplay(ctx context.Context, org ids.OrgID, id string) (*learn.Replay, error) {
	var out *learn.Replay
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		r, err := scanReplay(tx.QueryRow(ctx, replayCols+` WHERE j.org_id = $1 AND j.id = $2`, org, id))
		out = r
		return err
	})
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

// cursor is "<unix nanoseconds>.<id>" of the last row of a page (created_at DESC, id DESC).
func encodeCursor(t time.Time, id string) string {
	return strconv.FormatInt(t.UnixNano(), 10) + "." + id
}

func decodeCursor(c string) (time.Time, string, bool) {
	ns, id, ok := strings.Cut(c, ".")
	if !ok {
		return time.Time{}, "", false
	}
	n, err := strconv.ParseInt(ns, 10, 64)
	if err != nil || len(id) > 64 {
		return time.Time{}, "", false
	}
	return time.Unix(0, n).UTC(), id, true
}

// ErrBadCursor: a cursor that this store didn't hand out.
var ErrBadCursor = learn.ErrBadCursor

// ListReplays lists replays, newest first.
func (s *Store) ListReplays(ctx context.Context, org ids.OrgID, f learn.ReplayFilter, limit int, cursor string) ([]learn.Replay, string, error) {
	args := []any{org, f.PolicyID, f.Status}
	where := ` WHERE j.org_id = $1 AND ($2 = '' OR j.policy_id = $2) AND ($3 = '' OR j.status = $3)`
	if cursor != "" {
		t, id, ok := decodeCursor(cursor)
		if !ok {
			return nil, "", ErrBadCursor
		}
		args = append(args, t, id)
		where += ` AND (j.created_at, j.id) < ($4, $5)`
	}
	args = append(args, limit+1)
	var out []learn.Replay
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, replayCols+where+fmt.Sprintf(` ORDER BY j.created_at DESC, j.id DESC LIMIT $%d`, len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReplay(rows)
			if err != nil {
				return err
			}
			out = append(out, *r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

// CancelReplay ends a queued or running replay as cancelled. A running one stops at its next
// batch (its runner's renew finds it cancelled).
func (s *Store) CancelReplay(ctx context.Context, org ids.OrgID, id string, by learn.Actor, now time.Time) (*learn.Replay, error) {
	var out *learn.Replay
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM deedglass.replay_jobs WHERE org_id = $1 AND id = $2 FOR UPDATE`, org, id).Scan(&status); err != nil {
			return err
		}
		if status != learn.ReplayQueued && status != learn.ReplayRunning {
			return learn.ErrStateConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.replay_jobs SET status = 'cancelled', cancel_requested_at = $3, finished_at = $3,
		      locked_by = NULL, locked_until = NULL WHERE org_id = $1 AND id = $2`, org, id, now); err != nil {
			return err
		}
		r, err := scanReplay(tx.QueryRow(ctx, replayCols+` WHERE j.org_id = $1 AND j.id = $2`, org, id))
		out = r
		return err
	})
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

// ClaimReplays leases jobs (claim_replay_jobs through store).
func (s *Store) ClaimReplays(ctx context.Context, worker string, n int, lease time.Duration) ([]learn.ClaimedReplay, error) {
	cs, err := s.DB.ClaimReplayJobs(ctx, worker, n, lease)
	if err != nil {
		return nil, err
	}
	out := make([]learn.ClaimedReplay, len(cs))
	for i, c := range cs {
		out[i] = learn.ClaimedReplay{Org: c.OrgID, ID: c.ID, Attempts: c.Attempts}
	}
	return out, nil
}

// LoadReplay reads a claimed job with its candidate's and baseline's sources.
func (s *Store) LoadReplay(ctx context.Context, org ids.OrgID, id string) (*learn.StoredReplay, error) {
	var out *learn.StoredReplay
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		r, err := scanReplay(tx.QueryRow(ctx, replayCols+` WHERE j.org_id = $1 AND j.id = $2`, org, id))
		if err != nil {
			return err
		}
		st := &learn.StoredReplay{Replay: *r, Org: org}
		var src string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(j.source, v.yaml) FROM deedglass.replay_jobs AS j
		    LEFT JOIN deedglass.policy_versions AS v ON v.org_id = j.org_id AND v.id = j.version_id
		    WHERE j.org_id = $1 AND j.id = $2`, org, id).Scan(&src); err != nil {
			return err
		}
		st.Source = []byte(src)
		if r.Baseline.Type == learn.BaselineVersion && r.Baseline.VersionID != "" {
			var b string
			if err := tx.QueryRow(ctx, `SELECT yaml FROM deedglass.policy_versions WHERE org_id = $1 AND id = $2`, org, r.Baseline.VersionID).Scan(&b); err != nil {
				return err
			}
			st.BaselineSource = []byte(b)
		}
		out = st
		return nil
	})
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

// RenewReplay stores progress and extends the lease.
func (s *Store) RenewReplay(ctx context.Context, org ids.OrgID, id, worker string, processed, total int64, lease time.Duration) (bool, error) {
	cancel := false
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE deedglass.replay_jobs SET processed = $4, total = GREATEST(COALESCE(total, 0), $5),
		      progress = LEAST(100, CASE WHEN GREATEST(COALESCE(total, 0), $5) = 0 THEN 100 ELSE ($4 * 100 / GREATEST(COALESCE(total, 0), $5))::integer END),
		      locked_until = pg_catalog.now() + $6::interval
		    WHERE org_id = $1 AND id = $2 AND locked_by = $3 AND status = 'running'`, org, id, worker, processed, total, lease)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM deedglass.replay_jobs WHERE org_id = $1 AND id = $2`, org, id).Scan(&status); err != nil {
			return err
		}
		if status == learn.ReplayCancelled {
			cancel = true
			return nil
		}
		return learn.ErrLeaseLost
	})
	return cancel, err
}

// FinishReplay records a job's end (only while this worker still holds it).
func (s *Store) FinishReplay(ctx context.Context, org ids.OrgID, id, worker, status string, res *learn.ReplayResult, cause *learn.ReplayError) error {
	var (
		result, digest, errText *string
		processed               *int64
	)
	if res != nil {
		b, err := learn.MarshalResult(res)
		if err != nil {
			return err
		}
		x, d, n := string(b), res.ResultDigest, res.Summary.Actions
		result, digest, processed = &x, &d, &n
	}
	if cause != nil {
		b, _ := json.Marshal(cause)
		x := string(b)
		errText = &x
	}
	return s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE deedglass.replay_jobs SET status = $4, result = $5::jsonb, result_digest = $6, error = $7,
		      processed = COALESCE($8, processed), total = COALESCE($8, total), progress = CASE WHEN $4 = 'succeeded' THEN 100 ELSE progress END,
		      sqlguard_version = $9, finished_at = pg_catalog.now(), locked_by = NULL, locked_until = NULL
		    WHERE org_id = $1 AND id = $2 AND locked_by = $3 AND status = 'running'`,
			org, id, worker, status, result, digest, errText, processed, sqlguard.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return learn.ErrLeaseLost
		}
		return nil
	})
}

// RecentReplays counts manual replays created since t.
func (s *Store) RecentReplays(ctx context.Context, org ids.OrgID, since time.Time) (int, error) {
	var n int
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.replay_jobs WHERE org_id = $1 AND purpose = 'manual' AND created_at >= $2`,
			org, since).Scan(&n)
	})
	return n, err
}
