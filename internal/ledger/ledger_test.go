package ledger

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gate "github.com/deedglass/deedglass/internal/gate/lifecycle"
	"github.com/deedglass/deedglass/internal/ids"
)

func q(s string) url.Values {
	v, err := url.ParseQuery(s)
	if err != nil {
		panic(err)
	}
	return v
}

func paramOf(err error) string {
	var pe *ParamError
	if errors.As(err, &pe) {
		return pe.Param
	}
	return ""
}

func TestParseActionQuery(t *testing.T) {
	ok := []struct {
		in   string
		want func(f ActionFilter, p Page) bool
	}{
		{"", func(f ActionFilter, p Page) bool { return p.Limit == DefaultLimit && f.Tool == "" }},
		{"tool=stripe.*", func(f ActionFilter, p Page) bool { return f.Tool == "stripe.*" }},
		{"verdict=UNVERIFIABLE,MISMATCH,MISMATCH", func(f ActionFilter, p Page) bool {
			return slices.Equal(f.Verdicts, []string{"MISMATCH", "UNVERIFIABLE"})
		}},
		{"state=sealed,denied&decision=deny&level=L2", func(f ActionFilter, p Page) bool {
			return slices.Equal(f.States, []string{"denied", "sealed"}) && f.Decisions[0] == "deny" && f.Levels[0] == "L2"
		}},
		{"limit=100", func(f ActionFilter, p Page) bool { return p.Limit == 100 }},
		{"created_at[gte]=2026-10-02T10:00:00Z&created_at[lt]=2026-10-02T11:00:00.5-05:30", func(f ActionFilter, p Page) bool {
			return f.CreatedGTE.Equal(time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)) && f.CreatedLT.Location() == time.UTC
		}},
		{"q=refund&on_behalf_of=u_4821&agent=agt_01j9z9b1d3f5h7k9m1p3r5t7v9", func(f ActionFilter, p Page) bool {
			return f.Q == "refund" && f.OnBehalfOf == "u_4821" && f.Agent != ""
		}},
	}
	for _, c := range ok {
		f, p, err := ParseActionQuery(q(c.in))
		if err != nil || !c.want(f, p) {
			t.Errorf("%q: %+v %+v %v", c.in, f, p, err)
		}
	}
	bad := []struct{ in, param string }{
		{"tool=***", ""}, // three stars are allowed; four are not (below)
		{"tool=****", "tool"},
		{"tool=" + strings.Repeat("a", 201), "tool"},
		{"verdict=FAILED", "verdict"},
		{"state=done", "state"},
		{"decision=maybe", "decision"},
		{"level=L3", "level"},
		{"limit=101", "limit"},
		{"limit=0", "limit"},
		{"limit=ten", "limit"},
		{"limit=", "limit"},
		{"agent=bot", "agent"},
		{"on_behalf_of=a%20b", "on_behalf_of"},
		{"created_at[gte]=yesterday", "created_at[gte]"},
		{"created_at[gte]=2026-10-02T11:00:00Z&created_at[lt]=2026-10-02T10:00:00Z", "created_at[gte]"},
		{"q=x", "q"},
		{"q=" + strings.Repeat("x", 65), "q"},
		{"verdict=MISMATCH&verdict=VERIFIED", "verdict"},
		{"sort=asc", "sort"},
		{"cursor=", "cursor"},
	}
	for _, c := range bad {
		_, _, err := ParseActionQuery(q(c.in))
		if c.param == "" {
			if err != nil {
				t.Errorf("%q: %v", c.in, err)
			}
			continue
		}
		if got := paramOf(err); got != c.param {
			t.Errorf("%q: param %q (%v), want %q", c.in, got, err, c.param)
		}
		if err != nil && !strings.HasSuffix(err.Error(), ".") {
			t.Errorf("%q: message isn't a sentence: %q", c.in, err)
		}
	}
}

// Every filter value reaches SQL as a bound parameter; hostile values only ever fail validation or
// become LIKE literals.
func TestFiltersNeverReachSQLText(t *testing.T) {
	hostile := []string{"'; DROP TABLE deedglass.actions; --", `\'`, "$1", "%_\\", "a' OR '1'='1"}
	for _, h := range hostile {
		for _, k := range []string{"agent", "tool", "state", "decision", "verdict", "level", "on_behalf_of", "q", "created_at[gte]"} {
			f, _, err := ParseActionQuery(url.Values{k: {h}})
			if err != nil {
				continue
			}
			where, args := f.where(mustOrg())
			if strings.Contains(where, "DROP") || strings.Contains(where, "OR '1'") || strings.Contains(where, "%_") {
				t.Errorf("%s=%q reached the SQL text: %s", k, h, where)
			}
			if len(args) < 2 {
				t.Errorf("%s=%q: not bound (%d args)", k, h, len(args))
			}
		}
	}
}

