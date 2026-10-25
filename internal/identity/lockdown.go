package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Lockdown mode's org setting (ADR-117, docs/LOCKDOWN.md): orgs.settings.lockdown turns it on for
// one org; lockdown: true in the server's config pins it on for every org (the Gate reads both).
// There is no API route that changes it. These operations run on the server (deedglass lockdown),
// as the operator, and name the people given with --as in the record: the CLI can't tell who is
// typing (LIMITATIONS).

// LockdownOffWindow is how long a request to turn lockdown off waits for a second person.
const LockdownOffWindow = 30 * time.Minute

// LockdownError is a refusal of a lockdown operation, with the CLI's code and next step.
type LockdownError struct {
	Code, Message, Next string
}

func (e *LockdownError) Error() string { return e.Message }

// Lockdown refusal codes (CLI error codes).
const (
	CodeLockdownOff          = "lockdown_off" // off or ack while lockdown isn't on
	CodeLockdownPinned       = "lockdown_pinned_by_config"
	CodeLockdownNotEligible  = "lockdown_not_eligible"   // the person isn't an active owner or admin
	CodeLockdownNoRequest    = "lockdown_no_off_request" // countersign with nothing to countersign
	CodeCountersignSame      = "countersign_same_person"
	CodeLockdownNotHeld      = "lockdown_not_held" // ack of an action that holds nothing
	CodeLockdownAlreadyAcked = "lockdown_already_acknowledged"
)

// LockdownOffRequest is orgs.settings.lockdown_off: a pending request to turn lockdown off.
type LockdownOffRequest struct {
	RequestedBy string    `json:"requested_by"` // usr_…
	Email       string    `json:"email"`
	RequestedAt time.Time `json:"requested_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Deed        string    `json:"deed"`
}

// LockdownHold is one action that holds its agent's writes.
type LockdownHold struct {
	Action string `json:"action"`
	Agent  string `json:"agent"`
	Name   string `json:"agent_name"`
}

// LockdownStatus is what deedglass lockdown status shows for one org.
type LockdownStatus struct {
	Object      string              `json:"object"`
	Org         string              `json:"org"`
	OrgSetting  bool                `json:"org_setting"`
	PHIMode     bool                `json:"phi_mode"`
	FHIR        int                 `json:"fhir_connectors"`
	Approvers   int                 `json:"approvers"`            // active owners and admins
	Unlabelled  []string            `json:"agents_without_label"` // active agents with no connector label
	OffRequest  *LockdownOffRequest `json:"off_request"`
	Holds       []LockdownHold      `json:"holds"`
	ChangedDeed *string             `json:"deed,omitempty"`
}

// LockdownStatus reads the org's lockdown facts.
func (s *Service) LockdownStatus(ctx context.Context, org ids.OrgID) (LockdownStatus, error) {
	var st LockdownStatus
	err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		st, err = lockdownStatus(ctx, tx, org)
		return err
	})
	return st, err
}

func lockdownStatus(ctx context.Context, tx pgx.Tx, org ids.OrgID) (LockdownStatus, error) {
	st := LockdownStatus{Object: "lockdown_status", Org: org.String(), Unlabelled: []string{}, Holds: []LockdownHold{}}
	var off []byte
	if err := tx.QueryRow(ctx, `SELECT COALESCE((settings ->> 'lockdown') = 'true', false), COALESCE((settings ->> 'phi_mode') = 'true', false),
	      settings -> 'lockdown_off' FROM deedglass.orgs WHERE id = $1`, org).Scan(&st.OrgSetting, &st.PHIMode, &off); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return st, store.ErrNotFound
		}
		return st, err
	}
	if len(off) > 0 && string(off) != "null" {
		var r LockdownOffRequest
		if json.Unmarshal(off, &r) == nil && r.RequestedBy != "" {
			st.OffRequest = &r
		}
	}
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::integer FROM deedglass.connectors WHERE org_id = $1 AND type = 'fhir' AND deleted_at IS NULL`,
		org).Scan(&st.FHIR); err != nil {
		return st, err
	}
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::integer FROM deedglass.memberships WHERE org_id = $1 AND status = 'active' AND role IN ('owner', 'admin')`,
		org).Scan(&st.Approvers); err != nil {
		return st, err
	}
	rows, err := tx.Query(ctx, `SELECT name FROM deedglass.agents WHERE org_id = $1 AND kind = 'agent' AND status <> 'stopped'
	      AND COALESCE(labels ->> 'connector', '') = '' ORDER BY name LIMIT 100`, org)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return st, err
		}
		st.Unlabelled = append(st.Unlabelled, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT ON (a.agent_id) a.id, a.agent_id, g.name FROM deedglass.actions AS a
	    JOIN deedglass.agents AS g ON g.org_id = a.org_id AND g.id = a.agent_id
	    WHERE a.org_id = $1 AND a.witness_verdict = 'UNVERIFIABLE' AND (a.decision_detail -> 'lockdown') IS NOT NULL
	      AND NOT EXISTS (SELECT 1 FROM deedglass.admin_events AS e
	        WHERE e.org_id = a.org_id AND e.action = $2 AND e.target_type = 'action' AND e.target_id = a.id::text)
	    ORDER BY a.agent_id, a.created_at, a.id LIMIT 100`, org, ledger.AdminLockdownAcknowledged)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var h LockdownHold
		if err := rows.Scan(&h.Action, &h.Agent, &h.Name); err != nil {
			return st, err
		}
		st.Holds = append(st.Holds, h)
	}
	return st, rows.Err()
}

