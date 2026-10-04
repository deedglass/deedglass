package learnstore

//deedglass:allow-sql-build the list query appends constant WHERE fragments and placeholder numbers; every value is a parameter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/policy"
)

// S28: suggestions, the suggest pass's evidence reads, the accept saga's steps and the phrasing
// bookkeeping. Every write runs in one org transaction; deeds go through the Ledger.

var _ learn.SuggestStore = (*Store)(nil)

// MaxEvidenceRows bounds a suggest pass's read (14 days of a busy org); past it the pass reads the
// newest rows only.
const MaxEvidenceRows = 500_000

// evidenceTimeout is the evidence read's statement timeout (it reads up to 14 days).
const evidenceTimeout = "30s"

// EvidenceRows reads the window's decided actions for the generators.
func (s *Store) EvidenceRows(ctx context.Context, org ids.OrgID, w learn.Window) ([]learn.EvidenceRow, error) {
	var out []learn.EvidenceRow
	err := s.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '`+evidenceTimeout+`'`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT a.id, a.evaluated_at, a.agent_id, COALESCE(ag.name, ''), a.tool,
		      COALESCE(a.policy_input -> 'tool' ->> 'connector_type', ''), COALESCE(a.policy_input -> 'tool' ->> 'side_effect', ''),
		      a.decision, COALESCE(a.decision_source, ''), COALESCE(a.decision_rule_id, ''), COALESCE(a.policy_version_id, ''),
		      COALESCE(ap.status, ''),
		      CASE WHEN ap.decided_at IS NOT NULL AND a.pending_approval_at IS NOT NULL
		           THEN pg_catalog.date_part('epoch', ap.decided_at - a.pending_approval_at) ELSE -1 END,
		      COALESCE(ap.approved_by, '{}'::text[]), COALESCE(a.witness_verdict, ''), COALESCE(a.witness_level, ''),
		      CASE WHEN a.decision = 'require_approval' OR a.witness_verdict = 'MISMATCH' THEN a.policy_input END
		    FROM deedglass.actions AS a
		    LEFT JOIN deedglass.agents AS ag ON ag.org_id = a.org_id AND ag.id = a.agent_id
		    LEFT JOIN deedglass.approvals AS ap ON ap.org_id = a.org_id AND ap.id = a.approval_id
		    WHERE a.org_id = $1 AND a.evaluated_at >= $2 AND a.evaluated_at < $3 AND a.decision IS NOT NULL
		    ORDER BY a.evaluated_at DESC, a.id DESC LIMIT $4`, org, w.From, w.To, MaxEvidenceRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				r        learn.EvidenceRow
				approval string
				in       []byte
			)
			if err := rows.Scan(&r.ActionID, &r.At, &r.AgentID, &r.AgentName, &r.Tool, &r.ConnectorType, &r.SideEffect, &r.Decision,
				&r.Source, &r.RuleID, &r.VersionID, &approval, &r.WaitSeconds, &r.Approvers, &r.Verdict, &r.Level, &in); err != nil {
				return err
			}
			r.At = r.At.UTC()
			r.Approval = learn.ApprovalOutcome(approval)
			if in != nil {
				r.Input = json.RawMessage(in)
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	// Oldest first, as the generators read them.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}

// EpisodesIn lists the anomaly episodes that overlap the window.
func (s *Store) EpisodesIn(ctx context.Context, org ids.OrgID, w learn.Window) ([]learn.EpisodeRef, error) {
	var out []learn.EpisodeRef
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, COALESCE(agent_id, ''), kind, window_start, window_end FROM deedglass.anomalies
		    WHERE org_id = $1 AND window_end >= $2 AND window_start < $3 ORDER BY window_start, id LIMIT 1000`, org, w.From, w.To)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e learn.EpisodeRef
			if err := rows.Scan(&e.ID, &e.AgentID, &e.Kind, &e.FirstSeen, &e.LastSeen); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// LiveShadowActivation is the active shadow version and its activation deed.
func (s *Store) LiveShadowActivation(ctx context.Context, org ids.OrgID) (*learn.VersionInfo, string, error) {
	var (
		v     learn.VersionInfo
		d     *string
		since time.Time
	)
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT a.policy_id, p.name, v.version, v.id, a.activated_at, a.deed_id
		    FROM deedglass.policy_activations AS a
		    JOIN deedglass.policy_versions AS v ON v.org_id = a.org_id AND v.id = a.version_id
		    JOIN deedglass.policies AS p ON p.org_id = a.org_id AND p.id = a.policy_id
		    WHERE a.org_id = $1 AND a.mode = 'shadow' AND a.deactivated_at IS NULL`, org).Scan(&v.PolicyID, &v.Name, &v.Version, &v.VersionID, &since, &d)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	v.Live, v.Since = true, since.UTC()
	deedID := ""
	if d != nil {
		deedID = *d
	}
	return &v, deedID, nil
}