func TestLikePatterns(t *testing.T) {
	for in, want := range map[string]string{
		"stripe.*":   "stripe.%",
		"*.execute":  "%.execute",
		"a_b":        `a\_b`,
		"100%":       `100\%`,
		`back\slash`: `back\\slash`,
	} {
		if got := likePattern(in); got != want {
			t.Errorf("likePattern(%q) = %q, want %q", in, got, want)
		}
	}
	if got := containsPattern("re_f*"); got != `%re\_f*%` {
		t.Errorf("containsPattern = %q", got)
	}
}

func TestCursor(t *testing.T) {
	f1, _, _ := ParseActionQuery(q("verdict=MISMATCH"))
	f2, _, _ := ParseActionQuery(q("verdict=VERIFIED"))
	f3, _, _ := ParseActionQuery(q("verdict=MISMATCH&limit=7")) // limit isn't part of the query
	if f1.hash() == f2.hash() || f1.hash() != f3.hash() {
		t.Fatal("filter hashes")
	}
	c := cursor{ID: "act_01j9zt3a5c7e9g1j3m5p7r9t1v", K: "2026-10-02T10:14:03.401123Z", Q: f1.hash(), V: 1}
	enc := c.encode()
	raw, _ := base64.RawURLEncoding.DecodeString(enc)
	if want := `{"id":"act_01j9zt3a5c7e9g1j3m5p7r9t1v","k":"2026-10-02T10:14:03.401123Z","q":"` + f1.hash() + `","v":1}`; string(raw) != want {
		t.Fatalf("cursor JSON %s, want the JCS form %s", raw, want)
	}
	got, err := decodeCursor(enc, f1.hash())
	if err != nil || !reflect.DeepEqual(*got, c) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err := decodeCursor(enc, f2.hash()); paramOf(err) != "cursor" || !strings.Contains(err.Error(), "different query") {
		t.Errorf("stale cursor: %v", err)
	}
	for _, bad := range []string{"!!!", base64.RawURLEncoding.EncodeToString([]byte(`{"id":"x","k":"y","q":"z","v":2}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"id":"x","k":"y","q":"z","v":1,"org":"o"}`)), enc[:len(enc)-3], strings.Repeat("e", 600)} {
		if _, err := decodeCursor(bad, f1.hash()); paramOf(err) != "cursor" {
			t.Errorf("tampered cursor %q accepted: %v", bad, err)
		}
	}
}

func TestParseDeedAndRangeQuery(t *testing.T) {
	f, p, err := ParseDeedQuery(q("from_seq=5&to_seq=9&kind=admin,action&order=desc&limit=3"))
	if err != nil || f.FromSeq != 5 || f.ToSeq != 9 || !f.Desc || p.Limit != 3 || !slices.Equal(f.Kinds, []string{"action", "admin"}) {
		t.Fatalf("%+v %+v %v", f, p, err)
	}
	for in, param := range map[string]string{"from_seq=0": "from_seq", "from_seq=9&to_seq=5": "from_seq", "kind=ticket": "kind",
		"order=up": "order", "action=deed_x": "action", "to_seq=9007199254740993": "to_seq"} {
		if _, _, err := ParseDeedQuery(q(in)); paramOf(err) != param {
			t.Errorf("%q: %v", in, err)
		}
	}
	if _, err := ParseRangeQuery(q("from_seq=1&issued_at[gte]=2026-10-02T00:00:00Z")); paramOf(err) != "issued_at[gte]" {
		t.Errorf("seq and time together: %v", err)
	}
	if r, err := ParseRangeQuery(q("issued_at[gte]=2026-10-02T00:00:00Z")); err != nil || r.IssuedGTE == nil {
		t.Errorf("%+v %v", r, err)
	}
	if _, err := ParseRangeQuery(q("limit=5")); paramOf(err) != "limit" {
		t.Errorf("unknown range param: %v", err)
	}
}

