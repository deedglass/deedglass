package sim

//deedglass:allow-math-rand the simulated week must be reproducible from a seed (PCG); it never makes a secret, an id or a token

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/policy"
)

// Window is the simulated span: Days whole 24-hour days ending at End (exclusive).
type Window struct {
	Start, End time.Time
	Days       int
}

// Span returns the window of days days that ends at end truncated down to the org-local hour.
// Day d (1-based) is [Start+(d-1)·24h, Start+d·24h).
func Span(sc *Scenario, days int, end time.Time) (Window, error) {
	if days < 1 || days > MaxDays {
		return Window{}, fmt.Errorf("days is %d; simulate 1 to %d days", days, MaxDays)
	}
	if end.IsZero() {
		return Window{}, fmt.Errorf("the end time is missing; pass the instant the simulated week ends")
	}
	loc, err := location(sc)
	if err != nil {
		return Window{}, err
	}
	le := end.In(loc)
	e := time.Date(le.Year(), le.Month(), le.Day(), le.Hour(), 0, 0, 0, loc)
	if e.After(end) { // an ambiguous local hour resolved to its later instant
		e = e.Add(-time.Hour)
	}
	return Window{Start: e.Add(-time.Duration(days) * 24 * time.Hour).UTC(), End: e.UTC(), Days: days}, nil
}

// PlannedAction is one action the runner submits through the Gate.
type PlannedAction struct {
	Seq           int             `json:"seq"`            // 1.. in time order
	At            time.Time       `json:"at"`             // virtual decision time, UTC, ms precision, strictly increasing
	Day           int             `json:"day"`            // 1-based
	Agent         string          `json:"agent"`          // agent name
	Tool          string          `json:"tool"`           // e.g. "stripe.create_refund"
	ConnectorType string          `json:"connector_type"` // the connector before the dot
	SideEffect    string          `json:"side_effect"`    // read | write | destructive
	Args          json.RawMessage `json:"args"`           // compact JSON, keys sorted; postgres tools carry {"sql": "..."}
	UserRef       string          `json:"user_ref"`       // "" when the agent has no user_refs
	Outcome       Outcome         `json:"outcome"`
	Approval      ApprovalScript  `json:"approval"` // used only if the policy asks for approval
	Incident      string          `json:"incident"` // the incident kind that produced it, "" for baseline traffic
}

// Outcome is what the scripted tool and the scripted Witness do.
type Outcome struct {
	Exec      string         `json:"exec"`       // succeeded | failed | unknown
	ErrorCode string         `json:"error_code"` // failed: upstream_error; unknown: timeout
	Level     string         `json:"level"`      // L0 | L1 | L2
	Evidence  string         `json:"evidence"`   // clean | contradicts | gap: what the scripted witness reports; "" when Exec is failed. witness.Decide gives the verdict.
	LatencyMS int            `json:"latency_ms"`
	CostMinor int64          `json:"cost_minor"` // a succeeded stripe.create_refund: its amount; 0 otherwise
	Currency  string         `json:"currency"`   // with a cost, "" otherwise
	Detail    map[string]any `json:"detail"`     // a mismatch's table and row counts; nil otherwise
}

// ApprovalScript is how the approver answers if the policy asks for approval.
type ApprovalScript struct {
	Approver string        `json:"approver"` // one of the scenario's approvers
	Vote     string        `json:"vote"`     // approve | deny
	Wait     time.Duration `json:"wait"`     // at least 30 s
}

// Values of the plan's string fields.
const (
	ExecSucceeded = "succeeded"
	ExecFailed    = "failed"
	ExecUnknown   = "unknown"

	ErrUpstream = "upstream_error"
	ErrTimeout  = "timeout"

	// The evidence the scripted witness reports (seams.go turns it into reasons; witness.Decide gives
	// the verdict: clean → VERIFIED, contradicts → MISMATCH, gap → UNVERIFIABLE).
	EvidenceClean       = "clean"
	EvidenceContradicts = "contradicts"
	EvidenceGap         = "gap"

	VoteApprove = "approve"
	VoteDeny    = "deny"

	SideRead        = "read"
	SideWrite       = "write"
	SideDestructive = "destructive"
)

