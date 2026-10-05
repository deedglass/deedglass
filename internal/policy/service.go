package policy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/deed"
)

// Service manages policies as a team manages code (S12 §0): immutable versions, tests, shadow
// before enforce, and every activation sealed as a deed. HTTP handlers (internal/policyapi) and
// the CLI's server commands call it; it never re-implements auth: the route checked the role or
// scope, and the Caller says who is acting.
type Service struct {
	repo     Repo
	registry *Registry // optional: the dry run reads "active" through it; changes invalidate it
	now      func() time.Time
}

// ServiceOption configures a Service.
type ServiceOption func(*Service)

// WithRegistry makes the dry run read the active policies through r (exactly what the gate would
// evaluate), and invalidates r's entry for an org after each change on this instance.
func WithRegistry(r *Registry) ServiceOption { return func(s *Service) { s.registry = r } }

// WithServiceClock sets the clock (tests).
func WithServiceClock(now func() time.Time) ServiceOption { return func(s *Service) { s.now = now } }

// NewService returns a Service over repo.
func NewService(repo Repo, opts ...ServiceOption) *Service {
	s := &Service{repo: repo, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Caller is who acts: a person ({user, usr_…}), an org admin key ({api_key, key_…}) or Deedglass
// itself ({system, deedglass}: onboarding, dev seed).
type Caller struct {
	Org       string // org_…
	Actor     deed.Ref
	RequestID string
}

// SystemActor is Deedglass acting on its own (onboarding).
var SystemActor = deed.Ref{Type: "system", ID: "deedglass"}

func (c Caller) by() string {
	if c.Actor.Type == "system" {
		return "system"
	}
	return c.Actor.ID
}

// --- errors -------------------------------------------------------------------------------------

// ErrKind maps a service error to an API error code.
type ErrKind string

const (
	KindInvalid    ErrKind = "policy_invalid"    // 400, with diagnostics
	KindValidation ErrKind = "validation_failed" // 400, with a param
	KindConflict   ErrKind = "conflict"          // 409
	KindNotFound   ErrKind = "not_found"         // 404
)

// Error is a service refusal with the exact message the API returns.
type Error struct {
	Kind        ErrKind
	Param       string
	Message     string
	Diagnostics []Diagnostic
}

func (e *Error) Error() string { return string(e.Kind) + ": " + e.Message }

// The service's messages (S12 §5). Each is exact and ends with a next step.
const (
	msgAlreadyEnforced  = "%s v%d is already the enforced policy. Nothing changed."
	msgAlreadyShadow    = "%s v%d is already the shadow policy. Nothing changed."
	msgShadowEnforced   = "%s v%d is the enforced policy, so it can't also run in shadow. Shadow a newer version instead."
	msgDeactivateEnf    = "The enforced policy can't be switched off. Activate another version or default-deny instead."
	msgNotShadow        = "%s v%d isn't the shadow policy, so there is nothing to switch off. Nothing changed."
	msgAckDefaultAllow  = `This policy allows every unmatched action (default_allow). To enforce it anyway, repeat the request with acknowledge_warnings: ["default_allow"].`
	msgAckUnknown       = "This version has no %q warning to acknowledge. Remove it from acknowledge_warnings."
	msgNameMismatch     = "This YAML is named %q, but %s is %q. Push it as a new policy, or change the name."
	msgNameTaken        = "A policy named %q already exists (%s). Push a new version to it instead: POST /v1/policies/%s/versions."
	msgReasonMissing    = "A reason is required: say in 3 to 280 characters why this change is safe. It goes into the deed."
	msgReasonBad        = "The reason must be 3 to 280 characters on one line, without control characters. It goes into the deed."
	msgModeBad          = "mode must be enforce or shadow."
	msgNotesTooLong     = "Notes are at most 2,000 characters. Shorten them."
	msgSourceEmpty      = "source is required: the policy YAML, as a string."
	msgPolicyNotFound   = "There's no policy %s in this org. Check the id or name, or list policies with GET /v1/policies."
	msgVersionNotFound  = "%s has no version %s. List its versions with GET /v1/policies/%s/versions."
	msgAgentNotFound    = "There's no agent with id %s in this org. Check the id, or leave agent.id out to use a local agent."
	msgVersionTampered  = "%s v%d no longer compiles to its stored version_hash, so Deedglass won't activate it. Push the policy again as a new version."
	msgDryRunPolicy     = `policy must be "active", {"id": "pol_…", "version": 3} or {"source": "<yaml>"}.`
	msgDryRunNoneActive = "No policy is active in this org. Activate one, or evaluate a specific version or source."
)

func errf(kind ErrKind, param, format string, a ...any) *Error {
	return &Error{Kind: kind, Param: param, Message: fmt.Sprintf(format, a...)}
}

// invalidPolicy is policy_invalid with every diagnostic and a message naming the first error.
func invalidPolicy(list []Diagnostic) *Error {
	var errs []Diagnostic
	for _, d := range list {
		if d.Severity == SevError {
			errs = append(errs, d)
		}
	}
	msg := "The policy was not accepted."
	if len(errs) > 0 {
		first := errs[0]
		where := ""
		if first.Line > 0 {
			where = fmt.Sprintf(" on line %d", first.Line)
		}
		text := strings.ReplaceAll(first.Message, "`", "")
		if len(errs) == 1 {
			msg = fmt.Sprintf("The policy has 1 error%s: %s", where, text)
		} else {
			msg = fmt.Sprintf("The policy has %d errors. The first is%s: %s", len(errs), where, text)
		}
	}
	return &Error{Kind: KindInvalid, Param: "source", Message: msg, Diagnostics: list}
}

// --- API objects ---------------------------------------------------------------------------------

// Time renders as the API's timestamps: RFC 3339, UTC, milliseconds.
type Time time.Time

// MarshalJSON writes "2026-10-04T09:12:44.000Z".
func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(t).UTC().Format(deed.TimeFormat) + `"`), nil
}