// approverOf checks that the person is an active owner or admin of the org.
func approverOf(ctx context.Context, tx pgx.Tx, org ids.OrgID, user ids.UserID, email string) error {
	var role string
	err := tx.QueryRow(ctx, `SELECT role FROM deedglass.memberships WHERE org_id = $1 AND user_id = $2 AND status = 'active'`, org, user).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && role != string(RoleOwner) && role != string(RoleAdmin) {
		return &LockdownError{Code: CodeLockdownNotEligible, Message: fmt.Sprintf("%s isn't an active owner or admin of this org.", email),
			Next: "Name an owner or admin with --as."}
	}
	return err
}

// LockdownOn turns lockdown on for the org and seals org.updated (fields settings.lockdown, and
// settings.phi_mode when the org has a FHIR connector and PHI mode was off). Already on: no deed.
func (s *Service) LockdownOn(ctx context.Context, org ids.OrgID, reason string) (LockdownStatus, error) {
	var st LockdownStatus
	err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if _, err = tx.Exec(ctx, `SELECT 1 FROM deedglass.orgs WHERE id = $1 FOR NO KEY UPDATE`, org); err != nil {
			return err
		}
		if st, err = lockdownStatus(ctx, tx, org); err != nil {
			return err
		}
		fields := []string{}
		if !st.OrgSetting {
			fields = append(fields, "settings.lockdown")
		}
		phi := st.FHIR > 0 && !st.PHIMode
		if phi {
			fields = append(fields, "settings.phi_mode")
		}
		if len(fields) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.orgs SET settings = (settings - 'lockdown_off') || pg_catalog.jsonb_build_object('lockdown', true)
		    WHERE id = $1`, org); err != nil {
			return err
		}
		if phi {
			if _, err := tx.Exec(ctx, `UPDATE deedglass.orgs SET settings = pg_catalog.jsonb_set(settings, '{phi_mode}', 'true'::jsonb) WHERE id = $1`, org); err != nil {
				return err
			}
			if _, err := store.MarkPHI(ctx, tx, org); err != nil {
				return err
			}
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminOrgUpdated, Actor: operator(), Target: orgRef(org), Reason: reason,
			Data: map[string]any{"fields": fields, "lockdown": true, "phi_mode": st.PHIMode || phi, "via": "server_cli"}})
		if err != nil {
			return err
		}
		st.OrgSetting, st.PHIMode, st.OffRequest, st.ChangedDeed = true, st.PHIMode || phi, nil, d
		return nil
	})
	return st, err
}

// LockdownOff is the two-person turn-off. Without countersign it records a request by user (sealed
// lockdown.off_requested, waiting LockdownOffWindow); with countersign, a different owner or admin
// completes the open request: lockdown goes off and org.updated names both people. pinned is the
// server's lockdown: true, which no org can turn off.
func (s *Service) LockdownOff(ctx context.Context, org ids.OrgID, user ids.UserID, email string, countersign, pinned bool, reason string, now time.Time) (LockdownStatus, error) {
	if pinned {
		return LockdownStatus{}, &LockdownError{Code: CodeLockdownPinned, Message: "Lockdown is pinned on by lockdown: true in this server's config, so no org can turn it off.",
			Next: "Remove lockdown from deedglass.yaml (or DEEDGLASS_LOCKDOWN) on the server and restart it first; deedglass doctor shows where it is set."}
	}
	var st LockdownStatus
	err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if _, err = tx.Exec(ctx, `SELECT 1 FROM deedglass.orgs WHERE id = $1 FOR NO KEY UPDATE`, org); err != nil {
			return err
		}
		if st, err = lockdownStatus(ctx, tx, org); err != nil {
			return err
		}
		if !st.OrgSetting {
			return &LockdownError{Code: CodeLockdownOff, Message: "Lockdown isn't on for this org.", Next: "Nothing to turn off. deedglass lockdown status shows the details."}
		}
		if err := approverOf(ctx, tx, org, user, email); err != nil {
			return err
		}
		req := st.OffRequest
		open := req != nil && now.Before(req.ExpiresAt)
		if !countersign {
			if open && req.RequestedBy == user.String() {
				return nil // the same request again: no second deed
			}
			if open {
				return &LockdownError{Code: CodeLockdownNoRequest, Message: fmt.Sprintf("%s already asked to turn lockdown off, until %s UTC.", req.Email, req.ExpiresAt.UTC().Format("15:04")),
					Next: "Countersign it instead: deedglass lockdown off --countersign --as " + email + "."}
			}
			exp := now.Add(LockdownOffWindow)
			d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminLockdownOffRequested, Actor: operator(), Target: orgRef(org), Reason: reason,
				Data: map[string]any{"requested_by": user.String(), "expires_at": exp.UTC().Format(time.RFC3339), "via": "server_cli"}})
			if err != nil {
				return err
			}
			r := LockdownOffRequest{RequestedBy: user.String(), Email: email, RequestedAt: now.UTC(), ExpiresAt: exp.UTC(), Deed: deref(d)}
			b, _ := json.Marshal(r)
			if _, err := tx.Exec(ctx, `UPDATE deedglass.orgs SET settings = pg_catalog.jsonb_set(settings, '{lockdown_off}', $2::jsonb) WHERE id = $1`, org, b); err != nil {
				return err
			}
			st.OffRequest, st.ChangedDeed = &r, d
			return nil
		}
		if !open {
			return &LockdownError{Code: CodeLockdownNoRequest, Message: "Nobody has asked to turn lockdown off in the last 30 minutes, so there is nothing to countersign.",
				Next: "One owner or admin asks first: deedglass lockdown off --as <email>. Then a different one countersigns."}
		}
		if req.RequestedBy == user.String() {
			return &LockdownError{Code: CodeCountersignSame, Message: "The person who asked to turn lockdown off can't also countersign it. Lockdown stays on.",
				Next: "A different owner or admin countersigns: deedglass lockdown off --countersign --as <their email>."}
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.orgs SET settings = (settings - 'lockdown_off') || pg_catalog.jsonb_build_object('lockdown', false) WHERE id = $1`, org); err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminOrgUpdated, Actor: operator(), Target: orgRef(org), Reason: reason,
			Data: map[string]any{"fields": []string{"settings.lockdown"}, "lockdown": false, "requested_by": req.RequestedBy,
				"countersigned_by": user.String(), "off_request_deed": req.Deed, "via": "server_cli"}})
		if err != nil {
			return err
		}
		st.OrgSetting, st.OffRequest, st.ChangedDeed = false, nil, d
		return nil
	})
	return st, err
}

