package learn

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/policy"
)

// The six generators (S28 §3.2). Each is a pure function of Evidence: it proposes, it never
// stores. A proposal that loosens anything is narrow by construction (the narrowest pattern every
// approved action shares) and carries a Guard that the pipeline runs on its own replay.

// Suggestion kinds.
const (
	KindLoosen       = "loosen"
	KindTighten      = "tighten"
	KindBudget       = "budget"
	KindExplicitRule = "explicit_rule"
	KindAdvisory     = "advisory"
)

// Generators.
const (
	GenLoosenApprovalRate = "loosen.approval_rate"
	GenTightenMismatch    = "tighten.mismatch"
	GenTightenVerifyLevel = "tighten.verify_level"
	GenBudgetNearLimit    = "budget.near_limit"
	GenExplicitDefaultHit = "explicit_rule.default_hit"
	GenAdvisoryDenyHeavy  = "advisory.deny_heavy"
)

// GeneratorVersion versions the thresholds below (stored with every suggestion).
const GeneratorVersion = "1"

// Thresholds (S28 §3.2).
const (
	LoosenMinDecided      = 30
	LoosenMinRate         = 0.95
	LoosenMaxTables       = 3
	LoosenMaxAgents       = 3
	VerifyMinActions      = 30
	VerifyMinShare        = 0.90
	BudgetNearShare       = 0.90
	BudgetNearDays        = 3
	BudgetLookbackDays    = 7
	DefaultHitMinActions  = 10
	DefaultHitMinDays     = 2
	DenyHeavyMinActions   = 50
	DenyHeavyMinShare     = 0.30
	DenyHeavyLookbackDays = 7
)

// Number is one number a suggestion's text uses: the phrasing validator only lets a reworded
// sentence use these (normalized: 401, 97.3%, 21 minutes, $48.00).
type Number struct {
	Key   string  `json:"key"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"` // count | seconds | rate | days | minor:<currency>
}

// Proposal is one generator's output.
type Proposal struct {
	Kind, Generator, TargetRuleID string
	// The template text with {placeholders} for every org-chosen name; Refs resolves them.
	TemplateTitle, TemplateRationale, TemplateRisk string
	Refs                                           *Refs
	Ops                                            []policy.PatchOp // empty for an advisory
	Pattern                                        map[string]any   // what the change covers: the fingerprint and the UI
	Guard                                          func(r *ReplayResult) (bool, string)
	Numbers                                        []Number
	Evidence                                       map[string]any // the suggestion's evidence object (the pipeline adds the window)
	MainCount                                      int64          // the count a rejection is remembered by (30 days unless it doubles)
	Tool, ConnectorType, Before, After             string         // product-defined facts for phrasing
}

// Title, Rationale and RiskNote are the template text with the names put back.
func (p *Proposal) Title() string     { return p.Refs.Resolve(p.TemplateTitle) }
func (p *Proposal) Rationale() string { return p.Refs.Resolve(p.TemplateRationale) }
func (p *Proposal) RiskNote() string  { return p.Refs.Resolve(p.TemplateRisk) }

// Dropped is a proposal that didn't survive, with the reason (the learn_run object lists them).
type Dropped struct {
	Generator string `json:"generator"`
	Target    string `json:"target,omitempty"`
	Reason    string `json:"reason"`
}

// Generator is one generator.
type Generator func(ev *Evidence) ([]Proposal, []Dropped)

// Generators lists the six in the order they run.
var Generators = []struct {
	Name string
	Fn   Generator
}{
	{GenTightenMismatch, GenTightenMismatchFn},
	{GenTightenVerifyLevel, GenTightenVerifyLevelFn},
	{GenLoosenApprovalRate, GenLoosenFn},
	{GenBudgetNearLimit, GenBudgetFn},
	{GenExplicitDefaultHit, GenExplicitRuleFn},
	{GenAdvisoryDenyHeavy, GenDenyHeavyFn},
}

// --- placeholders -----------------------------------------------------------------------------------

// Refs maps placeholders to the org-chosen names they stand for. The phrasing client only ever
// sees the placeholders; the reply is resolved here, locally.
type Refs struct {
	Values map[string]string // "{agent_1}" → "ops-agent"
	Kinds  map[string]string // "{agent_1}" → "agent"
	byKey  map[string]string
	count  map[string]int
}

// NewRefs returns an empty set.
func NewRefs() *Refs {
	return &Refs{Values: map[string]string{}, Kinds: map[string]string{}, byKey: map[string]string{}, count: map[string]int{}}
}

// Ref returns the placeholder for value (the same one for the same kind and value).
func (r *Refs) Ref(kind, value string) string {
	if p, ok := r.byKey[kind+"\x00"+value]; ok {
		return p
	}
	r.count[kind]++
	p := "{" + kind + "_" + strconv.Itoa(r.count[kind]) + "}"
	r.byKey[kind+"\x00"+value] = p
	r.Values[p], r.Kinds[p] = value, kind
	return p
}

var placeholderRE = regexp.MustCompile(`\{[a-z]+_[0-9]+\}`)

// Resolve puts the names back.
func (r *Refs) Resolve(s string) string {
	if r == nil {
		return s
	}
	return placeholderRE.ReplaceAllStringFunc(s, func(p string) string {
		if v, ok := r.Values[p]; ok {
			return v
		}
		return p
	})
}

// productConnectors are the connector types Deedglass ships: a tool named after one of them is a
// product name, not an org's choice.
var productConnectors = map[string]bool{"postgres": true, "stripe": true, "gmail": true, "notion": true, "slack": true,
	"github": true, "rest": true, "fake": true, "mcp_http": true}

