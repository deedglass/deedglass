package ledger

import (
	"errors"
	"log/slog"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/gate/lifecycle"
	"github.com/deedglass/deedglass/internal/store"
)

// DefaultRetentionDays is how long action payloads are kept when orgs.retention_days is NULL.
const DefaultRetentionDays = 90

// Errors the write and read paths return. HTTP handlers and the CLI map them to their own copy.
var (
	// ErrIllegalTransition: gate.CanTransition(from, to) is false. Nothing was written. It is
	// lifecycle.ErrIllegalTransition (gate.ErrIllegalTransition), so errors.Is works across packages.
	ErrIllegalTransition = lifecycle.ErrIllegalTransition
	// ErrStateConflict: the action is not in the expected state (another transaction moved it, or
	// it doesn't exist in this org). Nothing was written.
	// It is lifecycle.ErrStateConflict (gate.ErrStateConflict).
	ErrStateConflict = lifecycle.ErrStateConflict
	// ErrNotSealable: SealAction was called for an action that is not in a final state.
	ErrNotSealable = errors.New("ledger: the action is not in a final state")
	// ErrNotFound: no such object in this org (an id from another org is not found too).
	ErrNotFound = errors.New("ledger: not found")
	// ErrEmptyRange: an export or check range holds no deeds.
	ErrEmptyRange = errors.New("ledger: no deeds in this range")
	// ErrPruneRunning: another prune holds this org's lock.
	ErrPruneRunning = errors.New("ledger: another prune is running for this org")
	// ErrNoSigner: a write method was called on a read-only Ledger (no sealer or signer).
	ErrNoSigner = errors.New("ledger: this Ledger has no seal key")
	// ErrPayload: an event's data, an admin event's data or a NOTIFY payload is over its limit.
	ErrPayload = errors.New("ledger: payload over its limit")
)

// Limits.
const (
	MaxEventData     = 2 << 10  // action_events.data, bytes (also a CHECK in 0010)
	MaxAdminData     = 16 << 10 // admin_events.data, bytes (also a CHECK in 0010)
	MaxNotifyPayload = 1 << 10  // one deedglass_events payload
	MaxEventsShown   = 200      // GetAction's events list
	MaxExportDeeds   = 1_000_000
	MaxCheckDeeds    = 2_000_000
	CheckBudget      = 30 * time.Second
	PruneBatch       = 1000
	readTimeout      = "5s" // statement_timeout for list and get reads
)

// Ledger reads and writes one deployment's history. A Ledger without a sealer and signer serves
// reads, CheckChain and dry-run prunes; writes return ErrNoSigner.
type Ledger struct {
	db     *store.DB
	sealer *deed.Sealer
	signer deed.Signer
	pq     deed.AlgSigner // ADR-109: the ML-DSA-65 key, for COSE exports of v2 deeds (nil without one)
	issuer string
	gen    archive.Generator
	log    *slog.Logger
	now    func() time.Time
	dev    bool
	// undoMaxAge is undo.max_age (S23), for the undo object's eligible flag.
	undoMaxAge time.Duration
}

// Option tunes a Ledger.
type Option func(*Ledger)

// WithUndoMaxAge sets undo.max_age (S23; 168h by default), which the undo object's eligible flag reads.
func WithUndoMaxAge(d time.Duration) Option { return func(l *Ledger) { l.undoMaxAge = d } }

// WithIssuer sets the issuer (DEEDGLASS_BASE_URL) written into exports and the key set.
func WithIssuer(u string) Option { return func(l *Ledger) { l.issuer = u } }

// WithGenerator names the software in export manifests.
func WithGenerator(g archive.Generator) Option { return func(l *Ledger) { l.gen = g } }

// WithLogger sets the logger (default slog.Default()).
func WithLogger(log *slog.Logger) Option { return func(l *Ledger) { l.log = log } }

// WithClock replaces time.Now (tests pin export created_at with it).
func WithClock(now func() time.Time) Option { return func(l *Ledger) { l.now = now } }

// WithPQSigner sets the ML-DSA-65 seal key the issuer signs COSE exports of v2 deeds with (alg −49,
// ADR-109). The Sealer gets the same key through deed.WithExtraSigners.
func WithPQSigner(s deed.AlgSigner) Option { return func(l *Ledger) { l.pq = s } }

// WithDev marks a dev deployment (DEEDGLASS_ENV=dev): Prune accepts cutoffs under 24 hours.
func WithDev(dev bool) Option { return func(l *Ledger) { l.dev = dev } }

// New returns a Ledger over db. sealer and signer may be nil for a read-only Ledger.
func New(db *store.DB, sealer *deed.Sealer, signer deed.Signer, opts ...Option) *Ledger {
	l := &Ledger{db: db, sealer: sealer, signer: signer, issuer: "http://127.0.0.1:8080",
		gen: archive.Generator{Name: "deedglass", Version: "dev", Commit: "unknown"}, log: slog.Default(), now: time.Now,
		undoMaxAge: 168 * time.Hour}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Issuer is the base URL exports and key sets name.
func (l *Ledger) Issuer() string { return l.issuer }

// SignerKeyID is the seal key this Ledger seals with, or "" for a read-only Ledger.
func (l *Ledger) SignerKeyID() string {
	if l.signer == nil {
		return ""
	}
	return l.signer.KeyID()
}

// DB is the store this Ledger reads and writes.
func (l *Ledger) DB() *store.DB { return l.db }
