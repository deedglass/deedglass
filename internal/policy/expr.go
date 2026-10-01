package policy

import (
	"fmt"
	"regexp"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"go.yaml.in/yaml/v3"
)

// exprKind says what an expression must return.
type exprKind int

const (
	exprWhen    exprKind = iota // bool or dyn
	exprMeasure                 // int or dyn
)

// compiledExpr is one checked CEL expression.
type compiledExpr struct {
	prog      cel.Program
	canonical string
	cost      uint64
}

// exprCtx carries what a diagnostic about one expression needs.
type exprCtx struct {
	src    *source
	d      *diags
	node   *yaml.Node
	path   string
	ruleID string
	text   []rune
	pmap   []Pos // value rune → file position; nil when unknown
	lines  []int // rune offset of each expression line start
}

// at returns the file span of expression runes [from, to).
func (c *exprCtx) at(from, to int) span {
	if c.pmap == nil {
		return spanOf(c.src, c.node)
	}
	if from < 0 {
		from = 0
	}
	if from >= len(c.pmap) {
		from = len(c.pmap) - 1
	}
	if to <= from {
		to = from + 1
	}
	if to >= len(c.pmap) {
		to = len(c.pmap) - 1
	}
	s := c.pmap[from]
	e := c.pmap[to]
	if to == len(c.pmap)-1 || e.Line != s.Line || e.Col <= s.Col {
		// Stop the underline at the end of the start line.
		last := c.pmap[max(from, to-1)]
		if last.Line == s.Line {
			e = Pos{s.Line, last.Col + 1}
		} else {
			e = Pos{s.Line, s.Col + 1}
		}
	}
	return span{Start: s, End: e}
}

// offset converts CEL's 1-based line and 0-based column (in runes) to a rune offset.
func (c *exprCtx) offset(line, col int) int {
	if line < 1 || line > len(c.lines) {
		return 0
	}
	return c.lines[line-1] + col
}

func (c *exprCtx) err(code string, sp span, msg, hint string) {
	c.d.add(SevError, code, sp, c.path, c.ruleID, msg, hint)
}

// compileExpr parses, checks, validates and costs one `when` or `measure`.
func compileExpr(env *cel.Env, src *source, d *diags, n *yaml.Node, path, ruleID string, kind exprKind) (*compiledExpr, bool) {
	field := "when"
	if kind == exprMeasure {
		field = "measure"
	}
	if !isText(n) {
		d.errorf("wrong_type", spanOf(src, n), path, ruleID, "", "`%s` must be a CEL expression; found %s.", field, kindName(n))
		return nil, false
	}
	text := n.Value
	if strings.TrimSpace(text) == "" {
		d.errorf("wrong_type", spanOf(src, n), path, ruleID, "Write a condition, or remove the key.", "`%s` is empty.", field)
		return nil, false
	}
	c := &exprCtx{src: src, d: d, node: n, path: path, ruleID: ruleID, text: []rune(text), pmap: sourceMap(src, n)}
	if c.pmap != nil && len(c.pmap) != len(c.text)+1 {
		c.pmap = nil
	}
	c.lines = []int{0}
	for i, r := range c.text {
		if r == '\n' {
			c.lines = append(c.lines, i+1)
		}
	}
	if len(c.text) > MaxExprChars {
		d.errorf("cel_too_long", spanOf(src, n), path, ruleID, "Split the rule, or move a list into `match`.",
			"This `%s` is %s characters; the limit is %s.", field, thousands(len(c.text)), thousands(MaxExprChars))
		return nil, false
	}
	if _, desc, bad := badChar(text, true); bad {
		d.errorf("control_character", spanOf(src, n), path, ruleID, "Remove it. It can make text display differently from what it says.",
			"This `%s` contains %s.", field, desc)
		return nil, false
	}
	parsed, iss := env.Parse(text)
	if iss.Err() != nil {
		c.syntaxError(iss, field)
		return nil, false
	}
	checked, iss := env.Check(parsed)
	if iss.Err() != nil {
		c.checkErrors(iss, parsed, field)
		return nil, false
	}
	out := checked.OutputType()
	switch kind {
	case exprWhen:
		if !out.IsExactType(types.BoolType) && !out.IsExactType(types.DynType) {
			c.err("cel_not_bool", spanOf(src, n), fmt.Sprintf("`when` must be true or false; this expression returns %s.", typeNoun(out)),
				"Compare it with something, like `args.status == \"open\"`.")
			return nil, false
		}
	case exprMeasure:
		if !out.IsExactType(types.IntType) && !out.IsExactType(types.DynType) {
			c.err("cel_not_int", spanOf(src, n), fmt.Sprintf("`measure` must be a whole number of minor units; this expression returns %s.", typeNoun(out)),
				"Return an int, like `args.amount`, or guard it: `has(args.amount) ? args.amount : 0`.")
			return nil, false
		}
	}
	if !c.quotedNumbers(checked) {
		return nil, false
	}
	est, err := env.EstimateCost(checked, sizeHints{})
	if err != nil {
		c.err("cel_cost", spanOf(src, n), "Deedglass could not estimate what this condition costs.", "Simplify it, or split the rule.")
		return nil, false
	}
	if est.Max > MaxCost {
		what := "condition"
		if kind == exprMeasure {
			what = "measure"
		}
		c.err("cel_cost", spanOf(src, n),
			fmt.Sprintf("This %s could cost up to %s units per call; the limit is %s.", what, costString(est.Max), thousands(MaxCost)),
			"Iterate a smaller list (sql.tables has at most 64 entries), avoid a loop inside a loop over args, or split the rule.")
		return nil, false
	}
	canon, err := cel.AstToString(checked)
	if err != nil {
		c.err("internal_compiler_error", spanOf(src, n), "Deedglass could not compile this policy (an internal error), so it was not accepted.",
			"Report it with the file; it is a bug in Deedglass.")
		return nil, false
	}
	prog, err := env.Program(checked, cel.EvalOptions(cel.OptOptimize), cel.CostLimit(MaxCost), cel.InterruptCheckFrequency(InterruptEvery))
	if err != nil {
		c.err("internal_compiler_error", spanOf(src, n), "Deedglass could not compile this policy (an internal error), so it was not accepted.",
			"Report it with the file; it is a bug in Deedglass.")
		return nil, false
	}
	return &compiledExpr{prog: prog, canonical: canon, cost: est.Max}, true
}

