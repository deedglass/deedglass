package securitypack

import "time"

// Facts is everything one pack is computed from: ids, counts, hashes, dates and the names of the
// org's own agents, connectors and rules. Never action arguments, results, row values, emails or
// secrets (the canary tests read every file a pack writes).
//
// A collector that fails records its reason in Errors under its name; every question that needs
// it is then "Can't tell", never "In place".
type Facts struct {
	Org     Org       `json:"org"`
	Period  Period    `json:"period"`
	Now     time.Time `json:"now"`
	Version string    `json:"deedglass_version"`

	Agents     []Agent     `json:"agents"`
	Keys       []Key       `json:"keys"`
	Connectors []Connector `json:"connectors"`
	Members    Members     `json:"members"`
	Egress     *Egress     `json:"egress"`
	Rules      Rules       `json:"rules"`
	Approvals  Approvals   `json:"approvals"`
	Stop       Stop        `json:"stop"`
	Actions    Actions     `json:"actions"`
	Sessions   Sessions    `json:"sessions"`
	Undos      Undos       `json:"undos"`
	Records    Records     `json:"records"`
	Build      BuildFacts  `json:"build"`

	// Errors maps a collector name (the Collector* constants) to why it couldn't be read.
	Errors map[string]string `json:"errors,omitempty"`
}

// Collector names: what a question needs, and what Facts.Errors is keyed by.
const (
	CollectorAgents     = "agents"
	CollectorKeys       = "keys"
	CollectorConnectors = "connectors"
	CollectorMembers    = "members"
	CollectorEgress     = "egress"
	CollectorRules      = "rules"
	CollectorApprovals  = "approvals"
	CollectorStop       = "stop"
	CollectorActions    = "actions"
	CollectorSessions   = "sessions"
	CollectorUndos      = "undos"
	CollectorRecords    = "records"
	CollectorBuild      = "build"
)

// Collectors is every collector name, in a stable order.
var Collectors = []string{CollectorAgents, CollectorKeys, CollectorConnectors, CollectorMembers, CollectorEgress,
	CollectorRules, CollectorApprovals, CollectorStop, CollectorActions, CollectorSessions, CollectorUndos,
	CollectorRecords, CollectorBuild}

// Fail records that collector c couldn't be read.
func (f *Facts) Fail(c, reason string) {
	if f.Errors == nil {
		f.Errors = map[string]string{}
	}
	f.Errors[c] = reason
}

// Org is the org the pack is about.
type Org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Period is the window the pack counts, [From, To).
type Period struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Agent is one registered agent.
type Agent struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"` // active | suspended | stopped
	IntentMode string    `json:"intent_mode"`
	HasOwner   bool      `json:"has_owner"`
	HasBudget  bool      `json:"has_budget"`
	CreatedAt  time.Time `json:"created_at"`
}

// Key is one live API key (not revoked, not expired).
type Key struct {
	ID         string     `json:"id"`
	AgentID    string     `json:"agent_id"` // "" for an org admin key
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// Connector is one connector that isn't deleted.
type Connector struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"` // active | disabled | error
	// Writes is true when the connector's type has a tool that isn't a read.
	Writes bool `json:"writes"`
	// Undo is true when Deedglass can undo this connector's writes (Postgres).
	Undo bool `json:"undo"`
	// Observe is witness.observe.enabled.
	Observe bool `json:"observe"`
	// Credential is the vault entry's metadata; nil when the connector has none. Never a value.
	Credential *Credential `json:"credential"`
}

// Credential is a vault entry's metadata (vault.Meta): never the value.
type Credential struct {
	Version   int        `json:"version"`
	Alg       string     `json:"alg"`
	KEK       string     `json:"kek"`
	UpdatedAt time.Time  `json:"updated_at"`
	RotatedAt *time.Time `json:"rotated_at"`
}

// Members counts the people who can press Stop all.
type Members struct {
	OwnersAdmins int `json:"owners_admins"`
	AdminKeys    int `json:"admin_keys"`
}

// Egress is the server's outbound policy (internal/connector/egress), as serve configured it.
type Egress struct {
	Env          string `json:"env"` // dev | prod
	AllowPrivate bool   `json:"allow_private"`
	AllowCIDRs   int    `json:"allow_cidrs"`
}

// Rules is the org's active policy versions and what they do on fixed probe inputs.
type Rules struct {
	Enforce *ActiveVersion `json:"enforce"`
	Shadow  *ActiveVersion `json:"shadow"`
	// Changes are activations and deactivations in the period, plus the live ones.
	Changes []RuleChange `json:"changes"`
	// Probes is the enforced version's real evaluation of the destructive SQL and money inputs.
	Probes []Probe `json:"probes"`
}

