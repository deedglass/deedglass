package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Writer is all a handler ever holds: it stores, removes and describes credentials, and never
// returns a value.
type Writer interface {
	Put(ctx context.Context, ref Ref, kind string, fields map[string][]byte, actor Actor) (Meta, error)
	Delete(ctx context.Context, ref Ref, actor Actor) (Meta, error)
	Meta(ctx context.Context, ref Ref) (Meta, error)
}

// Opener decrypts a credential for one call. Only internal/connector (Instance.Secret) and the
// vault's own rotation call it (TestOpenerCallSites).
type Opener interface {
	Open(ctx context.Context, ref Ref) (*Secret, error)
}

// Rotator moves every credential to the ring's active key (deedglass vault status|rotate).
type Rotator interface {
	Status(ctx context.Context) (Status, error)
	Rotate(ctx context.Context, progress func(done, total int)) (RotateResult, error)
}

// Actor is who changes a credential: the admin deed's actor and the text in updated_by.
type Actor struct {
	Ref deed.Ref
	By  string
}

// Operator is the actor of database-direct CLI commands (deedglass vault rotate).
var Operator = Actor{Ref: deed.Ref{Type: "system", ID: "operator"}, By: "operator"}

// Meta describes a stored credential without its values: the credential object of the API.
type Meta struct {
	Object    string     `json:"object"` // "credential"
	Connector string     `json:"connector"`
	Set       bool       `json:"set"`
	Kind      string     `json:"kind,omitempty"`
	Fields    []string   `json:"fields,omitempty"`
	Hint      string     `json:"hint,omitempty"`
	Version   int        `json:"version,omitempty"`
	KEKID     string     `json:"kek_id,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	Deleted   bool       `json:"deleted,omitempty"`
	Deed      *string    `json:"deed,omitempty"`
}

// Status is the vault's key use across every org.
type Status struct {
	Object  string         `json:"object"` // "vault_status"
	Active  string         `json:"active"`
	Ring    []string       `json:"ring"`
	ByKEK   map[string]int `json:"by_kek"`  // credentials per key id in the ring
	Unknown map[string]int `json:"unknown"` // credentials wrapped by keys not in the ring
	Total   int            `json:"total"`
	Pending int            `json:"pending"` // not on the active key
	Orgs    int            `json:"orgs"`
}

// RotateResult is what Rotate did.
type RotateResult struct {
	Object    string   `json:"object"` // "vault_rotation"
	Active    string   `json:"active"`
	Rewrapped int      `json:"rewrapped"`
	Total     int      `json:"total"`
	Unused    []string `json:"unused"` // ring keys no credential uses any more: safe to remove
	Deeds     []string `json:"deeds"`
}

// Store is the vault over Postgres: Writer, Opener and Rotator in org-scoped transactions.
type Store struct {
	db   *store.DB
	kp   KEKProvider
	ring *Ring // nil when kp isn't a Ring (Status and Rotate need it)
	led  *ledger.Ledger
	rand io.Reader
	log  *slog.Logger
	prod bool
}

// StoreOptions configures NewStore.
type StoreOptions struct {
	Ledger *ledger.Ledger // seals the admin deeds; required for Put, Delete and Rotate
	Rand   io.Reader      // data keys and nonces; nil = crypto/rand
	Logger *slog.Logger
	Prod   bool // refuse secret values shorter than MinFieldLength
}

// NewStore returns the vault over db with the key provider kp.
func NewStore(db *store.DB, kp KEKProvider, o StoreOptions) *Store {
	s := &Store{db: db, kp: kp, led: o.Ledger, rand: o.Rand, log: o.Logger, prod: o.Prod}
	if r, ok := kp.(*Ring); ok {
		s.ring = r
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

var (
	_ Writer  = (*Store)(nil)
	_ Opener  = (*Store)(nil)
	_ Rotator = (*Store)(nil)
)

// FieldError is a refused field: param names it, never its value.
type FieldError struct {
	Param   string // "secret.token"
	Message string // a sentence with the next step
}

func (e *FieldError) Error() string { return e.Message }

var reFieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// required lists each kind's fields; KindFields takes any names.
var required = map[string][]string{
	KindBearer: {"token"},
	KindHeader: {"value"},
	KindBasic:  {"username", "password"},
	KindQuery:  {"value"},
	KindDSN:    {"dsn"},
}

// optional fields a kind accepts besides its required ones.
var optional = map[string][]string{KindDSN: {"witness_dsn"}}

// secretField reports whether a field's value is secret (the username of basic auth isn't).
func secretField(kind, name string) bool { return !(kind == KindBasic && name == "username") }

// CheckFields validates a credential's shape: the kind's fields, UTF-8 values without line
// breaks, at most 8 KiB each, and (prod) no secret shorter than MinFieldLength bytes.
func CheckFields(kind string, fields map[string][]byte, prod bool) error {
	if !slices.Contains(Kinds, kind) {
		return &FieldError{Param: "kind", Message: "kind must be one of bearer, header, basic, query, dsn or fields."}
	}
	if len(fields) == 0 || len(fields) > MaxFields {
		return &FieldError{Param: "secret", Message: fmt.Sprintf("secret must hold 1 to %d fields.", MaxFields)}
	}
	if req, ok := required[kind]; ok {
		for _, r := range req {
			if _, ok := fields[r]; !ok {
				return &FieldError{Param: "secret." + r, Message: fmt.Sprintf("A %s credential needs secret.%s.", kind, r)}
			}
		}
		for name := range fields {
			if !slices.Contains(req, name) && !slices.Contains(optional[kind], name) {
				return &FieldError{Param: "secret." + clipName(name), Message: fmt.Sprintf("A %s credential takes only %s.", kind, strings.Join(append(append([]string(nil), req...), optional[kind]...), " and "))}
			}
		}
	}
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		v := fields[name]
		p := "secret." + clipName(name)
		switch {
		case !reFieldName.MatchString(name):
			return &FieldError{Param: "secret", Message: "Field names use lowercase letters, digits and underscores, up to 32 characters, like api_key."}
		case len(v) == 0:
			return &FieldError{Param: p, Message: p + " is empty. Send the value, or remove the credential instead."}
		case len(v) > 8<<10:
			return &FieldError{Param: p, Message: p + " is longer than 8 KiB."}
		case !utf8.Valid(v) || strings.ContainsAny(string(v), "\r\n\x00"):
			return &FieldError{Param: p, Message: p + " must be one line of UTF-8 text."}
		case prod && secretField(kind, name) && len(v) < MinFieldLength:
			return &FieldError{Param: p, Message: fmt.Sprintf("%s is shorter than %d characters, too short to keep out of logs reliably. Use the full credential.", p, MinFieldLength)}
		}
	}
	return nil
}

func clipName(n string) string {
	if len(n) > 32 || !utf8.ValidString(n) {
		return "field"
	}
	return n
}

// Hint is the one non-secret echo of a credential: the last 4 characters of a token of at least
// 24, the username of basic auth, or user@host:port/db of a DSN.
func Hint(kind string, fields map[string][]byte) string {
	switch kind {
	case KindBearer, KindHeader, KindQuery:
		v := fields["token"]
		if kind != KindBearer {
			v = fields["value"]
		}
		if r := []rune(string(v)); len(r) >= 24 {
			return "…" + string(r[len(r)-4:])
		}
	case KindBasic:
		u := string(fields["username"])
		if utf8.RuneCountInString(u) <= 64 {
			return u
		}
	case KindDSN:
		u, err := url.Parse(string(fields["dsn"]))
		if err == nil && u.Host != "" {
			h := u.Host
			if u.Port() == "" {
				h += ":5432"
			}
			out := u.User.Username() + "@" + h + u.Path
			if utf8.RuneCountInString(out) <= 120 {
				return out
			}
		}
	}
	return ""
}

func (s *Store) needLedger() error {
	if s.led == nil {
		return errors.New("vault: the store has no ledger to seal admin deeds with")
	}
	return nil
}

// Put stores (or replaces) a connector's credential as version n+1, in one org transaction: the
// connector row locked, the envelope upserted (the old data key overwritten), and the admin deed
// sealed with kind, version and key id only.
func (s *Store) Put(ctx context.Context, ref Ref, kind string, fields map[string][]byte, actor Actor) (Meta, error) {
	if err := s.needLedger(); err != nil {
		return Meta{}, err
	}
	if err := CheckFields(kind, fields, s.prod); err != nil {
		return Meta{}, err
	}
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	hint := Hint(kind, fields)
	var m Meta
	err := s.db.OrgTx(ctx, ref.Org, func(ctx context.Context, tx pgx.Tx) error {
		con, err := store.GetConnector(ctx, tx, ref.Connector.String(), true)
		if err != nil {
			return err
		}
		v, err := store.CredentialVersion(ctx, tx, con.ID)
		if err != nil {
			return err
		}
		sealed, err := Seal(ctx, s.kp, s.rand, ref, v+1, kind, fields)
		if err != nil {
			return err
		}
		if err := store.UpsertCredential(ctx, tx, store.Credential{OrgID: ref.Org.String(), ConnectorID: con.ID, Version: sealed.Version,
			Kind: kind, FieldNames: names, Hint: hint, KEKID: sealed.KEKID, WrappedDEK: sealed.WrappedDEK, Ciphertext: sealed.Ciphertext,
			UpdatedBy: actor.By}); err != nil {
			return err
		}
		env, err := s.led.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminConnectorCredentialsUpdated, Actor: actor.Ref,
			Target: &deed.Ref{Type: "connector", ID: con.ID},
			Data: map[string]any{"type": con.Type, "connector": con.Name, "config_hash": con.ConfigHash, "kind": kind,
				"fields": names, "version": sealed.Version, "kek_id": sealed.KEKID}})
		if err != nil {
			return err
		}
		row, err := store.GetCredential(ctx, tx, con.ID)
		if err != nil {
			return err
		}
		m = metaOf(row)
		m.Deed = &env.ID
		return nil
	})
	return m, err
}

// Delete removes a connector's credential, which crypto-shreds it, and seals the admin deed.
// Removing nothing is not an error (set false, no deed).
func (s *Store) Delete(ctx context.Context, ref Ref, actor Actor) (Meta, error) {
	if err := s.needLedger(); err != nil {
		return Meta{}, err
	}
	m := Meta{Object: "credential", Connector: ref.Connector.String(), Deleted: true}
	err := s.db.OrgTx(ctx, ref.Org, func(ctx context.Context, tx pgx.Tx) error {
		con, err := store.GetConnector(ctx, tx, ref.Connector.String(), true)
		if err != nil {
			return err
		}
		d, err := s.DeleteTx(ctx, tx, con, actor)
		m.Deed = d
		return err
	})
	return m, err
}

// DeleteTx removes the credential inside the caller's transaction (connector delete uses it) and
// seals connector.credentials_removed when there was one. It returns the deed id, or nil.
func (s *Store) DeleteTx(ctx context.Context, tx pgx.Tx, con store.Connector, actor Actor) (*string, error) {
	if err := s.needLedger(); err != nil {
		return nil, err
	}
	old, err := store.GetCredential(ctx, tx, con.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := store.DeleteCredential(ctx, tx, con.ID); err != nil {
		return nil, err
	}
	env, err := s.led.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminConnectorCredentialsRemoved, Actor: actor.Ref,
		Target: &deed.Ref{Type: "connector", ID: con.ID},
		Data:   map[string]any{"type": con.Type, "connector": con.Name, "config_hash": con.ConfigHash, "kind": old.Kind, "version": old.Version, "kek_id": old.KEKID}})
	if err != nil {
		return nil, err
	}
	return &env.ID, nil
}

// Meta describes the connector's credential (set false when it has none).
func (s *Store) Meta(ctx context.Context, ref Ref) (Meta, error) {
	m := Meta{Object: "credential", Connector: ref.Connector.String()}
	err := s.db.OrgReadTx(ctx, ref.Org, func(ctx context.Context, tx pgx.Tx) error {
		row, err := store.GetCredential(ctx, tx, ref.Connector.String())
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		m = metaOf(row)
		return nil
	})
	return m, err
}

// MetaTx is Meta inside the caller's transaction.
func MetaTx(ctx context.Context, tx pgx.Tx, connectorID string) (Meta, error) {
	row, err := store.GetCredential(ctx, tx, connectorID)
	if errors.Is(err, store.ErrNotFound) {
		return Meta{Object: "credential", Connector: connectorID}, nil
	}
	if err != nil {
		return Meta{}, err
	}
	return metaOf(row), nil
}

func metaOf(r store.Credential) Meta {
	t := r.UpdatedAt.UTC()
	return Meta{Object: "credential", Connector: r.ConnectorID, Set: true, Kind: r.Kind, Fields: r.FieldNames, Hint: r.Hint,
		Version: r.Version, KEKID: r.KEKID, UpdatedAt: &t, UpdatedBy: r.UpdatedBy}
}

// Open decrypts a connector's credential for one call. A missing credential is ErrNotFound; a key
// not in the ring is *UnknownKEKError; anything that fails to authenticate is ErrIntegrity, and
// raises the vault_integrity alert.
func (s *Store) Open(ctx context.Context, ref Ref) (*Secret, error) {
	var row store.Credential
	err := s.db.OrgReadTx(ctx, ref.Org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		row, err = store.GetCredential(ctx, tx, ref.Connector.String())
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sec, err := Open(ctx, s.kp, ref, Sealed{Version: row.Version, Kind: row.Kind, KEKID: row.KEKID, WrappedDEK: row.WrappedDEK, Ciphertext: row.Ciphertext})
	var uk *UnknownKEKError
	switch {
	case errors.Is(err, ErrIntegrity):
		s.log.ErrorContext(ctx, "vault: a credential failed to authenticate; no call was sent", "alert", "vault_integrity",
			"org_id", ref.Org.String(), "connector_id", ref.Connector.String(), "kek_id", row.KEKID, "version", row.Version)
	case errors.As(err, &uk):
		s.log.ErrorContext(ctx, "vault: a credential uses a key that isn't in DEEDGLASS_VAULT_KEY; no call was sent", "alert", "vault_unavailable",
			"org_id", ref.Org.String(), "connector_id", ref.Connector.String(), "kek_id", row.KEKID)
	}
	return sec, err
}

// Status counts credentials per key id across every org.
func (s *Store) Status(ctx context.Context) (Status, error) {
	if s.ring == nil {
		return Status{}, ErrNoVault
	}
	st := Status{Object: "vault_status", Active: s.ring.ActiveID(), Ring: s.ring.IDs(), ByKEK: map[string]int{}, Unknown: map[string]int{}}
	for _, id := range st.Ring {
		st.ByKEK[id] = 0
	}
	orgs, err := s.db.OrgIDs(ctx)
	if err != nil {
		return st, err
	}
	for _, o := range orgs {
		org, err := ids.Parse[ids.OrgKind](o)
		if err != nil {
			return st, err
		}
		var counts map[string]int
		if err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			counts, err = store.CredentialsByKEK(ctx, tx)
			return err
		}); err != nil {
			return st, err
		}
		if len(counts) > 0 {
			st.Orgs++
		}
		for k, n := range counts {
			st.Total += n
			if k != st.Active {
				st.Pending += n
			}
			if s.ring.Has(k) {
				st.ByKEK[k] += n
			} else {
				st.Unknown[k] += n
			}
		}
	}
	return st, nil
}

// RotateBatch is how many rows one transaction rewraps.
const RotateBatch = 100

// Rotate rewraps every data key not on the active KEK, org by org, RotateBatch rows per
// transaction (FOR UPDATE SKIP LOCKED), and seals one vault.rotated deed per org it changed. It
// never re-encrypts a secret, is safe to interrupt and re-run, and is safe to run twice at once.
// A key that isn't in the ring stops it with *UnknownKEKError, naming the key and its row count.
func (s *Store) Rotate(ctx context.Context, progress func(done, total int)) (RotateResult, error) {
	if s.ring == nil {
		return RotateResult{}, ErrNoVault
	}
	if err := s.needLedger(); err != nil {
		return RotateResult{}, err
	}
	st, err := s.Status(ctx)
	if err != nil {
		return RotateResult{}, err
	}
	res := RotateResult{Object: "vault_rotation", Active: st.Active, Total: st.Total, Deeds: []string{}, Unused: []string{}}
	for k, n := range st.Unknown {
		return res, &UnknownKEKError{KEKID: k, Rows: n}
	}
	if progress != nil {
		progress(0, st.Pending)
	}
	active := s.ring.ActiveID()
	orgs, err := s.db.OrgIDs(ctx)
	if err != nil {
		return res, err
	}
	for _, o := range orgs {
		org, err := ids.Parse[ids.OrgKind](o)
		if err != nil {
			return res, err
		}
		from := map[string]int{}
		n := 0
		for {
			moved := 0
			err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
				rows, err := store.CredentialsToRewrap(ctx, tx, active, RotateBatch)
				if err != nil {
					return err
				}
				for _, r := range rows {
					con, err := ids.Parse[ids.ConnectorKind](r.ConnectorID)
					if err != nil {
						return err
					}
					if !s.ring.Has(r.KEKID) {
						return &UnknownKEKError{KEKID: r.KEKID}
					}
					ref := Ref{Org: org, Connector: con}
					nw, err := Rewrap(ctx, s.kp, ref, Sealed{Version: r.Version, Kind: r.Kind, KEKID: r.KEKID, WrappedDEK: r.WrappedDEK, Ciphertext: r.Ciphertext})
					if err != nil {
						return fmt.Errorf("vault: rewrapping %s: %w", r.ConnectorID, err)
					}
					ok, err := store.RewrapCredential(ctx, tx, r.ConnectorID, r.KEKID, nw.KEKID, nw.WrappedDEK)
					if err != nil {
						return err
					}
					if ok {
						moved++
						from[r.KEKID]++
					}
				}
				return nil
			})
			var uk *UnknownKEKError
			if errors.As(err, &uk) {
				uk.Rows = st.Unknown[uk.KEKID]
				return res, uk
			}
			if err != nil {
				return res, err
			}
			if moved == 0 {
				break
			}
			n += moved
			res.Rewrapped += moved
			if progress != nil {
				progress(res.Rewrapped, st.Pending)
			}
		}
		if n == 0 {
			continue
		}
		var deedID string
		froms := make([]string, 0, len(from))
		for k := range from {
			froms = append(froms, k)
		}
		sort.Strings(froms)
		if err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			env, err := s.led.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminVaultRotated, Actor: Operator.Ref,
				Target: &deed.Ref{Type: "org", ID: org.String()}, Data: map[string]any{"rewrapped": n, "from": froms, "to": active}})
			if err != nil {
				return err
			}
			deedID = env.ID
			return nil
		}); err != nil {
			return res, err
		}
		res.Deeds = append(res.Deeds, deedID)
	}
	after, err := s.Status(ctx)
	if err != nil {
		return res, err
	}
	for _, id := range after.Ring[1:] {
		if after.ByKEK[id] == 0 {
			res.Unused = append(res.Unused, id)
		}
	}
	return res, nil
}
