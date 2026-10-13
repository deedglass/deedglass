package ids

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Prefixes from contract §3, plus the additive ones from ADR-014.
const (
	Org           = "org"
	User          = "usr"
	Membership    = "mem"
	Session       = "ses"
	Agent         = "agt"
	APIKey        = "key"
	Policy        = "pol"
	PolicyVersion = "pv"
	Connector     = "con"
	Action        = "act"
	ActionEvent   = "evt"
	Approval      = "apr"
	Deed          = "deed"
	SealKey       = "sk"
	Suggestion    = "sug"
	Anomaly       = "anm"
	Replay        = "rpl"
	Webhook       = "whk"

	// Additive (ADR-014): request ids (the error envelope already shows req_), and rows that need
	// an id before they get a route.
	Request          = "req"
	LoginToken       = "ltk"
	PolicyActivation = "pa"
	Credential       = "cred"
	ApprovalToken    = "atk"
	DecodedTx        = "dtx"
	WebhookDelivery  = "whd"
	AdminEvent       = "adm"
	Waitlist         = "wl"
	Idempotency      = "idk"
	// S50 (contract §14.2): a watched session of observe mode.
	ObserveSession = "obs"
	// Not here on purpose: witness_jobs use a bigint identity id (S13). S36's site waitlist
	// (Cloudflare D1) reuses wl_.
)

// PrefixInfo describes one id prefix. Registry returns the full table; docs/IDS.md is checked
// against it.
type PrefixInfo struct {
	Prefix      string // "agt"
	Name        string // Go constant name: "Agent"
	Noun        string // used in messages: "agent"
	Description string
	Public      bool   // shown in API responses
	Source      string // "contract §3" or "ADR-014"
}

var registry = []PrefixInfo{
	{Org, "Org", "org", "An org: the company account that owns agents, policies and deeds.", true, "contract §3"},
	{User, "User", "user", "A person. Global: access comes only from memberships.", true, "contract §3"},
	{Membership, "Membership", "membership", "A user's role (owner, admin, viewer) in one org.", true, "contract §3"},
	{Session, "Session", "session", "A Console sign-in session. The cookie holds a secret, never this id alone.", true, "contract §3"},
	{Agent, "Agent", "agent", "A registered AI agent.", true, "contract §3"},
	{APIKey, "APIKey", "API key", "The record id of an API key (the secret is dg_live_… / dg_test_…).", true, "contract §3"},
	{Policy, "Policy", "policy", "A named policy.", true, "contract §3"},
	{PolicyVersion, "PolicyVersion", "policy version", "One immutable version of a policy.", true, "contract §3"},
	{Connector, "Connector", "connector", "A configured tool integration.", true, "contract §3"},
	{Action, "Action", "action", "One tool call that passed through the Gate.", true, "contract §3"},
	{ActionEvent, "ActionEvent", "event", "An action event row, and the event object sent over SSE and webhooks.", true, "contract §3"},
	{Approval, "Approval", "approval", "A request for a human decision.", true, "contract §3"},
	{Deed, "Deed", "deed", "A sealed deed.", true, "contract §3"},
	{SealKey, "SealKey", "seal key", "A deployment-wide seal key.", true, "contract §3"},
	{Suggestion, "Suggestion", "suggestion", "An Insights suggestion that a person accepts or rejects.", true, "contract §3"},
	{Anomaly, "Anomaly", "anomaly", "An Insights anomaly.", true, "contract §3"},
	{Replay, "Replay", "replay", "A policy replay job.", true, "contract §3"},
	{Webhook, "Webhook", "webhook", "A webhook endpoint.", true, "contract §3"},
	{Request, "Request", "request", "One API request (Deedglass-Request-Id, the error envelope).", true, "ADR-014"},
	{LoginToken, "LoginToken", "login token", "A magic-link login token record. The link holds a secret, never this id alone.", false, "ADR-014"},
	{PolicyActivation, "PolicyActivation", "policy activation", "One activation of a policy version.", true, "ADR-014"},
	{Credential, "Credential", "credential", "An encrypted connector credential record.", true, "ADR-014"},
	{ApprovalToken, "ApprovalToken", "approval token", "An approval-link token record.", false, "ADR-014"},
	{DecodedTx, "DecodedTx", "decoded transaction", "A transaction decoded from a Postgres change stream.", false, "ADR-014"},
	{WebhookDelivery, "WebhookDelivery", "webhook delivery", "One attempt to deliver an event to a webhook.", true, "ADR-014"},
	{AdminEvent, "AdminEvent", "admin event", "An administrative change in an org.", true, "ADR-014"},
	{Waitlist, "Waitlist", "waitlist entry", "A waitlist entry (shared shape with the site waitlist).", false, "ADR-014"},
	{Idempotency, "Idempotency", "idempotency key", "A stored Idempotency-Key response.", false, "ADR-014"},
	{ObserveSession, "ObserveSession", "watched session", "A watched session: every change Postgres committed on the watched tables while it was open (S50).", true, "ADR-094"},
}