const (
	hourMS     = int64(time.Hour / time.Millisecond)
	dayMS      = 24 * hourMS
	inHours    = 0.97 // the share of an agent's volume inside its hours
	minRefund  = 100  // the smallest refund budget_pressure adds, in minor units
	timeoutMS  = 30000
	refundTool = "stripe.create_refund"
	// pcgSeq is the PCG sequence constant; the seed and the sub-stream pick the state.
	pcgSeq = 0x6465_6564_676c_6173
)

// Sub-stream purposes: baseline traffic per agent, and each incident on its own, so adding an
// incident never reshuffles the baseline.
const (
	streamAgent    = 1
	streamIncident = 2
)

func newStream(seed, purpose, idx uint64) *rand.Rand {
	return rand.New(rand.NewPCG(splitmix(seed^splitmix(purpose<<40|idx)), pcgSeq))
}

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// draft is a planned action before it is placed in time order.
type draft struct {
	off      int64 // ms from the window's start
	src, idx int   // tie-breakers: the stream (agent, then incident) and its running index
	a        PlannedAction

	evaluated bool
	counted   int64     // the refund budget's reservation amount
	window    time.Time // that reservation's window start
}

type gen struct {
	sc      *Scenario
	w       Window
	seed    uint64
	loc     *time.Location
	tod0    int64 // the org-local time of day of w.Start, in ms
	drafts  []*draft
	next    map[int]int
	enforce *policy.Compiled
}

// Plan decides what happens when, for the whole window. It is pure and deterministic: the same
// scenario, seed and window give the same plan, byte for byte (PlanDigest).
func Plan(sc *Scenario, seed uint64, w Window) ([]PlannedAction, error) {
	loc, err := location(sc)
	if err != nil {
		return nil, err
	}
	if w.Days < 1 || w.Days > MaxDays || !w.End.Equal(w.Start.Add(time.Duration(w.Days)*24*time.Hour)) {
		return nil, fmt.Errorf("the window is not %d whole days; build it with sim.Span", w.Days)
	}
	if len(sc.Agents) == 0 || len(sc.Approvers) == 0 {
		return nil, fmt.Errorf("the scenario has no agents or no approvers; build it with sim.Load or sim.Parse")
	}
	for i, inc := range sc.Incidents {
		for _, d := range inc.Days {
			if d > w.Days {
				return nil, &Error{File: sc.Source, Line: inc.Line, Msg: fmt.Sprintf(
					"incidents[%d] is on day %d, but the window has %d days; simulate at least %d days, or move the incident.", i, d, w.Days, d)}
			}
		}
	}
	ls := w.Start.In(loc)
	g := &gen{sc: sc, w: w, seed: seed, loc: loc, next: map[int]int{},
		tod0: int64(ls.Hour())*hourMS + int64(ls.Minute())*60000 + int64(ls.Second())*1000 + int64(ls.Nanosecond()/1e6)}
	for i := range sc.Agents {
		g.baseline(i)
	}
	for j, inc := range sc.Incidents {
		if inc.Kind != KindBudgetPressure {
			g.incident(j, inc)
		}
	}
	for j, inc := range sc.Incidents {
		if inc.Kind == KindBudgetPressure {
			if err := g.pressure(j, inc); err != nil {
				return nil, err
			}
		}
	}
	return g.finish()
}

func location(sc *Scenario) (*time.Location, error) {
	if sc.Org.Location != nil {
		return sc.Org.Location, nil
	}
	loc, err := time.LoadLocation(sc.Org.Timezone)
	if err != nil {
		return nil, fmt.Errorf("org timezone %q is not a known IANA time zone; fix the scenario", sc.Org.Timezone)
	}
	return loc, nil
}

func (g *gen) add(off int64, src int, a PlannedAction) *draft {
	d := &draft{off: off, src: src, idx: g.next[src], a: a}
	g.next[src]++
	g.drafts = append(g.drafts, d)
	return d
}

// spanOffset places an org-local time of day (ms) inside a 24-hour span that starts at tod0.
func (g *gen) spanOffset(tod int64) int64 { return ((tod-g.tod0)%dayMS + dayMS) % dayMS }

func dayStart(d int) int64 { return int64(d-1) * dayMS }

// ---- baseline traffic ----

