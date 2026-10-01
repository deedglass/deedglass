package identity

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/intent"
	"github.com/deedglass/deedglass/internal/keys"
	"github.com/deedglass/deedglass/internal/store"
)

// Signed intents (S09, docs/INTENTS.md). An agent's runtime signs exactly what it means to do (the
// tool, a digest of the exact arguments, a one-time nonce and a short time window) with an Ed25519
// key that never leaves the runtime. Deedglass checks the statement against the call, refuses
// anything that doesn't match, refuses replays, and S13 records the statement's hash in the deed.
//
// What it doesn't do: an attacker who controls the agent's runtime holds both the API key and the
// intent key, and acts as the agent. Intents can't help there; policy, Stop and approvals still
// apply.

// The deedglass.intent/1 limits (contract §8).
const (
	IntentVersion  = intent.AgentVersion
	MaxIntentBytes = intent.MaxAgentBytes
	MaxIntentTTL   = 5 * time.Minute
	ClockSkew      = 60 * time.Second
	MaxIntentKeys  = 2
)

// IntentMode is an agent's signed-intent mode (§3.5).
type IntentMode string

const (
	IntentOff      IntentMode = "off"      // the default: headers are ignored
	IntentOptional IntentMode = "optional" // a header is checked when sent; none is fine
	IntentRequired IntentMode = "required" // every call needs a valid header
)

// Valid reports whether m is one of the three modes.
func (m IntentMode) Valid() bool {
	return m == IntentOff || m == IntentOptional || m == IntentRequired
}

// IntentKey is one registered intent public key, as agents.intent_keys stores it.
type IntentKey struct {
	KID       string `json:"kid"`
	PublicKey string `json:"public_key"` // "ed25519:<base64url of 32 bytes>"
	CreatedAt string `json:"created_at"` // RFC 3339 UTC
	CreatedBy string `json:"created_by"` // usr_…, key_… or operator

	pub ed25519.PublicKey
}

// PublicKeyPrefix is the only public-key encoding Deedglass accepts.
const PublicKeyPrefix = "ed25519:"

// Intent-key registration errors (each maps to its own 422 message).
var (
	ErrPublicKeyFormat = errors.New(`identity: the public key must be "ed25519:" followed by 43 base64url characters`)
)