func TestEventMapping(t *testing.T) {
	want := map[[2]gate.State]string{
		{gate.Evaluated, gate.Denied}: EventActionDecided, {gate.Evaluated, gate.PendingApproval}: EventActionDecided,
		{gate.Evaluated, gate.Executing}: EventActionDecided, {gate.Executing, gate.Executed}: EventActionExecuted,
		{gate.Executing, gate.Failed}: EventActionExecuted, {gate.Witnessing, gate.Verified}: EventActionWitnessed,
		{gate.Witnessing, gate.Mismatch}: EventActionWitnessed, {gate.Witnessing, gate.Unverifiable}: EventActionWitnessed,
	}
	for _, from := range gate.States() {
		for _, to := range from.Next() {
			got := eventFor(from, to)
			switch {
			case to == gate.Sealed:
				if got != EventDeedSealed {
					t.Errorf("%s → sealed: %q", from, got)
				}
			case got != want[[2]gate.State{from, to}]:
				t.Errorf("%s → %s: %q, want %q", from, to, got, want[[2]gate.State{from, to}])
			}
		}
	}
	if eventFor("", gate.Received) != EventActionCreated {
		t.Error("received")
	}
}

// Event payloads carry ids and states only, every key present, under 1 KiB.
func TestEventPayload(t *testing.T) {
	seq := int64(9007199254740991)
	id, act := "deed_01j9zt3q5s7v9w1y3a5c7e9g1j", "act_01j9zt3a5c7e9g1j3m5p7r9t1v"
	ev := Event{Type: EventDeedSealed, Org: "org_01j9z8m2k4p6r8t0v2x4z6b8d0", ID: id, Seq: &seq, Action: &act, State: strp("sealed"), Deed: &id}
	b, err := marshalEvent(&ev, time.Date(2026, 10, 2, 10, 14, 3, 512_000_000, time.UTC))
	if err != nil || len(b) > MaxNotifyPayload {
		t.Fatalf("%d bytes, %v", len(b), err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"action", "at", "deed", "event", "id", "org", "seq", "state", "type", "v"}; !slices.Equal(keys, want) {
		t.Fatalf("keys %v, want %v", keys, want)
	}
	if !strings.HasPrefix(m["event"].(string), "evt_") || m["at"] != "2026-10-02T10:14:03.512Z" {
		t.Fatalf("%s", b)
	}
	admin := Event{Type: EventDeedSealed, Org: "org_01j9z8m2k4p6r8t0v2x4z6b8d0", ID: id, Seq: &seq, Deed: &id}
	b, _ = marshalEvent(&admin, time.Now())
	if !strings.Contains(string(b), `"action":null`) || !strings.Contains(string(b), `"state":null`) {
		t.Fatalf("null keys missing: %s", b)
	}
	big := Event{Type: strings.Repeat("x", 1100)}
	if _, err := marshalEvent(&big, time.Now()); !errors.Is(err, ErrPayload) {
		t.Fatalf("oversize event: %v", err)
	}
}

func TestEventData(t *testing.T) {
	s := strings.Repeat("x", 250)
	full := EventData{Reason: &s, RuleID: &s, ErrorCode: &s, Level: &s, Verdict: &s, ApprovalID: &s, DetailDigest: &s, Actor: "agt_x", RequestID: "req_x"}
	b, err := full.marshal()
	if err != nil || len(b) > MaxEventData {
		t.Fatalf("%d %v", len(b), err)
	}
	if strings.Contains(string(b), "agt_x") || strings.Contains(string(b), "req_x") {
		t.Fatal("actor and request id are columns, not data")
	}
	huge := strings.Repeat("y", 3000)
	if _, err := (EventData{Reason: &huge}).marshal(); !errors.Is(err, ErrPayload) {
		t.Fatalf("over 2 KiB: %v", err)
	}
	// The struct is closed: no field could carry a payload.
	ty := reflect.TypeOf(EventData{})
	for i := range ty.NumField() {
		f := ty.Field(i)
		if f.Type.Kind() != reflect.Pointer && f.Type.Kind() != reflect.String {
			t.Errorf("EventData.%s is %s; only short strings belong here", f.Name, f.Type)
		}
	}
	if (EventData{}).actor() != "system" {
		t.Error("default actor")
	}
}

func TestAdminCatalogue(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range AdminActions {
		if !reAdminAction.MatchString(a) {
			t.Errorf("%q doesn't match ^[a-z_]+\\.[a-z_]+$", a)
		}
		if seen[a] {
			t.Errorf("%q twice", a)
		}
		seen[a] = true
	}
	if !knownAdminAction(AdminLedgerPruned) || knownAdminAction("key.created") {
		t.Error("catalogue lookups")
	}
}

func TestStateColumnsCoverEveryState(t *testing.T) {
	for _, s := range gate.States() {
		if s == gate.Received {
			continue // created_at
		}
		col, ok := stateColumn[s]
		if !ok || col != string(s)+"_at" || !strings.Contains(transitionSQL[s], col+" = pg_catalog.clock_timestamp()") {
			t.Errorf("%s: %q", s, col)
		}
	}
}

func TestTransitionRefusesSealedAndIllegalEdges(t *testing.T) {
	l := New(nil, nil, nil)
	if err := l.Transition(context.Background(), nil, "act_x", gate.Verified, gate.Sealed, EventData{}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("→ sealed: %v", err)
	}
	if err := l.Transition(context.Background(), nil, "act_x", gate.Executed, gate.Verified, EventData{}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("executed → verified: %v", err)
	}
	if _, err := l.SealAction(context.Background(), nil, "act_x"); !errors.Is(err, ErrNoSigner) {
		t.Fatalf("read-only ledger sealed: %v", err)
	}
}

func mustOrg() ids.OrgID { return ids.MustParse[ids.OrgKind]("org_01j9z8m2k4p6r8t0v2x4z6b8d0") }

// TestPatchSetsAgree: the written-out SET list of Transition is patchSetFrom(8), so Advance and
// Transition write the same columns with the same types.
func TestPatchSetsAgree(t *testing.T) {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, ",", ", ")), " ") }
	if norm(patchSet) != norm(patchSetFrom(8)) {
		t.Fatalf("patchSet drifted:\n%s\n%s", norm(patchSet), norm(patchSetFrom(8)))
	}
	if n := len((&Patch{}).args()); n != len(patchColumns) {
		t.Fatalf("args %d, columns %d", n, len(patchColumns))
	}
}