func (g *gen) baseline(ai int) {
	ag := &g.sc.Agents[ai]
	r := newStream(g.seed, streamAgent, uint64(ai))
	z := zipfFor(r, ag)
	width := ag.HourTo - ag.HourFrom
	for d := 1; d <= g.w.Days; d++ {
		n := int(math.Round(float64(ag.PerDay) * (0.9 + 0.2*r.Float64())))
		out := 0
		if width < 24 {
			out = int(math.Round((1 - inHours) * float64(n)))
		}
		for i := range n {
			var tod int64
			if i < n-out {
				tod = bellTOD(r, ag)
			} else {
				tod = offTOD(r, ag)
			}
			a := g.action(r, z, ag, pickTool(r, ag), d)
			g.add(dayStart(d)+g.spanOffset(tod), ai, a)
		}
	}
}

// bellTOD draws a time of day inside the agent's hours from a bell centred on them.
func bellTOD(r *rand.Rand, ag *Agent) int64 {
	from, to := float64(ag.HourFrom), float64(ag.HourTo)
	mu, sd := (from+to)/2, (to-from)/4
	for range 64 {
		x := mu + sd*r.NormFloat64()
		if x >= from && x < to {
			return int64(x * float64(hourMS))
		}
	}
	return int64(from*float64(hourMS)) + r.Int64N(int64(ag.HourTo-ag.HourFrom)*hourMS)
}

// offTOD draws a time of day uniformly outside the agent's hours.
func offTOD(r *rand.Rand, ag *Agent) int64 {
	width := int64(ag.HourTo-ag.HourFrom) * hourMS
	u := r.Int64N(dayMS - width)
	if u >= int64(ag.HourFrom)*hourMS {
		u += width
	}
	return u
}

// hourShare is the expected share of an agent's daily volume in one org-local hour.
func hourShare(ag *Agent, h int) float64 {
	width := ag.HourTo - ag.HourFrom
	if h < ag.HourFrom || h >= ag.HourTo {
		if width >= 24 {
			return 0
		}
		return (1 - inHours) / float64(24-width)
	}
	share := inHours
	if width >= 24 {
		share = 1
	}
	from, to := float64(ag.HourFrom), float64(ag.HourTo)
	mu, sd := (from+to)/2, (to-from)/4
	phi := func(x float64) float64 { return 0.5 * math.Erfc(-(x-mu)/(sd*math.Sqrt2)) }
	return share * (phi(float64(h+1)) - phi(float64(h))) / (phi(to) - phi(from))
}

func pickTool(r *rand.Rand, ag *Agent) ToolSpec {
	total := 0.0
	for _, t := range ag.Tools {
		total += t.Weight
	}
	u := r.Float64() * total
	for _, t := range ag.Tools {
		if u < t.Weight {
			return t
		}
		u -= t.Weight
	}
	return ag.Tools[len(ag.Tools)-1]
}

func zipfFor(r *rand.Rand, ag *Agent) *rand.Zipf {
	if ag.UserRefs == nil || ag.UserRefs.Pool < 2 {
		return nil
	}
	return rand.NewZipf(r, 1.2, 30, uint64(ag.UserRefs.Pool-1))
}

func poolRef(u *UserRefs, z *rand.Zipf) string {
	if u == nil {
		return ""
	}
	var i uint64
	if z != nil {
		i = z.Uint64()
	}
	return fmt.Sprintf("%s%04d", u.Prefix, 1000+(int(i)*7919+17)%9000)
}

// action makes one call of spec by ag, with its arguments, ref, outcome and approval script.
func (g *gen) action(r *rand.Rand, z *rand.Zipf, ag *Agent, spec ToolSpec, day int) PlannedAction {
	args := makeArgs(r, spec)
	a := PlannedAction{Day: day, Agent: ag.Name, Tool: spec.Tool, ConnectorType: connectorOf(spec.Tool),
		SideEffect: sideEffect(spec.Tool, args), Args: compactJSON(args), UserRef: poolRef(ag.UserRefs, z)}
	a.Outcome = outcome(r, ag, spec.Tool, args)
	a.Approval = g.approval(r, ag)
	return a
}

// ---- arguments ----

var (
	defaultReads = []string{
		"SELECT id, status FROM public.orders WHERE id = {id}",
		"SELECT count(*) FROM public.orders WHERE status = 'pending'",
		"SELECT sku, reserved FROM public.inventory WHERE sku = {sku}",
		"SELECT id, total_cents FROM public.orders ORDER BY created_at DESC LIMIT 20",
	}
	defaultWrites = []string{"UPDATE public.inventory SET reserved = reserved + 1 WHERE sku = {sku}"}
)