// UnmarshalJSON reads any RFC 3339 time (clients such as the CLI decode API objects).
func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	x, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	*t = Time(x)
	return nil
}

func tp(t *time.Time) *Time {
	if t == nil {
		return nil
	}
	x := Time(*t)
	return &x
}

// ActorRef is an actor as the API shows it: {"object": "user", "id": "usr_…"}, {"object":
// "api_key", "id": "key_…"} or {"object": "system", "id": null}.
type ActorRef struct {
	Object string  `json:"object"`
	ID     *string `json:"id"`
}

func actorRef(r deed.Ref) ActorRef {
	if r.Type == "system" || r.ID == "" {
		return ActorRef{Object: "system"}
	}
	id := r.ID
	return ActorRef{Object: r.Type, ID: &id}
}

// refFromBy turns a stored activated_by / deactivated_by ("usr_…", "key_…", "system") into a ref.
func refFromBy(by string) ActorRef {
	switch {
	case strings.HasPrefix(by, "usr_"):
		return actorRef(deed.Ref{Type: "user", ID: by})
	case strings.HasPrefix(by, "key_"):
		return actorRef(deed.Ref{Type: "api_key", ID: by})
	}
	return ActorRef{Object: "system"}
}

// Policy statuses.
const (
	StatusEnforced = "enforced"
	StatusShadow   = "shadow"
	StatusInactive = "inactive"
)

// Policy is the policy object.
type Policy struct {
	Object        string `json:"object"` // "policy"
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Status        string `json:"status"` // enforced | shadow | inactive
	ActiveVersion *int   `json:"active_version"`
	ShadowVersion *int   `json:"shadow_version"`
	LatestVersion int    `json:"latest_version"`
	CreatedAt     Time   `json:"created_at"`
	UpdatedAt     Time   `json:"updated_at"`
}

// Version is the policy_version object. Source is present on a single get only.
type Version struct {
	Object      string       `json:"object"` // "policy_version"
	ID          string       `json:"id"`
	Policy      string       `json:"policy"`
	Version     int          `json:"version"`
	Name        string       `json:"name"`
	Status      string       `json:"status"`
	VersionHash string       `json:"version_hash"`
	SourceHash  string       `json:"source_hash"`
	Lang        string       `json:"lang"`
	Origin      Origin       `json:"origin"`
	Author      ActorRef     `json:"author"`
	Notes       *string      `json:"notes"`
	Rules       int          `json:"rules"`
	Budgets     int          `json:"budgets"`
	Warnings    []Diagnostic `json:"warnings"`
	Source      *string      `json:"source,omitempty"`
	CreatedAt   Time         `json:"created_at"`
}