// toolRef is the tool's own name when it is a product tool, else a placeholder.
func (r *Refs) toolRef(tool string) string {
	if c, _, ok := strings.Cut(tool, "."); ok && productConnectors[c] {
		return tool
	}
	return r.Ref("tool", tool)
}

func (r *Refs) agentsRef(names []string) string {
	refs := make([]string, len(names))
	for i, n := range names {
		refs[i] = r.Ref("agent", n)
	}
	return joinAnd(refs)
}

func (r *Refs) tablesRef(tables []string) string {
	refs := make([]string, len(tables))
	for i, t := range tables {
		refs[i] = r.Ref("table", t)
	}
	return joinAnd(refs)
}

// --- shared helpers -----------------------------------------------------------------------------------

// rowsOf returns the base version's rows decided by rule id with decision.
func (ev *Evidence) rowsOf(ruleID, decision string) []EvidenceRow {
	var out []EvidenceRow
	for i := range ev.Rows {
		x := &ev.Rows[i]
		if x.VersionID == ev.BaseVersionID && x.Source == policy.SourceRule && x.RuleID == ruleID && x.Decision == decision {
			out = append(out, *x)
		}
	}
	return out
}

func distinctSorted(xs []string) []string {
	m := map[string]bool{}
	for _, x := range xs {
		if x != "" {
			m[x] = true
		}
	}
	return sortedKeys(m)
}

func celString(s string) string { return strconv.Quote(s) }

func celStringList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = celString(x)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// niceFloor rounds n down to a multiple of 5 × 10^(d−2), d being n's digit count (4,870 → 4,500,
// 1,260 → 1,000, 87 → 85, 9 → 9): a round limit that stays close to the evidence.
func niceFloor(n int64) int64 {
	if n <= 0 {
		return 0
	}
	step := int64(1)
	for x := n; x >= 100; x /= 10 {
		step *= 10
	}
	if n >= 10 {
		step *= 5
		if n < 100 {
			step = 5
		}
	}
	return max(n-n%step, 1)
}

// niceCeil rounds n up to 1, 2, 2.5 or 5 × 10^k.
func niceCeil(n int64) int64 {
	if n <= 1 {
		return 1
	}
	for p := int64(1); p > 0 && p <= math.MaxInt64/10; p *= 10 {
		for _, m := range []float64{1, 2, 2.5, 5} {
			v := int64(m * float64(p))
			if float64(v) == m*float64(p) && v >= n {
				return v
			}
		}
	}
	return n
}

func kindNoun(kind string) string {
	switch kind {
	case "update":
		return "updates"
	case "insert":
		return "inserts"
	case "delete":
		return "deletes"
	case "select":
		return "reads"
	}
	return kind + " statements"
}

func kindVerb(kind string) string {
	switch kind {
	case "update":
		return "update"
	case "insert":
		return "insert into"
	case "delete":
		return "delete from"
	}
	return "run " + kind + " statements on"
}