func makeArgs(r *rand.Rand, spec ToolSpec) map[string]any {
	conn := connectorOf(spec.Tool)
	if spec.Args == nil && spec.SQL == nil {
		return defaultArgs(r, spec.Tool)
	}
	args := map[string]any{}
	for _, a := range spec.Args {
		if a.Lognormal != nil {
			args[a.Key] = lognormalInt(r, a.Lognormal.Median, a.Lognormal.Sigma)
		} else {
			args[a.Key] = a.Value
		}
	}
	if conn == "postgres" {
		sqls := spec.SQL
		if sqls == nil {
			sqls = defaultSQL(spec.Tool)
		}
		args["sql"] = fillSQL(r, sqls[r.IntN(len(sqls))])
	}
	return args
}

func defaultSQL(tool string) []string {
	if tool == "postgres.execute" {
		return defaultWrites
	}
	return defaultReads
}

func defaultArgs(r *rand.Rand, tool string) map[string]any {
	switch tool {
	case refundTool:
		return map[string]any{"amount": lognormalInt(r, 2400, 0.8), "currency": "usd"}
	case "gmail.create_draft":
		return map[string]any{"thread": fmt.Sprintf("thr_%d", 10000+r.IntN(90000)), "template": "refund_update"}
	case "slack.post_message":
		ch := "#support"
		switch u := r.Float64(); {
		case u >= 0.95:
			ch = "#random"
		case u >= 0.70:
			ch = "#launches"
		}
		return map[string]any{"channel": ch, "text_chars": 40 + r.IntN(361)}
	case "notion.append_block":
		return map[string]any{"page": fmt.Sprintf("pg_%d", 10000+r.IntN(90000)), "blocks": 1 + r.IntN(3)}
	case "github.merge_pr":
		return map[string]any{"repo": "acme/store", "pr": 100 + r.IntN(900)}
	}
	if connectorOf(tool) == "postgres" {
		sqls := defaultSQL(tool)
		return map[string]any{"sql": fillSQL(r, sqls[r.IntN(len(sqls))])}
	}
	return map[string]any{}
}

// fillSQL fills each {id} with an order id (1000 to 9999) and each {sku} with a quoted SKU.
func fillSQL(r *rand.Rand, s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		switch {
		case strings.HasPrefix(s[i:], "{id}"):
			b.WriteString(strconv.Itoa(1000 + r.IntN(9000)))
			s = s[i+4:]
		case strings.HasPrefix(s[i:], "{sku}"):
			fmt.Fprintf(&b, "'SKU-%04d'", 1+r.IntN(500))
			s = s[i+5:]
		default:
			b.WriteByte('{')
			s = s[i+1:]
		}
	}
}

func lognormalInt(r *rand.Rand, median, sigma float64) int64 {
	v := math.Round(median * math.Exp(sigma*r.NormFloat64()))
	return int64(max(1, min(v, float64(policy.MaxMinor))))
}

func sideEffect(tool string, args map[string]any) string {
	conn, op, _ := strings.Cut(tool, ".")
	if conn == "postgres" {
		switch op {
		case "query", "describe":
			return SideRead
		case "execute":
			if s, ok := args["sql"].(string); ok {
				f := strings.Fields(strings.ToUpper(s))
				if len(f) > 0 && (f[0] == "DELETE" || f[0] == "DROP" || f[0] == "TRUNCATE") {
					return SideDestructive
				}
			}
		}
		return SideWrite
	}
	for _, p := range []string{"get_", "list_", "search_", "read_"} {
		if strings.HasPrefix(op, p) {
			return SideRead
		}
	}
	for _, p := range []string{"delete_", "remove_"} {
		if strings.HasPrefix(op, p) {
			return SideDestructive
		}
	}
	return SideWrite
}

func compactJSON(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic("sim: arguments do not encode: " + err.Error()) // only maps of strings and numbers reach here
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n"))
}

// ---- outcomes and approvals ----

var latencyMedian = map[string]float64{"postgres": 40, "stripe": 300, "gmail": 250, "slack": 180, "notion": 350, "github": 500}

