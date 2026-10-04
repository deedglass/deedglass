package learnstore

//deedglass:allow-sql-build the SQL is assembled only from this file's constants and fixed tables (the bucket expressions, the sample kinds) and $n placeholders; every value is a parameter

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
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// DetectOrgs lists the orgs a detect pass covers (claim_learn_orgs through store).
func (s *Store) DetectOrgs(ctx context.Context, recent time.Duration) ([]ids.OrgID, error) {
	return s.DB.LearnOrgs(ctx, recent)
}

// WithDetectLock runs fn in one org transaction holding the org's detect advisory lock.
func (s *Store) WithDetectLock(ctx context.Context, org ids.OrgID, fn func(ctx context.Context, tx learn.DetectTx) error) (bool, error) {
	locked := false
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.pg_try_advisory_xact_lock(pg_catalog.hashtextextended('deedglass/learn/detect/' || $1, 0))`,
			org.String()).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return nil
		}
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '30s'`); err != nil {
			return err
		}
		return fn(ctx, &detectTx{s: s, tx: tx, org: org})
	})
	return locked, err
}

// detectTx is learn.DetectTx inside one org transaction.
type detectTx struct {
	s   *Store
	tx  pgx.Tx
	org ids.OrgID
	pub int
}

// Agents lists the org's agents; CreatedAt is the earlier of the row's creation and its first
// decision (simulated history is older than the agents that made it).
func (d *detectTx) Agents(ctx context.Context) ([]learn.AgentInfo, error) {
	rows, err := d.tx.Query(ctx, `SELECT g.id, g.name, g.kind,
	      LEAST(g.created_at, COALESCE((SELECT pg_catalog.min(a.evaluated_at) FROM deedglass.actions AS a
	                                     WHERE a.org_id = g.org_id AND a.agent_id = g.id), g.created_at))
	    FROM deedglass.agents AS g WHERE g.org_id = $1 ORDER BY g.name, g.id`, d.org)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (learn.AgentInfo, error) {
		var a learn.AgentInfo
		err := r.Scan(&a.ID, &a.Name, &a.Kind, &a.CreatedAt)
		a.CreatedAt = a.CreatedAt.UTC()
		return a, err
	})
}

// localHour is the org-local hour bucket of an action, as an instant ($tz is a parameter).
const localHour = `(pg_catalog.date_trunc('hour', a.evaluated_at AT TIME ZONE %s) AT TIME ZONE %s)`

// sideRank ranks the side effect the decision saw: 1 read, 2 unknown, 3 write, 4 destructive.
const sideRank = `CASE a.policy_input -> 'tool' ->> 'side_effect' WHEN 'read' THEN 1 WHEN 'write' THEN 3 WHEN 'destructive' THEN 4 ELSE 2 END`

const bucketSelect = `SELECT a.agent_id, %s AS b, a.tool, pg_catalog.count(*)::integer,
    (pg_catalog.count(*) FILTER (WHERE a.decision = 'deny' AND a.decision_source IS DISTINCT FROM 'stop'))::integer,
    (pg_catalog.count(*) FILTER (WHERE a.witness_verdict = 'MISMATCH'))::integer,
    (pg_catalog.count(*) FILTER (WHERE ` + sideRank + ` >= 3))::integer,
    pg_catalog.max(` + sideRank + `)
  FROM deedglass.actions AS a
  WHERE a.org_id = $1 AND a.evaluated_at >= $2 AND a.evaluated_at < $3 AND a.decision IS NOT NULL
  GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`

func scanBuckets(rows pgx.Rows) ([]learn.BucketRow, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (learn.BucketRow, error) {
		var b learn.BucketRow
		err := r.Scan(&b.Agent, &b.Start, &b.Tool, &b.Actions, &b.Denied, &b.Mismatch, &b.Writes, &b.SideRank)
		b.Start = b.Start.UTC()
		return b, err
	})
}

// Buckets aggregates [from, to) per agent, org-local hour and tool.
func (d *detectTx) Buckets(ctx context.Context, tz string, from, to time.Time) ([]learn.BucketRow, error) {
	rows, err := d.tx.Query(ctx, fmt.Sprintf(bucketSelect, fmt.Sprintf(localHour, "$4", "$4")), d.org, from, to, tzOr(tz))
	if err != nil {
		return nil, err
	}
	return scanBuckets(rows)
}

