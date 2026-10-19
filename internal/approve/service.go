package approve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/notify"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// Status is an approval's status. It leaves pending exactly once.
type Status string

const (
	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusDenied    Status = "denied"
	StatusExpired   Status = "expired"
	StatusCancelled Status = "cancelled"
)

// Vote is one person's decision.
type Vote string

const (
	VoteApprove Vote = "approve"
	VoteDeny    Vote = "deny"
)

// Via is how a person decided.
type Via string

const (
	ViaConsole   Via = "console"
	ViaEmailLink Via = "email_link"
	ViaSlack     Via = "slack"
	ViaCLIDev    Via = "cli_dev"
)

// Config is the approvals.* section of deedglass.yaml, with defaults for zero values.
type Config struct {
	DefaultExpiresIn time.Duration // 30m: when neither the rule nor the policy defaults give one
	EstimateTimeout  time.Duration // 500ms
	ContextMaxBytes  int           // 32768
	LinkMaxTTL       time.Duration // 24h
	LinkStatusWindow time.Duration // 15m
	MaxRecipients    int           // 20
}

func (c Config) withDefaults() Config {
	if c.DefaultExpiresIn <= 0 {
		c.DefaultExpiresIn = gate.DefaultApprovalTTL
	}
	if c.EstimateTimeout <= 0 || c.EstimateTimeout > 5*time.Second {
		c.EstimateTimeout = 500 * time.Millisecond
	}
	if c.ContextMaxBytes <= 0 {
		c.ContextMaxBytes = maxContextBytes
	}
	if c.LinkMaxTTL <= 0 || c.LinkMaxTTL > 24*time.Hour {
		c.LinkMaxTTL = 24 * time.Hour
	}
	if c.LinkStatusWindow <= 0 {
		c.LinkStatusWindow = 15 * time.Minute
	}
	if c.MaxRecipients <= 0 {
		c.MaxRecipients = 20
	}
	return c
}

// Gate is what approvals need from the Gate (*gate.Pipeline): the in-transaction conclusion of a
// denied action and the wait broker.
type Gate interface {
	ConcludeApprovalTx(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ActionID) error
	Broker() *gate.Broker
}

// Options are a Service's dependencies. DB and Ledger are required; Gate is set with Attach once
// the pipeline exists (it needs the Service first).
type Options struct {
	DB        *store.DB
	Ledger    *ledger.Ledger // with a seal key: every vote seals an admin deed
	BaseURL   *url.URL       // links: {base}/approve#t=…
	Notifiers []notify.Notifier
	Config    Config
	Logger    *slog.Logger
	Clock     func() time.Time
}

// Service is approvals (S22). It implements gate.Approvals and gate.ApprovalHooks; every way a
// person decides (Console, email link, Slack in S29, the dev CLI) ends in Decide.
type Service struct {
	o    Options
	cfg  Config
	log  *slog.Logger
	now  func() time.Time
	gate Gate
}

var (
	_ gate.Approvals     = (*Service)(nil)
	_ gate.ApprovalHooks = (*Service)(nil)
)

// New returns a Service.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Ledger == nil {
		return nil, errors.New("approve: DB and Ledger are required")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.BaseURL == nil {
		o.BaseURL, _ = url.Parse("http://127.0.0.1:8080")
	}
	return &Service{o: o, cfg: o.Config.withDefaults(), log: o.Logger, now: o.Clock}, nil
}

// Attach gives the Service the Gate it resumes and concludes actions through.
func (s *Service) Attach(g Gate) { s.gate = g }

// Config is the effective configuration.
func (s *Service) Config() Config { return s.cfg }

// --- gate.Approvals ---