func outcome(r *rand.Rand, ag *Agent, tool string, args map[string]any) Outcome {
	u, lv, sub, lz := r.Float64(), r.Float64(), r.Float64(), r.NormFloat64()
	o := Outcome{Level: pickLevel(ag.Levels, lv), LatencyMS: latency(connectorOf(tool), lz)}
	oc := ag.Outcomes
	switch {
	case u < oc.Verified || (oc.Unverifiable == 0 && oc.Failed == 0):
		o.Exec, o.Evidence = ExecSucceeded, EvidenceClean
	case u < oc.Verified+oc.Unverifiable || oc.Failed == 0:
		o.Evidence = EvidenceGap
		o.Exec = ExecSucceeded
		if sub >= 0.7 {
			o.Exec, o.ErrorCode, o.LatencyMS = ExecUnknown, ErrTimeout, timeoutMS
		}
	default:
		o.Exec, o.ErrorCode, o.Level = ExecFailed, ErrUpstream, "L0"
	}
	if tool == refundTool && o.Exec == ExecSucceeded {
		if amt, ok := args["amount"].(int64); ok {
			o.CostMinor = amt
			o.Currency = "usd"
			if c, ok := args["currency"].(string); ok && c != "" {
				o.Currency = strings.ToLower(c)
			}
		}
	}
	return o
}

func pickLevel(l Levels, u float64) string {
	switch {
	case u < l.L0 || (l.L1 == 0 && l.L2 == 0):
		return "L0"
	case u < l.L0+l.L1 || l.L2 == 0:
		return "L1"
	}
	return "L2"
}

func latency(conn string, z float64) int {
	med := latencyMedian[conn]
	if med == 0 {
		med = 200
	}
	return int(max(1, math.Round(med*math.Exp(0.5*z))))
}

// approval scripts the answer: a seeded approver, a seeded vote and a lognormal wait through the
// agent's median and p95, at least 30 s.
func (g *gen) approval(r *rand.Rand, ag *Agent) ApprovalScript {
	who := g.sc.Approvers[r.IntN(len(g.sc.Approvers))]
	vote := VoteApprove
	if r.Float64() >= ag.Approvals.Approve {
		vote = VoteDeny
	}
	z := r.NormFloat64()
	med := ag.Approvals.WaitMedian.Seconds()
	sigma := 0.0
	if p95 := ag.Approvals.WaitP95.Seconds(); p95 > med && med > 0 {
		sigma = math.Log(p95/med) / 1.6448536269514722
	}
	secs := math.Round(med * math.Exp(sigma*z))
	secs = max(30, min(secs, 30*24*3600))
	return ApprovalScript{Approver: who, Vote: vote, Wait: time.Duration(secs) * time.Second}
}

// ---- incidents ----

func (g *gen) agentIndex(name string) int {
	return slices.IndexFunc(g.sc.Agents, func(a Agent) bool { return a.Name == name })
}

func specFor(ag *Agent, tool string) ToolSpec {
	for _, t := range ag.Tools {
		if t.Tool == tool {
			return t
		}
	}
	return ToolSpec{Tool: tool, Weight: 1}
}

func (g *gen) incident(j int, inc Incident) {
	r := newStream(g.seed, streamIncident, uint64(j))
	ai := g.agentIndex(inc.Agent)
	ag := &g.sc.Agents[ai]
	z := zipfFor(r, ag)
	src := len(g.sc.Agents) + j
	newRefBase := r.IntN(9000)
	newRefs := 0
	for _, d := range inc.Days {
		base := dayStart(d) + g.spanOffset(int64(inc.Hour)*hourMS)
		at := func() int64 { return base + r.Int64N(hourMS-1000) }
		n := inc.Count
		if inc.Kind == KindSpike {
			n = max(1, int(math.Round((inc.Factor-1)*float64(ag.PerDay)*hourShare(ag, inc.Hour))))
		}
		if inc.Kind == KindNewUserRefs {
			// A burst of new on-behalf-of users is the agent's usual calls in that hour acting for
			// people it never served: the calls already there get the new refs first, and only the
			// shortfall is added. (Adding all of them would also be a volume spike, a second signal
			// the scenario doesn't mean to send.)
			for _, dr := range g.drafts {
				if n == 0 {
					break
				}
				if dr.src == ai && dr.off >= base && dr.off < base+hourMS && dr.a.Incident == "" {
					dr.a.UserRef = fmt.Sprintf("%sn%04d", ag.UserRefs.Prefix, 1000+(newRefs*7919+newRefBase)%9000)
					dr.a.Incident = inc.Kind
					newRefs++
					n--
				}
			}
		}
		for range n {
			var a PlannedAction
			switch inc.Kind {
			case KindMismatch:
				a = g.mismatch(r, z, ag, inc, d)
			case KindFirstUse:
				a = g.action(r, z, ag, ToolSpec{Tool: inc.Tool, Weight: 1}, d)
				a.Outcome = Outcome{Exec: ExecSucceeded, Level: "L0", Evidence: EvidenceClean, LatencyMS: a.Outcome.LatencyMS}
			case KindSpike:
				a = g.action(r, z, ag, specFor(ag, inc.Tool), d)
			case KindOffHours:
				a = g.action(r, z, ag, pickTool(r, ag), d)
			case KindNewUserRefs:
				a = g.action(r, z, ag, pickTool(r, ag), d)
				a.UserRef = fmt.Sprintf("%sn%04d", ag.UserRefs.Prefix, 1000+(newRefs*7919+newRefBase)%9000)
				newRefs++
			}
			a.Incident = inc.Kind
			g.add(at(), src, a)
		}
	}
}