func idPart(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	dash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func clipRuleID(s string) string {
	if len(s) > 56 {
		s = strings.TrimRight(s[:56], "-")
	}
	return s
}

func (ev *Evidence) dateOf(t time.Time) string { return t.In(ev.Loc).Format("2006-01-02") }

// sampleIDs is the first n action ids.
func sampleIDs(rows []EvidenceRow, n int) []string {
	out := []string{}
	for i := 0; i < len(rows) && len(out) < n; i++ {
		out = append(out, rows[i].ActionID)
	}
	return out
}

// matchOf copies a rule's match as written (globs, names, ids), with tool narrowed to one tool.
// ok is false when the match uses labels, which a generated rule doesn't write.
func matchOf(r *policy.Rule, tool string) (policy.MatchSpec, bool) {
	var m policy.MatchSpec
	if len(r.Match.Labels) > 0 {
		return m, false
	}
	m.Tool = []string{tool}
	for _, g := range r.Match.ConnectorType {
		m.ConnectorType = append(m.ConnectorType, g.String())
	}
	m.SideEffect = slices.Clone(r.Match.SideEffect)
	for _, g := range r.Match.AgentNames {
		m.Agent = append(m.Agent, g.String())
	}
	m.Agent = append(m.Agent, r.Match.AgentIDs...)
	return m, true
}

// --- loosen.approval_rate -------------------------------------------------------------------------------

// GenLoosenFn proposes an allow rule before a require_approval rule people approve every time.
func GenLoosenFn(ev *Evidence) ([]Proposal, []Dropped) {
	var out []Proposal
	var dropped []Dropped
	for _, re := range ev.Rules {
		if re.Decision != string(policy.RequireApproval) {
			continue
		}
		a := re.Approvals
		decided := a.Approved + a.Denied
		if decided < LoosenMinDecided || a.Denied > 0 || float64(a.Approved)/float64(decided) < LoosenMinRate {
			continue
		}
		rule := ev.rule(re.RuleID)
		rows := ev.rowsOf(re.RuleID, string(policy.RequireApproval))
		byTool := map[string][]EvidenceRow{}
		for _, x := range rows {
			byTool[x.Tool] = append(byTool[x.Tool], x)
		}
		tools := make([]string, 0, len(byTool))
		for t := range byTool {
			tools = append(tools, t)
		}
		sort.Strings(tools)
		for _, tool := range tools {
			trows := byTool[tool]
			var approved []EvidenceRow
			for _, x := range trows {
				if x.Approval == ApprovalApproved {
					approved = append(approved, x)
				}
			}
			if len(approved) < LoosenMinDecided {
				continue
			}
			p, why := loosenProposal(ev, rule, re, tool, trows, approved, len(tools) > 1)
			if p == nil {
				dropped = append(dropped, Dropped{Generator: GenLoosenApprovalRate, Target: re.RuleID, Reason: why})
				continue
			}
			out = append(out, *p)
		}
	}
	return out, dropped
}

func loosenProposal(ev *Evidence, rule *policy.Rule, re RuleEvidence, tool string, trows, approved []EvidenceRow, manyTools bool) (*Proposal, string) {
	refs := NewRefs()
	var conds []string
	pattern := map[string]any{"tool": tool}
	var what string // "updates to {table_1}" or "{tool} calls"
	risk := ""
	facts := make([]*ActionFacts, len(approved))
	for i, x := range approved {
		facts[i] = ev.Facts[x.ActionID]
		if facts[i] == nil {
			return nil, "the inputs of some approved actions were pruned, so no pattern can be checked"
		}
	}
	withSQL := 0
	for _, f := range facts {
		if f.SQL != nil {
			withSQL++
		}
	}
	agents := distinctSorted(func() []string {
		var n []string
		for _, x := range approved {
			n = append(n, x.AgentName)
		}
		return n
	}())
	agentText := ""
	switch {
	case withSQL == len(facts):
		kinds := map[string]bool{}
		tables := map[string]bool{}
		complete := true
		for _, f := range facts {
			s := f.SQL
			kinds[s.Kind] = true
			for _, t := range s.Tables {
				tables[t] = true
			}
			if s.Statements != 1 || s.Multi || !s.HasWhere || s.UnboundedWrite || len(s.DangerousFunctions) > 0 || len(s.Tables) == 0 {
				return nil, "not every approved statement is a single statement with a WHERE clause on named tables"
			}
			complete = complete && s.TablesComplete
		}
		if len(kinds) != 1 {
			return nil, "the approved statements are of more than one kind"
		}
		ts := sortedKeys(tables)
		if len(ts) > LoosenMaxTables {
			return nil, fmt.Sprintf("the approved statements touch %d tables; a suggestion covers at most %d", len(ts), LoosenMaxTables)
		}
		kind := sortedKeys(kinds)[0]
		conds = append(conds, "sql.kind == "+celString(kind))
		if len(ts) == 1 {
			conds = append(conds, "sql.tables == "+celStringList(ts))
		} else {
			conds = append(conds, "size(sql.tables) > 0 && sql.tables.all(t, t in "+celStringList(ts)+")")
		}
		conds = append(conds, "sql.statements == 1", "sql.has_where", "!sql.unbounded_write", "size(sql.dangerous_functions) == 0")
		if complete {
			conds = append(conds, "sql.tables_complete")
		}
		pattern["sql.kind"], pattern["sql.tables"], pattern["sql.statements"] = kind, ts, 1
		what = kindNoun(kind) + " to " + refs.tablesRef(ts)
		agentText = refs.agentsRef(agents)
		if len(agents) > LoosenMaxAgents {
			agentText = "its agents"
		}
		risk = agentText + " could " + kindVerb(kind) + " single rows of " + refs.tablesRef(ts) +
			" without a person looking. Other statements, and writes without a WHERE clause, still go to approval."
	case withSQL == 0:
		amounts := []int64{}
		cur := ""
		allAmounts := true
		for i, f := range facts {
			if f.Amount == nil {
				allAmounts = false
				break
			}
			if i == 0 {
				cur = f.Currency
			} else if f.Currency != cur {
				allAmounts = false
				break
			}
			amounts = append(amounts, *f.Amount)
		}
		if len(agents) <= LoosenMaxAgents {
			agentText = refs.agentsRef(agents)
		} else {
			agentText = "its agents"
		}
		toolText := refs.toolRef(tool)
		if allAmounts && len(amounts) > 0 {
			n := niceFloor(percentile(amounts, 95))
			if n <= 0 {
				return nil, "the approved amounts are zero"
			}
			covered := 0
			for _, v := range amounts {
				if v <= n {
					covered++
				}
			}
			conds = append(conds, "has(args.amount) && type(args.amount) == int && args.amount <= "+strconv.FormatInt(n, 10))
			if cur != "" {
				conds = append(conds, "has(args.currency) && args.currency == "+celString(cur))
			}
			pattern["args.amount.max"], pattern["args.currency"] = n, cur
			money := strconv.FormatInt(n, 10)
			if cur != "" {
				money = policy.FormatMoney(n, cur)
			}
			what = "calls to " + toolText + " up to " + money
			pattern["covers"] = map[string]any{"approved": covered, "of": len(amounts)}
			risk = agentText + " could run " + toolText + " up to " + money + " without a person looking. Larger amounts still go to approval."
		} else {
			what = "calls to " + toolText
			risk = agentText + " could run " + toolText + " without a person looking."
		}
	default:
		return nil, "some approved calls carried SQL and some didn't"
	}
	match := policy.MatchSpec{Tool: []string{tool}}
	if len(agents) <= LoosenMaxAgents {
		match.Agent = agents
		pattern["agents"] = agents
	}
	// The window's count: people approved N of M (M = what concluded: approved, expired, cancelled).
	var concluded, expired, pending int64
	var waits []float64
	var approvers = map[string]bool{}
	for _, x := range trows {
		switch x.Approval {
		case ApprovalApproved, ApprovalExpired, ApprovalCancelled:
			concluded++
			if x.Approval == ApprovalExpired {
				expired++
			}
		case "", ApprovalPending:
			pending++
		}
		if x.Approval == ApprovalApproved && x.WaitSeconds >= 0 {
			waits = append(waits, x.WaitSeconds)
		}
		for _, p := range x.Approvers {
			approvers[p] = true
		}
	}
	nApproved := int64(len(approved))
	wait := median(waits)
	base := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(rule.ID, "approve-"), "require-approval-"), "approval-")
	id := "allow-" + base
	if manyTools {
		id += "-" + idPart(tool)
	}
	id = policy.UniqueRuleID(ev.Base, clipRuleID(id))
	from, to := ev.dateOf(ev.Window.From), ev.dateOf(ev.Window.To)
	spec := policy.RuleSpec{
		ID:          id,
		Description: fmt.Sprintf("People approved %s of %s of these between %s and %s.", fmtCount(nApproved), fmtCount(concluded), from, to),
		Match:       match,
		When:        strings.Join(conds, " && "),
		Decision:    policy.Allow,
		Verify:      ev.effectiveVerify(rule),
	}
	ruleRef := refs.Ref("rule", rule.ID)
	byAgents := ""
	if agentText != "" && agentText != "its agents" {
		byAgents = " by " + agentText
	}
	p := &Proposal{
		Kind: KindLoosen, Generator: GenLoosenApprovalRate, TargetRuleID: rule.ID, Refs: refs,
		TemplateTitle: "Allow " + what + " without approval",
		TemplateRationale: fmt.Sprintf("People approved %s of %s %s%s in the last %d days and denied none. Each waited a median of %s.",
			fmtCount(nApproved), fmtCount(concluded), what, byAgents, ev.days(), fmtWait(wait)),
		TemplateRisk: risk,
		Ops:          []policy.PatchOp{policy.InsertRuleBefore{RuleID: rule.ID, Rule: spec}},
		Pattern:      pattern,
		Numbers: []Number{{Key: "approved", Value: float64(nApproved), Unit: "count"}, {Key: "concluded", Value: float64(concluded), Unit: "count"},
			{Key: "median_wait", Value: wait, Unit: "seconds"}, {Key: "window_days", Value: float64(ev.days()), Unit: "days"}},
		Evidence: map[string]any{
			"approvals":           map[string]any{"requested": len(trows), "approved": nApproved, "denied": 0, "expired": expired, "pending": pending},
			"median_wait_seconds": math.Round(wait), "approvers": len(approvers), "agents": agents, "tool": tool,
			"rule": rule.ID, "pattern": pattern, "sample_actions": sampleIDs(approved, 5),
		},
		MainCount: nApproved, Tool: tool, Before: string(policy.RequireApproval), After: string(policy.Allow),
	}
	_ = ruleRef
	if c, ok := pattern["covers"].(map[string]any); ok {
		p.Numbers = append(p.Numbers, Number{Key: "covered", Value: float64(c["approved"].(int)), Unit: "count"})
		p.TemplateRationale += fmt.Sprintf(" The limit covers %s of the %s approved amounts.", fmtCount(int64(c["approved"].(int))), fmtCount(int64(c["of"].(int))))
	}
	if n, ok := pattern["args.amount.max"].(int64); ok {
		p.Numbers = append(p.Numbers, Number{Key: "limit", Value: float64(n), Unit: "minor:" + pattern["args.currency"].(string)})
	}
	allowedMax := nApproved + expired + pending
	p.Guard = func(r *ReplayResult) (bool, string) {
		var toAllow int64
		for _, t := range r.Transitions {
			if t.To != string(policy.Allow) {
				continue
			}
			if t.From == string(policy.Deny) {
				return false, fmt.Sprintf("its replay would allow %s %s the policy denies today", fmtCount(t.Count), plural(int(t.Count), "action", "actions"))
			}
			if t.HumanOutcome != nil && t.HumanOutcome.Denied > 0 {
				return false, fmt.Sprintf("its replay would allow %s %s a person denied", fmtCount(t.HumanOutcome.Denied), plural(int(t.HumanOutcome.Denied), "action", "actions"))
			}
			if t.HumanOutcome != nil && t.HumanOutcome.NotRequested > 0 {
				return false, fmt.Sprintf("its replay would allow %s %s that never went to a person", fmtCount(t.HumanOutcome.NotRequested), plural(int(t.HumanOutcome.NotRequested), "action", "actions"))
			}
			toAllow += t.Count
		}
		if toAllow > allowedMax {
			return false, fmt.Sprintf("its replay would allow %s actions, more than people approved, let expire or left waiting (%s)", fmtCount(toAllow), fmtCount(allowedMax))
		}
		if toAllow == 0 {
			return false, "its replay changes nothing"
		}
		return true, ""
	}
	return p, ""
}

