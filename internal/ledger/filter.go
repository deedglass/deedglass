package ledger

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
)

// ParamError is a query parameter the Ledger refuses. HTTP renders it as 400 validation_failed
// with param; the CLI prints it with a next step. Message is one calm sentence.
type ParamError struct {
	Param   string
	Message string
}

func (e *ParamError) Error() string { return e.Message }

func paramErr(param, format string, args ...any) *ParamError {
	return &ParamError{Param: param, Message: fmt.Sprintf(format, args...)}
}

// Page sizes.
const (
	DefaultLimit = 25
	MaxLimit     = 100
	maxToolBytes = 200 // the Gate's tool-name limit
	maxToolStars = 3
)

// Enumerations a filter accepts.
var (
	Decisions = []string{"allow", "deny", "require_approval"}
	Verdicts  = []string{"VERIFIED", "MISMATCH", "UNVERIFIABLE"}
	Levels    = []string{"L0", "L1", "L2"}
	DeedKinds = []string{"action", "admin", "amendment"}
)

// reRef is the Deedglass-On-Behalf-Of grammar (contract §8, S09).
var reRef = regexp.MustCompile(`^[A-Za-z0-9._~:@/+=-]{1,256}$`)

// ActionFilter is GET /v1/actions's query, validated. Lists are sorted and deduplicated, so equal
// filters hash equally (the cursor is bound to that hash).
type ActionFilter struct {
	Agent      string
	Tool       string // as given: exact, or a glob with * ("stripe.*")
	States     []string
	Decisions  []string
	Verdicts   []string
	Levels     []string
	OnBehalfOf string
	CreatedGTE *time.Time
	CreatedLT  *time.Time
	Q          string
}

// Page is a page request: limit and the opaque cursor from the previous page.
type Page struct {
	Limit  int
	Cursor string
}

// ParseActionQuery validates GET /v1/actions's query (the CLI builds the same url.Values).
// Unknown parameters are refused, so a typo never silently widens a query.
func ParseActionQuery(v url.Values) (ActionFilter, Page, error) {
	var f ActionFilter
	known := []string{"agent", "tool", "state", "decision", "verdict", "level", "on_behalf_of", "created_at[gte]", "created_at[lt]", "q", "limit", "cursor"}
	if err := onlyKnown(v, known); err != nil {
		return f, Page{}, err
	}
	var err error
	if f.Agent, err = single(v, "agent"); err != nil {
		return f, Page{}, err
	}
	if f.Agent != "" && ids.Check(ids.Agent, f.Agent) != nil {
		return f, Page{}, paramErr("agent", "agent must be an agent id like agt_01j9z9b1d3f5h7k9m1p3r5t7v9. You sent %q.", clip(f.Agent))
	}
	if f.Tool, err = single(v, "tool"); err != nil {
		return f, Page{}, err
	}
	if f.Tool != "" {
		switch {
		case len(f.Tool) > maxToolBytes:
			return f, Page{}, paramErr("tool", "tool must be at most %d bytes.", maxToolBytes)
		case strings.Count(f.Tool, "*") > maxToolStars:
			return f, Page{}, paramErr("tool", "tool may use * at most %d times.", maxToolStars)
		case !utf8.ValidString(f.Tool):
			return f, Page{}, paramErr("tool", "tool must be valid UTF-8.")
		}
	}
	states := make([]string, 0, len(gate.States()))
	for _, s := range gate.States() {
		states = append(states, string(s))
	}
	if f.States, err = enumList(v, "state", states, "a lifecycle state (received, denied, sealed, …)"); err != nil {
		return f, Page{}, err
	}
	if f.Decisions, err = enumList(v, "decision", Decisions, "allow, deny or require_approval"); err != nil {
		return f, Page{}, err
	}
	if f.Verdicts, err = enumList(v, "verdict", Verdicts, "VERIFIED, MISMATCH or UNVERIFIABLE"); err != nil {
		return f, Page{}, err
	}
	if f.Levels, err = enumList(v, "level", Levels, "L0, L1 or L2"); err != nil {
		return f, Page{}, err
	}
	if f.OnBehalfOf, err = single(v, "on_behalf_of"); err != nil {
		return f, Page{}, err
	}
	if f.OnBehalfOf != "" && !reRef.MatchString(f.OnBehalfOf) {
		return f, Page{}, paramErr("on_behalf_of", "on_behalf_of must be 1 to 256 characters: letters, digits and . _ ~ : @ / + = -.")
	}
	if f.CreatedGTE, err = timeParam(v, "created_at[gte]"); err != nil {
		return f, Page{}, err
	}
	if f.CreatedLT, err = timeParam(v, "created_at[lt]"); err != nil {
		return f, Page{}, err
	}
	if f.CreatedGTE != nil && f.CreatedLT != nil && !f.CreatedGTE.Before(*f.CreatedLT) {
		return f, Page{}, paramErr("created_at[gte]", "created_at[gte] must be before created_at[lt].")
	}
	if f.Q, err = single(v, "q"); err != nil {
		return f, Page{}, err
	}
	if f.Q != "" {
		if n := utf8.RuneCountInString(f.Q); n < 2 || n > 64 || !utf8.ValidString(f.Q) {
			return f, Page{}, paramErr("q", "q must be 2 to 64 characters.")
		}
	}
	p, err := parsePage(v)
	return f, p, err
}