// Window aggregates [from, to) per agent and tool, Start = from.
func (d *detectTx) Window(ctx context.Context, from, to time.Time) ([]learn.BucketRow, error) {
	rows, err := d.tx.Query(ctx, fmt.Sprintf(bucketSelect, "$2::timestamptz"), d.org, from, to)
	if err != nil {
		return nil, err
	}
	return scanBuckets(rows)
}

// refFirsts is each (agent, ref)'s first use since $2, for refs first used in [$3, $4).
const refFirsts = `WITH f AS (
    SELECT a.agent_id, a.on_behalf_of, pg_catalog.min(a.evaluated_at) AS first
      FROM deedglass.actions AS a
     WHERE a.org_id = $1 AND a.evaluated_at >= $2 AND a.evaluated_at < $4 AND a.on_behalf_of IS NOT NULL AND a.decision IS NOT NULL
     GROUP BY 1, 2)
  SELECT f.agent_id, %s AS b, pg_catalog.count(*)::integer FROM f WHERE f.first >= $3 GROUP BY 1, 2 ORDER BY 1, 2`

func scanRefs(rows pgx.Rows) ([]learn.RefRow, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (learn.RefRow, error) {
		var x learn.RefRow
		err := r.Scan(&x.Agent, &x.Start, &x.NewRefs)
		x.Start = x.Start.UTC()
		return x, err
	})
}

// RefBuckets counts first uses per agent and org-local hour.
func (d *detectTx) RefBuckets(ctx context.Context, tz string, lookback, from, to time.Time) ([]learn.RefRow, error) {
	rows, err := d.tx.Query(ctx, fmt.Sprintf(refFirsts, `(pg_catalog.date_trunc('hour', f.first AT TIME ZONE $5) AT TIME ZONE $5)`),
		d.org, lookback, from, to, tzOr(tz))
	if err != nil {
		return nil, err
	}
	return scanRefs(rows)
}

// WindowRefs counts first uses per agent in [from, to).
func (d *detectTx) WindowRefs(ctx context.Context, lookback, from, to time.Time) ([]learn.RefRow, error) {
	rows, err := d.tx.Query(ctx, fmt.Sprintf(refFirsts, `$3::timestamptz`), d.org, lookback, from, to)
	if err != nil {
		return nil, err
	}
	return scanRefs(rows)
}

// sampleWhere picks a sample kind's actions: a fixed table, never input.
var sampleWhere = map[learn.SampleKind]string{
	learn.SampleAll:      `TRUE`,
	learn.SampleDenied:   `a.decision = 'deny' AND a.decision_source IS DISTINCT FROM 'stop'`,
	learn.SampleMismatch: `a.witness_verdict = 'MISMATCH'`,
	learn.SampleTool:     `a.tool = $5`,
	learn.SampleNewRefs: `a.on_behalf_of IS NOT NULL AND NOT EXISTS (SELECT 1 FROM deedglass.actions AS p
	    WHERE p.org_id = a.org_id AND p.agent_id = a.agent_id AND p.on_behalf_of = a.on_behalf_of
	      AND p.evaluated_at >= $6 AND (p.evaluated_at, p.id) < (a.evaluated_at, a.id))`,
}