func (g *gen) mismatch(r *rand.Rand, z *rand.Zipf, ag *Agent, inc Incident, day int) PlannedAction {
	var sql string
	switch inc.Table {
	case "public.inventory":
		sql = fmt.Sprintf("UPDATE public.inventory SET reserved = reserved - 1 WHERE sku = 'SKU-%04d'", 1+r.IntN(500))
	case "public.orders":
		sql = fmt.Sprintf("UPDATE public.orders SET status = 'shipped' WHERE id = %d", 1000+r.IntN(9000))
	default:
		sql = fmt.Sprintf("UPDATE %s SET status = 'done' WHERE id = %d", inc.Table, 1000+r.IntN(9000))
	}
	args := map[string]any{"sql": sql}
	a := PlannedAction{Day: day, Agent: ag.Name, Tool: inc.Tool, ConnectorType: connectorOf(inc.Tool),
		SideEffect: sideEffect(inc.Tool, args), Args: compactJSON(args), UserRef: poolRef(ag.UserRefs, z)}
	a.Outcome = Outcome{Exec: ExecSucceeded, Level: "L2", Evidence: EvidenceContradicts, LatencyMS: latency("postgres", r.NormFloat64()),
		Detail: map[string]any{"table": inc.Table, "expected_rows": 1, "observed_rows": 301}}
	a.Approval = g.approval(r, ag)
	return a
}

// ---- budget pressure ----

// pressure adds refunds on each listed day until the org-local calendar day that holds most of
// that day's span counts target × the budget's max_spend, as the enforce policy would reserve it.
func (g *gen) pressure(j int, inc Incident) error {
	c, err := g.compiled()
	if err != nil {
		return err
	}
	bi := slices.IndexFunc(c.Budgets, func(b policy.Budget) bool { return b.ID == inc.Budget })
	if bi < 0 || c.Budgets[bi].MaxSpend == nil {
		return &Error{File: g.sc.Source, Line: inc.Line, Msg: fmt.Sprintf("incidents[%d].budget %q is not a max_spend budget in %s; name one.", j, inc.Budget, g.sc.Policies.EnforceFile)}
	}
	b := c.Budgets[bi]
	goal := int64(math.Round(inc.Target * float64(b.MaxSpend.Amount)))
	r := newStream(g.seed, streamIncident, uint64(j))
	ag := &g.sc.Agents[g.agentIndex(inc.Agent)]
	z := zipfFor(r, ag)
	src := len(g.sc.Agents) + j
	med, sigma, cur := 2400.0, 0.8, b.MaxSpend.Currency
	for _, a := range specFor(ag, refundTool).Args {
		if a.Key == "amount" && a.Lognormal != nil {
			med, sigma = a.Lognormal.Median, a.Lognormal.Sigma
		}
	}
	for _, d := range inc.Days {
		lo, hi, ws := g.pressureRange(d)
		remaining := goal - g.budgetTotal(c, b, ws, ag.Name)
		fails := 0
		for remaining >= minRefund {
			amt := min(lognormalInt(r, med, sigma), remaining)
			args := map[string]any{"amount": max(amt, minRefund), "currency": cur}
			a := PlannedAction{Day: d, Agent: ag.Name, Tool: refundTool, ConnectorType: "stripe", SideEffect: SideWrite,
				Args: compactJSON(args), UserRef: poolRef(ag.UserRefs, z), Incident: KindBudgetPressure}
			a.Outcome = outcome(r, ag, refundTool, args)
			a.Approval = g.approval(r, ag)
			off := g.placeIn(r, ag, d, lo, hi)
			dr := &draft{off: off, a: a}
			g.evaluate(c, b, dr)
			if dr.counted <= 0 || !dr.window.Equal(ws) {
				if fails++; fails > 200 {
					return &Error{File: g.sc.Source, Line: inc.Line, Msg: fmt.Sprintf(
						"incidents[%d] can't add refunds that %s counts in budget %q; check that the policy allows refunds by %s.",
						j, g.sc.Policies.EnforceFile, b.ID, ag.Name)}
				}
				continue
			}
			fails = 0
			added := g.add(off, src, a)
			added.evaluated, added.counted, added.window = true, dr.counted, dr.window
			remaining -= dr.counted
		}
	}
	return nil
}