func costString(n uint64) string {
	if n >= 1<<62 {
		return "more than " + thousands(uint64(1)<<62)
	}
	return thousands(n)
}

func typeNoun(t *types.Type) string {
	switch t.Kind() {
	case types.StringKind:
		return "a string"
	case types.IntKind, types.UintKind, types.DoubleKind:
		return "a number"
	case types.BoolKind:
		return "true or false"
	case types.ListKind:
		return "a list"
	case types.MapKind:
		return "a map"
	case types.TimestampKind:
		return "a timestamp"
	case types.DurationKind:
		return "a duration"
	case types.NullTypeKind:
		return "null"
	case types.StructKind:
		return "an object"
	}
	return "a " + t.String()
}

var (
	loneEquals = regexp.MustCompile(`(^|[^=!<>])=([^=]|$)`)
	wordOps    = regexp.MustCompile(`\b(and|or|not)\b`)
)

// syntaxError reports the first parse error, with the specific advice for `=` and `and`/`or`/`not`.
func (c *exprCtx) syntaxError(iss *cel.Issues, field string) {
	errs := iss.Errors()
	e := errs[0]
	msg := strings.TrimPrefix(e.Message, "Syntax error: ")
	code := "cel_syntax"
	hint := "Check the expression against the CEL operators: ==, !=, <, <=, >, >=, &&, ||, !, in."
	plain := maskStrings(c.text)
	start := c.offset(e.Location.Line(), e.Location.Column())
	width := 1
	switch {
	case strings.Contains(msg, "recursion") || strings.Contains(msg, "nesting"):
		msg = fmt.Sprintf("This `%s` is nested too deeply to read (more than %d levels).", field, MaxParserRecursion)
		hint = "Split the rule, or use `in [...]` instead of a long chain of `||`."
		c.err(code, spanOf(c.src, c.node), msg, hint)
		return
	case strings.Contains(msg, "size") && strings.Contains(msg, "exceeds"):
		c.err("cel_too_long", spanOf(c.src, c.node), fmt.Sprintf("This `%s` is longer than %s characters.", field, thousands(MaxExprChars)),
			"Split the rule, or move a list into `match`.")
		return
	}
	if loc := loneEquals.FindStringIndex(plain); loc != nil {
		i := loc[0]
		if plain[i] != '=' {
			i++
		}
		start, width = runeIndex(plain, i), 1
		msg = fmt.Sprintf("Syntax error in `%s`: unexpected token '='.", field)
		hint = "Compare with `==`."
	} else if loc := wordOps.FindStringIndex(plain); loc != nil {
		w := plain[loc[0]:loc[1]]
		start, width = runeIndex(plain, loc[0]), loc[1]-loc[0]
		op := map[string]string{"and": "&&", "or": "||", "not": "!"}[w]
		msg = fmt.Sprintf("Syntax error in `%s`: `%s` is not an operator in CEL.", field, w)
		hint = fmt.Sprintf("Write `%s` instead of `%s`.", op, w)
	} else {
		msg = fmt.Sprintf("Syntax error in `%s`: %s.", field, strings.TrimSuffix(cleanCELMsg(msg), "."))
	}
	c.err(code, c.at(start, start+width), msg, hint)
}