// Samples returns up to n action ids of one kind in [from, to), in decision-time order.
func (d *detectTx) Samples(ctx context.Context, agent string, from, to time.Time, kind learn.SampleKind, tool string, lookback time.Time, n int) ([]string, error) {
	w, ok := sampleWhere[kind]
	if !ok {
		return nil, fmt.Errorf("learnstore: unknown sample kind %q", kind)
	}
	rows, err := d.tx.Query(ctx, `SELECT a.id FROM deedglass.actions AS a
	    WHERE a.org_id = $1 AND a.agent_id = $2 AND a.evaluated_at >= $3 AND a.evaluated_at < $4 AND a.decision IS NOT NULL AND (`+w+`)
	      AND ($5::text IS NOT NULL OR TRUE) AND ($6::timestamptz IS NOT NULL OR TRUE)
	    ORDER BY a.evaluated_at, a.id LIMIT $7`, d.org, agent, from, to, tool, lookback, n)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// episodeEvidence is what an episode stores: the latest finding's evidence plus its hourly points.
type episodePoint struct {
	WindowStart string  `json:"window_start"`
	WindowEnd   string  `json:"window_end"`
	Count       any     `json:"count,omitempty"`
	Score       float64 `json:"score"`
}

// UpsertEpisode opens or extends the agent|kind|tool episode for one finding (S27 §3.7).
func (d *detectTx) UpsertEpisode(ctx context.Context, in learn.EpisodeInput) (learn.EpisodeResult, error) {
	f := in.Finding
	key := in.Agent + "|" + f.Kind + "|" + f.Tool
	w := in.Window
	activeUntil := w.End.Add(time.Duration(in.ActiveMinutes) * time.Minute)
	point := episodePoint{WindowStart: deed.FormatTime(w.Start), WindowEnd: deed.FormatTime(w.End), Count: f.Evidence["count"], Score: f.Score}
	var (
		id                   string
		windowEnd, prevUntil time.Time
		occurrences          int
		score                float64
		severity             string
		evidence             []byte
	)
	err := d.tx.QueryRow(ctx, `SELECT id, window_end, active_until, occurrences, score, severity, evidence FROM deedglass.anomalies
	    WHERE org_id = $1 AND episode_key = $2 AND status <> 'resolved' FOR UPDATE`, d.org, key).
		Scan(&id, &windowEnd, &prevUntil, &occurrences, &score, &severity, &evidence)
	tool := nullable(f.Tool)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		ev := map[string]any{}
		for k, v := range f.Evidence {
			ev[k] = v
		}
		ev["points"] = []episodePoint{point}
		b, err := json.Marshal(ev)
		if err != nil {
			return learn.EpisodeResult{}, err
		}
		id = ids.New[ids.AnomalyKind]().String()
		if _, err := d.tx.Exec(ctx, `INSERT INTO deedglass.anomalies (id, org_id, agent_id, kind, severity, window_start, window_end, evidence,
		      status, tool, score, episode_key, occurrences, active_until, detector_version, title)
		    VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, 'open', $9, $10, $11, 1, $12, $13, $14)`,
			id, d.org, in.Agent, f.Kind, f.Severity, w.Start, w.End, string(b), tool, f.Score, key, activeUntil, learn.DetectorVersion,
			clipRunes(f.Title, 280)); err != nil {
			return learn.EpisodeResult{}, err
		}
		if err := d.publish(ctx, id, in, f); err != nil {
			return learn.EpisodeResult{}, err
		}
		return learn.EpisodeResult{ID: id, Inserted: true, Published: true}, store.NotifyLearn(ctx, d.tx, d.org.String())
	case err != nil:
		return learn.EpisodeResult{}, err
	}
	if !w.End.After(windowEnd) {
		return learn.EpisodeResult{ID: id, Skipped: true}, nil // this window is already in the episode (a re-run)
	}
	newOccurrence := !prevUntil.After(w.Start)
	var ev map[string]any
	_ = json.Unmarshal(evidence, &ev)
	var points []episodePoint
	if raw, ok := ev["points"]; ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &points)
	}
	points = append(points, point)
	if len(points) > learn.MaxEvidencePoints {
		points = points[len(points)-learn.MaxEvidencePoints:]
	}
	next := map[string]any{}
	for k, v := range f.Evidence {
		next[k] = v
	}
	next["points"] = points
	b, err := json.Marshal(next)
	if err != nil {
		return learn.EpisodeResult{}, err
	}
	if severityRank(f.Severity) > severityRank(severity) {
		severity = f.Severity
	}
	occ := occurrences
	if newOccurrence {
		occ++
	}
	if _, err := d.tx.Exec(ctx, `UPDATE deedglass.anomalies SET window_end = $3, score = GREATEST(score, $4), severity = $5, evidence = $6::jsonb,
	      occurrences = $7, active_until = $8, title = $9 WHERE org_id = $1 AND id = $2`,
		d.org, id, w.End, f.Score, severity, string(b), occ, activeUntil, clipRunes(f.Title, 280)); err != nil {
		return learn.EpisodeResult{}, err
	}
	res := learn.EpisodeResult{ID: id, Extended: true}
	if newOccurrence {
		if err := d.publish(ctx, id, in, f); err != nil {
			return res, err
		}
		res.Published = true
	}
	return res, store.NotifyLearn(ctx, d.tx, d.org.String())
}

func severityRank(s string) int {
	switch s {
	case "info":
		return 1
	case "warn":
		return 2
	case "high":
		return 3
	}
	return 0
}