func (g *gen) compiled() (*policy.Compiled, error) {
	if g.enforce != nil {
		return g.enforce, nil
	}
	c := g.sc.enforce
	if c == nil {
		var diags []policy.Diagnostic
		if c, diags = policy.Compile(g.sc.Policies.Enforce, policy.Options{File: g.sc.Policies.EnforceFile}); c == nil {
			msg := "it has errors"
			if len(diags) > 0 {
				msg = diags[0].Message
			}
			return nil, fmt.Errorf("the enforce policy %s does not compile (%s); run deedglass policy lint on it", g.sc.Policies.EnforceFile, strings.TrimSuffix(msg, "."))
		}
	}
	g.enforce = c
	return c, nil
}

// pressureRange is the part of day d's span on the org-local calendar day that holds most of the
// span, as offsets, with that calendar day's budget window start.
func (g *gen) pressureRange(d int) (lo, hi int64, ws time.Time) {
	start := g.w.Start.Add(time.Duration(dayStart(d)) * time.Millisecond)
	mid := start.Add(12*time.Hour - time.Millisecond)
	ws = policy.WindowStart(policy.WindowDay, mid, g.loc)
	we := policy.WindowEnd(policy.WindowDay, mid, g.loc)
	lo = max(dayStart(d), ws.Sub(g.w.Start).Milliseconds())
	hi = min(dayStart(d)+dayMS, we.Sub(g.w.Start).Milliseconds())
	return lo, hi, ws.UTC()
}

// placeIn draws a time inside the agent's hours that falls in [lo, hi), or any time there.
func (g *gen) placeIn(r *rand.Rand, ag *Agent, d int, lo, hi int64) int64 {
	for range 64 {
		off := dayStart(d) + g.spanOffset(bellTOD(r, ag))
		if off >= lo && off < hi {
			return off
		}
	}
	return lo + r.Int64N(hi-lo)
}

func (g *gen) at(off int64) time.Time { return g.w.Start.Add(time.Duration(off) * time.Millisecond) }

// evaluate records what the enforce policy reserves against budget b for the action.
func (g *gen) evaluate(c *policy.Compiled, b policy.Budget, dr *draft) {
	dr.evaluated = true
	a := dr.a
	a.At = g.at(dr.off)
	dec := policy.Evaluate(context.Background(), c, policyInput(g.sc, a), policy.EvalOptions{Deadline: time.Second})
	for _, res := range dec.Reservations {
		if res.Budget == b.ID {
			dr.counted += res.Amount
			dr.window = res.WindowStart.UTC()
		}
	}
}

// budgetTotal is what the plan so far reserves against budget b in the window starting at ws.
func (g *gen) budgetTotal(c *policy.Compiled, b policy.Budget, ws time.Time, agent string) int64 {
	we := policy.WindowEnd(policy.WindowDay, ws, g.loc)
	lo, hi := ws.Sub(g.w.Start).Milliseconds(), we.Sub(g.w.Start).Milliseconds()
	var total int64
	for _, dr := range g.drafts {
		if dr.off < lo || dr.off >= hi || (b.Per == "agent" && dr.a.Agent != agent) {
			continue
		}
		if !b.Match.Match(policy.ToolFacts{Name: dr.a.Tool, Connector: dr.a.ConnectorType, ConnectorType: dr.a.ConnectorType, SideEffect: dr.a.SideEffect},
			policy.AgentFacts{ID: policy.LocalAgentID, Name: dr.a.Agent, Labels: map[string]string{}, Status: policy.StatusActive}) {
			continue
		}
		if !dr.evaluated {
			g.evaluate(c, b, dr)
		}
		if dr.window.Equal(ws.UTC()) {
			total += dr.counted
		}
	}
	return total
}