func TestPatchRules(t *testing.T) {
	d, s := "allow", "succeeded"
	if err := (&Patch{Decision: &d}).allowed(gate.Executed); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("a decision written on executed: %v", err)
	}
	if err := (&Patch{ExecutionStatus: &s}).allowed(gate.Executed); err != nil {
		t.Fatal(err)
	}
	p := Patch{Decision: &d}
	if err := p.merge(&Patch{ExecutionStatus: &s}); err != nil || *p.Decision != "allow" || *p.ExecutionStatus != "succeeded" {
		t.Fatalf("%v %+v", err, p)
	}
	if err := p.merge(&Patch{Decision: &d}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("two steps wrote one column: %v", err)
	}
	if err := p.merge(nil); err != nil {
		t.Fatal(err)
	}
	var q Patch
	q.fromArgs(p.args())
	if *q.Decision != "allow" || *q.ExecutionStatus != "succeeded" || q.Result != nil {
		t.Fatalf("%+v", q)
	}
}

func TestStatusOf(t *testing.T) {
	cases := []struct {
		state, mode                   string
		denied, expired, cancel, fail bool
		want                          string
	}{
		{"received", "gateway", false, false, false, false, StatusProcessing},
		{"executing", "gateway", false, false, false, false, StatusProcessing},
		{"executing", "proxy", false, false, false, false, StatusAuthorized},
		{"pending_approval", "gateway", false, false, false, false, StatusPendingApproval},
		{"denied", "gateway", true, false, false, false, StatusDenied},
		{"expired", "gateway", false, true, false, false, StatusExpired},
		{"cancelled", "gateway", false, false, true, false, StatusCancelled},
		{"failed", "gateway", false, false, false, true, StatusFailed},
		{"verified", "gateway", false, false, false, false, StatusExecuted},
		{"unverifiable", "proxy", false, false, false, false, StatusExecuted},
		{"witnessing", "gateway", false, false, false, false, StatusProcessing},
		{"sealed", "gateway", true, false, false, false, StatusDenied},
		{"sealed", "gateway", false, true, false, false, StatusExpired},
		{"sealed", "gateway", false, false, true, false, StatusCancelled},
		{"sealed", "gateway", false, false, false, true, StatusFailed},
		{"sealed", "proxy", false, false, false, false, StatusExecuted},
	}
	for _, c := range cases {
		if got := StatusOf(c.state, c.mode, c.denied, c.expired, c.cancel, c.fail); got != c.want {
			t.Errorf("%s/%s: %s, want %s", c.state, c.mode, got, c.want)
		}
	}
	if len(Statuses) != 8 {
		t.Fatal(Statuses)
	}
	if r := WitnessReasons([]byte(`{"reasons":["timeout"]}`)); len(r) != 1 || r[0] != "timeout" {
		t.Fatal(r)
	}
	if r := WitnessReasons(nil); r == nil || len(r) != 0 {
		t.Fatal("nil detail")
	}
}