func parsePage(v url.Values) (Page, error) {
	p := Page{Limit: DefaultLimit}
	if s, err := single(v, "limit"); err != nil {
		return p, err
	} else if s != "" || v.Has("limit") {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > MaxLimit {
			return p, paramErr("limit", "limit must be between 1 and %d. You sent %q.", MaxLimit, clip(s))
		}
		p.Limit = n
	}
	c, err := single(v, "cursor")
	if err != nil {
		return p, err
	}
	if v.Has("cursor") && c == "" {
		return p, errBadCursor()
	}
	p.Cursor = c
	return p, nil
}

func onlyKnown(v url.Values, known []string) error {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if !slices.Contains(known, k) {
			return paramErr(clip(k), "%q isn't a parameter of this list. Use %s.", clip(k), strings.Join(known, ", "))
		}
	}
	return nil
}

func single(v url.Values, k string) (string, error) {
	vs := v[k]
	switch len(vs) {
	case 0:
		return "", nil
	case 1:
		return vs[0], nil
	}
	return "", paramErr(k, "Send %s once. For several values use a comma list: %s=a,b.", k, k)
}

func enumList(v url.Values, k string, allowed []string, desc string) ([]string, error) {
	s, err := single(v, k)
	if err != nil || s == "" {
		return nil, err
	}
	var out []string
	for _, x := range strings.Split(s, ",") {
		x = strings.TrimSpace(x)
		if !slices.Contains(allowed, x) {
			return nil, paramErr(k, "%s must be %s. You sent %q.", k, desc, clip(x))
		}
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return out, nil
}

func timeParam(v url.Values, k string) (*time.Time, error) {
	s, err := single(v, k)
	if err != nil || s == "" {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, paramErr(k, "%s must be an RFC 3339 time like 2026-10-02T10:00:00Z. You sent %q.", k, clip(s))
	}
	t = t.UTC()
	return &t, nil
}

func clip(s string) string {
	if len(s) <= 64 {
		return s
	}
	n := 64
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// likePattern turns a tool filter into a LIKE pattern: %, _ and \ are escaped, * becomes %.
func likePattern(tool string) string {
	var b strings.Builder
	for _, r := range tool {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '*':
			b.WriteByte('%')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// containsPattern is q as an ILIKE pattern: escaped, wrapped in %.
func containsPattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// hash is the first 16 hex characters of sha256 over the canonical filter: the cursor's "q".
func (f ActionFilter) hash() string {
	return shortHash("actions", f)
}

func shortHash(kind string, v any) string {
	b, _ := json.Marshal(v) // plain fields, cannot fail; field order is fixed
	sum := sha256.Sum256(append([]byte(kind+"\n"), b...))
	return hex.EncodeToString(sum[:])[:16]
}

// cursor is base64url(JCS({"id","k","q","v"})): the last row's sort key, its id and the filter
// hash. Field order here is the JCS order.
type cursor struct {
	ID string `json:"id"`
	K  string `json:"k"`
	Q  string `json:"q"`
	V  int    `json:"v"`
}

func (c cursor) encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func errBadCursor() *ParamError {
	return paramErr("cursor", "cursor isn't a cursor this API issued. Pass next_cursor from the previous page unchanged.")
}

func errStaleCursor() *ParamError {
	return paramErr("cursor", "This cursor belongs to a different query. Start again without cursor, or repeat the original filters.")
}

// decodeCursor parses s and checks it belongs to the filter whose hash is q.
func decodeCursor(s, q string) (*cursor, error) {
	if s == "" {
		return nil, nil
	}
	if len(s) > 512 {
		return nil, errBadCursor()
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errBadCursor()
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var c cursor
	if err := dec.Decode(&c); err != nil || c.V != 1 || c.ID == "" || c.K == "" {
		return nil, errBadCursor()
	}
	if c.Q != q {
		return nil, errStaleCursor()
	}
	return &c, nil
}

// DeedFilter is GET /v1/deeds's query.
type DeedFilter struct {
	FromSeq int64    // 0: from the start
	ToSeq   int64    // 0: to the head
	Kinds   []string // action, admin, amendment
	Action  string   // act_…: the action's deed and its amendments
	Desc    bool     // order=desc (the Console); auditors read forward
	// OwnAgent (never from the query) limits the list to one agent's action and amendment deeds:
	// an agent key reads only its own agent's rows (S08).
	OwnAgent string
}

// ParseDeedQuery validates GET /v1/deeds's query.
func ParseDeedQuery(v url.Values) (DeedFilter, Page, error) {
	var f DeedFilter
	if err := onlyKnown(v, []string{"from_seq", "to_seq", "kind", "action", "order", "limit", "cursor"}); err != nil {
		return f, Page{}, err
	}
	var err error
	if f.FromSeq, f.ToSeq, err = seqRange(v); err != nil {
		return f, Page{}, err
	}
	if f.Kinds, err = enumList(v, "kind", DeedKinds, "action, admin or amendment"); err != nil {
		return f, Page{}, err
	}
	if f.Action, err = single(v, "action"); err != nil {
		return f, Page{}, err
	}
	if f.Action != "" && ids.Check(ids.Action, f.Action) != nil {
		return f, Page{}, paramErr("action", "action must be an action id like act_01j9zt3a5c7e9g1j3m5p7r9t1v. You sent %q.", clip(f.Action))
	}
	order, err := single(v, "order")
	if err != nil {
		return f, Page{}, err
	}
	switch order {
	case "", "asc":
	case "desc":
		f.Desc = true
	default:
		return f, Page{}, paramErr("order", "order must be asc or desc. You sent %q.", clip(order))
	}
	p, err := parsePage(v)
	return f, p, err
}

func (f DeedFilter) hash() string { return shortHash("deeds", f) }

// seqRange reads from_seq and to_seq: positive integers, from ≤ to.
func seqRange(v url.Values) (from, to int64, err error) {
	read := func(k string) (int64, error) {
		s, err := single(v, k)
		if err != nil || s == "" {
			return 0, err
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 1 || n > 1<<53-1 {
			return 0, paramErr(k, "%s must be a deed seq: a whole number from 1. You sent %q.", k, clip(s))
		}
		return n, nil
	}
	if from, err = read("from_seq"); err != nil {
		return 0, 0, err
	}
	if to, err = read("to_seq"); err != nil {
		return 0, 0, err
	}
	if from > 0 && to > 0 && from > to {
		return 0, 0, paramErr("from_seq", "from_seq must not be after to_seq.")
	}
	return from, to, nil
}

// Range selects deeds by seq or by issued_at. Zero values mean the whole chain.
type Range struct {
	FromSeq   int64
	ToSeq     int64
	IssuedGTE *time.Time
	IssuedLT  *time.Time
}

// ParseRangeQuery validates the export and check query: from_seq/to_seq or
// issued_at[gte]/issued_at[lt], not both.
func ParseRangeQuery(v url.Values) (Range, error) {
	var r Range
	if err := onlyKnown(v, []string{"from_seq", "to_seq", "issued_at[gte]", "issued_at[lt]"}); err != nil {
		return r, err
	}
	var err error
	if r.FromSeq, r.ToSeq, err = seqRange(v); err != nil {
		return r, err
	}
	if r.IssuedGTE, err = timeParam(v, "issued_at[gte]"); err != nil {
		return r, err
	}
	if r.IssuedLT, err = timeParam(v, "issued_at[lt]"); err != nil {
		return r, err
	}
	if (r.IssuedGTE != nil || r.IssuedLT != nil) && (r.FromSeq != 0 || r.ToSeq != 0) {
		return r, paramErr("issued_at[gte]", "Choose deeds by seq or by issued_at, not both.")
	}
	if r.IssuedGTE != nil && r.IssuedLT != nil && !r.IssuedGTE.Before(*r.IssuedLT) {
		return r, paramErr("issued_at[gte]", "issued_at[gte] must be before issued_at[lt].")
	}
	return r, nil
}