// maskStrings blanks out string literals so operator checks don't look inside them.
func maskStrings(text []rune) string {
	out := make([]rune, len(text))
	var q rune
	for i := 0; i < len(text); i++ {
		r := text[i]
		switch {
		case q == 0 && (r == '"' || r == '\''):
			q = r
			out[i] = r
		case q != 0 && r == '\\' && i+1 < len(text):
			out[i], out[i+1] = ' ', ' '
			i++
		case q != 0 && r == q:
			q = 0
			out[i] = r
		case q != 0:
			out[i] = ' '
		default:
			out[i] = r
		}
	}
	return string(out)
}

// runeIndex converts a byte index in s to a rune index.
func runeIndex(s string, b int) int { return runeLen(s[:b]) }

func cleanCELMsg(m string) string {
	m = strings.ReplaceAll(m, "<EOF>", "the end")
	m = strings.ReplaceAll(m, "token recognition error at: ", "unexpected character ")
	return m
}

var (
	undeclaredRE = regexp.MustCompile(`^undeclared reference to '([^']+)'`)
	noFieldRE    = regexp.MustCompile(`^undefined field '([^']+)'`)
	overloadRE   = regexp.MustCompile(`^found no matching overload for '([^']+)' applied to '\((.*)\)'`)
)

var operatorNames = map[string]string{"_<_": "<", "_<=_": "<=", "_>_": ">", "_>=_": ">=", "_==_": "==", "_!=_": "!=",
	"_+_": "+", "_-_": "-", "_*_": "*", "_/_": "/", "_%_": "%", "_&&_": "&&", "_||_": "||", "!_": "!", "@in": "in", "-_": "-"}

// checkErrors maps type-check and validator issues to diagnostics.
func (c *exprCtx) checkErrors(iss *cel.Issues, parsed *cel.Ast, field string) {
	info := parsed.NativeRep().SourceInfo()
	for _, e := range iss.Errors() {
		start := c.offset(e.Location.Line(), e.Location.Column())
		end := start + 1
		if e.ExprID != 0 {
			if x := findExpr(parsed, e.ExprID); x != nil {
				if r, ok := exprRange(info, x); ok {
					start, end = int(r.Start), int(r.Stop)
				}
			} else if r, ok := info.GetOffsetRange(e.ExprID); ok {
				start, end = int(r.Start), int(r.Stop)
			}
		}
		msg := e.Message
		switch {
		case strings.HasPrefix(msg, "dg:"):
			parts := strings.SplitN(strings.TrimPrefix(msg, "dg:"), "|", 3)
			for len(parts) < 3 {
				parts = append(parts, "")
			}
			c.err(parts[0], c.at(start, end), parts[1], parts[2])
		case undeclaredRE.MatchString(msg):
			name := undeclaredRE.FindStringSubmatch(msg)[1]
			start = c.offset(e.Location.Line(), e.Location.Column())
			s := c.identAt(start)
			hint := "Available: " + quoteList(Variables) + "."
			if name == "now" {
				hint = "Use `time.now`; a policy has no clock of its own."
			} else if sg := suggest(name, Variables); sg != "" {
				hint = fmt.Sprintf("Did you mean %q? %s", sg, hint)
			}
			c.err("cel_undeclared", c.at(start, start+max(runeLen(s), 1)), fmt.Sprintf("Unknown name %q.", clip(name)), hint)
		case noFieldRE.MatchString(msg):
			name := noFieldRE.FindStringSubmatch(msg)[1]
			start = c.offset(e.Location.Line(), e.Location.Column())
			recv := c.receiverBefore(start)
			fstart := start
			if fstart < len(c.text) && c.text[fstart] == '.' {
				fstart++
			}
			fields := celFields(factTypes[recv])
			hint := ""
			if sg := suggest(name, fields); sg != "" {
				hint = fmt.Sprintf("Did you mean %q?", sg)
			} else if len(fields) > 0 {
				hint = "Fields: " + quoteList(fields) + "."
			}
			who := "This object"
			if recv != "" {
				who = "`" + recv + "`"
			}
			c.err("cel_no_field", c.at(fstart, fstart+runeLen(name)), fmt.Sprintf("%s has no field %q.", who, clip(name)), hint)
		case overloadRE.MatchString(msg):
			m := overloadRE.FindStringSubmatch(msg)
			op, argTypes := m[1], splitTypes(m[2])
			snippet := clip(string(c.text[clampOff(start, len(c.text)):clampOff(end, len(c.text))]))
			if sym, ok := operatorNames[op]; ok && len(argTypes) == 2 {
				a, b := friendlyType(argTypes[0]), friendlyType(argTypes[1])
				if a != b {
					c.err("cel_type", c.at(start, end), fmt.Sprintf("`%s` compares %s with %s.", snippet, a, b),
						"Compare values of the same type; quotes make a string.")
				} else {
					c.err("cel_type", c.at(start, end), fmt.Sprintf("`%s` can't use `%s` with %s and %s.", snippet, sym, argTypes[0], argTypes[1]),
						"Compare values of the same type.")
				}
			} else {
				c.err("cel_type", c.at(start, end), fmt.Sprintf("`%s` can't be applied to %s.", snippet, strings.Join(argTypes, " and ")),
					"Check the argument types; see the CEL environment in docs/POLICY_LANGUAGE.md.")
			}
		case strings.Contains(msg, "nesting limit"):
			start = c.offset(e.Location.Line(), e.Location.Column())
			for start > 0 && start <= len(c.text) && (start == len(c.text) || c.text[start] == '(') && isIdent(c.text[start-1]) {
				start--
			}
			for start > 0 && isIdent(c.text[start-1]) {
				start--
			}
			end = start + runeLen(c.identAt(start))
			c.err("cel_nesting", c.at(start, end), fmt.Sprintf("Comprehensions are nested %d deep; the limit is %d.", nestingDepth(parsed), MaxNesting),
				"Flatten the condition, or split it into two rules.")
		default:
			c.err("cel_type", c.at(start, end), fmt.Sprintf("This `%s` does not type-check: %s.", field, strings.TrimSuffix(cleanCELMsg(msg), ".")),
				"Check the names and types against the CEL environment in docs/POLICY_LANGUAGE.md.")
		}
	}
}