// publish sends anomaly.detected (SSE and the webhook outbox) in the episode's transaction.
func (d *detectTx) publish(ctx context.Context, id string, in learn.EpisodeInput, f learn.Finding) error {
	if d.s.Ledger == nil {
		return nil
	}
	agent, kind, sev, state := in.Agent, f.Kind, f.Severity, "open"
	ev := ledger.Event{Type: ledger.EventAnomalyDetected, Org: d.org.String(), ID: id, Agent: &agent, Kind: &kind, Severity: &sev, State: &state}
	if f.Tool != "" {
		t := f.Tool
		ev.Tool = &t
	}
	d.pub++
	return d.s.Ledger.Publish(ctx, d.tx, ev)
}

// CloseStale closes episodes with no new finding since before (resolved by the system, no deed).
func (d *detectTx) CloseStale(ctx context.Context, before, now time.Time) (int, error) {
	tag, err := d.tx.Exec(ctx, `UPDATE deedglass.anomalies SET status = 'resolved', resolved_by = 'system', resolved_at = $3,
	      resolution_reason = 'No new finding for 14 days.', active_until = LEAST(active_until, $3)
	    WHERE org_id = $1 AND status <> 'resolved' AND window_end < $2`, d.org, before, now)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() > 0 {
		if err := store.NotifyLearn(ctx, d.tx, d.org.String()); err != nil {
			return 0, err
		}
	}
	return int(tag.RowsAffected()), nil
}

// RecordRun upserts a learn_runs row.
func (d *detectTx) RecordRun(ctx context.Context, kind, period string, started, finished time.Time, status string, stats map[string]any, errMsg string) error {
	b, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	_, err = d.tx.Exec(ctx, `INSERT INTO deedglass.learn_runs (org_id, kind, period_key, status, started_at, finished_at, stats, error)
	    VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8)
	    ON CONFLICT (org_id, kind, period_key) DO UPDATE SET status = EXCLUDED.status, started_at = EXCLUDED.started_at,
	      finished_at = EXCLUDED.finished_at, stats = EXCLUDED.stats, error = EXCLUDED.error`,
		d.org, kind, period, status, started, finished, string(b), nullable(errMsg))
	return err
}

// PruneRuns deletes the org's runs of kind that started before t.
func (d *detectTx) PruneRuns(ctx context.Context, kind string, before time.Time) error {
	_, err := d.tx.Exec(ctx, `DELETE FROM deedglass.learn_runs WHERE org_id = $1 AND kind = $2 AND started_at < $3`, d.org, kind, before)
	return err
}

// --- the inbox ---

const anomalyCols = `SELECT n.id, n.agent_id, COALESCE(g.name, ''), n.kind, n.tool, n.severity, n.score, n.title, n.status, n.active_until,
    n.window_start, n.window_end, n.occurrences, n.evidence, n.detector_version, n.acknowledged_by, n.acknowledged_at,
    n.resolved_by, n.resolved_at, n.resolution_reason, n.resolution_deed_id, n.created_at, n.updated_at
  FROM deedglass.anomalies AS n
  LEFT JOIN deedglass.agents AS g ON g.org_id = n.org_id AND g.id = n.agent_id`