// policyInput is the policy input the Gate would build for a planned action (no usage).
func policyInput(sc *Scenario, a PlannedAction) policy.Input {
	_, op, _ := strings.Cut(a.Tool, ".")
	in := policy.Input{
		V:     policy.InputVersion,
		Org:   policy.OrgInput{OrgFacts: policy.OrgFacts{ID: policy.LocalOrgID, Timezone: sc.Org.Timezone}},
		Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{ID: policy.LocalAgentID, Name: a.Agent, Labels: map[string]string{}, Status: policy.StatusActive}},
		User:  policy.UserFacts{Ref: a.UserRef},
		Tool: policy.ToolInput{ToolFacts: policy.ToolFacts{Name: a.Tool, Connector: a.ConnectorType, ConnectorType: a.ConnectorType,
			SideEffect: a.SideEffect, Type: a.ConnectorType, Op: op}},
		Args: a.Args,
		Time: a.At,
	}
	if a.ConnectorType == "postgres" {
		in.Tool.DefaultSchema = "public"
	}
	return in
}

// ---- ordering and the digest ----

func (g *gen) finish() ([]PlannedAction, error) {
	slices.SortFunc(g.drafts, func(x, y *draft) int {
		if x.off != y.off {
			if x.off < y.off {
				return -1
			}
			return 1
		}
		if x.src != y.src {
			return x.src - y.src
		}
		return x.idx - y.idx
	})
	n := len(g.drafts)
	for i := 1; i < n; i++ { // distinct times: a tie moves forward by a millisecond
		if g.drafts[i].off <= g.drafts[i-1].off {
			g.drafts[i].off = g.drafts[i-1].off + 1
		}
	}
	if limit := int64(g.w.Days) * dayMS; n > 0 && g.drafts[n-1].off >= limit { // keep the tail inside the window
		g.drafts[n-1].off = limit - 1
		for i := n - 2; i >= 0 && g.drafts[i].off >= g.drafts[i+1].off; i-- {
			g.drafts[i].off = g.drafts[i+1].off - 1
		}
	}
	out := make([]PlannedAction, n)
	for i, d := range g.drafts {
		if d.off < 0 || (i > 0 && d.off <= g.drafts[i-1].off) {
			return nil, fmt.Errorf("the plan has more actions than milliseconds in the window; simulate more days or less volume")
		}
		a := d.a
		a.Seq = i + 1
		a.At = g.at(d.off).UTC()
		out[i] = a
	}
	return out, nil
}

type canonApproval struct {
	Approver string `json:"approver"`
	Vote     string `json:"vote"`
	WaitMS   int64  `json:"wait_ms"`
}

type canonAction struct {
	Seq           int             `json:"seq"`
	At            string          `json:"at"`
	Day           int             `json:"day"`
	Agent         string          `json:"agent"`
	Tool          string          `json:"tool"`
	ConnectorType string          `json:"connector_type"`
	SideEffect    string          `json:"side_effect"`
	Args          json.RawMessage `json:"args"`
	UserRef       string          `json:"user_ref"`
	Outcome       Outcome         `json:"outcome"`
	Approval      canonApproval   `json:"approval"`
	Incident      string          `json:"incident"`
}

// PlanDigest is "sha256:" + hex(sha256) over one canonical JSON line per action (At in RFC 3339
// with milliseconds, in UTC; the wait in milliseconds).
func PlanDigest(p []PlannedAction) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	enc.SetEscapeHTML(false)
	for _, a := range p {
		args := a.Args
		if len(args) == 0 {
			args = json.RawMessage("null")
		}
		_ = enc.Encode(canonAction{Seq: a.Seq, At: a.At.UTC().Format("2006-01-02T15:04:05.000Z07:00"), Day: a.Day, Agent: a.Agent,
			Tool: a.Tool, ConnectorType: a.ConnectorType, SideEffect: a.SideEffect, Args: args, UserRef: a.UserRef, Outcome: a.Outcome,
			Approval: canonApproval{Approver: a.Approval.Approver, Vote: a.Approval.Vote, WaitMS: a.Approval.Wait.Milliseconds()},
			Incident: a.Incident})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