// VersionBrief names a version inside other objects.
type VersionBrief struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	VersionHash string `json:"version_hash"`
}

// PreviousRef is what an activation replaced.
type PreviousRef struct {
	Policy  string `json:"policy"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// Activation is the policy_activation object.
type Activation struct {
	Object               string       `json:"object"` // "policy_activation"
	ID                   string       `json:"id"`     // pa_…
	Policy               string       `json:"policy"`
	Name                 string       `json:"name"`
	Mode                 Mode         `json:"mode"`
	Version              VersionBrief `json:"version"`
	Previous             *PreviousRef `json:"previous"`
	PromotedFromShadow   bool         `json:"promoted_from_shadow"`
	AcknowledgedWarnings []string     `json:"acknowledged_warnings"`
	Reason               string       `json:"reason"`
	ActivatedBy          ActorRef     `json:"activated_by"`
	ActivatedAt          Time         `json:"activated_at"`
	DeactivatedAt        *Time        `json:"deactivated_at"`
	DeactivatedBy        *ActorRef    `json:"deactivated_by"`
	Deeds                []string     `json:"deeds"` // the sealed record of this change, in seal order
}

// ActiveEntry is one live activation in the active set.
type ActiveEntry struct {
	Policy      string `json:"policy"`
	Name        string `json:"name"`
	Version     int    `json:"version"`
	VersionID   string `json:"version_id"`
	VersionHash string `json:"version_hash"`
	Activation  string `json:"activation"`
	ActivatedAt Time   `json:"activated_at"`
	Deed        string `json:"deed"`
}

// ActiveSet is the policy_active_set object: the org's enforce and shadow.
type ActiveSet struct {
	Object  string       `json:"object"` // "policy_active_set"
	Enforce *ActiveEntry `json:"enforce"`
	Shadow  *ActiveEntry `json:"shadow"`
}

// List is a cursor page.
type List[T any] struct {
	Object     string  `json:"object"` // "list"
	Data       []T     `json:"data"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

// Page is a list request: 1..100 items after an opaque cursor.
type Page struct {
	Limit  int
	Cursor string
}

// --- requests ------------------------------------------------------------------------------------

// PushRequest is a new policy or version.
type PushRequest struct {
	Source       []byte
	Notes        string
	Origin       Origin // default manual
	SuggestionID string // origin suggestion only (S28)
}

// ActivateRequest is POST …/activate.
type ActivateRequest struct {
	Mode                Mode
	Reason              string
	AcknowledgeWarnings []string
}

// DeactivateRequest is POST …/deactivate.
type DeactivateRequest struct {
	Reason string
}

// MaxNotes is the longest notes field.
const MaxNotes = 2000

// --- create and versions -------------------------------------------------------------------------

func compileForPush(req PushRequest) (*Compiled, *Error) {
	if len(bytes.TrimSpace(req.Source)) == 0 {
		return nil, errf(KindValidation, "source", msgSourceEmpty)
	}
	if utf8.RuneCountInString(req.Notes) > MaxNotes {
		return nil, errf(KindValidation, "notes", msgNotesTooLong)
	}
	c, list := Compile(req.Source, Options{})
	if c == nil {
		return nil, invalidPolicy(list)
	}
	return c, nil
}

func versionRow(c *Compiled, req PushRequest, caller Caller, policyID, id string, n int) VersionRow {
	origin := req.Origin
	if origin == "" {
		origin = OriginManual
	}
	w := c.Warnings
	if w == nil {
		w = []Diagnostic{}
	}
	return VersionRow{ID: id, PolicyID: policyID, Name: c.Name, Version: n, Source: req.Source, VersionHash: c.VersionHash,
		SourceHash: c.SourceHash, Lang: c.Lang, Rules: len(c.Rules), Budgets: len(c.Budgets), Warnings: w, Origin: origin,
		SuggestionID: req.SuggestionID, Author: caller.Actor, Notes: req.Notes}
}

// Create saves a new policy and its v1 (not active). No deed: the immutable version row (author,
// time, source_hash, version_hash) is the record; deeds are for changes that take effect.
func (s *Service) Create(ctx context.Context, caller Caller, req PushRequest) (*Policy, *Version, error) {
	c, e := compileForPush(req)
	if e != nil {
		return nil, nil, e
	}
	var pol *Policy
	var ver *Version
	err := s.repo.InOrgTx(ctx, caller.Org, func(tx Tx) error {
		var err error
		pol, ver, err = createTx(ctx, tx, caller, c, req)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return pol, ver, nil
}

// createTx is Create inside tx, for a compiled policy.
func createTx(ctx context.Context, tx Tx, caller Caller, c *Compiled, req PushRequest) (*Policy, *Version, error) {
	if err := tx.LockOrgPolicies(ctx); err != nil {
		return nil, nil, err
	}
	if p, err := tx.GetPolicy(ctx, c.Name); err == nil {
		return nil, nil, errf(KindConflict, "source", msgNameTaken, c.Name, p.ID, p.ID)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, nil, err
	}
	pr := PolicyRow{ID: tx.NewID("pol"), Name: c.Name, Description: c.Description, CreatedBy: caller.by()}
	if err := tx.InsertPolicy(ctx, pr); err != nil {
		return nil, nil, err
	}
	vr := versionRow(c, req, caller, pr.ID, tx.NewID("pv"), 1)
	if err := tx.InsertVersion(ctx, vr); err != nil {
		return nil, nil, err
	}
	p, err := tx.GetPolicy(ctx, pr.ID)
	if err != nil {
		return nil, nil, err
	}
	v, err := tx.GetVersion(ctx, pr.ID, VersionRef{ID: vr.ID})
	if err != nil {
		return nil, nil, err
	}
	v.Name = p.Name
	return policyObject(p, 1, nil), versionObject(v, StatusInactive, false), nil
}

// AddVersion saves a new version of policyRef. Content identical to an existing version (the same
// version_hash) saves nothing and returns that version with created false.
func (s *Service) AddVersion(ctx context.Context, caller Caller, policyRef string, req PushRequest) (*Version, bool, error) {
	c, e := compileForPush(req)
	if e != nil {
		return nil, false, e
	}
	var ver *Version
	created := false
	err := s.repo.InOrgTx(ctx, caller.Org, func(tx Tx) error {
		if err := tx.LockOrgPolicies(ctx); err != nil {
			return err
		}
		p, err := getPolicy(ctx, tx, policyRef)
		if err != nil {
			return err
		}
		if c.Name != p.Name {
			return errf(KindValidation, "source", msgNameMismatch, c.Name, p.ID, p.Name)
		}
		live, err := tx.LiveActivations(ctx)
		if err != nil {
			return err
		}
		if v, err := tx.GetVersion(ctx, p.ID, VersionRef{Hash: c.VersionHash}); err == nil {
			v.Name = p.Name
			ver = versionObject(v, statusOf(v.ID, live), false)
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		n, err := tx.NextVersion(ctx, p.ID)
		if err != nil {
			return err
		}
		vr := versionRow(c, req, caller, p.ID, tx.NewID("pv"), n)
		if err := tx.InsertVersion(ctx, vr); err != nil {
			return err
		}
		if err := tx.TouchPolicy(ctx, p.ID, c.Description); err != nil {
			return err
		}
		v, err := tx.GetVersion(ctx, p.ID, VersionRef{ID: vr.ID})
		if err != nil {
			return err
		}
		v.Name = p.Name
		ver, created = versionObject(v, StatusInactive, false), true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return ver, created, nil
}

func getPolicy(ctx context.Context, tx Tx, ref string) (*PolicyRow, error) {
	p, err := tx.GetPolicy(ctx, ref)
	if errors.Is(err, ErrNotFound) {
		return nil, errf(KindNotFound, "", msgPolicyNotFound, clipN(ref, 64))
	}
	return p, err
}

func getVersion(ctx context.Context, tx Tx, p *PolicyRow, ref VersionRef) (*VersionRow, error) {
	v, err := tx.GetVersion(ctx, p.ID, ref)
	if errors.Is(err, ErrNotFound) {
		label := ref.ID
		if ref.Number > 0 {
			label = strconv.Itoa(ref.Number)
		}
		return nil, errf(KindNotFound, "", msgVersionNotFound, p.Name, clipN(label, 64), p.ID)
	}
	return v, err
}

// ParseVersionRef reads a path's {v}: a version number or a pv_ id.
func ParseVersionRef(s string) (VersionRef, bool) {
	if strings.HasPrefix(s, "pv_") {
		return VersionRef{ID: s}, len(s) == 29
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 1_000_000 || strconv.Itoa(n) != s {
		return VersionRef{}, false
	}
	return VersionRef{Number: n}, true
}

// Get returns one policy (by pol_ id or name).
func (s *Service) Get(ctx context.Context, caller Caller, policyRef string) (*Policy, error) {
	var out *Policy
	err := s.repo.InOrgReadTx(ctx, caller.Org, func(tx Tx) error {
		p, err := getPolicy(ctx, tx, policyRef)
		if err != nil {
			return err
		}
		live, err := tx.LiveActivations(ctx)
		if err != nil {
			return err
		}
		out = policyObject(p, p.Latest, live)
		return nil
	})
	return out, err
}

// List lists policies by name.
func (s *Service) List(ctx context.Context, caller Caller, pg Page) (*List[Policy], error) {
	after, err := decodeCursor(pg.Cursor, "pol")
	if err != nil {
		return nil, err
	}
	limit := clampLimit(pg.Limit)
	out := &List[Policy]{Object: "list", Data: []Policy{}}
	err = s.repo.InOrgReadTx(ctx, caller.Org, func(tx Tx) error {
		rows, err := tx.ListPolicies(ctx, after, limit+1)
		if err != nil {
			return err
		}
		live, err := tx.LiveActivations(ctx)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows, out.HasMore = rows[:limit], true
			c := encodeCursor("pol", rows[limit-1].Name)
			out.NextCursor = &c
		}
		for i := range rows {
			out.Data = append(out.Data, *policyObject(&rows[i], rows[i].Latest, live))
		}
		return nil
	})
	return out, err
}

// ListVersions lists a policy's versions, newest first, without their source.
func (s *Service) ListVersions(ctx context.Context, caller Caller, policyRef string, pg Page) (*List[Version], error) {
	cur, err := decodeCursor(pg.Cursor, "pv")
	if err != nil {
		return nil, err
	}
	below := 0
	if cur != "" {
		if below, err = strconv.Atoi(cur); err != nil || below < 1 {
			return nil, errf(KindValidation, "cursor", "This cursor isn't valid. Start again without cursor.")
		}
	}
	limit := clampLimit(pg.Limit)
	out := &List[Version]{Object: "list", Data: []Version{}}
	err = s.repo.InOrgReadTx(ctx, caller.Org, func(tx Tx) error {
		p, err := getPolicy(ctx, tx, policyRef)
		if err != nil {
			return err
		}
		rows, err := tx.ListVersions(ctx, p.ID, below, limit+1)
		if err != nil {
			return err
		}
		live, err := tx.LiveActivations(ctx)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows, out.HasMore = rows[:limit], true
			c := encodeCursor("pv", strconv.Itoa(rows[limit-1].Version))
			out.NextCursor = &c
		}
		for i := range rows {
			rows[i].Name = p.Name
			out.Data = append(out.Data, *versionObject(&rows[i], statusOf(rows[i].ID, live), false))
		}
		return nil
	})
	return out, err
}

// GetVersion returns one version with its source.
func (s *Service) GetVersion(ctx context.Context, caller Caller, policyRef string, ref VersionRef) (*Version, error) {
	var out *Version
	err := s.repo.InOrgReadTx(ctx, caller.Org, func(tx Tx) error {
		p, err := getPolicy(ctx, tx, policyRef)
		if err != nil {
			return err
		}
		v, err := getVersion(ctx, tx, p, ref)
		if err != nil {
			return err
		}
		live, err := tx.LiveActivations(ctx)
		if err != nil {
			return err
		}
		v.Name = p.Name
		out = versionObject(v, statusOf(v.ID, live), true)
		return nil
	})
	return out, err
}

// --- activation -----------------------------------------------------------------------------------

func checkReason(r string) (string, *Error) {
	r = strings.TrimSpace(r)
	if r == "" {
		return "", errf(KindValidation, "reason", msgReasonMissing)
	}
	n := utf8.RuneCountInString(r)
	if n < 3 || n > deed.MaxReason {
		return "", errf(KindValidation, "reason", msgReasonBad)
	}
	for _, c := range r {
		if unicode.IsControl(c) || isBidiOrInvisible(c) {
			return "", errf(KindValidation, "reason", msgReasonBad)
		}
	}
	return r, nil
}

// Activate makes a version the org's enforce or shadow policy (S12 §3.2), in one transaction under
// the org's policy lock, and seals the change: one deed, or two for a promote (the shadow ends,
// then the version is enforced). The registry on every instance swaps within 2 s.
func (s *Service) Activate(ctx context.Context, caller Caller, policyRef string, ref VersionRef, req ActivateRequest) (*Activation, error) {
	if req.Mode != ModeEnforce && req.Mode != ModeShadow {
		return nil, errf(KindValidation, "mode", msgModeBad)
	}
	reason, e := checkReason(req.Reason)
	if e != nil {
		return nil, e
	}
	var out *Activation
	err := s.repo.InOrgTx(ctx, caller.Org, func(tx Tx) error {
		a, err := activateTx(ctx, tx, caller, policyRef, ref, req.Mode, reason, req.AcknowledgeWarnings)
		out = a
		return err
	})
	if err != nil {
		return nil, err
	}
	s.changed(caller.Org)
	return out, nil
}

// activateTx is Activate inside tx (also used by Bootstrap, inside org creation's transaction).
func activateTx(ctx context.Context, tx Tx, caller Caller, policyRef string, ref VersionRef, mode Mode, reason string, acks []string) (*Activation, error) {
	req := ActivateRequest{Mode: mode, Reason: reason, AcknowledgeWarnings: acks}
	var out *Activation
	err := func() error {
		if err := tx.LockOrgPolicies(ctx); err != nil {
			return err
		}
		p, err := getPolicy(ctx, tx, policyRef)
		if err != nil {
			return err
		}
		v, err := getVersion(ctx, tx, p, ref)
		if err != nil {
			return err
		}
		v.Name = p.Name
		// Compile again: the stored hash must still be what this build computes.
		c, _ := Compile(v.Source, Options{})
		if c == nil || c.VersionHash != v.VersionHash || c.SourceHash != v.SourceHash { //deedglass:allow-compare a policy source's public digest
			return errf(KindConflict, "", msgVersionTampered, p.Name, v.Version)
		}
		acked, e := checkAcks(c, req)
		if e != nil {
			return e
		}
		live, err := tx.LiveActivations(ctx)
		if err != nil {
			return err
		}
		var enf, sh *ActivationRow
		for i := range live {
			switch live[i].Mode {
			case ModeEnforce:
				enf = &live[i]
			case ModeShadow:
				sh = &live[i]
			}
		}
		a := &Activation{Object: "policy_activation", Policy: p.ID, Name: p.Name, Mode: req.Mode,
			Version: VersionBrief{ID: v.ID, Version: v.Version, VersionHash: v.VersionHash}, AcknowledgedWarnings: acked,
			Reason: reason, ActivatedBy: actorRef(caller.Actor), Deeds: []string{}}
		var replaced *ActivationRow
		switch req.Mode {
		case ModeEnforce:
			if enf != nil && enf.VersionID == v.ID {
				return errf(KindConflict, "", msgAlreadyEnforced, p.Name, v.Version)
			}
			if sh != nil && sh.VersionID == v.ID { // promote: the shadow ends first, with its own deed
				d, err := endActivation(ctx, tx, caller, sh, reason)
				if err != nil {
					return err
				}
				a.Deeds = append(a.Deeds, d)
				a.PromotedFromShadow = true
			}
			replaced = enf
		case ModeShadow:
			if enf != nil && enf.VersionID == v.ID {
				return errf(KindConflict, "", msgShadowEnforced, p.Name, v.Version)
			}
			if sh != nil && sh.VersionID == v.ID {
				return errf(KindConflict, "", msgAlreadyShadow, p.Name, v.Version)
			}
			replaced = sh
		}
		row := ActivationRow{ID: tx.NewID("pa"), PolicyID: p.ID, VersionID: v.ID, Mode: req.Mode, ActivatedBy: caller.by(), Reason: reason,
			Version: v.Version, PolicyName: p.Name}
		data := ActivationData{PolicyID: p.ID, Name: p.Name, Version: v.Version, VersionID: v.ID, VersionHash: v.VersionHash,
			SourceHash: v.SourceHash, Mode: req.Mode, ActivationID: row.ID, AcknowledgedWarnings: acked}
		if replaced != nil {
			pv := replaced.VersionID
			data.PreviousVersionID = &pv
			a.Previous = &PreviousRef{Policy: replaced.PolicyID, Name: replaced.PolicyName, Version: replaced.Version}
		}
		deedID, err := tx.RecordAdmin(ctx, AdminChange{Action: ActionActivated, Actor: caller.Actor, VersionID: v.ID, Reason: reason,
			Data: data, RequestID: caller.RequestID})
		if err != nil {
			return err
		}
		if replaced != nil {
			if _, err := tx.EndActivation(ctx, replaced.ID, caller.Actor, deedID); err != nil {
				return err
			}
		}
		row.DeedID = deedID
		if err := tx.InsertActivation(ctx, &row); err != nil {
			return err
		}
		a.ID, a.ActivatedAt = row.ID, Time(row.ActivatedAt)
		a.Deeds = append(a.Deeds, deedID)
		if err := tx.Publish(ctx, PolicyEvent{Type: ActionActivated, ActivationID: row.ID, Mode: req.Mode, DeedID: deedID}); err != nil {
			return err
		}
		if err := tx.NotifyPolicyChanged(ctx); err != nil {
			return err
		}
		out = a
		return nil
	}()
	return out, err
}

// endActivation seals policy.deactivated for a live activation and ends it.
func endActivation(ctx context.Context, tx Tx, caller Caller, row *ActivationRow, reason string) (string, error) {
	v, err := tx.GetVersion(ctx, "", VersionRef{ID: row.VersionID})
	if err != nil {
		return "", err
	}
	data := ActivationData{PolicyID: row.PolicyID, Name: row.PolicyName, Version: v.Version, VersionID: v.ID, VersionHash: v.VersionHash,
		SourceHash: v.SourceHash, Mode: row.Mode, ActivationID: row.ID, AcknowledgedWarnings: []string{}}
	d, err := tx.RecordAdmin(ctx, AdminChange{Action: ActionDeactivated, Actor: caller.Actor, VersionID: v.ID, Reason: reason,
		Data: data, RequestID: caller.RequestID})
	if err != nil {
		return "", err
	}
	if _, err := tx.EndActivation(ctx, row.ID, caller.Actor, d); err != nil {
		return "", err
	}
	return d, nil
}

// checkAcks validates acknowledge_warnings and requires default_allow's for enforce.
func checkAcks(c *Compiled, req ActivateRequest) ([]string, *Error) {
	have := map[string]bool{}
	for _, w := range c.Warnings {
		have[w.Code] = true
	}
	acked := []string{}
	for _, a := range req.AcknowledgeWarnings {
		if !have[a] {
			return nil, errf(KindValidation, "acknowledge_warnings", msgAckUnknown, clipN(a, 40))
		}
		if !slices.Contains(acked, a) {
			acked = append(acked, a)
		}
	}
	sort.Strings(acked)
	if req.Mode == ModeEnforce && have["default_allow"] && !slices.Contains(acked, "default_allow") {
		return nil, errf(KindValidation, "acknowledge_warnings", msgAckDefaultAllow)
	}
	return acked, nil
}

// Deactivate switches off the shadow policy. The enforced policy can't be switched off: replace it.
func (s *Service) Deactivate(ctx context.Context, caller Caller, policyRef string, ref VersionRef, req DeactivateRequest) (*Activation, error) {
	reason, e := checkReason(req.Reason)
	if e != nil {
		return nil, e
	}
	var out *Activation
	err := s.repo.InOrgTx(ctx, caller.Org, func(tx Tx) error {
		if err := tx.LockOrgPolicies(ctx); err != nil {
			return err
		}
		p, err := getPolicy(ctx, tx, policyRef)
		if err != nil {
			return err
		}
		v, err := getVersion(ctx, tx, p, ref)
		if err != nil {
			return err
		}
		live, err := tx.LiveActivations(ctx)
		if err != nil {
			return err
		}
		var row *ActivationRow
		for i := range live {
			if live[i].VersionID != v.ID {
				continue
			}
			if live[i].Mode == ModeEnforce {
				return errf(KindConflict, "", msgDeactivateEnf)
			}
			row = &live[i]
		}
		if row == nil {
			return errf(KindConflict, "", msgNotShadow, p.Name, v.Version)
		}
		d, err := endActivation(ctx, tx, caller, row, reason)
		if err != nil {
			return err
		}
		if err := tx.NotifyPolicyChanged(ctx); err != nil {
			return err
		}
		ended, err := tx.GetActivation(ctx, row.ID)
		if err != nil {
			return err
		}
		by := actorRef(caller.Actor)
		out = &Activation{Object: "policy_activation", ID: row.ID, Policy: p.ID, Name: p.Name, Mode: row.Mode,
			Version: VersionBrief{ID: v.ID, Version: v.Version, VersionHash: v.VersionHash}, AcknowledgedWarnings: []string{},
			Reason: row.Reason, ActivatedBy: refFromBy(row.ActivatedBy), ActivatedAt: Time(row.ActivatedAt),
			DeactivatedAt: tp(ended.DeactivatedAt), DeactivatedBy: &by, Deeds: []string{d}}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.changed(caller.Org)
	return out, nil
}

func (s *Service) changed(org string) {
	if s.registry != nil {
		s.registry.Invalidate(org)
	}
}

// Active returns the org's live enforce and shadow.
func (s *Service) Active(ctx context.Context, caller Caller) (*ActiveSet, error) {
	out := &ActiveSet{Object: "policy_active_set"}
	err := s.repo.InOrgReadTx(ctx, caller.Org, func(tx Tx) error {
		live, err := tx.LiveActivations(ctx)
		if err != nil {
			return err
		}
		for _, a := range live {
			v, err := tx.GetVersion(ctx, "", VersionRef{ID: a.VersionID})
			if err != nil {
				return err
			}
			e := &ActiveEntry{Policy: a.PolicyID, Name: a.PolicyName, Version: a.Version, VersionID: a.VersionID,
				VersionHash: v.VersionHash, Activation: a.ID, ActivatedAt: Time(a.ActivatedAt), Deed: a.DeedID}
			if a.Mode == ModeEnforce {
				out.Enforce = e
			} else {
				out.Shadow = e
			}
		}
		return nil
	})
	return out, err
}

// --- helpers ---------------------------------------------------------------------------------------

func statusOf(versionID string, live []ActivationRow) string {
	for _, a := range live {
		if a.VersionID == versionID {
			if a.Mode == ModeEnforce {
				return StatusEnforced
			}
			return StatusShadow
		}
	}
	return StatusInactive
}

func policyObject(p *PolicyRow, latest int, live []ActivationRow) *Policy {
	o := &Policy{Object: "policy", ID: p.ID, Name: p.Name, Description: p.Description, Status: StatusInactive, LatestVersion: latest,
		CreatedAt: Time(p.CreatedAt), UpdatedAt: Time(p.UpdatedAt)}
	for _, a := range live {
		if a.PolicyID != p.ID {
			continue
		}
		n := a.Version
		if a.Mode == ModeEnforce {
			o.ActiveVersion, o.Status = &n, StatusEnforced
		} else {
			o.ShadowVersion = &n
			if o.Status == StatusInactive {
				o.Status = StatusShadow
			}
		}
	}
	return o
}

func versionObject(v *VersionRow, status string, withSource bool) *Version {
	o := &Version{Object: "policy_version", ID: v.ID, Policy: v.PolicyID, Version: v.Version, Name: v.Name, Status: status,
		VersionHash: v.VersionHash, SourceHash: v.SourceHash, Lang: v.Lang, Origin: v.Origin, Author: actorRef(v.Author),
		Rules: v.Rules, Budgets: v.Budgets, Warnings: v.Warnings, CreatedAt: Time(v.CreatedAt)}
	if o.Warnings == nil {
		o.Warnings = []Diagnostic{}
	}
	if v.Notes != "" {
		n := v.Notes
		o.Notes = &n
	}
	if withSource {
		src := string(v.Source)
		o.Source = &src
	}
	return o
}

// MaxListLimit is the largest page.
const MaxListLimit = 100

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return 25
	case n > MaxListLimit:
		return MaxListLimit
	}
	return n
}

// Cursors are opaque: base64url("<kind>:<position>").
func encodeCursor(kind, pos string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + pos))
}

func decodeCursor(c, kind string) (string, error) {
	if c == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || !strings.HasPrefix(string(b), kind+":") || len(b) > 200 {
		return "", errf(KindValidation, "cursor", "This cursor isn't valid here. Use next_cursor from the previous page, or start again without cursor.")
	}
	return strings.TrimPrefix(string(b), kind+":"), nil
}

// MarshalIndent renders an API object the way the API does (no HTML escaping), for tests and the
// CLI.
func MarshalIndent(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return b.Bytes()
}