func scanAnomaly(row pgx.Row, now time.Time) (*learn.Anomaly, error) {
	var (
		a                                learn.Anomaly
		agent                            *string
		ackBy, resBy, resReason, resDeed *string
		ackAt, resAt                     *time.Time
	)
	if err := row.Scan(&a.ID, &agent, &a.Agent.Name, &a.Kind, &a.Tool, &a.Severity, &a.Score, &a.Title, &a.Status, &a.ActiveUntil,
		&a.Window.Start, &a.Window.End, &a.Occurrences, &a.Evidence, &a.DetectorVersion, &ackBy, &ackAt,
		&resBy, &resAt, &resReason, &resDeed, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.Object = "anomaly"
	if agent != nil {
		a.Agent.ID = *agent
	}
	a.ActiveUntil, a.Window.Start, a.Window.End = a.ActiveUntil.UTC(), a.Window.Start.UTC(), a.Window.End.UTC()
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	a.Active = a.Status != learn.StatusResolved && a.ActiveUntil.After(now)
	if ackBy != nil && ackAt != nil {
		a.Acknowledged = &learn.ReviewMark{By: *ackBy, At: ackAt.UTC()}
	}
	if resBy != nil && resAt != nil {
		a.Resolved = &learn.Resolution{By: *resBy, At: resAt.UTC(), Reason: resReason, Deed: resDeed}
	}
	return &a, nil
}

// ListAnomalies lists episodes, the most recent finding first.
func (s *Store) ListAnomalies(ctx context.Context, org ids.OrgID, f learn.AnomalyFilter, limit int, cursor string, now time.Time) ([]learn.Anomaly, string, error) {
	statuses := []string{learn.StatusOpen, learn.StatusAcknowledged}
	switch f.Status {
	case "all":
		statuses = append(statuses, learn.StatusResolved)
	case learn.StatusOpen, learn.StatusAcknowledged, learn.StatusResolved:
		statuses = []string{f.Status}
	}
	var since *time.Time
	if !f.Since.IsZero() {
		since = &f.Since
	}
	args := []any{org, statuses, f.Agent, f.Kind, since}
	q := anomalyCols + ` WHERE n.org_id = $1 AND n.status = ANY($2::text[]) AND ($3 = '' OR n.agent_id = $3) AND ($4 = '' OR n.kind = $4)
	    AND ($5::timestamptz IS NULL OR n.window_end >= $5)`
	if cursor != "" {
		t, id, ok := decodeCursor(cursor)
		if !ok {
			return nil, "", ErrBadCursor
		}
		args = append(args, t, id)
		q += ` AND (n.window_end, n.id) < ($6, $7)`
	}
	args = append(args, limit+1)
	q += fmt.Sprintf(` ORDER BY n.window_end DESC, n.id DESC LIMIT $%d`, len(args))
	var out []learn.Anomaly
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAnomaly(rows, now)
			if err != nil {
				return err
			}
			out = append(out, *a)
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
		next = encodeCursor(last.Window.End, last.ID)
	}
	return out, next, nil
}

// GetAnomaly reads one episode.
func (s *Store) GetAnomaly(ctx context.Context, org ids.OrgID, id string, now time.Time) (*learn.Anomaly, error) {
	var out *learn.Anomaly
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		a, err := scanAnomaly(tx.QueryRow(ctx, anomalyCols+` WHERE n.org_id = $1 AND n.id = $2`, org, id), now)
		out = a
		return err
	})
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

// CountActive counts episodes active at now.
func (s *Store) CountActive(ctx context.Context, org ids.OrgID, now time.Time) (int, error) {
	var n int
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.anomalies WHERE org_id = $1 AND status <> 'resolved' AND active_until > $2`,
			org, now).Scan(&n)
	})
	return n, err
}

// Acknowledge records who acknowledged (open → acknowledged; acknowledging again changes nothing).
func (s *Store) Acknowledge(ctx context.Context, org ids.OrgID, id string, by learn.Actor, now time.Time) (*learn.Anomaly, error) {
	var out *learn.Anomaly
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM deedglass.anomalies WHERE org_id = $1 AND id = $2 FOR UPDATE`, org, id).Scan(&status); err != nil {
			return err
		}
		switch status {
		case learn.StatusResolved:
			return learn.ErrStateConflict
		case learn.StatusOpen:
			if _, err := tx.Exec(ctx, `UPDATE deedglass.anomalies SET status = 'acknowledged', acknowledged_by = $3, acknowledged_at = $4
			    WHERE org_id = $1 AND id = $2`, org, id, by.ID, now); err != nil {
				return err
			}
		}
		a, err := scanAnomaly(tx.QueryRow(ctx, anomalyCols+` WHERE n.org_id = $1 AND n.id = $2`, org, id), now)
		out = a
		return err
	})
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