// Prepare builds the context snapshot with no transaction open: the agent and its recent record,
// the rule, the arguments as a person may see them, S11's SQL facts and the connector's estimate
// (time-boxed; a slow or failed estimate is recorded as method none, never an error).
func (s *Service) Prepare(ctx context.Context, in gate.ApprovalInput, d policy.Decision) (gate.PreparedApproval, error) {
	snap := Snapshot{V: SnapshotVersion,
		Action: SnapAction{Tool: in.Tool, Connector: in.Connector, ConnectorType: in.ConnectorType, SideEffect: in.SideEffect,
			Mode: string(in.Mode), RequestDigest: in.RequestDigest},
		Agent:    SnapAgent{ID: in.Agent.String(), Name: in.AgentName, Labels: map[string]string{}, Recent: []SnapRecent{}},
		Sections: buildSections(in),
		Schedule: in.Schedule,
	}
	if in.OnBehalfOf != "" {
		o := in.OnBehalfOf
		snap.OnBehalfOf = &o
	}
	spec := specOf(d)
	snap.Policy = SnapPolicy{RuleID: d.RuleID, Reason: d.Reason, Verify: string(d.Verify), Required: spec.Required, Approvers: spec.Approvers}
	if in.PolicyID != "" {
		id, name, vid, v := in.PolicyID, in.PolicyName, in.PolicyVersionID, in.PolicyVersion
		snap.Policy.ID, snap.Policy.Name, snap.Policy.VersionID, snap.Policy.Version = &id, &name, &vid, &v
	}
	snap.Args, snap.ArgsNote = redactArgs(in.Args)
	if f := d.Facts.SQL; f.Present {
		tables := append([]string{}, f.Tables...)
		snap.SQL = &SnapSQL{Kind: f.Kind, Tables: tables, Writes: f.Writes, HasWhere: f.HasWhere}
	}
	// The agent: its status and labels now, and its last sealed actions; and whether the org is in
	// PHI mode (HC-P09), which changes what the snapshot may hold.
	var phiMode bool
	err := s.o.DB.OrgReadTx(ctx, in.Org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT COALESCE((settings ->> 'phi_mode') = 'true', false) FROM deedglass.orgs WHERE id = $1`,
			in.Org).Scan(&phiMode); err != nil {
			return err
		}
		var labels []byte
		if err := tx.QueryRow(ctx, `SELECT status, labels FROM deedglass.agents WHERE org_id = $1 AND id = $2`, in.Org, in.Agent).
			Scan(&snap.Agent.Status, &labels); err != nil {
			return err
		}
		_ = json.Unmarshal(labels, &snap.Agent.Labels)
		if snap.Agent.Labels == nil {
			snap.Agent.Labels = map[string]string{}
		}
		return nil
	})
	if err != nil {
		return gate.PreparedApproval{}, err
	}
	if phiMode {
		phiSnapshot(&snap, in)
	}
	if list, err := s.o.Ledger.ListActions(ctx, in.Org, ledger.ActionFilter{Agent: in.Agent.String(), States: []string{"sealed"}},
		ledger.Page{Limit: MaxRecent}); err == nil {
		for _, a := range list.Data {
			r := SnapRecent{Action: a.ID, Tool: a.Tool, Status: a.Status, At: formatTime(time.Time(a.CreatedAt))}
			if a.Witness != nil {
				lv, vd := a.Witness.Level, a.Witness.Verdict
				r.Level, r.Verdict = &lv, &vd
			}
			snap.Agent.Recent = append(snap.Agent.Recent, r)
		}
	}
	// The estimate, outside any transaction, within its budget.
	if snap.SQL != nil {
		est := connector.NoEstimate(connector.EstimateUnsupported)
		if e, ok := in.Exec.(connector.Estimating); ok && e.CanEstimate() {
			ectx, cancel := context.WithTimeout(ctx, s.cfg.EstimateTimeout)
			got, eerr := e.Estimate(ectx, in.Call)
			cancel()
			est = got
			if eerr != nil {
				s.log.Warn("approve: the connector's estimate failed", "org_id", in.Org.String(), "agent_id", in.Agent.String(),
					"tool", in.Tool, "err", eerr)
			}
		}
		se := &SnapEstimate{Method: est.Method}
		if est.Method == connector.EstimateExplain {
			rows, ms := est.Rows, est.Took.Milliseconds()
			se.Rows, se.TookMS = &rows, &ms
		} else {
			se.Reason = est.Detail
		}
		snap.SQL.Estimate = se
	}
	snap.Summary = Summary(in.AgentName, snap.summaryFacts())
	if sum := sectionSummary(snap.Sections); sum != "" {
		snap.Summary = sum // S23: the undo section says what is put back, by whom, for whom
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return gate.PreparedApproval{}, err
	}
	return gate.PreparedApproval{Context: b, RuleID: d.RuleID}, nil
}

// specOf is the decision's approval spec with defaults.
func specOf(d policy.Decision) Spec {
	s := Spec{Approvers: []string{"role:admin"}, Required: 1}
	if a := d.Approval; a != nil {
		if len(a.Approvers) > 0 {
			s.Approvers = append([]string{}, a.Approvers...)
		}
		if a.Required > 0 {
			s.Required = a.Required
		}
		s.ExpiresIn = a.ExpiresIn
	}
	return s
}

// Request records the approval inside Tx A: the snapshot completed with the action id and times,
// digested, then the approval row, the approval_notify job and approval.requested. When fewer
// eligible people exist than the rule needs (after leaving out the person the action is for,
// unless the org allows self-countersign), the approval is recorded denied by the system and
// *gate.UnsatisfiableError is returned: the Gate denies and seals the action in this transaction.
func (s *Service) Request(ctx context.Context, tx pgx.Tx, in gate.ApprovalInput, d policy.Decision, p gate.PreparedApproval) (ids.ApprovalID, time.Time, error) {
	var snap Snapshot
	if err := json.Unmarshal(p.Context, &snap); err != nil {
		return ids.ApprovalID{}, time.Time{}, fmt.Errorf("approve: the prepared snapshot can't be read: %w", err)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	spec := specOf(d)
	ttl := s.cfg.DefaultExpiresIn
	if spec.ExpiresIn > 0 {
		ttl = time.Duration(spec.ExpiresIn) * time.Second
	}
	exp := now.Add(ttl)
	id := ids.New[ids.ApprovalKind]()
	snap.Action.ID = in.ID.String()
	snap.RequestedAt, snap.ExpiresAt = formatTime(now), formatTime(exp)
	ctxJSON, err := fitContext(&snap, s.cfg.ContextMaxBytes)
	if err != nil {
		return ids.ApprovalID{}, time.Time{}, err
	}
	digest, err := digestOf(ctxJSON)
	if err != nil {
		return ids.ApprovalID{}, time.Time{}, err
	}
	org, err := readOrg(ctx, tx, in.Org)
	if err != nil {
		return ids.ApprovalID{}, time.Time{}, err
	}
	all, err := members(ctx, tx, in.Org)
	if err != nil {
		return ids.ApprovalID{}, time.Time{}, err
	}
	_, can := eligiblePeople(all, spec, in.OnBehalfOf, org.Settings.SelfCountersign == "allow")
	specJSON, _ := json.Marshal(spec)
	row := []any{id, in.Org, in.ID, nonEmpty(d.RuleID), nonEmpty(clip(d.Reason, 2000)), spec.Required, string(specJSON), exp,
		string(ctxJSON), digest, in.Agent, nonEmpty(in.Tool), nonEmpty(in.OnBehalfOf), nonEmpty(in.PolicyVersionID)}
	if can < spec.Required {
		msg := MsgUnsatisfiable(spec.Required, can)
		_, err := tx.Exec(ctx, `INSERT INTO deedglass.approvals (id, org_id, action_id, status, rule_id, reason, approvers_required,
		    approver_spec, expires_at, context, context_digest, agent_id, tool, on_behalf_of, policy_version_id, decided_at, decision_reason)
		    VALUES ($1, $2, $3, 'denied', $4, $5, $6, $7::jsonb, $8, $9::jsonb, $10, $11, $12, $13, $14, $16::timestamptz, $15)`,
			append(row, msg, now)...)
		if err != nil {
			return ids.ApprovalID{}, time.Time{}, err
		}
		if err := s.publish(ctx, tx, in.Org, id, ledger.EventApprovalDecided, StatusDenied, nil); err != nil {
			return ids.ApprovalID{}, time.Time{}, err
		}
		return id, exp, &gate.UnsatisfiableError{Approval: id, Message: msg}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deedglass.approvals (id, org_id, action_id, status, rule_id, reason, approvers_required,
	    approver_spec, expires_at, context, context_digest, agent_id, tool, on_behalf_of, policy_version_id)
	    VALUES ($1, $2, $3, 'pending', $4, $5, $6, $7::jsonb, $8, $9::jsonb, $10, $11, $12, $13, $14)`, row...); err != nil {
		return ids.ApprovalID{}, time.Time{}, err
	}
	if !d.NotifyNone { // S40: `notify: []` sends no email or Slack; the approval waits in the Console
		if err := store.EnqueueJob(ctx, tx, in.Org, in.ID, gate.JobApprovalNotify, now, 5); err != nil {
			return ids.ApprovalID{}, time.Time{}, err
		}
	}
	if err := s.publish(ctx, tx, in.Org, id, ledger.EventApprovalRequested, StatusPending, nil); err != nil {
		return ids.ApprovalID{}, time.Time{}, err
	}
	return id, exp, nil
}