// SupersedeStale marks open suggestions written for a version that is no longer enforced.
func (s *Store) SupersedeStale(ctx context.Context, org ids.OrgID, enforcedVersionID string) (int, error) {
	var n int64
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE deedglass.suggestions SET status = 'superseded', stale_reason = 'base_changed'
		    WHERE org_id = $1 AND status = 'open' AND base_version_id <> $2`, org, enforcedVersionID)
		n = tag.RowsAffected()
		return err
	})
	return int(n), err
}

// OpenFingerprints are the fingerprints of open and accepting suggestions, and of accepted ones
// written for the version still enforced (their change already runs in shadow, or ran there),
// with each one's status.
func (s *Store) OpenFingerprints(ctx context.Context, org ids.OrgID, enforcedVersionID string) (map[string]string, error) {
	out := map[string]string{}
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT fingerprint, status FROM deedglass.suggestions WHERE org_id = $1
		    AND (status IN ('open', 'accepting') OR (status = 'accepted' AND base_version_id = $2))`, org, enforcedVersionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var fp, st string
			if err := rows.Scan(&fp, &st); err != nil {
				return err
			}
			if out[fp] == "" || st != learn.SugAccepted {
				out[fp] = st
			}
		}
		return rows.Err()
	})
	return out, err
}

// LastRejected is each fingerprint's latest rejection, with the count it was remembered by.
func (s *Store) LastRejected(ctx context.Context, org ids.OrgID, fps []string) (map[string]learn.RejectedRef, error) {
	out := map[string]learn.RejectedRef{}
	if len(fps) == 0 {
		return out, nil
	}
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (fingerprint) fingerprint, decided_at, COALESCE((evidence ->> '_main_count')::bigint, 0)
		    FROM deedglass.suggestions WHERE org_id = $1 AND fingerprint = ANY($2::text[]) AND status = 'rejected'
		    ORDER BY fingerprint, decided_at DESC`, org, fps)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				fp string
				r  learn.RejectedRef
			)
			if err := rows.Scan(&fp, &r.At, &r.MainCount); err != nil {
				return err
			}
			r.At = r.At.UTC()
			out[fp] = r
		}
		return rows.Err()
	})
	return out, err
}

// InsertSuggestions stores the rows with their replay jobs and publishes suggestion.created, in
// one transaction. A row whose fingerprint is already open (another instance won) is skipped;
// the ids actually inserted come back.
func (s *Store) InsertSuggestions(ctx context.Context, org ids.OrgID, rows []learn.NewSuggestion) ([]string, error) {
	var created []string
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		created = created[:0]
		for _, r := range rows {
			var replayID, replayJSON *string
			if r.Replay != nil {
				res, err := json.Marshal(r.Replay)
				if err != nil {
					return err
				}
				base, _ := json.Marshal(map[string]any{"type": learn.BaselineVersion, "version_id": r.BaseVersionID, "version": r.PolicyVersion})
				src := string(r.CandidateSource)
				if _, err := tx.Exec(ctx, `INSERT INTO deedglass.replay_jobs (id, org_id, policy_id, source, source_hash, baseline, purpose,
				      from_ts, to_ts, total, processed, status, result, result_digest, requested_by, requested_by_type, requested_by_id,
				      started_at, finished_at)
				    VALUES ($1, $2, $3, $4, $5, $6::jsonb, 'suggestion', $7, $8, $9, $9, 'succeeded', $10::jsonb, $11, $12, $13, $12,
				      pg_catalog.now(), pg_catalog.now())`,
					r.ReplayID, org, r.PolicyID, src, r.CandidateSourceHash, string(base), r.Window.From, r.Window.To, r.Replay.Summary.Actions,
					string(res), r.Replay.ResultDigest, r.RequestedBy.ID, r.RequestedBy.Type); err != nil {
					return err
				}
				id, js := r.ReplayID, string(res)
				replayID, replayJSON = &id, &js
			}
			var ev map[string]any
			if err := json.Unmarshal(r.Evidence, &ev); err != nil {
				return err
			}
			ev["_main_count"] = r.MainCount
			evJSON, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			warn, err := json.Marshal(r.Warnings)
			if err != nil {
				return err
			}
			var cand, candHash, diff, target *string
			if r.CandidateSource != nil {
				c, h, d := string(r.CandidateSource), r.CandidateSourceHash, r.Diff
				cand, candHash, diff = &c, &h, &d
			}
			if r.TargetRuleID != "" {
				t := r.TargetRuleID
				target = &t
			}
			tag, err := tx.Exec(ctx, `INSERT INTO deedglass.suggestions (id, org_id, kind, title, rationale, evidence, diff, replay_job_id,
			      replay_result, risk_note, status, generator, generator_version, fingerprint, policy_id, base_version_id, base_version_hash,
			      target_rule_id, candidate_source, candidate_source_hash, evidence_digest, window_from, window_to, impact, warnings)
			    VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9::jsonb, $10, 'open', $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
			      $22, $23, $24::jsonb)
			    ON CONFLICT (org_id, fingerprint) WHERE status IN ('open', 'accepting') DO NOTHING`,
				r.ID, org, r.Kind, r.Title, r.Rationale, string(evJSON), diff, replayID, replayJSON, r.RiskNote, r.Generator,
				learn.GeneratorVersion, r.Fingerprint, r.PolicyID, r.BaseVersionID, r.BaseVersionHash, target, cand, candHash,
				r.EvidenceDigest, r.Window.From, r.Window.To, r.Impact, string(warn))
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			kind, status := r.Kind, learn.SugOpen
			ev2 := ledger.Event{V: 1, Type: ledger.EventSuggestionCreated, Org: org.String(), ID: r.ID, Kind: &kind, Status: &status}
			if target != nil {
				ev2.RuleID = target
			}
			if s.Ledger != nil {
				if err := s.Ledger.Publish(ctx, tx, ev2); err != nil {
					return err
				}
			}
			created = append(created, r.ID)
		}
		return nil
	})
	return created, err
}

const suggestionCols = `SELECT s.id, s.status, s.kind, s.generator, s.title, COALESCE(s.rationale, ''), s.phrased_title, s.phrased_rationale,
    s.phrasing, s.policy_id, p.name, bv.version, s.base_version_id, s.base_version_hash, s.target_rule_id, s.evidence, s.evidence_digest,
    s.diff, s.warnings, s.replay_job_id, s.replay_result, COALESCE(s.risk_note, ''), s.impact, s.stale_reason, s.created_at,
    s.decided_by_type, s.decided_by, s.decided_at, s.decision_reason, s.resulting_version_id, rv.version, rv.version_hash,
    s.decision_deed_id, s.fingerprint, s.candidate_source, s.replace_shadow, s.decision_request_id, s.updated_at
  FROM deedglass.suggestions AS s
  JOIN deedglass.policies AS p ON p.org_id = s.org_id AND p.id = s.policy_id
  JOIN deedglass.policy_versions AS bv ON bv.org_id = s.org_id AND bv.id = s.base_version_id
  LEFT JOIN deedglass.policy_versions AS rv ON rv.org_id = s.org_id AND rv.id = s.resulting_version_id`

func scanSuggestion(row pgx.Row) (*learn.SuggestionRow, error) {
	var (
		r                                        learn.SuggestionRow
		phrasedTitle, phrasedRationale, target   *string
		phrasing, evidence, warnings, replayJSON []byte
		diff, replayID, stale, byType, byID      *string
		decidedAt                                *time.Time
		reason, resultID, resultHash, deedID     *string
		resultVersion                            *int
		cand, reqID                              *string
		created                                  time.Time
	)
	if err := row.Scan(&r.ID, &r.Status, &r.Kind, &r.Generator, &r.Title, &r.Rationale, &phrasedTitle, &phrasedRationale, &phrasing,
		&r.Policy.ID, &r.Policy.Name, &r.Policy.BaseVersion, &r.Policy.BaseVersionID, &r.Policy.BaseVersionHash, &target, &evidence,
		&r.EvidenceDigest, &diff, &warnings, &replayID, &replayJSON, &r.RiskNote, &r.Impact, &stale, &created, &byType, &byID,
		&decidedAt, &reason, &resultID, &resultVersion, &resultHash, &deedID, &r.Fingerprint, &cand, &r.ReplaceShadow, &reqID, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Object = "suggestion"
	r.CreatedAt = created.UTC()
	r.TargetRuleID, r.StaleReason, r.DecisionReason, r.DecisionDeed = target, stale, reason, deedID
	_ = json.Unmarshal(phrasing, &r.Phrasing)
	if r.Phrasing.Source == "" {
		r.Phrasing.Source = "template"
	}
	if r.Phrasing.Source == "claude" && phrasedTitle != nil && phrasedRationale != nil {
		r.Phrasing.TemplateTitle = r.Title
		r.Title, r.Rationale = *phrasedTitle, *phrasedRationale
	}
	var ev map[string]any
	if json.Unmarshal(evidence, &ev) == nil {
		delete(ev, "_main_count") // the dedupe count, internal
		if b, err := json.Marshal(ev); err == nil {
			evidence = b
		}
	}
	r.Evidence = evidence
	r.Warnings = []policy.Diagnostic{}
	_ = json.Unmarshal(warnings, &r.Warnings)
	if diff != nil {
		u, err := policy.ParseUnified(*diff)
		if err == nil {
			r.Diff = &u
		}
	}
	if replayID != nil && len(replayJSON) > 0 {
		var res learn.ReplayResult
		if err := json.Unmarshal(replayJSON, &res); err == nil {
			r.Replay = &learn.SuggestionReplay{ID: *replayID, Window: res.Window, Summary: res.Summary, Transitions: res.Transitions, ResultDigest: res.ResultDigest}
			if r.Replay.Transitions == nil {
				r.Replay.Transitions = []learn.Transition{}
			}
			r.ReplayDigest = res.ResultDigest
		}
	}
	if byType != nil && byID != nil {
		r.DecidedBy = &learn.Actor{Type: *byType, ID: *byID}
		r.DecidedByType, r.DecidedByID = *byType, *byID
	}
	if decidedAt != nil {
		t := decidedAt.UTC()
		r.DecidedAt = &t
	}
	if r.Status == learn.SugAccepting && reason != nil {
		r.AcceptReason = *reason
	}
	if resultID != nil && resultVersion != nil && resultHash != nil {
		r.ResultingVersion = &learn.VersionBrief{ID: *resultID, Version: *resultVersion, VersionHash: *resultHash}
	}
	if cand != nil {
		r.CandidateSource = []byte(*cand)
	}
	if reqID != nil {
		r.RequestID = *reqID
	}
	return &r, nil
}

// GetSuggestion reads one suggestion.
func (s *Store) GetSuggestion(ctx context.Context, org ids.OrgID, id string) (*learn.SuggestionRow, error) {
	var out *learn.SuggestionRow
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		r, err := scanSuggestion(tx.QueryRow(ctx, suggestionCols+` WHERE s.org_id = $1 AND s.id = $2`, org, id))
		out = r
		return err
	})
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

// sugCursor is "<impact>.<unix ns>.<id>" (impact DESC, created_at DESC, id DESC).
func sugCursor(impact int, t time.Time, id string) string {
	return strconv.Itoa(impact) + "." + strconv.FormatInt(t.UnixNano(), 10) + "." + id
}

// ListSuggestions lists a page, by impact.
func (s *Store) ListSuggestions(ctx context.Context, org ids.OrgID, f learn.SuggestionFilter, limit int, cursor string) ([]learn.Suggestion, string, error) {
	where := []string{"s.org_id = $1"}
	args := []any{org}
	add := func(frag string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(frag, "?", "$"+strconv.Itoa(len(args))))
	}
	switch f.Status {
	case "":
		where = append(where, "s.status IN ('open', 'accepting')")
	case "all":
	default:
		add("s.status = ?", f.Status)
	}
	if f.Kind != "" {
		add("s.kind = ?", f.Kind)
	}
	if cursor != "" {
		parts := strings.SplitN(cursor, ".", 3)
		if len(parts) != 3 {
			return nil, "", ErrBadCursor
		}
		imp, err1 := strconv.Atoi(parts[0])
		ns, err2 := strconv.ParseInt(parts[1], 10, 64)
		if err1 != nil || err2 != nil || len(parts[2]) > 64 {
			return nil, "", ErrBadCursor
		}
		args = append(args, imp, time.Unix(0, ns).UTC(), parts[2])
		n := len(args)
		where = append(where, fmt.Sprintf("(s.impact, s.created_at, s.id) < ($%d, $%d, $%d)", n-2, n-1, n))
	}
	args = append(args, limit+1)
	q := suggestionCols + " WHERE " + strings.Join(where, " AND ") + fmt.Sprintf(" ORDER BY s.impact DESC, s.created_at DESC, s.id DESC LIMIT $%d", len(args))
	var out []learn.Suggestion
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanSuggestion(rows)
			if err != nil {
				return err
			}
			out = append(out, r.Suggestion)
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
		next = sugCursor(last.Impact, last.CreatedAt, last.ID)
	}
	return out, next, nil
}

// BeginAccept is step 1: open → accepting, as the person (or key) accepting.
func (s *Store) BeginAccept(ctx context.Context, org ids.OrgID, id string, by learn.Actor, reason string, replace bool, requestID string) (*learn.SuggestionRow, error) {
	var out *learn.SuggestionRow
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM deedglass.suggestions WHERE org_id = $1 AND id = $2 FOR UPDATE`, org, id).Scan(&status); err != nil {
			return err
		}
		if status != learn.SugOpen {
			return learn.ErrStateConflict
		}
		var rs *string
		if reason != "" {
			rs = &reason
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.suggestions SET status = 'accepting', decided_by_type = $3, decided_by = $4,
		      decision_reason = $5, replace_shadow = $6, decision_request_id = NULLIF($7, '') WHERE org_id = $1 AND id = $2`,
			org, id, by.Type, by.ID, rs, replace, requestID); err != nil {
			return err
		}
		r, err := scanSuggestion(tx.QueryRow(ctx, suggestionCols+` WHERE s.org_id = $1 AND s.id = $2`, org, id))
		out = r
		return err
	})
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

// ReopenSuggestion puts an accepting suggestion back to open (step 3's 409, a refused step 4–5).
func (s *Store) ReopenSuggestion(ctx context.Context, org ids.OrgID, id string) error {
	return s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.suggestions SET status = 'open', decided_by_type = NULL, decided_by = NULL,
		      decision_reason = NULL, replace_shadow = false, decision_request_id = NULL
		    WHERE org_id = $1 AND id = $2 AND status = 'accepting'`, org, id)
		return err
	})
}