// ParseIntentPublicKey reads "ed25519:<base64url>" and applies the registration rules: 32 bytes, a
// canonical encoding, on the curve, and not of small order (internal/keys; CLAUDE.md invariant 7,
// attack case A13). It returns the key and its kid.
func ParseIntentPublicKey(s string) (ed25519.PublicKey, string, error) {
	rest, ok := strings.CutPrefix(s, PublicKeyPrefix)
	if !ok || len(rest) != 43 {
		return nil, "", ErrPublicKeyFormat
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(rest)
	if err != nil {
		return nil, "", ErrPublicKeyFormat
	}
	pub, err := keys.ParsePublicKey(raw)
	if err != nil {
		return nil, "", err
	}
	kid, err := keys.KID(pub)
	if err != nil {
		return nil, "", err
	}
	return pub, kid, nil
}

// FormatIntentPublicKey is the inverse of ParseIntentPublicKey.
func FormatIntentPublicKey(pub ed25519.PublicKey) string {
	return PublicKeyPrefix + base64.RawURLEncoding.EncodeToString(pub)
}

// parseIntentKeys reads agents.intent_keys. Every stored key passed registration; they are parsed
// again so a row edited outside the API can't bring in a small-order key.
func parseIntentKeys(raw []byte) ([]IntentKey, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var ks []IntentKey
	if err := json.Unmarshal(raw, &ks); err != nil {
		return nil, fmt.Errorf("identity: agents.intent_keys isn't valid: %w", err)
	}
	for i := range ks {
		pub, kid, err := ParseIntentPublicKey(ks[i].PublicKey)
		if err != nil || kid != ks[i].KID {
			return nil, errors.New("identity: agents.intent_keys holds a key that fails registration")
		}
		ks[i].pub = pub
	}
	return ks, nil
}

// IntentInput is one call to check.
type IntentInput struct {
	Principal *Principal
	Header    string          // Deedglass-Intent; "" if absent
	Tool      string          // the canonical connector.tool
	Args      json.RawMessage // exactly as received
	OBO       OBO             // from the headers (OBOFrom)
	Now       time.Time       // zero: the checker's clock
}

// IntentResult is a checked call. When Signed is false nothing was checked, and OBO is the
// headers' unsigned claim.
type IntentResult struct {
	Signed     bool
	IntentHash string // "sha256:…" when signed
	KID        string
	OBO        OBO // the effective values: the intent's when signed
	ExpiresAt  time.Time
	Tool       string
	ArgsDigest string
	Nonce      string
	Mode       IntentMode
}

// IntentChecker is what the Gate (S13) and POST /v1/intents/verify call. Check consumes the nonce
// in its own short transaction; CheckTx consumes it inside the caller's org transaction, so a
// failed action insert doesn't burn a nonce the agent can't retry.
type IntentChecker interface {
	Check(ctx context.Context, in IntentInput) (IntentResult, error)
	CheckTx(ctx context.Context, tx pgx.Tx, in IntentInput) (IntentResult, error)
}

// ArgsDigest is "sha256:" + hex(SHA-256(JCS(args))) under the strict args profile.
func ArgsDigest(args json.RawMessage) (string, error) { return intent.ArgsDigest(args) }

// IntentRefusal is the logged cause of every intent error: its stable reason (docs/INTENTS.md §6).
type IntentRefusal struct{ Reason string }

func (e *IntentRefusal) Error() string { return "intent refused: " + e.Reason }

// IntentReason returns the reason of an intent error, or "".
func IntentReason(err error) string {
	var r *IntentRefusal
	if errors.As(err, &r) {
		return r.Reason
	}
	return ""
}

// refuse builds the 403 for a refusal. key picks the copy (httpx.IntentCopy); the cause carries
// the stable reason of docs/INTENTS.md §6, which merges the copy variants.
func refuse(key, param string, kv ...string) error {
	e := httpx.ErrIntent(key, param, kv...)
	httpx.Cause(&IntentRefusal{Reason: reasonOf(key)})(e)
	return e
}

// reasonOf maps a copy key to its logged reason.
func reasonOf(key string) string {
	switch key {
	case "schema_unknown", "schema_missing":
		return "schema"
	case "unsigned_session":
		return "unsigned_obo"
	case "replay_unknown":
		return "replay"
	}
	return key
}

// Intents implements IntentChecker on Postgres, with the principal cache for agents' modes and keys.
type Intents struct {
	db   *store.DB
	keys *KeyAuth
	now  func() time.Time
}

var _ IntentChecker = (*Intents)(nil)

// NewIntents returns the checker. keys may be nil (every read goes to Postgres).
func NewIntents(db *store.DB, k *KeyAuth, now func() time.Time) *Intents {
	if now == nil {
		now = time.Now
	}
	return &Intents{db: db, keys: k, now: now}
}

// intentAgent is what the checks need to know about the calling agent.
type intentAgent struct {
	org  ids.OrgID
	id   ids.AgentID
	name string
	mode IntentMode
	keys []IntentKey
}

// Check implements IntentChecker.
func (c *Intents) Check(ctx context.Context, in IntentInput) (IntentResult, error) {
	return c.check(ctx, nil, in, false)
}

// CheckTx implements IntentChecker. tx must be an org transaction for the principal's org.
func (c *Intents) CheckTx(ctx context.Context, tx pgx.Tx, in IntentInput) (IntentResult, error) {
	if tx == nil {
		return IntentResult{}, errors.New("identity: CheckTx needs a transaction")
	}
	return c.check(ctx, tx, in, false)
}

// CheckAlways checks a header in every mode, including off: POST /v1/intents/verify, the
// conformance check for SDK authors. A missing header is still intent_required for a required
// agent, and Signed false otherwise.
func (c *Intents) CheckAlways(ctx context.Context, in IntentInput) (IntentResult, error) {
	return c.check(ctx, nil, in, true)
}

func (c *Intents) check(ctx context.Context, tx pgx.Tx, in IntentInput, force bool) (IntentResult, error) {
	p := in.Principal
	if p == nil || p.Kind != httpx.KindAgentKey || p.AgentID.IsZero() || p.OrgID.IsZero() {
		return IntentResult{}, errors.New("identity: signed intents belong to agent keys")
	}
	if in.Now.IsZero() {
		in.Now = c.now()
	}
	a, err := c.agent(ctx, tx, p)
	if err != nil {
		return IntentResult{}, err
	}
	res := IntentResult{OBO: in.OBO, Mode: a.mode}
	switch {
	case in.Header == "" && a.mode == IntentRequired:
		return res, refuse("required", "", "agent", a.name)
	case in.Header == "":
		return res, nil
	case a.mode == IntentOff && !force:
		return res, nil // off: ignored, not checked
	}
	res, err = verifyIntent(a, in)
	if err != nil {
		return IntentResult{Mode: a.mode}, err
	}
	res.Mode = a.mode
	consume := func(ctx context.Context, tx pgx.Tx) error {
		return consumeNonce(ctx, tx, a.org, a.id, res.Nonce, res.IntentHash, res.ExpiresAt)
	}
	if tx != nil {
		err = consume(ctx, tx)
	} else {
		err = c.db.OrgTx(ctx, a.org, consume)
	}
	if err != nil {
		return IntentResult{Mode: a.mode}, err
	}
	return res, nil
}

// agent reads the calling agent's mode and keys: from the cache when its entry carries them, else
// from Postgres (inside tx when one is given), caching the fresh value under the epoch guard.
func (c *Intents) agent(ctx context.Context, tx pgx.Tx, p *Principal) (intentAgent, error) {
	if c.keys != nil {
		if e, ok := c.keys.cache.Agent(p.AgentID); ok && e.IntentLoaded && e.OrgID == p.OrgID {
			return intentAgent{org: p.OrgID, id: p.AgentID, name: e.Name, mode: e.IntentMode, keys: e.IntentKeys}, nil
		}
	}
	var e0 uint64
	if c.keys != nil {
		e0 = c.keys.cache.Epoch()
	}
	var (
		name, status, mode string
		raw                []byte
	)
	read := func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT name, status, intent_mode, intent_keys FROM deedglass.agents WHERE org_id = $1 AND id = $2`,
			p.OrgID, p.AgentID).Scan(&name, &status, &mode, &raw)
	}
	var err error
	if tx != nil {
		err = read(ctx, tx)
	} else {
		err = c.db.OrgReadTx(ctx, p.OrgID, read)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return intentAgent{}, httpx.ErrAgentStopped(p.AgentName) // an agent that doesn't exist never acts
	}
	if err != nil {
		return intentAgent{}, err
	}
	ks, err := parseIntentKeys(raw)
	if err != nil {
		return intentAgent{}, err
	}
	m := IntentMode(mode)
	if !m.Valid() {
		return intentAgent{}, fmt.Errorf("identity: agent %s has intent mode %q", p.AgentID, mode)
	}
	if c.keys != nil && tx == nil {
		c.keys.cache.PutIfCurrent(e0, Loaded{Agent: &AgentEntry{ID: p.AgentID, OrgID: p.OrgID, Name: name, Status: AgentStatus(status),
			IntentLoaded: true, IntentMode: m, IntentKeys: ks}})
	}
	return intentAgent{org: p.OrgID, id: p.AgentID, name: name, mode: m, keys: ks}, nil
}

// verifyIntent runs checks 1–12 of §3.4 in memory, cheapest first. It never touches the nonce
// ledger. Nothing from a failed check is cached.
func verifyIntent(a intentAgent, in IntentInput) (IntentResult, error) {
	// 1. Two base64url parts; a 64-byte signature.
	raw, sig, err := intent.SplitHeader(in.Header)
	if err != nil {
		return IntentResult{}, refuse("syntax", "")
	}
	// 2–5. Size, the strict profile, canonical form, the closed schema and its patterns, the version.
	it, err := intent.ParseAgent(raw)
	if err != nil {
		return IntentResult{}, parseRefusal(err)
	}
	// 6. The intent names the key's org and agent.
	if it.Org != a.org.String() {
		return IntentResult{}, refuse("org_mismatch", "", "intent_org", it.Org, "org", a.org.String())
	}
	if it.Agent != a.id.String() {
		return IntentResult{}, refuse("agent_mismatch", "", "intent_agent", it.Agent, "agent", a.name, "agent_id", a.id.String())
	}
	// 7. The kid is registered for the agent.
	var pub ed25519.PublicKey
	for _, k := range a.keys {
		if k.KID == it.KID {
			pub = k.pub
		}
	}
	if pub == nil {
		return IntentResult{}, refuse("unknown_kid", "", "kid", ShortKID(it.KID), "agent", a.name)
	}
	// 8. The Ed25519 signature over "deedglass/intent/v1\n" + intent_hash.
	hash := intent.Digest(raw)
	if !ed25519.Verify(pub, intent.SigningInput(hash), sig) {
		return IntentResult{}, refuse("bad_signature", "", "kid", ShortKID(it.KID))
	}
	// 9. The clock: iat ≤ now + 60 s, now ≤ exp + 60 s, 0 < exp − iat ≤ 300000.
	now := in.Now.UnixMilli()
	skew := ClockSkew.Milliseconds()
	switch {
	case it.Iat > now+skew:
		return IntentResult{}, refuse("not_yet_valid", "", "ahead", humanDuration(time.Duration(it.Iat-now)*time.Millisecond))
	case now > it.Exp+skew:
		exp := time.UnixMilli(it.Exp).UTC()
		return IntentResult{}, refuse("expired", "", "time", exp.Format("15:04:05"), "ago", humanDuration(time.Duration(now-it.Exp)*time.Millisecond))
	case it.Exp-it.Iat <= 0 || it.Exp-it.Iat > intent.MaxAgentTTLMillis:
		return IntentResult{}, refuse("ttl", "", "ms", fmt.Sprint(it.Exp-it.Iat))
	}
	// 10. The tool is the call's tool.
	if it.Tool != in.Tool {
		return IntentResult{}, refuse("tool_mismatch", "", "intent_tool", it.Tool, "tool", in.Tool)
	}
	// 11. args_digest is the digest of the arguments actually sent.
	digest, err := intent.ArgsDigest(in.Args)
	if err != nil {
		var ae *intent.AgentError
		if errors.As(err, &ae) && ae.Reason == intent.WhyBigInt {
			return IntentResult{}, refuse("big_int", "")
		}
		return IntentResult{}, refuse("args_not_canonicalizable", "", "detail", argsDetail(err))
	}
	if digest != it.ArgsDigest {
		return IntentResult{}, refuse("args_mismatch", "")
	}
	// 12. If you sign, sign everything: the intent's OBO wins; a header that differs, or a header
	// the intent doesn't cover, is refused.
	eff := OBO{OnBehalfOf: it.OnBehalfOf, Session: it.Session}
	switch {
	case in.OBO.OnBehalfOf != "" && it.OnBehalfOf == "":
		return IntentResult{}, refuse("unsigned_obo", HeaderOnBehalfOf, "name", HeaderOnBehalfOf, "field", "on_behalf_of")
	case in.OBO.OnBehalfOf != "" && in.OBO.OnBehalfOf != it.OnBehalfOf:
		return IntentResult{}, refuse("obo_mismatch", "", "header", in.OBO.OnBehalfOf, "intent", it.OnBehalfOf)
	case in.OBO.Session != "" && it.Session == "":
		return IntentResult{}, refuse("unsigned_session", HeaderSession, "name", HeaderSession, "field", "session")
	case in.OBO.Session != "" && in.OBO.Session != it.Session:
		return IntentResult{}, refuse("session_mismatch", "", "header", in.OBO.Session, "intent", it.Session)
	}
	return IntentResult{Signed: true, IntentHash: hash, KID: it.KID, OBO: eff, ExpiresAt: time.UnixMilli(it.Exp).UTC(),
		Tool: it.Tool, ArgsDigest: it.ArgsDigest, Nonce: it.Nonce}, nil
}

// parseRefusal maps a ParseAgent error (checks 2–5) to its refusal.
func parseRefusal(err error) error {
	var ae *intent.AgentError
	if !errors.As(err, &ae) {
		return refuse("strict", "", "detail", "it can't be read")
	}
	switch ae.Reason {
	case intent.WhyTooLarge:
		return refuse("too_large", "")
	case intent.WhyStrict:
		return refuse("strict", "", "detail", strictDetail(ae))
	case intent.WhyNotCanonical:
		return refuse("not_canonical", "")
	case intent.WhyVersion:
		return refuse("version", "", "got", ae.Detail)
	}
	switch ae.Detail {
	case "unknown field":
		return refuse("schema_unknown", ae.Field, "field", ae.Field)
	case "missing field":
		return refuse("schema_missing", ae.Field, "field", ae.Field)
	}
	return refuse("schema", ae.Field, "field", ae.Field, "want", fieldWant(ae.Field))
}

// strictDetail turns a strict-parser failure into the clause the message needs.
func strictDetail(ae *intent.AgentError) string {
	switch intent.CodeOf(ae.Err) {
	case intent.CodeDuplicateKey:
		return fmt.Sprintf("the key %q appears twice", ae.Field)
	case intent.CodeIntegerRange:
		return "it contains an integer larger than 9007199254740991"
	case intent.CodeNonInteger:
		return "it contains a number with a fraction or exponent, and intents carry integers only"
	case intent.CodeInvalidUTF8:
		return "it isn't valid UTF-8"
	case intent.CodeLoneSurrogate:
		return "it contains a lone UTF-16 surrogate"
	case intent.CodeTooDeep:
		return "it is nested too deeply"
	}
	return "it isn't valid JSON"
}

func argsDetail(err error) string {
	switch intent.CodeOf(errors.Unwrap(err)) {
	case intent.CodeDuplicateKey:
		return "a key appears twice"
	case intent.CodeTooDeep:
		return "they are nested more than 32 levels deep"
	case intent.CodeNumberRange:
		return "a number is outside the range of a double"
	case intent.CodeInvalidUTF8:
		return "they aren't valid UTF-8"
	case intent.CodeLoneSurrogate:
		return "they contain a lone UTF-16 surrogate"
	}
	if strings.Contains(err.Error(), "256 KiB") {
		return "they are larger than 256 KiB"
	}
	return "they aren't valid JSON"
}

// fieldWant describes a field's valid form, for the schema message.
func fieldWant(field string) string {
	switch field {
	case "org":
		return "the org's id (org_…)"
	case "agent":
		return "the agent's id (agt_…)"
	case "kid":
		return "32 lowercase hex characters"
	case "nonce":
		return "32 lowercase hex characters (128 random bits)"
	case "tool":
		return "a connector.tool name such as stripe.refund"
	case "args_digest":
		return "sha256: followed by 64 lowercase hex characters"
	case "iat", "exp":
		return "positive Unix milliseconds"
	case "on_behalf_of", "session":
		return "1 to 256 characters: letters, digits and . _ ~ : @ / + = -"
	case "v":
		return "deedglass.intent/1"
	}
	return "the form the field table gives"
}

// ShortKID is a kid as messages show it: the first and last four hex characters.
func ShortKID(kid string) string {
	if len(kid) <= 8 {
		return kid
	}
	return kid[:4] + "…" + kid[len(kid)-4:]
}

// humanDuration says d the way the copy does: "45 seconds", "1 minute", "3 minutes", "2 hours".
func humanDuration(d time.Duration) string {
	s := int64(d.Round(time.Second) / time.Second)
	switch {
	case s < 1:
		return "less than a second"
	case s == 1:
		return "1 second"
	case s < 60:
		return fmt.Sprintf("%d seconds", s)
	case s < 120:
		return "1 minute"
	case s < 3600:
		return fmt.Sprintf("%d minutes", s/60)
	case s < 7200:
		return "1 hour"
	case s < 2*86400:
		return fmt.Sprintf("%d hours", s/3600)
	}
	return fmt.Sprintf("%d days", s/86400)
}

// consumeNonce is check 13: insert the nonce; a conflict means it was already used. The primary
// key (agent_id, nonce) makes this atomic: of any number of concurrent uses, exactly one inserts.
func consumeNonce(ctx context.Context, tx pgx.Tx, org ids.OrgID, agent ids.AgentID, nonce, hash string, exp time.Time) error {
	var created time.Time
	err := tx.QueryRow(ctx, `INSERT INTO deedglass.intent_nonces (org_id, agent_id, nonce, intent_hash, expires_at)
	    VALUES ($1, $2, $3, $4, $5) ON CONFLICT (agent_id, nonce) DO NOTHING RETURNING created_at`,
		org, agent, nonce, hash, exp).Scan(&created)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT created_at FROM deedglass.intent_nonces WHERE org_id = $1 AND agent_id = $2 AND nonce = $3`,
		org, agent, nonce).Scan(&created); err != nil {
		return refuse("replay_unknown", "") // pruned between the two statements: still a replay
	}
	return refuse("replay", "", "time", created.UTC().Format("15:04:05"))
}