// --- tighten.mismatch -------------------------------------------------------------------------------

// GenTightenMismatchFn sends a tool to approval when an allow rule let through a MISMATCH.
func GenTightenMismatchFn(ev *Evidence) ([]Proposal, []Dropped) {
	type key struct{ rule, tool string }
	groups := map[key][]EvidenceRow{}
	for _, x := range ev.Rows {
		if x.VersionID == ev.BaseVersionID && x.Source == policy.SourceRule && x.Decision == string(policy.Allow) && x.Verdict == "MISMATCH" {
			groups[key{x.RuleID, x.Tool}] = append(groups[key{x.RuleID, x.Tool}], x)
		}
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int { return strings.Compare(a.rule+"\x00"+a.tool, b.rule+"\x00"+b.tool) })
	var out []Proposal
	var dropped []Dropped
	for _, k := range keys {
		rule := ev.rule(k.rule)
		if rule == nil {
			continue
		}
		rows := groups[k]
		match, ok := matchOf(rule, k.tool)
		if !ok {
			dropped = append(dropped, Dropped{Generator: GenTightenMismatch, Target: k.rule, Reason: "the rule matches on labels, which a suggested rule doesn't copy"})
			continue
		}
		refs := NewRefs()
		var conds []string
		if w := strings.Join(strings.Fields(rule.WhenCanonical), " "); w != "" && w != "true" {
			conds = append(conds, "("+w+")")
		}
		pattern := map[string]any{"tool": k.tool, "rule": k.rule}
		what := "calls to " + refs.toolRef(k.tool)
		// Narrow to the tables and kind every mismatched statement shared.
		var tables []string
		kind := ""
		same := true
		for i, x := range rows {
			f := ev.Facts[x.ActionID]
			if f == nil || f.SQL == nil || len(f.SQL.Tables) == 0 {
				same = false
				break
			}
			ts := slices.Clone(f.SQL.Tables)
			sort.Strings(ts)
			if i == 0 {
				tables, kind = ts, f.SQL.Kind
			} else if !slices.Equal(ts, tables) || f.SQL.Kind != kind {
				same = false
				break
			}
		}
		if same && len(tables) > 0 && len(tables) <= LoosenMaxTables {
			conds = append(conds, "sql.kind == "+celString(kind))
			if len(tables) == 1 {
				conds = append(conds, celString(tables[0])+" in sql.tables")
			} else {
				for _, t := range tables {
					conds = append(conds, celString(t)+" in sql.tables")
				}
			}
			pattern["sql.kind"], pattern["sql.tables"] = kind, tables
			what = kindNoun(kind) + " to " + refs.tablesRef(tables)
		}
		n := int64(len(rows))
		id := policy.UniqueRuleID(ev.Base, clipRuleID("approve-"+strings.TrimPrefix(rule.ID, "allow-")))
		reasonWhat := "calls to " + k.tool
		if t, ok := pattern["sql.tables"].([]string); ok {
			reasonWhat = kindNoun(kind) + " to " + strings.Join(t, " and ")
		}
		spec := policy.RuleSpec{
			ID:          id,
			Description: fmt.Sprintf("%s %s allowed by %s ended MISMATCH between %s and %s.", fmtCount(n), plural(int(n), "action", "actions"), rule.ID, ev.dateOf(ev.Window.From), ev.dateOf(ev.Window.To)),
			Match:       match,
			When:        strings.Join(conds, " && "),
			Decision:    policy.RequireApproval,
			Reason:      upperFirst(reasonWhat) + " wait for a person after " + fmtCount(n) + " " + plural(int(n), "mismatch", "mismatches") + ". An admin can approve in Console › Approvals.",
			Verify:      ev.effectiveVerify(rule),
		}
		if len(ev.Base.Defaults.Approval.Approvers) == 0 {
			spec.Approval = &policy.ApprovalYAML{Approvers: []string{"role:admin"}, Required: 1, ExpiresIn: "4h"}
		}
		ruleRef := refs.Ref("rule", rule.ID)
		p := Proposal{
			Kind: KindTighten, Generator: GenTightenMismatch, TargetRuleID: rule.ID, Refs: refs,
			TemplateTitle: fmt.Sprintf("Send %s to approval after %s %s", what, fmtCount(n), plural(int(n), "mismatch", "mismatches")),
			TemplateRationale: fmt.Sprintf("%s %s allowed by %s ended MISMATCH in the last %d days: the system of record didn't match what was reported. A person looks first until you remove the rule.",
				fmtCount(n), plural(int(n), "action", "actions"), ruleRef, ev.days()),
			TemplateRisk: upperFirst(what) + " that " + ruleRef + " allows today will wait for a person, so they run later. Other actions under " + ruleRef + " don't change.",
			Ops:          []policy.PatchOp{policy.InsertRuleBefore{RuleID: rule.ID, Rule: spec}},
			Pattern:      pattern,
			Numbers:      []Number{{Key: "mismatches", Value: float64(n), Unit: "count"}, {Key: "window_days", Value: float64(ev.days()), Unit: "days"}},
			Evidence: map[string]any{"mismatches": n, "tool": k.tool, "rule": rule.ID, "agents": distinctSorted(agentNames(rows)),
				"pattern": pattern, "sample_actions": sampleIDs(rows, 5)},
			MainCount: n, Tool: k.tool, Before: string(policy.Allow), After: string(policy.RequireApproval),
			Guard: func(r *ReplayResult) (bool, string) {
				if r.Summary.Changed == 0 {
					return false, "its replay changes nothing"
				}
				for _, t := range r.Transitions {
					if t.To != string(policy.RequireApproval) || t.From == string(policy.Deny) {
						return false, fmt.Sprintf("its replay would change %s %s from %s to %s", fmtCount(t.Count), plural(int(t.Count), "action", "actions"), t.From, t.To)
					}
				}
				return true, ""
			},
		}
		out = append(out, p)
	}
	return out, dropped
}