// --- gate.ApprovalHooks ---

// OnExpired runs in the sweeper's transaction after the approval moved to expired: links are
// voided and approval.decided (status expired, no decider, no deed) is published.
func (s *Service) OnExpired(ctx context.Context, tx pgx.Tx, org ids.OrgID, approval string) error {
	return s.ended(ctx, tx, org, approval, StatusExpired)
}

// OnCancelled runs in Pipeline.Cancel's transaction after the approval moved to cancelled.
func (s *Service) OnCancelled(ctx context.Context, tx pgx.Tx, org ids.OrgID, approval string, _ string) error {
	return s.ended(ctx, tx, org, approval, StatusCancelled)
}

func (s *Service) ended(ctx context.Context, tx pgx.Tx, org ids.OrgID, approval string, st Status) error {
	id, err := ids.Parse[ids.ApprovalKind](approval)
	if err != nil {
		return err
	}
	if err := voidTokens(ctx, tx, org, id); err != nil {
		return err
	}
	return s.publish(ctx, tx, org, id, ledger.EventApprovalDecided, st, nil)
}

// publish sends approval.requested or approval.decided for approval id, read in tx.
func (s *Service) publish(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ApprovalID, typ string, st Status, deedID *string) error {
	var (
		action, agent string
		tool, rule    *string
		required, n   int
	)
	if err := tx.QueryRow(ctx, `SELECT ap.action_id, COALESCE(ap.agent_id, a.agent_id), COALESCE(ap.tool, a.tool), ap.rule_id,
	    ap.approvers_required, pg_catalog.cardinality(ap.approved_by)
	    FROM deedglass.approvals AS ap JOIN deedglass.actions AS a ON a.org_id = ap.org_id AND a.id = ap.action_id
	    WHERE ap.org_id = $1 AND ap.id = $2`, org, id).Scan(&action, &agent, &tool, &rule, &required, &n); err != nil {
		return err
	}
	status := string(st)
	return s.o.Ledger.Publish(ctx, tx, ledger.Event{Type: typ, Org: org.String(), ID: id.String(), Action: &action, Agent: &agent,
		Tool: tool, RuleID: rule, Status: &status, Required: &required, Countersigned: &n, Deed: deedID})
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