func clampOff(i, n int) int {
	if i < 0 {
		return 0
	}
	if i > n {
		return n
	}
	return i
}

func splitTypes(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ", ")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

func friendlyType(t string) string {
	switch {
	case t == "int" || t == "uint" || t == "double":
		return "a number"
	case t == "string":
		return "a string"
	case t == "bool":
		return "true or false"
	case strings.HasPrefix(t, "list"):
		return "a list"
	case strings.HasPrefix(t, "map"):
		return "a map"
	case strings.HasPrefix(t, "google.protobuf.Timestamp") || t == "timestamp":
		return "a timestamp"
	case strings.HasPrefix(t, "google.protobuf.Duration") || t == "duration":
		return "a duration"
	case t == "null_type":
		return "null"
	}
	return t
}

func isIdent(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func (c *exprCtx) identAt(i int) string {
	if i < 0 || i >= len(c.text) {
		return ""
	}
	j := i
	for j < len(c.text) && isIdent(c.text[j]) {
		j++
	}
	return string(c.text[i:j])
}

// receiverBefore returns the identifier right before the '.' at i, when it is a whole variable
// (`sql` in `sql.knid`, not `x` in `a.x.knid`).
func (c *exprCtx) receiverBefore(i int) string {
	if i <= 0 || i > len(c.text) {
		return ""
	}
	j := i
	for j > 0 && isIdent(c.text[j-1]) {
		j--
	}
	if j > 0 && c.text[j-1] == '.' {
		return ""
	}
	return string(c.text[j:i])
}

func nestingDepth(a *cel.Ast) int {
	var depth func(e ast.Expr) int
	depth = func(e ast.Expr) int {
		best := 0
		switch e.Kind() {
		case ast.ComprehensionKind:
			cp := e.AsComprehension()
			inner := max(depth(cp.LoopStep()), depth(cp.LoopCondition()), depth(cp.Result()), depth(cp.AccuInit()))
			return max(1+inner, depth(cp.IterRange()))
		case ast.CallKind:
			call := e.AsCall()
			if call.IsMemberFunction() {
				best = depth(call.Target())
			}
			for _, a := range call.Args() {
				best = max(best, depth(a))
			}
		case ast.SelectKind:
			best = depth(e.AsSelect().Operand())
		case ast.ListKind:
			for _, x := range e.AsList().Elements() {
				best = max(best, depth(x))
			}
		case ast.MapKind:
			for _, en := range e.AsMap().Entries() {
				me := en.AsMapEntry()
				best = max(best, depth(me.Key()), depth(me.Value()))
			}
		}
		return best
	}
	return depth(a.NativeRep().Expr())
}

// findExpr finds the subexpression with this id in a parsed AST.
func findExpr(a *cel.Ast, id int64) ast.Expr {
	root := ast.NavigateAST(a.NativeRep())
	found := ast.MatchDescendants(root, func(e ast.NavigableExpr) bool { return e.ID() == id })
	if len(found) == 0 {
		return nil
	}
	return found[0]
}

var numericString = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// quotedNumbers refuses `<`, `<=`, `>` and `>=` between a non-string value and a quoted number:
// "5000" is a string, and strings compare letter by letter.
func (c *exprCtx) quotedNumbers(checked *cel.Ast) bool {
	a := checked.NativeRep()
	info := a.SourceInfo()
	ok := true
	for _, e := range ast.MatchDescendants(ast.NavigateAST(a), ast.KindMatcher(ast.CallKind)) {
		call := e.AsCall()
		sym, cmp := map[string]string{"_<_": "<", "_<=_": "<=", "_>_": ">", "_>=_": ">="}[call.FunctionName()]
		if !cmp || len(call.Args()) != 2 {
			continue
		}
		for side, lit := range call.Args() {
			s, isStr := constString(lit)
			if !isStr || !numericString.MatchString(s) {
				continue
			}
			other := call.Args()[1-side]
			if a.GetType(other.ID()).Kind() == types.StringKind {
				continue
			}
			r, has := info.GetOffsetRange(lit.ID())
			cr, hasCall := callRange(info, call.Args())
			if !has || !hasCall {
				continue
			}
			snippet := string(c.text[clampOff(int(cr.Start), len(c.text)):clampOff(int(cr.Stop), len(c.text))])
			fixed := strings.Replace(snippet, `"`+s+`"`, s, 1)
			fixed = strings.Replace(fixed, `'`+s+`'`, s, 1)
			hint := fmt.Sprintf("Compare numbers with numbers: `%s`.", fixed)
			if strings.Contains(strings.ToLower(snippet), "amount") {
				hint = fmt.Sprintf("Compare numbers with numbers: `%s` (minor units, so %s is $%s).", fixed, s, centsToDollars(s))
			}
			c.err("cel_type", c.at(int(r.Start), int(r.Stop)), fmt.Sprintf("`%s` compares a number with a string.", clip(snippet)), hint)
			_ = sym
			ok = false
		}
	}
	return ok
}

func callRange(info *ast.SourceInfo, args []ast.Expr) (ast.OffsetRange, bool) {
	out := ast.OffsetRange{Start: 1 << 30, Stop: -1}
	for _, a := range args {
		r, ok := exprRange(info, a)
		if !ok {
			return ast.OffsetRange{}, false
		}
		out.Start, out.Stop = min(out.Start, r.Start), max(out.Stop, r.Stop)
	}
	return out, true
}

// exprRange is the source range of a whole subexpression: CEL records a select at its dot and a
// call at its operator, so take the union over the subtree.
func exprRange(info *ast.SourceInfo, e ast.Expr) (ast.OffsetRange, bool) {
	r, ok := info.GetOffsetRange(e.ID())
	if !ok {
		return r, false
	}
	grow := func(x ast.Expr) {
		if s, ok := exprRange(info, x); ok {
			r.Start, r.Stop = min(r.Start, s.Start), max(r.Stop, s.Stop)
		}
	}
	switch e.Kind() {
	case ast.SelectKind:
		// CEL records a select at its dot; the field name follows it.
		r.Stop = max(r.Stop, r.Start+1+int32(runeLen(e.AsSelect().FieldName())))
		grow(e.AsSelect().Operand())
	case ast.CallKind:
		c := e.AsCall()
		if c.IsMemberFunction() {
			grow(c.Target())
		}
		for _, a := range c.Args() {
			grow(a)
		}
	case ast.ListKind:
		for _, x := range e.AsList().Elements() {
			grow(x)
		}
	}
	return r, true
}

func centsToDollars(s string) string {
	if strings.Contains(s, ".") || strings.HasPrefix(s, "-") {
		return s
	}
	for len(s) < 3 {
		s = "0" + s
	}
	return thousandsStr(s[:len(s)-2]) + "." + s[len(s)-2:]
}

func thousandsStr(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

var _ = common.NoLocation
