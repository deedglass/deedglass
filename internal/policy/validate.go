package policy

import (
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Key catalogs, in fmt order.
var (
	topKeys      = []string{"deedglass", "name", "description", "defaults", "budgets", "rules"}
	defaultsKeys = []string{"decision", "reason", "verify", "approval", "notify"}
	budgetKeys   = []string{"id", "description", "match", "per", "window", "max_calls", "max_spend", "measure"}
	ruleKeys     = []string{"id", "description", "match", "when", "decision", "reason", "approval", "notify", "verify"}
	matchKeys    = []string{"tool", "connector_type", "side_effect", "agent", "labels"}
	approvalKeys = []string{"approvers", "required", "expires_in"}
)

// ConnectorTypes are the known connector types (contract §2; `sdk` is S31's proxy-mode edge).
var ConnectorTypes = []string{"postgres", "rest", "mcp", "mcp_http", "sdk", "github", "slack", "stripe", "gmail", "notion", "fhir", "fake"}

// SideEffects are the declared side effects of a tool.
var SideEffects = []string{"read", "write", "destructive", "unknown"}

var (
	notifyValues = []string{"email", "slack", "webhook"}
	levels       = []string{"L0", "L1", "L2"}
	labelKeyRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,62}$`)
	// agentIDRE is ids.Pattern("agt") (TestAgentIDPattern). policy doesn't import internal/ids,
	// which pulls in database/sql/driver; the package stays pure (internal/archtest).
	agentIDRE   = regexp.MustCompile(`^agt_[0-7][0-9a-hjkmnp-tv-z]{25}$`)
	decisionMap = map[string]string{"block": "deny", "reject": "deny", "refuse": "deny", "forbid": "deny", "denied": "deny",
		"approve": "require_approval", "ask": "require_approval", "review": "require_approval", "approval": "require_approval",
		"require-approval": "require_approval", "requireapproval": "require_approval",
		"permit": "allow", "allowed": "allow", "accept": "allow", "pass": "allow"}
)

// draft is the policy as read, before CEL: the parts plus the nodes CEL and the warnings need.
type draft struct {
	name, description  string
	defaults           Defaults
	defaultsReasonSet  bool
	defaultsNode       *yaml.Node
	defaultsDecision   *yaml.Node
	defaultsReasonNode *yaml.Node
	budgets            []*draftBudget
	rules              []*draftRule
	nodes              int
}

type draftBudget struct {
	b       Budget
	measure *yaml.Node
	path    string
}

type draftRule struct {
	r           Rule
	when        *yaml.Node
	approval    *approvalPart
	path        string
	idNode      *yaml.Node
	matchNode   *yaml.Node
	decision    *yaml.Node
	label       string // `rule "x"` or `rule 3`
	reasonNode  *yaml.Node
	ignored     []keyValue
	hasDecision bool
}

// approvalPart is the fields an `approval:` mapping actually sets.
type approvalPart struct {
	approvers    []string
	approversSet bool
	approverNode *yaml.Node
	approverBad  bool // an entry was refused: don't also report approvals_unsatisfiable
	required     int
	requiredSet  bool
	requiredNode *yaml.Node
	expiresIn    int64
	expiresSet   bool
	node         *yaml.Node
}

type validator struct {
	src   *source
	d     *diags
	ids   map[string]idUse
	keyOf map[*yaml.Node]*yaml.Node // value → its key, to point at `rules:` rather than the first rule
}

// at is where a diagnostic about a value points: the value itself, or its key when the value is
// a block collection (whose position is its first child).
func (v *validator) at(n *yaml.Node) span {
	if n != nil && (n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode) && n.Style&yaml.FlowStyle == 0 {
		if k, ok := v.keyOf[n]; ok {
			return keySpan(k)
		}
	}
	return spanOf(v.src, n)
}

func indexKeys(n *yaml.Node, m map[*yaml.Node]*yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			m[n.Content[i+1]] = n.Content[i]
		}
	}
	for _, c := range n.Content {
		indexKeys(c, m)
	}
}

// wrongTypeHints gives the next step for the commonest wrong types.
var wrongTypeHints = map[string]string{
	"rules":       "Write each rule as a list item that starts with `- id:`.",
	"budgets":     "Write each budget as a list item that starts with `- id:`.",
	"defaults":    "Write `defaults:` as a mapping with `decision:` and `reason:` on their own lines.",
	"match":       "Write it as a mapping, like `match: { tool: \"stripe.*\" }`.",
	"approval":    "Write it as a mapping, like `approval: { approvers: [role:admin], required: 1 }`.",
	"notify":      "Write a channel or a list of them, like `notify: [slack, email]`.",
	"labels":      "Write it as a mapping, like `labels: { env: prod }`.",
	"required":    "Write a whole number without quotes, like `required: 2`.",
	"max_calls":   "Write a whole number without quotes, like `max_calls: 60`.",
	"description": "Write one line of text, or a block with `description: >-`.",
	"reason":      "Write one sentence of text.",
}

type idUse struct {
	line int
	what string
}

// validate checks the schema of a root mapping and returns the draft. Diagnostics go to d; the
// draft is usable for CEL even when some fields were wrong, so one pass reports as much as it can.
func validate(src *source, d *diags, root *yaml.Node) *draft {
	v := &validator{src: src, d: d, ids: map[string]idUse{}, keyOf: map[*yaml.Node]*yaml.Node{}}
	indexKeys(root, v.keyOf)
	p := &draft{}
	var marker, defaults, budgets, rules, versionKey *yaml.Node
	topTypos := map[string]bool{}
	for _, kv := range pairs(root) {
		k := kv.Key.Value
		switch k {
		case "deedglass":
			marker = kv.Value
		case "name":
			p.name = v.slug(kv.Value, "name", "", "Policy name")
		case "description":
			p.description = v.text(kv.Value, "description", "", "The description", MaxDescription, true)
		case "defaults":
			defaults = kv.Value
			p.defaultsNode = kv.Key
		case "budgets":
			budgets = kv.Value
		case "rules":
			rules = kv.Value
		case "version":
			versionKey = kv.Key
			d.errorf("unknown_key", keySpan(kv.Key), "version", "",
				"Use `deedglass: policy/v1` as the first key. `version` is the number the API gives each saved version.",
				"Unknown key %q at the top level.", "version")
		default:
			if s := v.unknownKey(kv.Key, topKeys, "at the top level", "", "", nil); s != "" {
				topTypos[s] = true
			}
		}
	}
	switch {
	case marker == nil && versionKey == nil && !topTypos["deedglass"]:
		d.errorf("missing_marker", span{Start: Pos{root.Line, root.Column}}, "deedglass", "",
			"Add `deedglass: policy/v1` as the first line.", "This file has no `deedglass: policy/v1` marker.")
	case marker != nil && !isText(marker):
		v.wrongType(marker, "deedglass", "", "`deedglass` must be the string policy/v1")
	case marker != nil && marker.Value != Marker:
		hint := "Write `deedglass: policy/v1`."
		msg := fmt.Sprintf("`deedglass: %s` is not a policy format.", clip(marker.Value))
		if strings.HasPrefix(marker.Value, "policy/") {
			msg = fmt.Sprintf("`deedglass: %s` is not supported by this version of Deedglass, which reads policy/v1.", clip(marker.Value))
			hint = "Write `deedglass: policy/v1`, or upgrade Deedglass."
		}
		d.errorf("unsupported_format", spanOf(src, marker), "deedglass", "", hint, "%s", msg)
	}
	if p.name == "" && !hasKey(root, "name") && !topTypos["name"] {
		d.errorf("missing_key", span{Start: Pos{root.Line, root.Column}}, "name", "",
			"Add `name:` with a short slug, like `name: refunds`.", "This policy has no `name`.")
	}
	if defaults == nil {
		if !topTypos["defaults"] {
			d.errorf("missing_key", span{Start: Pos{root.Line, root.Column}}, "defaults", "",
				"Add `defaults:` with `decision: deny` and a `reason`, so unmatched actions are refused.", "This policy has no `defaults`.")
		}
		p.defaults = Defaults{Decision: Deny, Verify: L0, Approval: builtinApproval()}
	} else {
		v.defaults(p, defaults)
	}
	if budgets != nil && !isNull(budgets) {
		if budgets.Kind != yaml.SequenceNode {
			v.wrongType(budgets, "budgets", "", "`budgets` must be a list")
		} else {
			if len(budgets.Content) > MaxBudgets {
				d.errorf("too_many_budgets", v.at(budgets), "budgets", "", "Combine budgets with `match` globs, or split the policy.",
					"This policy has %s budgets; the limit is %s.", thousands(len(budgets.Content)), thousands(MaxBudgets))
			}
			for i, n := range budgets.Content {
				if b := v.budget(n, i, p); b != nil {
					p.budgets = append(p.budgets, b)
				}
			}
		}
	}
	if rules != nil && !isNull(rules) {
		if rules.Kind != yaml.SequenceNode {
			v.wrongType(rules, "rules", "", "`rules` must be a list")
		} else {
			if len(rules.Content) > MaxRules {
				d.errorf("too_many_rules", v.at(rules), "rules", "", "Combine rules with `match` globs and lists, or split the policy.",
					"This policy has %s rules; the limit is %s.", thousands(len(rules.Content)), thousands(MaxRules))
			}
			for i, n := range rules.Content {
				if r := v.rule(n, i, p); r != nil {
					p.rules = append(p.rules, r)
				}
			}
		}
	}
	return p
}

func builtinApproval() ApprovalSpec {
	return ApprovalSpec{Approvers: []string{"role:admin"}, Required: 1, ExpiresIn: 30 * 60}
}

func hasKey(m *yaml.Node, k string) bool {
	for _, kv := range pairs(m) {
		if kv.Key.Value == k {
			return true
		}
	}
	return false
}

func keySpan(k *yaml.Node) span {
	return span{Start: Pos{k.Line, k.Column}, End: Pos{k.Line, k.Column + runeLen(k.Value)}}
}

func (v *validator) wrongType(n *yaml.Node, path, ruleID, what string) {
	v.d.errorf("wrong_type", v.at(n), path, ruleID, wrongTypeHints[lastSeg(path)], "%s; found %s.", what, kindName(n))
}

// unknownKey reports a key with did-you-mean, or the valid keys. moved maps keys that belong in a
// nested mapping to that mapping's name.
func (v *validator) unknownKey(k *yaml.Node, valid []string, where, path, ruleID string, moved map[string]string) string {
	name := k.Value
	hint := ""
	s := suggest(name, valid)
	if s != "" {
		hint = fmt.Sprintf("Did you mean %q?", s)
	} else if parent, ok := moved[name]; ok {
		hint = fmt.Sprintf("`%s` belongs inside `%s:`.", name, parent)
	} else {
		hint = "Valid keys: " + quoteList(valid) + "."
	}
	p := name
	if path != "" {
		p = path + "." + name
	}
	v.d.errorf("unknown_key", keySpan(k), p, ruleID, hint, "Unknown key %q %s.", clip(name), where)
	return s
}

// text reads a string field: no control or bidi characters, at most max characters.
func (v *validator) text(n *yaml.Node, path, ruleID, what string, max int, allowTab bool) string {
	if isNull(n) {
		return ""
	}
	if !isText(n) {
		v.wrongType(n, path, ruleID, fmt.Sprintf("`%s` must be text", lastSeg(path)))
		return ""
	}
	s := n.Value
	if !allowTab {
		// A folded or literal block ends with a line break; it isn't part of a one-line value.
		s = strings.TrimRight(s, "\n")
	}
	if _, desc, bad := badChar(s, allowTab); bad {
		v.d.errorf("control_character", spanOf(v.src, n), path, ruleID, "Remove it. It can make text display differently from what it says.",
			"%s contains %s.", what, desc)
		return ""
	}
	if max > 0 && runeLen(s) > max {
		if path == "reason" || strings.HasSuffix(path, ".reason") {
			v.d.warnf("reason_too_long", spanOf(v.src, n), path, ruleID, "Shorten it to one sentence; the rest is cut off.",
				"This reason is %s characters. Agents and approvers see at most %s.", thousands(runeLen(s)), thousands(max))
		} else {
			v.d.errorf("value_too_long", spanOf(v.src, n), path, ruleID, "Shorten it.",
				"%s is %s characters; the limit is %s.", what, thousands(runeLen(s)), thousands(max))
		}
	}
	return s
}

func lastSeg(path string) string {
	if i := strings.LastIndexAny(path, ".]"); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	return path
}

// slug reads a name or id.
func (v *validator) slug(n *yaml.Node, path, ruleID, what string) string {
	if !isText(n) {
		v.wrongType(n, path, ruleID, fmt.Sprintf("`%s` must be a slug like `block-drop`", lastSeg(path)))
		return ""
	}
	s := n.Value
	if slugRE.MatchString(s) {
		return s
	}
	code := "invalid_id"
	if path == "name" {
		code = "invalid_name"
	}
	if _, desc, bad := badChar(s, false); bad {
		v.d.errorf("control_character", spanOf(v.src, n), path, ruleID, "Remove it. It can make text display differently from what it says.",
			"%s contains %s.", what, desc)
		return ""
	}
	ex := slugify(s)
	hint := "Use lowercase letters, digits and dashes, starting with a letter, like `" + ex + "`."
	if !isASCII(s) {
		hint = "Use ASCII lowercase letters, digits and dashes, starting with a letter, like `" + ex + "`."
	}
	v.d.errorf(code, spanOf(v.src, n), path, ruleID, hint, "%s %q is not a slug.", what, clip(s))
	return ""
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

var accents = strings.NewReplacer("à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i", "ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ù", "u", "ú", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n", "ß", "ss", "ø", "o", "æ", "ae")

func slugify(s string) string {
	t := strings.Trim(nonSlug.ReplaceAllString(accents.Replace(strings.ToLower(s)), "-"), "-")
	if t == "" || t[0] < 'a' || t[0] > 'z' {
		t = "rule-" + t
	}
	if len(t) > 63 {
		t = strings.TrimRight(t[:63], "-")
	}
	if len(t) < 2 {
		t = "rule-" + t
	}
	return strings.Trim(t, "-")
}

// enum reads one value from a fixed set.
func (v *validator) enum(n *yaml.Node, path, ruleID, noun string, valid []string) string {
	if !isText(n) {
		v.wrongType(n, path, ruleID, fmt.Sprintf("`%s` must be one of %s", lastSeg(path), orList(valid)))
		return ""
	}
	for _, x := range valid {
		if n.Value == x {
			return x
		}
	}
	hint := "Use " + orList(valid) + "."
	if s := suggest(n.Value, valid); s != "" {
		hint = fmt.Sprintf("Did you mean `%s`? Use %s.", s, orList(valid))
	}
	v.d.errorf("invalid_value", spanOf(v.src, n), path, ruleID, hint, "`%s` is not %s.", clip(n.Value), noun)
	return ""
}

func (v *validator) decision(n *yaml.Node, path, ruleID string) Outcome {
	if !isText(n) {
		v.wrongType(n, path, ruleID, "`decision` must be allow, deny or require_approval")
		return ""
	}
	for _, x := range Outcomes {
		if n.Value == x {
			return Outcome(x)
		}
	}
	s := decisionMap[strings.ToLower(n.Value)]
	if s == "" {
		s = suggest(n.Value, Outcomes)
	}
	hint := "Decisions are allow, deny and require_approval."
	if s != "" {
		hint = fmt.Sprintf("Did you mean `%s`? %s", s, hint)
	}
	v.d.errorf("invalid_decision", spanOf(v.src, n), path, ruleID, hint, "`%s` is not a decision.", clip(n.Value))
	return ""
}

// claimID records an id across budgets and rules.
func (v *validator) claimID(id string, n *yaml.Node, path, ruleID, what string) {
	if id == "" {
		return
	}
	if first, ok := v.ids[id]; ok {
		if first.what == what {
			v.d.errorf("duplicate_id", spanOf(v.src, n), path, ruleID, "Give each "+what+" its own id.",
				"%s id %q is used twice (first on line %d).", capitalize(what), id, first.line)
		} else {
			v.d.errorf("duplicate_id", spanOf(v.src, n), path, ruleID, "Budgets and rules share one set of ids; rename one of them.",
				"Id %q is already used by the %s on line %d.", id, first.what, first.line)
		}
		return
	}
	v.ids[id] = idUse{n.Line, what}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (v *validator) defaults(p *draft, n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		v.wrongType(n, "defaults", "", "`defaults` must be a mapping with `decision` and `reason`")
		p.defaults = Defaults{Decision: Deny, Verify: L0, Approval: builtinApproval()}
		return
	}
	df := Defaults{Verify: L0, Approval: builtinApproval()}
	var reasonNode *yaml.Node
	dTypos := map[string]bool{}
	for _, kv := range pairs(n) {
		path := "defaults." + kv.Key.Value
		switch kv.Key.Value {
		case "decision":
			df.Decision = v.decision(kv.Value, path, "")
			p.defaultsDecision = kv.Value
		case "reason":
			reasonNode = kv.Value
			p.defaultsReasonNode = kv.Value
			df.Reason = v.text(kv.Value, path, "", "The reason", MaxReason, false)
			p.defaultsReasonSet = strings.TrimSpace(df.Reason) != ""
		case "verify":
			df.Verify = Level(v.enum(kv.Value, path, "", "a witness level", levels))
		case "approval":
			if a := v.approval(kv.Value, path, ""); a != nil {
				df.Approval = mergeApproval(builtinApproval(), a)
				v.checkSatisfiable(df.Approval, a, kv.Value, path, "", "The default approval")
			}
		case "notify":
			df.Notify = v.notify(kv.Value, path, "")
		default:
			if s := v.unknownKey(kv.Key, defaultsKeys, "in defaults", "defaults", "", nil); s != "" {
				dTypos[s] = true
			}
		}
	}
	if df.Verify == "" {
		df.Verify = L0
	}
	if p.defaultsDecision == nil {
		if !dTypos["decision"] {
			v.d.errorf("missing_key", span{Start: Pos{n.Line, n.Column}}, "defaults.decision", "",
				"Add `decision: deny`; it decides every action no rule matches.", "`defaults` has no `decision`.")
		}
		df.Decision = Deny
	}
	if (df.Decision == Deny || df.Decision == RequireApproval) && !dTypos["reason"] && p.defaultsDecision != nil &&
		(reasonNode == nil || isNull(reasonNode) || (isText(reasonNode) && strings.TrimSpace(reasonNode.Value) == "")) {
		verb := "denies"
		if df.Decision == RequireApproval {
			verb = "requires approval"
		}
		v.d.errorf("reason_missing", spanOf(v.src, p.defaultsDecision), "defaults.reason", "",
			`Write the one sentence the agent will see, like "No rule allows this action. Add a rule in Console › Policies to allow it."`,
			"`defaults` %s but has no `reason`.", verb)
	}
	p.defaults = df
}

func (v *validator) notify(n *yaml.Node, path, ruleID string) []string {
	vals := v.list(n, path, ruleID)
	var out []string
	for _, x := range vals {
		if s := v.enum(x, path, ruleID, "a notification channel", notifyValues); s != "" {
			out = append(out, s)
		}
	}
	return sortUnique(out)
}

// list reads a scalar or a list of scalars, at most MaxListValues entries.
func (v *validator) list(n *yaml.Node, path, ruleID string) []*yaml.Node {
	if isNull(n) {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return []*yaml.Node{n}
	case yaml.SequenceNode:
		if len(n.Content) > MaxListValues {
			v.d.errorf("too_many_values", spanOf(v.src, n), path, ruleID, "Use a glob, or split the rule.",
				"`%s` lists %s values; the limit is %s.", lastSeg(path), thousands(len(n.Content)), thousands(MaxListValues))
		}
		var out []*yaml.Node
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode || isNull(c) {
				v.wrongType(c, path, ruleID, fmt.Sprintf("Each entry of `%s` must be a single value", lastSeg(path)))
				continue
			}
			out = append(out, c)
		}
		return out
	}
	v.wrongType(n, path, ruleID, fmt.Sprintf("`%s` must be a value or a list of values", lastSeg(path)))
	return nil
}

func sortUnique(xs []string) []string {
	if len(xs) == 0 {
		return xs
	}
	sort.Strings(xs)
	out := xs[:1]
	for _, x := range xs[1:] {
		if x != out[len(out)-1] {
			out = append(out, x)
		}
	}
	return out
}

func (v *validator) approval(n *yaml.Node, path, ruleID string) *approvalPart {
	if n.Kind != yaml.MappingNode {
		v.wrongType(n, path, ruleID, "`approval` must be a mapping with `approvers`, `required` and `expires_in`")
		return nil
	}
	a := &approvalPart{node: n}
	for _, kv := range pairs(n) {
		kp := path + "." + kv.Key.Value
		switch kv.Key.Value {
		case "approvers":
			a.approversSet = true
			a.approverNode = kv.Value
			for _, x := range v.list(kv.Value, kp, ruleID) {
				if s := v.approver(x, kp, ruleID); s != "" {
					a.approvers = append(a.approvers, s)
				} else {
					a.approverBad = true
				}
			}
			a.approvers = sortUnique(a.approvers)
			if len(a.approvers) == 0 && isNull(kv.Value) {
				v.wrongType(kv.Value, kp, ruleID, "`approvers` must list at least one approver")
			}
		case "required":
			a.requiredNode = kv.Value
			if n, ok := v.integer(kv.Value, kp, ruleID, 1, MaxRequired, "approvals"); ok {
				a.required, a.requiredSet = int(n), true
			}
		case "expires_in":
			if s, ok := v.duration(kv.Value, kp, ruleID); ok {
				a.expiresIn, a.expiresSet = s, true
			}
		default:
			v.unknownKey(kv.Key, approvalKeys, "in "+approvalWhere(path, ruleID), path, ruleID, nil)
		}
	}
	return a
}

func approvalWhere(path, ruleID string) string {
	if ruleID != "" {
		return fmt.Sprintf("the approval of rule %q", ruleID)
	}
	return "`" + path + "`"
}

func (v *validator) approver(n *yaml.Node, path, ruleID string) string {
	s := n.Value
	hint := "Use role:admin, role:owner or user:<email>."
	switch {
	case s == "role:admin" || s == "role:owner":
		return s
	case s == "role:viewer":
		v.d.errorf("invalid_approvers", spanOf(v.src, n), path, ruleID, hint, "`role:viewer` cannot approve.")
	case strings.HasPrefix(s, "user:"):
		addr := strings.TrimPrefix(s, "user:")
		if a, err := mail.ParseAddress(addr); err == nil && a.Address == addr && a.Name == "" && strings.Contains(addr, "@") && isASCII(addr) {
			return "user:" + strings.ToLower(addr)
		}
		v.d.errorf("invalid_approvers", spanOf(v.src, n), path, ruleID, "Write user: and the person's email, like user:priya@acme.com.",
			"`%s` is not an email address.", clip(s))
	case strings.HasPrefix(s, "role:"):
		v.d.errorf("invalid_approvers", spanOf(v.src, n), path, ruleID, hint, "`%s` is not a role that can approve.", clip(s))
	default:
		if strings.Contains(s, "@") {
			hint = fmt.Sprintf("Write it as `user:%s`.", clip(s))
		}
		v.d.errorf("invalid_approvers", spanOf(v.src, n), path, ruleID, hint, "`%s` is not an approver.", clip(s))
	}
	return ""
}

func (v *validator) integer(n *yaml.Node, path, ruleID string, lo, hi int64, noun string) (int64, bool) {
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" || n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) != 0 {
		v.wrongType(n, path, ruleID, fmt.Sprintf("`%s` must be a whole number", lastSeg(path)))
		return 0, false
	}
	x, err := strconv.ParseInt(strings.ReplaceAll(n.Value, "_", ""), 0, 64)
	if err != nil || x < lo || x > hi {
		v.d.errorf("out_of_range", spanOf(v.src, n), path, ruleID, fmt.Sprintf("Use a number from %s to %s.", thousands(lo), thousands(hi)),
			"`%s: %s` is out of range.", lastSeg(path), clip(n.Value))
		return 0, false
	}
	return x, true
}

func (v *validator) duration(n *yaml.Node, path, ruleID string) (int64, bool) {
	hint := "Use a number and one unit: 30m, 2h or 1d (from 1m to 7d)."
	if !isText(n) {
		v.wrongType(n, path, ruleID, "`expires_in` must be a duration like 30m")
		return 0, false
	}
	s, ok := parseDuration(n.Value)
	switch {
	case !ok:
		v.d.errorf("invalid_duration", spanOf(v.src, n), path, ruleID, hint, "`expires_in: %s` is not a duration.", clip(n.Value))
	case s < MinExpiry:
		v.d.errorf("invalid_duration", spanOf(v.src, n), path, ruleID, hint, "`expires_in: %s` is shorter than 1m.", clip(n.Value))
	case s > MaxExpiry:
		v.d.errorf("invalid_duration", spanOf(v.src, n), path, ruleID, hint, "`expires_in: %s` is longer than 7d.", clip(n.Value))
	default:
		return s, true
	}
	return 0, false
}

func mergeApproval(base ApprovalSpec, a *approvalPart) ApprovalSpec {
	out := base
	out.Approvers = append([]string(nil), base.Approvers...)
	if a == nil {
		return out
	}
	if a.approversSet && len(a.approvers) > 0 {
		out.Approvers = a.approvers
	}
	if a.requiredSet {
		out.Required = a.required
	}
	if a.expiresSet {
		out.ExpiresIn = a.expiresIn
	}
	return out
}

// checkSatisfiable: when every approver is a person, `required` can't exceed how many there are.
func (v *validator) checkSatisfiable(s ApprovalSpec, a *approvalPart, n *yaml.Node, path, ruleID, what string) {
	if a != nil && a.approverBad {
		return
	}
	people := 0
	for _, x := range s.Approvers {
		if !strings.HasPrefix(x, "user:") {
			return
		}
		people++
	}
	if s.Required > people {
		at := n
		if a != nil && a.requiredNode != nil {
			at = a.requiredNode
		}
		v.d.errorf("approvals_unsatisfiable", spanOf(v.src, at), path, ruleID, "Lower `required`, or add people or a role to `approvers`.",
			"%s needs %s but names only %s.", what, plural(s.Required, "approval", "approvals"), plural(people, "person", "people"))
	}
}

// match reads a `match` mapping into a Matcher.
func (v *validator) match(n *yaml.Node, path, ruleID, where string) Matcher {
	var m Matcher
	if isNull(n) {
		return m
	}
	if n.Kind != yaml.MappingNode {
		v.wrongType(n, path, ruleID, "`match` must be a mapping, like `{ tool: \"stripe.*\" }`")
		return m
	}
	for _, kv := range pairs(n) {
		kp := path + "." + kv.Key.Value
		if isNull(kv.Value) && contains(matchKeys, kv.Key.Value) {
			// An empty field would silently match everything; say so instead.
			v.d.errorf("wrong_type", keySpan(kv.Key), kp, ruleID, "Give it a value, or remove the key to match everything.",
				"`%s` has no value.", kv.Key.Value)
			continue
		}
		switch kv.Key.Value {
		case "tool":
			m.Tool = append(m.Tool, v.globs(kv.Value, kp, ruleID)...)
		case "connector_type":
			for _, x := range v.list(kv.Value, kp, ruleID) {
				g, ok := v.glob(x, kp, ruleID)
				if !ok {
					continue
				}
				m.ConnectorType = append(m.ConnectorType, g)
				if g.kind == globExact && !contains(ConnectorTypes, g.src) {
					hint := "Known types: " + quoteList(ConnectorTypes) + "."
					if s := suggest(g.src, ConnectorTypes); s != "" {
						hint = fmt.Sprintf("Did you mean `%s`?", s)
					}
					v.d.warnf("unknown_connector_type", spanOf(v.src, x), kp, ruleID, hint, "`%s` is not a known connector type.", clip(g.src))
				}
			}
		case "side_effect":
			var out []string
			for _, x := range v.list(kv.Value, kp, ruleID) {
				if contains(SideEffects, x.Value) {
					out = append(out, x.Value)
					continue
				}
				hint := "Use read, write, destructive or unknown."
				if s := suggest(x.Value, SideEffects); s != "" {
					hint = fmt.Sprintf("Did you mean `%s`? Use read, write, destructive or unknown.", s)
				}
				v.d.errorf("invalid_side_effect", spanOf(v.src, x), kp, ruleID, hint, "`%s` is not a side effect.", clip(x.Value))
			}
			m.SideEffect = sortUnique(out)
		case "agent":
			m.hasAgent = true
			for _, x := range v.list(kv.Value, kp, ruleID) {
				if strings.HasPrefix(x.Value, "agt_") && !strings.ContainsAny(x.Value, "*?") {
					if agentIDRE.MatchString(x.Value) {
						m.AgentIDs = append(m.AgentIDs, x.Value)
					} else {
						v.d.errorf("invalid_glob", spanOf(v.src, x), kp, ruleID,
							"Agent ids are agt_ and 26 lowercase letters and digits; or match the agent's name instead.",
							"`%s` is not an agent id.", clip(x.Value))
					}
					continue
				}
				if g, ok := v.glob(x, kp, ruleID); ok {
					m.AgentNames = append(m.AgentNames, g)
				}
			}
			m.AgentIDs = sortUnique(m.AgentIDs)
		case "labels":
			m.Labels = v.labels(kv.Value, kp, ruleID)
		default:
			v.unknownKey(kv.Key, matchKeys, "in "+where, path, ruleID, nil)
		}
	}
	m.Tool = sortGlobs(m.Tool)
	m.ConnectorType = sortGlobs(m.ConnectorType)
	m.AgentNames = sortGlobs(m.AgentNames)
	return m
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func sortGlobs(gs []Glob) []Glob {
	sort.Slice(gs, func(i, j int) bool { return gs[i].src < gs[j].src })
	out := gs[:0]
	for i, g := range gs {
		if i > 0 && g.src == gs[i-1].src {
			continue
		}
		out = append(out, g)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (v *validator) globs(n *yaml.Node, path, ruleID string) []Glob {
	var out []Glob
	for _, x := range v.list(n, path, ruleID) {
		if g, ok := v.glob(x, path, ruleID); ok {
			out = append(out, g)
		}
	}
	return out
}

func (v *validator) glob(n *yaml.Node, path, ruleID string) (Glob, bool) {
	s := n.Value
	switch {
	case s == "":
		v.d.errorf("invalid_glob", spanOf(v.src, n), path, ruleID, "Use `*` to match everything, or remove the field.", "An empty pattern matches nothing.")
		return Glob{}, false
	case runeLen(s) > MaxGlob:
		v.d.errorf("invalid_glob", spanOf(v.src, n), path, ruleID, "Shorten it with `*`.",
			"This pattern is %s characters; the limit is %s.", thousands(runeLen(s)), thousands(MaxGlob))
		return Glob{}, false
	}
	g, ok := CompileGlob(s)
	if !ok {
		if _, desc, bad := badChar(s, false); bad {
			v.d.errorf("control_character", spanOf(v.src, n), path, ruleID, "Remove it. It can make text display differently from what it says.",
				"The pattern contains %s.", desc)
			return Glob{}, false
		}
		v.d.errorf("invalid_glob", spanOf(v.src, n), path, ruleID, "Patterns use `*` and `?` only, with letters, digits and _ . : -.",
			"`%s` is not a valid pattern.", clip(s))
		return Glob{}, false
	}
	return g, true
}

func (v *validator) labels(n *yaml.Node, path, ruleID string) []LabelPair {
	if n.Kind != yaml.MappingNode {
		v.wrongType(n, path, ruleID, "`labels` must be a mapping, like `{ env: prod }`")
		return nil
	}
	if len(n.Content)/2 > MaxListValues {
		v.d.errorf("too_many_values", spanOf(v.src, n), path, ruleID, "Match on fewer labels.",
			"`labels` lists %s pairs; the limit is %s.", thousands(len(n.Content)/2), thousands(MaxListValues))
	}
	var out []LabelPair
	for _, kv := range pairs(n) {
		k := kv.Key.Value
		if !labelKeyRE.MatchString(k) {
			v.d.errorf("invalid_label", keySpan(kv.Key), path+"."+k, ruleID,
				"Label keys use lowercase letters, digits and . _ / -, up to 63 characters, like env or team.", "`%s` is not a label key.", clip(k))
			continue
		}
		if !isText(kv.Value) {
			v.wrongType(kv.Value, path+"."+k, ruleID, fmt.Sprintf("The value of label `%s` must be text", k))
			continue
		}
		val := kv.Value.Value
		if _, desc, bad := badChar(val, false); bad || runeLen(val) > 128 {
			msg := fmt.Sprintf("The value of label `%s` is longer than 128 characters.", k)
			if bad {
				msg = fmt.Sprintf("The value of label `%s` contains %s.", k, desc)
			}
			v.d.errorf("invalid_label", spanOf(v.src, kv.Value), path+"."+k, ruleID, "Label values are at most 128 printable characters.", "%s", msg)
			continue
		}
		out = append(out, LabelPair{k, val})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (v *validator) budget(n *yaml.Node, i int, p *draft) *draftBudget {
	path := fmt.Sprintf("budgets[%d]", i)
	if n.Kind != yaml.MappingNode {
		v.wrongType(n, path, "", fmt.Sprintf("Budget %d must be a mapping with `id`, `window` and a limit", i+1))
		return nil
	}
	b := &draftBudget{path: path}
	b.b.Pos = Pos{n.Line, n.Column}
	b.b.Per = "agent"
	label := fmt.Sprintf("Budget %d", i+1)
	var hasID bool
	var calls, spend *yaml.Node
	var window *yaml.Node
	var measureKey *yaml.Node
	bTypos := map[string]bool{}
	for _, kv := range pairs(n) {
		kp := path + "." + kv.Key.Value
		switch kv.Key.Value {
		case "id":
			hasID = true
			b.b.ID = v.slug(kv.Value, kp, "", "Budget id")
			if b.b.ID != "" {
				label = fmt.Sprintf("Budget %q", b.b.ID)
			}
			v.claimID(b.b.ID, kv.Value, kp, "", "budget")
		case "description":
			b.b.Description = v.text(kv.Value, kp, b.b.ID, "The description", MaxDescription, true)
		case "match":
			b.b.Match = v.match(kv.Value, kp, b.b.ID, "the match of "+strings.ToLower(label[:1])+label[1:])
		case "per":
			b.b.Per = v.enum(kv.Value, kp, b.b.ID, "a budget scope", []string{"agent", "org"})
		case "window":
			window = kv.Value
			b.b.Window = v.enum(kv.Value, kp, b.b.ID, "a budget window", []string{"minute", "hour", "day"})
		case "max_calls":
			calls = kv.Value
			if x, ok := v.integer(kv.Value, kp, b.b.ID, 1, MaxCalls, "calls"); ok {
				b.b.MaxCalls = x
			}
		case "max_spend":
			spend = kv.Value
			b.b.MaxSpend = v.money(kv.Value, kp, b.b.ID)
		case "measure":
			measureKey = kv.Key
			b.measure = kv.Value
		default:
			if s := v.unknownKey(kv.Key, budgetKeys, "in "+strings.ToLower(label[:1])+label[1:], path, b.b.ID, nil); s != "" {
				bTypos[s] = true
			}
		}
	}
	at := span{Start: Pos{n.Line, n.Column}}
	if !hasID && !bTypos["id"] {
		v.d.errorf("missing_key", at, path+".id", "", "Add `id:` with a short slug, like `refunds-per-day`.", "%s has no `id`.", label)
	}
	if window == nil && !bTypos["window"] {
		v.d.errorf("missing_key", at, path+".window", b.b.ID, "Add `window: minute`, `hour` or `day`.", "%s has no `window`.", label)
	}
	switch {
	case calls == nil && spend == nil && !bTypos["max_calls"] && !bTypos["max_spend"]:
		v.d.errorf("missing_key", at, path, b.b.ID, "Add `max_calls: 60` or `max_spend: \"500.00 usd\"`.", "%s has no limit.", label)
	case calls != nil && spend != nil:
		v.d.errorf("conflicting_keys", spanOf(v.src, spend), path+".max_spend", b.b.ID, "Keep one; use two budgets to limit both.",
			"%s sets both `max_calls` and `max_spend`.", label)
	}
	if measureKey != nil && spend == nil {
		v.d.errorf("conflicting_keys", keySpan(measureKey), path+".measure", b.b.ID, "Remove `measure`, or limit spending with `max_spend`.",
			"`measure` only works with `max_spend`.")
		b.measure = nil
	}
	return b
}

func (v *validator) money(n *yaml.Node, path, ruleID string) *Money {
	if n.Kind == yaml.ScalarNode && (n.ShortTag() == "!!float" || n.ShortTag() == "!!int") && n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) == 0 {
		v.d.errorf("money_float", spanOf(v.src, n), path, ruleID, `Write money as a string, like "50.00 usd", so no precision is lost.`,
			"`%s: %s` is a YAML number.", lastSeg(path), clip(n.Value))
		return nil
	}
	if !isText(n) {
		v.wrongType(n, path, ruleID, "`max_spend` must be money, like \"500.00 usd\"")
		return nil
	}
	m, prob := ParseMoney(n.Value)
	if prob != nil {
		v.d.errorf(prob.Code, spanOf(v.src, n), path, ruleID, prob.Hint, "%s", prob.Message)
		return nil
	}
	if m.Amount == 0 {
		v.d.errorf("invalid_money", spanOf(v.src, n), path, ruleID, "A spending budget needs an amount above zero.", "`%s` is zero.", clip(n.Value))
		return nil
	}
	return &m
}

func (v *validator) rule(n *yaml.Node, i int, p *draft) *draftRule {
	path := fmt.Sprintf("rules[%d]", i)
	if n.Kind != yaml.MappingNode {
		v.wrongType(n, path, "", fmt.Sprintf("Rule %d must be a mapping with `id` and `decision`", i+1))
		return nil
	}
	r := &draftRule{path: path, label: fmt.Sprintf("Rule %d", i+1)}
	r.r.Pos = Pos{n.Line, n.Column}
	// The id first, so every message about this rule can name it.
	for _, kv := range pairs(n) {
		if kv.Key.Value == "id" {
			r.idNode = kv.Value
			r.r.ID = v.slug(kv.Value, path+".id", "", "Rule id")
			if r.r.ID != "" {
				r.label = fmt.Sprintf("Rule %q", r.r.ID)
			}
			v.claimID(r.r.ID, kv.Value, path+".id", "", "rule")
		}
	}
	id := r.r.ID
	where := "in rule " + strings.TrimPrefix(r.label, "Rule ")
	moved := map[string]string{}
	for _, k := range matchKeys {
		moved[k] = "match"
	}
	for _, k := range approvalKeys {
		moved[k] = "approval"
	}
	var reasonNode, approvalNode, notifyNode, verifyNode *yaml.Node
	var approvalKey, notifyKey, verifyKey *yaml.Node
	typos := map[string]bool{} // keys a misspelling probably meant: no missing_key for them
	for _, kv := range pairs(n) {
		kp := path + "." + kv.Key.Value
		switch kv.Key.Value {
		case "id":
		case "description":
			r.r.Description = v.text(kv.Value, kp, id, "The description", MaxDescription, true)
		case "match":
			r.matchNode = kv.Value
			r.r.Match = v.match(kv.Value, kp, id, "the match of "+strings.ToLower(r.label[:1])+r.label[1:])
		case "when":
			r.when = kv.Value
		case "decision":
			r.hasDecision = true
			r.decision = kv.Value
			r.r.Decision = v.decision(kv.Value, kp, id)
		case "reason":
			reasonNode = kv.Value
			r.reasonNode = kv.Value
			r.r.Reason = v.text(kv.Value, kp, id, "The reason", MaxReason, false)
		case "approval":
			approvalKey, approvalNode = kv.Key, kv.Value
		case "notify":
			notifyKey, notifyNode = kv.Key, kv.Value
		case "verify":
			verifyKey, verifyNode = kv.Key, kv.Value
		default:
			if s := v.unknownKey(kv.Key, ruleKeys, where, path, id, moved); s != "" {
				typos[s] = true
			}
		}
	}
	at := span{Start: Pos{n.Line, n.Column}}
	if r.idNode == nil && !typos["id"] {
		v.d.errorf("missing_key", at, path+".id", "", "Add `id:` with a short slug, like `block-drop`.", "%s has no `id`.", r.label)
	}
	if !r.hasDecision && !typos["decision"] {
		v.d.errorf("missing_key", at, path+".decision", id, "Add `decision: allow`, `deny` or `require_approval`.", "%s has no `decision`.", r.label)
	}
	dec := r.r.Decision
	if (dec == Deny || dec == RequireApproval) && !typos["reason"] && (reasonNode == nil || isNull(reasonNode) || (isText(reasonNode) && strings.TrimSpace(reasonNode.Value) == "")) {
		verb := "denies"
		if dec == RequireApproval {
			verb = "requires approval"
		}
		v.d.errorf("reason_missing", spanOf(v.src, r.decision), path+".reason", id,
			`Write the one sentence the agent will see, like "DROP is never allowed for agents."`, "%s %s but has no `reason`.", r.label, verb)
	}
	if approvalNode != nil {
		a := v.approval(approvalNode, path+".approval", id)
		if dec == RequireApproval {
			r.approval = a
		} else if dec != "" {
			r.ignored = append(r.ignored, keyValue{approvalKey, approvalNode})
		}
	}
	if dec == RequireApproval {
		s := mergeApproval(p.defaults.Approval, r.approval)
		r.r.Approval = &s
		var an *yaml.Node = n
		if approvalNode != nil {
			an = approvalNode
		}
		v.checkSatisfiable(s, r.approval, an, path+".approval", id, "This rule")
	}
	if notifyNode != nil {
		ns := v.notify(notifyNode, path+".notify", id)
		if dec == Allow {
			r.ignored = append(r.ignored, keyValue{notifyKey, notifyNode})
		} else {
			r.r.Notify = ns
			r.r.NotifyNone = dec == RequireApproval && notifyNode.Kind == yaml.SequenceNode && len(notifyNode.Content) == 0
		}
	}
	if verifyNode != nil {
		lv := Level(v.enum(verifyNode, path+".verify", id, "a witness level", levels))
		if dec == Deny {
			r.ignored = append(r.ignored, keyValue{verifyKey, verifyNode})
		} else {
			r.r.Verify = lv
		}
	}
	if dec != Deny && r.r.Verify == "" {
		r.r.Verify = p.defaults.Verify
	}
	if dec == Deny {
		r.r.Verify = ""
	}
	if r.r.Notify == nil && dec != Allow {
		r.r.Notify = []string{}
	}
	return r
}