// Resolve closes an episode, ends its activity now and seals anomaly.resolved.
func (s *Store) Resolve(ctx context.Context, org ids.OrgID, id string, by learn.Actor, reason, requestID string, now time.Time) (*learn.Anomaly, error) {
	if s.Ledger == nil {
		return nil, errors.New("learnstore: resolving needs a Ledger with a seal key")
	}
	var out *learn.Anomaly
	err := s.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var (
			status, kind, severity string
			agent, tool            *string
			occ                    int
		)
		if err := tx.QueryRow(ctx, `SELECT status, kind, severity, agent_id, tool, occurrences FROM deedglass.anomalies
		    WHERE org_id = $1 AND id = $2 FOR UPDATE`, org, id).Scan(&status, &kind, &severity, &agent, &tool, &occ); err != nil {
			return err
		}
		if status == learn.StatusResolved {
			return learn.ErrStateConflict
		}
		data := map[string]any{"anomaly": id, "kind": kind, "severity": severity, "occurrences": occ}
		if agent != nil {
			data["agent"] = *agent
		}
		if tool != nil {
			data["tool"] = *tool
		}
		env, err := s.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminAnomalyResolved, Actor: deed.Ref{Type: by.Type, ID: by.ID},
			Target: &deed.Ref{Type: "anomaly", ID: id}, Reason: reason, Data: data, RequestID: requestID})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.anomalies SET status = 'resolved', resolved_by = $3, resolved_at = $4, resolution_reason = $5,
		      resolution_deed_id = $6, active_until = LEAST(active_until, $4) WHERE org_id = $1 AND id = $2`,
			org, id, by.ID, now, reason, env.ID); err != nil {
			return err
		}
		if err := store.NotifyLearn(ctx, tx, org.String()); err != nil {
			return err
		}
		a, err := scanAnomaly(tx.QueryRow(ctx, anomalyCols+` WHERE n.org_id = $1 AND n.id = $2`, org, id), now)
		out = a
		return err
	})
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

// FactsState loads what the facts cache needs.
func (s *Store) FactsState(ctx context.Context, org ids.OrgID, now time.Time) (learn.Settings, []learn.ActiveEpisode, time.Time, error) {
	var (
		set     learn.Settings
		eps     []learn.ActiveEpisode
		lastRun time.Time
	)
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT settings FROM deedglass.orgs WHERE id = $1`, org).Scan(&raw); err != nil {
			return err
		}
		set = learn.ParseSettings(raw)
		if !set.AnomalyFacts {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT agent_id, kind, severity, active_until FROM deedglass.anomalies
		    WHERE org_id = $1 AND status <> 'resolved' AND active_until > $2 AND agent_id IS NOT NULL`, org, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		for rows.Next() {
			var e learn.ActiveEpisode
			if err := rows.Scan(&e.Agent, &e.Kind, &e.Severity, &e.ActiveUntil); err != nil {
				rows.Close()
				return err
			}
			eps = append(eps, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var last *time.Time
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.max(finished_at) FROM deedglass.learn_runs
		    WHERE org_id = $1 AND kind = 'detect' AND status = 'succeeded' AND period_key NOT LIKE 'bf:%'`, org).Scan(&last); err != nil {
			return err
		}
		if last != nil {
			lastRun = last.UTC()
		}
		return nil
	})
	return set, eps, lastRun, err
}

// UpdateSettings merges the Insights settings into orgs.settings and seals
// insights.settings_changed with the before and after values.
func (s *Store) UpdateSettings(ctx context.Context, org ids.OrgID, before, after learn.Settings, by learn.Actor, reason, requestID string) (string, error) {
	if s.Ledger == nil {
		return "", errors.New("learnstore: changing settings needs a Ledger with a seal key")
	}
	patch, err := json.Marshal(after)
	if err != nil {
		return "", err
	}
	var deedID string
	err = s.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE deedglass.orgs SET settings = pg_catalog.jsonb_set(settings, '{insights}',
		      (CASE WHEN pg_catalog.jsonb_typeof(settings -> 'insights') = 'object' THEN settings -> 'insights' ELSE '{}'::jsonb END) || $2::jsonb)
		    WHERE id = $1`, org, string(patch)); err != nil {
			return err
		}
		env, err := s.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminInsightsSettingsChanged, Actor: deed.Ref{Type: by.Type, ID: by.ID},
			Target: &deed.Ref{Type: "org", ID: org.String()}, Reason: reason, Data: map[string]any{"before": before, "after": after}, RequestID: requestID})
		if err != nil {
			return err
		}
		deedID = env.ID
		return store.NotifyLearn(ctx, tx, org.String())
	})
	return deedID, err
}

// AgentAges lists the agents with their effective creation (the earlier of the row and the first
// decision).
func (s *Store) AgentAges(ctx context.Context, org ids.OrgID) ([]learn.AgentInfo, error) {
	var out []learn.AgentInfo
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		d := &detectTx{s: s, tx: tx, org: org}
		a, err := d.Agents(ctx)
		out = a
		return err
	})
	return out, err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func tzOr(tz string) string {
	if tz == "" {
		return "UTC"
	}
	return tz
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