// ActiveVersion is one live activation.
type ActiveVersion struct {
	PolicyID     string    `json:"policy_id"`
	Name         string    `json:"name"`
	Version      int       `json:"version"`
	VersionID    string    `json:"version_id"`
	VersionHash  string    `json:"version_hash"`
	ActivationID string    `json:"activation_id"`
	DeedID       string    `json:"deed_id"`
	ActivatedBy  string    `json:"activated_by"`
	ActivatedAt  time.Time `json:"activated_at"`
	// YAML is the version's source, compiled here for the probes and rule counts. It never leaves
	// the server: Facts are not written to the pack, only the answers built from them.
	YAML string `json:"-"`
}

// RuleChange is one sealed (or not) change to the active rules.
type RuleChange struct {
	ActivationID string    `json:"activation_id"`
	DeedID       string    `json:"deed_id"`
	By           string    `json:"by"`
	At           time.Time `json:"at"`
}

// Probe is one fixed input evaluated by the enforced version.
type Probe struct {
	Name    string `json:"name"`    // drop_table, truncate, delete_all, large_refund
	Outcome string `json:"outcome"` // allow | deny | require_approval
	RuleID  string `json:"rule_id"`
}

// Approvals counts the period's approvals.
type Approvals struct {
	Total, Approved, Denied, Expired, Cancelled int
	// PendingOverdue are still pending after their expiry (the sweeper hasn't run).
	PendingOverdue int
	// MultiApprover asked for two or more people.
	MultiApprover int
	// MedianDecideSeconds is the median time from request to decision (-1: none decided).
	MedianDecideSeconds int64
}

// Stop counts Stop events.
type Stop struct {
	InPeriod int        `json:"in_period"`
	LastAt   *time.Time `json:"last_at"`
	LastDeed string     `json:"last_deed"`
}

// Actions counts the period's executed actions.
type Actions struct {
	Writes   int `json:"writes"`
	Claimed  int `json:"claimed"` // L0
	Checked  int `json:"checked"` // L1
	Proven   int `json:"proven"`  // L2
	Matches  int `json:"matches"`
	Mismatch int `json:"mismatch"`
	CantChk  int `json:"cant_check"`
	// MismatchFollowed have an undo started for them.
	MismatchFollowed int      `json:"mismatch_followed"`
	MismatchOpen     []string `json:"mismatch_open"` // up to 5 act_ ids without a follow-up
	// Proxy are executed actions that reached a tool through mcp wrap (no Deedglass connector).
	Proxy   int `json:"proxy"`
	Gateway int `json:"gateway"`
	// Unsealed are finished actions in the period with no deed.
	Unsealed int `json:"unsealed"`
	Finished int `json:"finished"`
}

// Sessions counts watched sessions (S50).
type Sessions struct {
	Total  int `json:"total"`
	Sealed int `json:"sealed"`
	Caught int `json:"caught"`
}

// Undos counts the period's undos.
type Undos struct {
	Actions      int `json:"actions"`       // per-action undos
	Matched      int `json:"matched"`       // of those, ended in Matches
	Sessions     int `json:"sessions"`      // session undos (S50)
	SessionsDone int `json:"sessions_done"` // of those, undone
}

// Records is the deed chain's state.
type Records struct {
	ChainIntact bool      `json:"chain_intact"`
	ChainHead   string    `json:"chain_head"` // sha256 of the last deed checked
	HeadSeq     int64     `json:"head_seq"`
	Checked     int64     `json:"checked"`
	FirstBroken string    `json:"first_broken"` // deed id
	Signer      string    `json:"signer"`       // the active seal key id, "" when the server has none
	SealKeys    []SealKey `json:"seal_keys"`
	// BundleDigest and BundleCount describe records.tar.gz when the pack carries it.
	BundleDigest string `json:"bundle_digest"`
	BundleCount  int64  `json:"bundle_count"`
}

// SealKey is one seal key's public facts.
type SealKey struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"` // active | retired | revoked
	ActivatedAt time.Time `json:"activated_at"`
}

// BuildFacts is what the binary knows about itself.
type BuildFacts struct {
	Version string         `json:"version"`
	Commit  string         `json:"commit"`
	Attack  *AttackSummary `json:"attack"`
}

// AttackSummary is the attack suite's last recorded run, embedded at build time.
type AttackSummary struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	RunOn    string `json:"run_on"` // YYYY-MM-DD
	Matched  int    `json:"matched"`
	Differs  int    `json:"differs"`
	Waiting  int    `json:"waiting"`
	Postgres string `json:"postgres"`
}