// SupersedeOne marks one open or accepting suggestion superseded (its base moved on).
func (s *Store) SupersedeOne(ctx context.Context, org ids.OrgID, id string) error {
	return s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.suggestions SET status = 'superseded', stale_reason = 'base_changed'
		    WHERE org_id = $1 AND id = $2 AND status IN ('open', 'accepting')`, org, id)
		return err
	})
}

// CompleteAccept is step 6: accepting → accepted and the suggestion.accepted deed, in one
// transaction. ErrStateConflict when another run finished it first (so it is sealed once).
func (s *Store) CompleteAccept(ctx context.Context, org ids.OrgID, id string, d learn.AcceptDone) (string, error) {
	if s.Ledger == nil {
		return "", errors.New("learnstore: accepting needs a Ledger with a seal key")
	}
	var deedID string
	err := s.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var (
			status, kind, gen, fp, evDigest, policyID string
			replay                                    []byte
		)
		if err := tx.QueryRow(ctx, `SELECT status, kind, generator, fingerprint, evidence_digest, policy_id, replay_result
		    FROM deedglass.suggestions WHERE org_id = $1 AND id = $2 FOR UPDATE`, org, id).Scan(&status, &kind, &gen, &fp, &evDigest, &policyID, &replay); err != nil {
			return err
		}
		if status != learn.SugAccepting {
			return learn.ErrStateConflict
		}
		var res struct {
			ResultDigest string `json:"result_digest"`
		}
		_ = json.Unmarshal(replay, &res)
		data := map[string]any{"suggestion": id, "kind": kind, "generator": gen, "fingerprint": fp, "evidence_digest": evDigest,
			"replay_result_digest": res.ResultDigest, "policy_id": policyID, "version_id": d.VersionID, "version": d.Version,
			"version_hash": d.VersionHash, "replaced_shadow_version_id": nil}
		if d.ReplacedShadowVersionID != "" {
			data["replaced_shadow_version_id"] = d.ReplacedShadowVersionID
		}
		env, err := s.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminSuggestionAccepted, Actor: deed.Ref{Type: d.By.Type, ID: d.By.ID},
			Target: &deed.Ref{Type: "suggestion", ID: id}, Reason: d.Reason, Data: data, RequestID: d.RequestID})
		if err != nil {
			return err
		}
		deedID = env.ID
		_, err = tx.Exec(ctx, `UPDATE deedglass.suggestions SET status = 'accepted', resulting_version_id = $3, decided_at = $4,
		      decision_deed_id = $5 WHERE org_id = $1 AND id = $2`, org, id, d.VersionID, d.At, env.ID)
		return err
	})
	if err != nil {
		return "", notFound(err)
	}
	return deedID, nil
}

// RejectSuggestion closes an open suggestion with the person's reason and seals suggestion.rejected.
func (s *Store) RejectSuggestion(ctx context.Context, org ids.OrgID, id string, by learn.Actor, reason, requestID string, now time.Time) (string, error) {
	if s.Ledger == nil {
		return "", errors.New("learnstore: rejecting needs a Ledger with a seal key")
	}
	var deedID string
	err := s.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var status, kind, gen, fp, evDigest string
		if err := tx.QueryRow(ctx, `SELECT status, kind, generator, fingerprint, evidence_digest FROM deedglass.suggestions
		    WHERE org_id = $1 AND id = $2 FOR UPDATE`, org, id).Scan(&status, &kind, &gen, &fp, &evDigest); err != nil {
			return err
		}
		if status != learn.SugOpen {
			return learn.ErrStateConflict
		}
		env, err := s.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminSuggestionRejected, Actor: deed.Ref{Type: by.Type, ID: by.ID},
			Target: &deed.Ref{Type: "suggestion", ID: id}, Reason: reason,
			Data: map[string]any{"suggestion": id, "kind": kind, "generator": gen, "fingerprint": fp, "evidence_digest": evDigest}, RequestID: requestID})
		if err != nil {
			return err
		}
		deedID = env.ID
		_, err = tx.Exec(ctx, `UPDATE deedglass.suggestions SET status = 'rejected', decided_by_type = $3, decided_by = $4, decided_at = $5,
		      decision_reason = $6, decision_deed_id = $7, decision_request_id = NULLIF($8, '') WHERE org_id = $1 AND id = $2`,
			org, id, by.Type, by.ID, now, reason, env.ID, requestID)
		return err
	})
	if err != nil {
		return "", notFound(err)
	}
	return deedID, nil
}

// StuckAccepting lists suggestions in accepting since before.
func (s *Store) StuckAccepting(ctx context.Context, org ids.OrgID, before time.Time) ([]string, error) {
	var out []string
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM deedglass.suggestions WHERE org_id = $1 AND status = 'accepting' AND updated_at < $2
		    ORDER BY updated_at LIMIT 100`, org, before)
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