func agentNames(rows []EvidenceRow) []string {
	out := make([]string, len(rows))
	for i, x := range rows {
		out[i] = x.AgentName
	}
	return out
}

func upperFirst(s string) string {
	if s == "" || s[0] == '{' {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// --- tighten.verify_level -------------------------------------------------------------------------------

var levelName = map[policy.Level]string{policy.L0: "L0 Reported", policy.L1: "L1 Confirmed", policy.L2: "L2 Witnessed"}

func levelRank(l string) int {
	switch l {
	case "L1":
		return 1
	case "L2":
		return 2
	}
	return 0
}

// GenTightenVerifyLevelFn raises an allow rule's verify level when its writes already reach it.
func GenTightenVerifyLevelFn(ev *Evidence) ([]Proposal, []Dropped) {
	var out []Proposal
	for _, re := range ev.Rules {
		if re.Decision != string(policy.Allow) {
			continue
		}
		rule := ev.rule(re.RuleID)
		var witnessed, l1, l2 int64
		for _, x := range ev.rowsOf(re.RuleID, string(policy.Allow)) {
			if x.Verdict == "" || (x.SideEffect != "write" && x.SideEffect != "destructive") {
				continue
			}
			witnessed++
			if x.Verdict == "VERIFIED" {
				if levelRank(x.Level) >= 1 {
					l1++
				}
				if levelRank(x.Level) >= 2 {
					l2++
				}
			}
		}
		if witnessed < VerifyMinActions {
			continue
		}
		have := ev.effectiveVerify(rule)
		var target policy.Level
		var reached int64
		switch {
		case float64(l2)/float64(witnessed) >= VerifyMinShare && levelRank(string(have)) < 2:
			target, reached = policy.L2, l2
		case float64(l1)/float64(witnessed) >= VerifyMinShare && levelRank(string(have)) < 1:
			target, reached = policy.L1, l1
		default:
			continue
		}
		refs := NewRefs()
		ruleRef := refs.Ref("rule", rule.ID)
		share := float64(reached) / float64(witnessed)
		out = append(out, Proposal{
			Kind: KindTighten, Generator: GenTightenVerifyLevel, TargetRuleID: rule.ID, Refs: refs,
			TemplateTitle: fmt.Sprintf("Ask %s for %s", ruleRef, levelName[target]),
			TemplateRationale: fmt.Sprintf("%s of %s writes allowed by %s in the last %d days were verified at %s or above (%s). The rule asks for %s, so a write that misses %s still reads as verified.",
				fmtCount(reached), fmtCount(witnessed), ruleRef, ev.days(), target, fmtPercent(share), have, target),
			TemplateRisk: fmt.Sprintf("Writes under %s that can't be checked at %s will read UNVERIFIABLE instead of verified. Nothing is blocked.", ruleRef, target),
			Ops:          []policy.PatchOp{policy.SetRuleField{RuleID: rule.ID, Key: "verify", Value: target}},
			Pattern:      map[string]any{"rule": rule.ID, "verify": string(target)},
			Numbers: []Number{{Key: "reached", Value: float64(reached), Unit: "count"}, {Key: "witnessed", Value: float64(witnessed), Unit: "count"},
				{Key: "share", Value: share, Unit: "rate"}, {Key: "window_days", Value: float64(ev.days()), Unit: "days"}},
			Evidence:  map[string]any{"rule": rule.ID, "witnessed_writes": witnessed, "reached": reached, "level": string(target), "asks_for": string(have)},
			MainCount: reached, Before: string(have), After: string(target),
		})
	}
	return out, nil
}

// --- budget.near_limit -------------------------------------------------------------------------------

// GenBudgetFn raises a policy budget that ran close to its limit on several days.
func GenBudgetFn(ev *Evidence) ([]Proposal, []Dropped) {
	var out []Proposal
	for _, b := range ev.Budgets {
		var near []BudgetDay
		var used []int64
		maxU := 0.0
		// "The last 7 days" are the 7 most recent days the budget was used (b.Days is sorted by
		// date and lists only days with use), so an idle stretch before the pass doesn't hide them.
		recent := b.Days[max(0, len(b.Days)-BudgetLookbackDays):]
		cut := ""
		if len(recent) > 0 {
			cut = recent[0].Date
		}
		for _, d := range b.Days {
			used = append(used, d.Used)
			if d.Date < cut {
				continue
			}
			if d.Utilisation >= BudgetNearShare {
				near = append(near, d)
			}
			maxU = max(maxU, d.Utilisation)
		}
		if len(near) < BudgetNearDays {
			continue
		}
		limit, unit := int64(0), "count"
		if b.Limit.Calls != nil {
			limit = *b.Limit.Calls
		} else if b.Limit.Amount != nil {
			limit, unit = *b.Limit.Amount, "minor:"+*b.Limit.Currency
		}
		if limit <= 0 {
			continue
		}
		newLimit := niceCeil(int64(math.Ceil(float64(percentile(used, 95)) * 1.2)))
		if newLimit <= limit {
			newLimit = niceCeil(limit + 1)
		}
		fmtLim := func(n int64) string {
			if b.Limit.Calls != nil {
				return fmtCount(n) + " calls"
			}
			return policy.FormatMoney(n, *b.Limit.Currency)
		}
		busiest := near[0]
		for _, d := range near {
			if d.Used > busiest.Used {
				busiest = d
			}
		}
		refs := NewRefs()
		budgetRef := refs.Ref("budget", b.Budget)
		nums := []Number{{Key: "near_days", Value: float64(len(near)), Unit: "count"}, {Key: "lookback_days", Value: BudgetLookbackDays, Unit: "days"},
			{Key: "max_share", Value: maxU, Unit: "rate"}, {Key: "busiest", Value: float64(busiest.Used), Unit: unit},
			{Key: "limit", Value: float64(limit), Unit: unit}, {Key: "new_limit", Value: float64(newLimit), Unit: unit}}
		evidence := map[string]any{"budget": b.Budget, "kind": b.Kind, "limit": b.Limit, "days": b.Days, "near_days": len(near), "max_utilisation": maxU}
		rationale := fmt.Sprintf("%s reached 90%% of its limit on %d of the last %d days, %s at most. The busiest day used %s of %s.",
			budgetRef, len(near), BudgetLookbackDays, fmtPercent(maxU), fmtLim(busiest.Used), fmtLim(limit))
		// An anomaly on a near-limit day means the rise may not be normal use: advice, not a change.
		if eps := ev.budgetAnomalies(b, near); len(eps) > 0 {
			agent := ""
			for _, x := range ev.Rows {
				if x.AgentID == eps[0].AgentID {
					agent = x.AgentName
					break
				}
			}
			a := refs.Ref("agent", agent)
			out = append(out, Proposal{
				Kind: KindAdvisory, Generator: GenBudgetNearLimit, TargetRuleID: "", Refs: refs,
				TemplateTitle:     fmt.Sprintf("Check %s before raising %s", a, budgetRef),
				TemplateRationale: rationale + " Usage rose with an anomaly; check it before raising the budget.",
				TemplateRisk:      fmt.Sprintf("Raising %s now would also raise what an unusual run of %s can spend.", budgetRef, a),
				Pattern:           map[string]any{"budget": b.Budget, "advisory": "anomaly", "kind": eps[0].Kind},
				Numbers:           nums, Evidence: mergeMaps(evidence, map[string]any{"anomaly": eps[0].ID, "anomaly_kind": eps[0].Kind}),
				MainCount: int64(len(near)),
			})
			continue
		}
		if b.Kind == "agent" {
			agentRef := refs.Ref("agent", deref(b.AgentName))
			field := "calls_per_day"
			val := strconv.FormatInt(newLimit, 10)
			if b.Limit.Amount != nil {
				field = "spend_per_day"
				val = fmt.Sprintf(`{"amount": %d, "currency": "%s"}`, newLimit, *b.Limit.Currency)
			}
			out = append(out, Proposal{
				Kind: KindAdvisory, Generator: GenBudgetNearLimit, Refs: refs,
				TemplateTitle:     fmt.Sprintf("Raise %s's daily budget from %s to %s", agentRef, fmtLim(limit), fmtLim(newLimit)),
				TemplateRationale: rationale + " Agent budgets live on the agent, not in the policy.",
				TemplateRisk:      fmt.Sprintf(`Raise it in Console › Agents, or with PATCH /v1/agents/%s and {"budgets": {"%s": %s}}.`, deref(b.Agent), field, val),
				Pattern:           map[string]any{"budget": b.Budget, "agent": deref(b.Agent), "advisory": "agent_budget"},
				Numbers:           nums, Evidence: evidence, MainCount: int64(len(near)),
			})
			continue
		}
		op := policy.SetBudgetLimit{BudgetID: b.Budget}
		if b.Limit.Calls != nil {
			op.MaxCalls = &newLimit
		} else {
			op.MaxSpend = &policy.Money{Amount: newLimit, Currency: *b.Limit.Currency}
		}
		risk := fmt.Sprintf("Agents can spend up to %s a day under %s before Deedglass refuses the next call.", fmtLim(newLimit), budgetRef)
		if b.Limit.Calls != nil {
			risk = fmt.Sprintf("Agents can make up to %s a day under %s before Deedglass refuses the next one.", fmtLim(newLimit), budgetRef)
		}
		out = append(out, Proposal{
			Kind: KindBudget, Generator: GenBudgetNearLimit, Refs: refs,
			TemplateTitle:     fmt.Sprintf("Raise %s from %s to %s", budgetRef, fmtLim(limit), fmtLim(newLimit)),
			TemplateRationale: rationale,
			TemplateRisk:      risk,
			Ops:               []policy.PatchOp{op},
			Pattern:           map[string]any{"budget": b.Budget, "new_limit": newLimit},
			Numbers:           nums, Evidence: evidence, MainCount: int64(len(near)),
		})
	}
	return out, nil
}

func mergeMaps(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// budgetAnomalies are the volume_spike and user_ref_burst episodes, of agents that used the
// budget's tools, that overlap a near-limit day.
func (ev *Evidence) budgetAnomalies(b BudgetUse, near []BudgetDay) []EpisodeRef {
	agents := map[string]bool{}
	var bud *policy.Budget
	for i := range ev.Base.Budgets {
		if ev.Base.Budgets[i].ID == b.Budget {
			bud = &ev.Base.Budgets[i]
		}
	}
	for _, x := range ev.Rows {
		if b.Agent != nil {
			if x.AgentID == *b.Agent {
				agents[x.AgentID] = true
			}
			continue
		}
		if bud != nil && bud.Match.Match(policy.ToolFacts{Name: x.Tool, ConnectorType: x.ConnectorType, SideEffect: x.SideEffect}, policy.AgentFacts{ID: x.AgentID, Name: x.AgentName}) {
			agents[x.AgentID] = true
		}
	}
	days := map[string]bool{}
	for _, d := range near {
		days[d.Date] = true
	}
	var out []EpisodeRef
	for _, e := range ev.Episodes {
		if (e.Kind != "volume_spike" && e.Kind != "user_ref_burst") || !agents[e.AgentID] {
			continue
		}
		for t := e.FirstSeen; !t.After(e.LastSeen.Add(time.Hour)); t = t.Add(time.Hour) {
			if days[ev.dateOf(t)] {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// --- explicit_rule.default_hit -----------------------------------------------------------------------

// GenExplicitRuleFn names a tool the defaults keep deciding, with the same decision.
func GenExplicitRuleFn(ev *Evidence) ([]Proposal, []Dropped) {
	type acc struct {
		n    int64
		days map[string]bool
		rows []EvidenceRow
	}
	by := map[string]*acc{}
	for _, x := range ev.Rows {
		if x.VersionID != ev.BaseVersionID || x.Source != policy.SourceDefault {
			continue
		}
		a := by[x.Tool]
		if a == nil {
			a = &acc{days: map[string]bool{}}
			by[x.Tool] = a
		}
		a.n++
		a.days[ev.dateOf(x.At)] = true
		a.rows = append(a.rows, x)
	}
	tools := make([]string, 0, len(by))
	for t := range by {
		tools = append(tools, t)
	}
	sort.Strings(tools)
	var out []Proposal
	d := ev.Base.Defaults
	for _, tool := range tools {
		a := by[tool]
		if a.n < DefaultHitMinActions || len(a.days) < DefaultHitMinDays {
			continue
		}
		refs := NewRefs()
		toolRef := refs.toolRef(tool)
		spec := policy.RuleSpec{
			ID:          policy.UniqueRuleID(ev.Base, clipRuleID("explicit-"+idPart(tool))),
			Description: fmt.Sprintf("The default decided %s %s times on %d days between %s and %s. Edit this rule to say why.", tool, fmtCount(a.n), len(a.days), ev.dateOf(ev.Window.From), ev.dateOf(ev.Window.To)),
			Match:       policy.MatchSpec{Tool: []string{tool}},
			Decision:    d.Decision,
		}
		switch d.Decision {
		case policy.Deny:
			spec.Reason = tool + " isn't allowed for agents. Edit this reason in Console › Policies to say why."
		case policy.RequireApproval:
			spec.Reason = tool + " waits for a person. An admin can approve in Console › Approvals."
			spec.Verify = d.Verify
		default:
			spec.Verify = d.Verify
		}
		out = append(out, Proposal{
			Kind: KindExplicitRule, Generator: GenExplicitDefaultHit, Refs: refs,
			TemplateTitle:     fmt.Sprintf("Name %s in the policy instead of leaving it to the default", toolRef),
			TemplateRationale: fmt.Sprintf("The default decided %s %s times on %d of the last %d days. A rule with the same decision and its own reason tells the next reader it was on purpose.", toolRef, fmtCount(a.n), len(a.days), ev.days()),
			TemplateRisk:      "None for decisions: the new rule decides exactly what the default decides today. Edit its reason before you promote it.",
			Ops:               []policy.PatchOp{policy.AppendRule{Rule: spec}},
			Pattern:           map[string]any{"tool": tool, "decision": string(d.Decision)},
			Numbers:           []Number{{Key: "default_hits", Value: float64(a.n), Unit: "count"}, {Key: "days", Value: float64(len(a.days)), Unit: "count"}, {Key: "window_days", Value: float64(ev.days()), Unit: "days"}},
			Evidence:          map[string]any{"tool": tool, "default_hits": a.n, "days": len(a.days), "decision": string(d.Decision), "sample_actions": sampleIDs(a.rows, 5)},
			MainCount:         a.n, Tool: tool, Before: string(d.Decision), After: string(d.Decision),
			Guard: func(r *ReplayResult) (bool, string) {
				if r.Summary.Changed > 0 {
					return false, "its replay changes decisions, which an explicit rule never should (please report this)"
				}
				return true, ""
			},
		})
	}
	return out, nil
}

// --- advisory.deny_heavy -------------------------------------------------------------------------------

// GenDenyHeavyFn flags an agent the policy keeps refusing: its instructions may ask for what the
// policy forbids. Advice only, no diff.
func GenDenyHeavyFn(ev *Evidence) ([]Proposal, []Dropped) {
	since := ev.Window.To.Add(-DenyHeavyLookbackDays * 24 * time.Hour)
	type acc struct {
		name           string
		actions, denys int64
		byRule         map[string]int64
	}
	by := map[string]*acc{}
	for _, x := range ev.Rows {
		if x.At.Before(since) {
			continue
		}
		a := by[x.AgentID]
		if a == nil {
			a = &acc{name: x.AgentName, byRule: map[string]int64{}}
			by[x.AgentID] = a
		}
		a.actions++
		if x.Decision == string(policy.Deny) {
			a.denys++
			r := x.RuleID
			if r == "" {
				r = "@" + x.Source
			}
			a.byRule[r]++
		}
	}
	ids := make([]string, 0, len(by))
	for id := range by {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []Proposal
	for _, id := range ids {
		a := by[id]
		if a.actions < DenyHeavyMinActions || float64(a.denys)/float64(a.actions) < DenyHeavyMinShare {
			continue
		}
		top := sortedCounts(a.byRule)[0]
		refs := NewRefs()
		agentRef := refs.Ref("agent", a.name)
		by := "the default"
		if !strings.HasPrefix(top.Key, "@") {
			by = refs.Ref("rule", top.Key)
		}
		share := float64(a.denys) / float64(a.actions)
		out = append(out, Proposal{
			Kind: KindAdvisory, Generator: GenAdvisoryDenyHeavy, Refs: refs,
			TemplateTitle: fmt.Sprintf("%s is denied %s of the time", agentRef, fmtPercent(share)),
			TemplateRationale: fmt.Sprintf("%s was denied %s of %s times in the last %d days, mostly by %s. Its instructions may ask for things the policy forbids.",
				agentRef, fmtCount(a.denys), fmtCount(a.actions), DenyHeavyLookbackDays, by),
			TemplateRisk: "Advice only: nothing changes. Look at the agent's denied actions in Console › Actions.",
			Pattern:      map[string]any{"agent": id, "advisory": "deny_heavy"},
			Numbers: []Number{{Key: "denied", Value: float64(a.denys), Unit: "count"}, {Key: "actions", Value: float64(a.actions), Unit: "count"},
				{Key: "share", Value: share, Unit: "rate"}, {Key: "lookback_days", Value: DenyHeavyLookbackDays, Unit: "days"}},
			Evidence:  map[string]any{"agent": id, "denied": a.denys, "actions": a.actions, "top_rule": top.Key, "top_rule_count": top.Count},
			MainCount: a.denys,
		})
	}
	return out, nil
}