var byPrefix = func() map[string]PrefixInfo {
	m := make(map[string]PrefixInfo, len(registry))
	for _, p := range registry {
		m[p.Prefix] = p
	}
	return m
}()

// Registry returns every prefix, in contract order. The slice is a copy.
func Registry() []PrefixInfo { return append([]PrefixInfo(nil), registry...) }

// Known reports whether prefix is registered.
func Known(prefix string) bool { _, ok := byPrefix[prefix]; return ok }

// Kind is sealed: only this package defines kinds, one per prefix.
type Kind interface{ prefix() string }

// Kinds, one per prefix. Use them as type arguments: ids.New[ids.AgentKind]().
type (
	OrgKind              struct{}
	UserKind             struct{}
	MembershipKind       struct{}
	SessionKind          struct{}
	AgentKind            struct{}
	APIKeyKind           struct{}
	PolicyKind           struct{}
	PolicyVersionKind    struct{}
	ConnectorKind        struct{}
	ActionKind           struct{}
	ActionEventKind      struct{}
	ApprovalKind         struct{}
	DeedKind             struct{}
	SealKeyKind          struct{}
	SuggestionKind       struct{}
	AnomalyKind          struct{}
	ReplayKind           struct{}
	WebhookKind          struct{}
	RequestKind          struct{}
	LoginTokenKind       struct{}
	PolicyActivationKind struct{}
	CredentialKind       struct{}
	ApprovalTokenKind    struct{}
	DecodedTxKind        struct{}
	WebhookDeliveryKind  struct{}
	AdminEventKind       struct{}
	WaitlistKind         struct{}
	IdempotencyKind      struct{}
	ObserveSessionKind   struct{}
)

func (OrgKind) prefix() string              { return Org }
func (UserKind) prefix() string             { return User }
func (MembershipKind) prefix() string       { return Membership }
func (SessionKind) prefix() string          { return Session }
func (AgentKind) prefix() string            { return Agent }
func (APIKeyKind) prefix() string           { return APIKey }
func (PolicyKind) prefix() string           { return Policy }
func (PolicyVersionKind) prefix() string    { return PolicyVersion }
func (ConnectorKind) prefix() string        { return Connector }
func (ActionKind) prefix() string           { return Action }
func (ActionEventKind) prefix() string      { return ActionEvent }
func (ApprovalKind) prefix() string         { return Approval }
func (DeedKind) prefix() string             { return Deed }
func (SealKeyKind) prefix() string          { return SealKey }
func (SuggestionKind) prefix() string       { return Suggestion }
func (AnomalyKind) prefix() string          { return Anomaly }
func (ReplayKind) prefix() string           { return Replay }
func (WebhookKind) prefix() string          { return Webhook }
func (RequestKind) prefix() string          { return Request }
func (LoginTokenKind) prefix() string       { return LoginToken }
func (PolicyActivationKind) prefix() string { return PolicyActivation }
func (CredentialKind) prefix() string       { return Credential }
func (ApprovalTokenKind) prefix() string    { return ApprovalToken }
func (DecodedTxKind) prefix() string        { return DecodedTx }
func (WebhookDeliveryKind) prefix() string  { return WebhookDelivery }
func (AdminEventKind) prefix() string       { return AdminEvent }
func (WaitlistKind) prefix() string         { return Waitlist }
func (IdempotencyKind) prefix() string      { return Idempotency }
func (ObserveSessionKind) prefix() string   { return ObserveSession }