// SetPhrasing stores the wording outcome (title and rationale only when Claude's reply passed).
func (s *Store) SetPhrasing(ctx context.Context, org ids.OrgID, id string, title, rationale *string, p learn.Phrasing) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.suggestions SET phrased_title = $3, phrased_rationale = $4, phrasing = $5::jsonb
		    WHERE org_id = $1 AND id = $2`, org, id, title, rationale, string(b))
		return err
	})
}

// PhrasingUsage is the month's phrasing spend and today's call count.
func (s *Store) PhrasingUsage(ctx context.Context, org ids.OrgID, monthStart, dayStart time.Time) (int64, int, error) {
	var (
		cost  int64
		calls int
	)
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(pg_catalog.sum((phrasing ->> 'cost_usd_micros')::bigint) FILTER (WHERE (phrasing ->> 'at')::timestamptz >= $2), 0)::bigint,
		      pg_catalog.count(*) FILTER (WHERE (phrasing ->> 'at')::timestamptz >= $3 AND phrasing ? 'model' AND phrasing ->> 'model' <> 'template')
		    FROM deedglass.suggestions WHERE org_id = $1 AND phrasing ? 'at'`, org, monthStart, dayStart).Scan(&cost, &calls)
	})
	return cost, calls, err
}

// PersonName is how a person (or key) is named in a message: the display name or email.
func (s *Store) PersonName(ctx context.Context, org ids.OrgID, a learn.Actor) (string, error) {
	if a.Type != "user" {
		return "an admin key", nil
	}
	var name string
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(u.display_name, ''), u.email) FROM deedglass.users AS u WHERE u.id = $1`, a.ID).Scan(&name)
	})
	if err != nil {
		return "", notFound(err)
	}
	return name, nil
}

// ClaimRun inserts a learn_runs row (running); false when the org, kind and period already ran.
func (s *Store) ClaimRun(ctx context.Context, org ids.OrgID, kind, period string, at time.Time) (bool, error) {
	var ok bool
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO deedglass.learn_runs (org_id, kind, period_key, status, started_at) VALUES ($1, $2, $3, 'running', $4)
		    ON CONFLICT (org_id, kind, period_key) DO NOTHING`, org, kind, period, at)
		ok = tag.RowsAffected() == 1
		return err
	})
	return ok, err
}

