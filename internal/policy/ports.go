package policy

import (
	"context"
	"errors"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// The ports of S12's policy management (S12 §3.3). This package holds the logic (the service, the
// registry, onboarding and the test runner) and depends only on these small interfaces;
// internal/store/policystore implements them with Postgres, ledger.RecordAdmin and the S08
// listener, and cmd/deedglass wires them. archtest keeps this package free of pgx, store and httpx; org ids are plain strings because
// internal/ids pulls in database/sql/driver (TestAgentIDPattern).

// Origin says where a version came from.
type Origin string

const (
	OriginManual     Origin = "manual"     // pushed by a person or an admin key
	OriginSuggestion Origin = "suggestion" // an Insights suggestion (S28)
	OriginStarter    Origin = "starter"    // a starter policy loaded by dev seed
	OriginOnboarding Origin = "onboarding" // default-deny for a new org
)

// PolicyRow is one row of deedglass.policies.
type PolicyRow struct {
	ID          string // pol_…
	Name        string
	Description string
	CreatedBy   string // usr_…, key_… or system
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Latest      int // the newest version number (read only)
}

// VersionRow is one row of deedglass.policy_versions (immutable). Source is empty in lists.
type VersionRow struct {
	ID           string // pv_…
	PolicyID     string // pol_…
	Name         string // the policy's name (joined)
	Version      int
	Source       []byte // the YAML, exactly as pushed
	VersionHash  string
	SourceHash   string
	Lang         string
	Rules        int
	Budgets      int
	Warnings     []Diagnostic
	Origin       Origin
	SuggestionID string // sug_… when Origin is suggestion
	Author       deed.Ref
	Notes        string
	CreatedAt    time.Time
}

// ActivationRow is one row of deedglass.policy_activations.
type ActivationRow struct {
	ID                 string // pa_…
	PolicyID           string
	VersionID          string
	Mode               Mode
	ActivatedBy        string // usr_…, key_… or system
	ActivatedAt        time.Time
	DeactivatedAt      *time.Time
	DeactivatedBy      string
	DeactivationDeedID string
	DeedID             string // the policy.activated deed
	Reason             string
	Version            int    // the version's number (read only, joined)
	PolicyName         string // the policy's name (read only, joined)
}

// LoadedRow is one live activation with everything the registry checks: its version (with the
// source), its activation deed (the exact stored envelope) and that deed's admin event data.
type LoadedRow struct {
	Activation ActivationRow
	Version    VersionRow
	Deed       []byte // JCS(envelope), as stored
	DeedData   []byte // admin_events.data of the deed (JSON)
}

// ActiveRows is what the registry loads for one org.
type ActiveRows struct {
	Rows []LoadedRow // 0, 1 or 2 (one enforce, at most one shadow)
	Keys *deed.KeySet
}

// ActivationData is the admin event data of policy.activated and policy.deactivated. Only its
// digest enters the deed (data_digest); the registry recomputes it on every load.
type ActivationData struct {
	PolicyID             string   `json:"policy_id"`
	Name                 string   `json:"name"`
	Version              int      `json:"version"`
	VersionID            string   `json:"version_id"`
	VersionHash          string   `json:"version_hash"`
	SourceHash           string   `json:"source_hash"`
	Mode                 Mode     `json:"mode"`
	ActivationID         string   `json:"activation_id"`
	PreviousVersionID    *string  `json:"previous_version_id"`
	AcknowledgedWarnings []string `json:"acknowledged_warnings"`
}

// Admin actions S12 records (ledger.AdminPolicyActivated and AdminPolicyDeactivated).
const (
	ActionActivated   = "policy.activated"
	ActionDeactivated = "policy.deactivated"
)

// AdminChange is one sealed admin change: Tx.RecordAdmin turns it into an admin_events row and a
// sealed admin deed whose target is the version and whose policy block stays null.
type AdminChange struct {
	Action    string // ActionActivated | ActionDeactivated
	Actor     deed.Ref
	VersionID string // the target: {policy_version, pv_…}
	Reason    string
	Data      ActivationData
	RequestID string
}

// PolicyEvent is the contract event policy.activated (ids only).
type PolicyEvent struct {
	Type         string // "policy.activated"
	ActivationID string
	Mode         Mode
	DeedID       string
}

// ErrNotFound is what a Tx returns for a missing row (the adapter maps store.ErrNotFound to it).
var ErrNotFound = errors.New("policy: not found")

// Repo opens org-scoped transactions and loads what the registry needs. Org ids are org_… strings.
type Repo interface {
	// InOrgTx runs fn in one read-write transaction scoped to org (row-level security).
	InOrgTx(ctx context.Context, org string, fn func(Tx) error) error
	// InOrgReadTx runs fn in a read-only transaction scoped to org.
	InOrgReadTx(ctx context.Context, org string, fn func(Tx) error) error
	// LoadActive reads the live activations, their versions, deeds and deed data, and the seal keys.
	LoadActive(ctx context.Context, org string) (*ActiveRows, error)
	// LiveActivationIDs is the registry's cheap safety-poll query: the ids of the live activations.
	LiveActivationIDs(ctx context.Context, org string) ([]string, error)
}

// Tx is one org-scoped transaction.
type Tx interface {
	// LockOrgPolicies takes pg_advisory_xact_lock(hashtextextended('deedglass/policy/' || org, 0)):
	// every policy change in an org runs one at a time, in a single order.
	LockOrgPolicies(ctx context.Context) error

	InsertPolicy(ctx context.Context, p PolicyRow) error
	// TouchPolicy sets updated_at (and the description, from the newest version).
	TouchPolicy(ctx context.Context, policyID, description string) error
	// GetPolicy finds a policy by pol_ id or by name; ErrNotFound when there is none.
	GetPolicy(ctx context.Context, ref string) (*PolicyRow, error)
	// ListPolicies lists by name, after the cursor name (exclusive).
	ListPolicies(ctx context.Context, afterName string, limit int) ([]PolicyRow, error)

	// NextVersion locks the policy row FOR UPDATE and returns its next version number.
	NextVersion(ctx context.Context, policyID string) (int, error)
	InsertVersion(ctx context.Context, v VersionRow) error
	LatestVersion(ctx context.Context, policyID string) (*VersionRow, error)
	// GetVersion finds a version by number or pv_ id (with its source); ErrNotFound when missing.
	// policyID "" looks in every policy of the org.
	GetVersion(ctx context.Context, policyID string, ref VersionRef) (*VersionRow, error)
	// ListVersions lists newest first, below the cursor version (exclusive; 0 means from the top),
	// without the source.
	ListVersions(ctx context.Context, policyID string, below int, limit int) ([]VersionRow, error)

	// LiveActivations returns the live activations (enforce first), with version numbers and names.
	LiveActivations(ctx context.Context) ([]ActivationRow, error)
	GetActivation(ctx context.Context, id string) (*ActivationRow, error)
	// EndActivation sets deactivated_at (the database clock), deactivated_by and the deed.
	EndActivation(ctx context.Context, id string, by deed.Ref, deedID string) (time.Time, error)
	// InsertActivation inserts a live activation and sets a.ActivatedAt from the database clock.
	InsertActivation(ctx context.Context, a *ActivationRow) error

	// RecordAdmin seals the change (ledger.RecordAdmin) and returns the deed id.
	RecordAdmin(ctx context.Context, c AdminChange) (deedID string, err error)
	// Publish sends a contract event through ledger.Publish (delivered at commit).
	Publish(ctx context.Context, ev PolicyEvent) error
	// NotifyPolicyChanged sends the org id on deedglass_policy (delivered at commit).
	NotifyPolicyChanged(ctx context.Context) error

	// LoadAgent reads an agent of this org for a dry run: its facts, status and S08 budgets.
	LoadAgent(ctx context.Context, id string) (*AgentInput, error)
	// LoadOrg reads this org's facts for a dry run: its id, time zone and Stop all overlay.
	LoadOrg(ctx context.Context) (*OrgInput, error)

	// NewID returns a fresh prefixed id ("pol", "pv", "pa") from internal/ids.
	NewID(prefix string) string
}

// Changes delivers org ids from LISTEN deedglass_policy, and a signal after every reconnect
// (anything cached may have missed a notification).
type Changes interface {
	Subscribe(ctx context.Context) (orgs <-chan string, reconnected <-chan struct{}, err error)
}

// VersionRef names a version by number (Number > 0), by pv_ id, or by version_hash (pushes).
type VersionRef struct {
	Number int
	ID     string
	Hash   string
}