// LockdownAck is a person acknowledging an action that ended Can't check in lockdown, which
// releases the hold on its agent's writes. Sealed as lockdown.acknowledged (target the action).
func (s *Service) LockdownAck(ctx context.Context, org ids.OrgID, user ids.UserID, email string, action ids.ActionID, reason string) (*string, error) {
	var d *string
	err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := approverOf(ctx, tx, org, user, email); err != nil {
			return err
		}
		var agent, verdict string
		var held, acked bool
		err := tx.QueryRow(ctx, `SELECT a.agent_id, COALESCE(a.witness_verdict, ''), (a.decision_detail -> 'lockdown') IS NOT NULL,
		      EXISTS (SELECT 1 FROM deedglass.admin_events AS e WHERE e.org_id = a.org_id AND e.action = $3 AND e.target_type = 'action' AND e.target_id = a.id::text)
		    FROM deedglass.actions AS a WHERE a.org_id = $1 AND a.id = $2`, org, action, ledger.AdminLockdownAcknowledged).Scan(&agent, &verdict, &held, &acked)
		if errors.Is(err, pgx.ErrNoRows) {
			return &LockdownError{Code: CodeLockdownNotHeld, Message: fmt.Sprintf("No action %s in this org.", action), Next: "deedglass lockdown status lists the actions that hold an agent."}
		}
		if err != nil {
			return err
		}
		if verdict != "UNVERIFIABLE" || !held {
			return &LockdownError{Code: CodeLockdownNotHeld, Message: fmt.Sprintf("Action %s doesn't hold its agent: only a Can't check result admitted in lockdown does.", action),
				Next: "deedglass lockdown status lists the actions that hold an agent."}
		}
		if acked {
			return &LockdownError{Code: CodeLockdownAlreadyAcked, Message: fmt.Sprintf("Action %s was already acknowledged.", action), Next: "Nothing to do."}
		}
		d, err = s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminLockdownAcknowledged, Actor: operator(),
			Target: &deed.Ref{Type: "action", ID: action.String()}, Reason: reason,
			Data: map[string]any{"action": action.String(), "agent": agent, "verdict": verdict, "acknowledged_by": user.String(), "via": "server_cli"}})
		return err
	})
	return d, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