// FinishRun records a run's outcome.
func (s *Store) FinishRun(ctx context.Context, org ids.OrgID, kind, period string, ok bool, stats map[string]any, errText string, at time.Time) error {
	if stats == nil {
		stats = map[string]any{}
	}
	b, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	status := "succeeded"
	if !ok {
		status = "failed"
	}
	if len(errText) > 2000 {
		errText = errText[:2000]
	}
	return s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.learn_runs SET status = $4, finished_at = $5, stats = $6::jsonb, error = NULLIF($7, '')
		    WHERE org_id = $1 AND kind = $2 AND period_key = $3`, org, kind, period, status, at, string(b), errText)
		return err
	})
}

// DigestRecipients lists the org's active members with one of roles, with their emails.
func (s *Store) DigestRecipients(ctx context.Context, org ids.OrgID, roles []string) ([]learn.DigestRecipient, error) {
	var out []learn.DigestRecipient
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT u.id, u.email, m.role FROM deedglass.memberships AS m
		    JOIN deedglass.users AS u ON u.id = m.user_id
		    WHERE m.org_id = $1 AND m.status = 'active' AND m.role = ANY($2::text[]) AND u.status = 'active'
		    ORDER BY m.role DESC, u.email LIMIT 200`, org, roles)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r learn.DigestRecipient
			if err := rows.Scan(&r.UserID, &r.Email, &r.Role); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// DigestNumbers reads the week's numbers.