// Typed ids, one alias per prefix.
type (
	OrgID              = ID[OrgKind]
	UserID             = ID[UserKind]
	MembershipID       = ID[MembershipKind]
	SessionID          = ID[SessionKind]
	AgentID            = ID[AgentKind]
	APIKeyID           = ID[APIKeyKind]
	PolicyID           = ID[PolicyKind]
	PolicyVersionID    = ID[PolicyVersionKind]
	ConnectorID        = ID[ConnectorKind]
	ActionID           = ID[ActionKind]
	ActionEventID      = ID[ActionEventKind]
	ApprovalID         = ID[ApprovalKind]
	DeedID             = ID[DeedKind]
	SealKeyID          = ID[SealKeyKind]
	SuggestionID       = ID[SuggestionKind]
	AnomalyID          = ID[AnomalyKind]
	ReplayID           = ID[ReplayKind]
	WebhookID          = ID[WebhookKind]
	RequestID          = ID[RequestKind]
	LoginTokenID       = ID[LoginTokenKind]
	PolicyActivationID = ID[PolicyActivationKind]
	CredentialID       = ID[CredentialKind]
	ApprovalTokenID    = ID[ApprovalTokenKind]
	DecodedTxID        = ID[DecodedTxKind]
	WebhookDeliveryID  = ID[WebhookDeliveryKind]
	AdminEventID       = ID[AdminEventKind]
	WaitlistID         = ID[WaitlistKind]
	IdempotencyID      = ID[IdempotencyKind]
	ObserveSessionID   = ID[ObserveSessionKind]
)

// ID is a prefixed ULID of kind K. The zero value means "no id".
type ID[K Kind] struct{ s string }

// Prefix returns the prefix of kind K ("agt").
func Prefix[K Kind]() string { var k K; return k.prefix() }

// New returns a fresh id from the process-wide monotonic generator.
func New[K Kind]() ID[K] {
	var k K
	return ID[K]{s: defaultGen.next(k.prefix())}
}

// Parse parses s strictly: exactly "<prefix>_" plus 26 lowercase Crockford base32 characters
// whose first character is 0-7. It never normalizes; see Normalize for CLI input.
func Parse[K Kind](s string) (ID[K], error) {
	var k K
	if err := check(k.prefix(), s); err != nil {
		return ID[K]{}, err
	}
	return ID[K]{s: s}, nil
}

// MustParse is Parse for tests and constants. It panics on an invalid id.
func MustParse[K Kind](s string) ID[K] {
	id, err := Parse[K](s)
	if err != nil {
		panic(err)
	}
	return id
}

func (id ID[K]) String() string { return id.s }

// IsZero reports whether id is the zero value (no id).
func (id ID[K]) IsZero() bool { return id.s == "" }

// Time returns the creation time encoded in the id, to the millisecond (UTC). The zero id returns
// the zero time.
func (id ID[K]) Time() time.Time {
	if id.s == "" {
		return time.Time{}
	}
	ms, _, _ := decode(id.s[len(id.s)-ulidLen:])
	return time.UnixMilli(int64(ms)).UTC()
}

// MarshalText returns the id; the zero id marshals to "".
func (id ID[K]) MarshalText() ([]byte, error) { return []byte(id.s), nil }