func (s *Store) DigestNumbers(ctx context.Context, org ids.OrgID, from, to time.Time) (*learn.DigestNumbers, error) {
	n := &learn.DigestNumbers{}
	err := readTx(ctx, s.DB, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*),
		      pg_catalog.count(*) FILTER (WHERE witness_verdict = 'VERIFIED'),
		      pg_catalog.count(*) FILTER (WHERE witness_verdict IS NOT NULL),
		      pg_catalog.count(*) FILTER (WHERE decision = 'deny'),
		      pg_catalog.count(*) FILTER (WHERE witness_verdict = 'MISMATCH')
		    FROM deedglass.actions WHERE org_id = $1 AND evaluated_at >= $2 AND evaluated_at < $3 AND decision IS NOT NULL`,
			org, from, to).Scan(&n.Actions, &n.Verified, &n.Witnessed, &n.Denied, &n.Mismatches); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT COALESCE(NULLIF(decision_rule_id, ''), '@' || COALESCE(decision_source, 'none')) AS r, pg_catalog.count(*) AS c
		    FROM deedglass.actions WHERE org_id = $1 AND evaluated_at >= $2 AND evaluated_at < $3 AND decision = 'deny'
		    GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 3`, org, from, to)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r mailer.DigestRule
			if err := rows.Scan(&r.Rule, &r.Count); err != nil {
				rows.Close()
				return err
			}
			n.TopDenyRules = append(n.TopDenyRules, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FILTER (WHERE status <> 'resolved'),
		      pg_catalog.count(*) FILTER (WHERE created_at >= $2 AND created_at < $3),
		      pg_catalog.count(*) FILTER (WHERE resolved_at >= $2 AND resolved_at < $3)
		    FROM deedglass.anomalies WHERE org_id = $1`, org, from, to).Scan(&n.AnomaliesOpen, &n.AnomaliesOpened, &n.AnomaliesResolved); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT title FROM deedglass.anomalies WHERE org_id = $1 AND status <> 'resolved' AND title <> ''
		    ORDER BY window_end DESC, id DESC LIMIT 1`, org).Scan(&n.AnomalyTitle)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return n, nil
}