// UnmarshalText parses strictly. Empty text yields the zero id; callers decide if an id is required.
func (id *ID[K]) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*id = ID[K]{}
		return nil
	}
	v, err := Parse[K](string(b))
	if err != nil {
		return err
	}
	*id = v
	return nil
}

// Scan implements sql.Scanner. NULL yields the zero id.
func (id *ID[K]) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*id = ID[K]{}
		return nil
	case string:
		return id.UnmarshalText([]byte(v))
	case []byte:
		return id.UnmarshalText(v)
	default:
		var k K
		return fmt.Errorf("ids: cannot scan %T into a %s id", src, byPrefix[k.prefix()].Noun)
	}
}

// Value implements driver.Valuer. The zero id is stored as NULL.
func (id ID[K]) Value() (driver.Value, error) {
	if id.s == "" {
		return nil, nil
	}
	return id.s, nil
}

// LogValue implements slog.LogValuer.
func (id ID[K]) LogValue() slog.Value { return slog.StringValue(id.s) }

// NewString returns a fresh id for a prefix chosen at run time (CLI, tests).
func NewString(prefix string) (string, error) {
	if !Known(prefix) {
		return "", fmt.Errorf("ids: unknown prefix %q (see docs/IDS.md)", prefix)
	}
	return defaultGen.next(prefix), nil
}

// Check validates s as an id with the given prefix, for callers that hold the prefix at run time.
func Check(prefix, s string) error { return check(prefix, s) }

// Pattern returns the one regular expression for ids with this prefix, for example
// "^agt_[0-7][0-9a-hjkmnp-tv-z]{25}$". S02 copies it into SQL domains.
func Pattern(prefix string) string {
	return "^" + prefix + "_[0-7][0-9a-hjkmnp-tv-z]{25}$"
}

// Normalize prepares ids typed by a person on the command line: it trims spaces and lowercases.
// The API never normalizes.
func Normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ErrInvalid is the sentinel every parse error wraps.
var ErrInvalid = errors.New("invalid id")

// Error explains why an id was refused.
type Error struct {
	Prefix string // the expected prefix
	Input  string
	Reason string
}

func (e *Error) Error() string {
	return fmt.Sprintf("invalid %s id %q: %s", noun(e.Prefix), shorten(e.Input), e.Reason)
}

func (e *Error) Unwrap() error { return ErrInvalid }

func noun(prefix string) string {
	if p, ok := byPrefix[prefix]; ok {
		return p.Noun
	}
	return prefix
}

func article(s string) string {
	if s != "" && strings.ContainsRune("aeiou", rune(s[0])) {
		return "an " + s
	}
	return "a " + s
}

func shorten(s string) string {
	const max = 40
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func check(prefix, s string) error {
	fail := func(reason string) error { return &Error{Prefix: prefix, Input: s, Reason: reason} }
	i := strings.IndexByte(s, '_')
	if i < 0 {
		return fail(fmt.Sprintf("expected %s id (%s_…)", article(noun(prefix)), prefix))
	}
	if got := s[:i]; got != prefix {
		if strings.ToLower(got) == prefix {
			return fail("ids are lowercase")
		}
		return fail(fmt.Sprintf("expected %s id (%s_…)", article(noun(prefix)), prefix))
	}
	u := s[i+1:]
	if len(u) != ulidLen {
		return fail(fmt.Sprintf("expected %d characters after %s_, got %d", ulidLen, prefix, len(u)))
	}
	for j := 0; j < len(u); j++ {
		c := u[j]
		if c >= 'A' && c <= 'Z' {
			return fail("ids are lowercase")
		}
		if dec[c] == 0xff {
			return fail(fmt.Sprintf("character %q is not in the id alphabet (0-9, a-z without i, l, o, u)", c))
		}
	}
	if u[0] > '7' {
		return fail("the first character after the prefix must be 0-7")
	}
	return nil
}
