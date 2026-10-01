package witness

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/checker"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/parser"
)

// CEL limits for postconditions (S19 §3.5). The module and version are S10's pin (ADR-017).
// Never ext.NativeTypes: GO-2026-6094 (GHSA-gcjh-h69q-9w9g; cel-go v0.22.0 up to v0.30.0, fixed in
// v0.30.0, checked on pkg.go.dev 2026-10-02) exposed private struct fields through it. We are on
// v0.32.0, and every variable here is a plain map anyway.
const (
	CostLimit      = 100_000
	InterruptEvery = 64
	MaxExprRunes   = 2048
	maxDiffOps     = 16
)

// Size hints for the cost estimate: CEL can't know how big a value is.
const (
	hintString = 4096
	hintList   = 256
	hintDyn    = 256 // a dyn value iterated as a list is assumed to hold 256 items, like lists and maps
)

var (
	celOnce sync.Once
	celEnv  *cel.Env
	celErr  error
)

// postEnv is the one CEL environment for postconditions: four dyn maps.
func postEnv() (*cel.Env, error) {
	celOnce.Do(func() {
		dyn := cel.MapType(cel.StringType, cel.DynType)
		celEnv, celErr = cel.NewEnv(
			cel.Variable("args", dyn),
			cel.Variable("request", dyn),
			cel.Variable("result", dyn),
			cel.Variable("observation", dyn),
			cel.CrossTypeNumericComparisons(true),
			cel.DefaultUTCTimeZone(true),
			cel.ParserRecursionLimit(64),
			cel.ParserExpressionSizeLimit(MaxExprRunes),
		)
	})
	return celEnv, celErr
}

// Postcondition is a compiled CEL postcondition, with programs for the operands of each top-level
// comparison so a false result can say which field disagreed.
type Postcondition struct {
	Source string
	prog   cel.Program
	ops    []conjunct
}

type conjunct struct {
	whole       cel.Program // the conjunct itself
	op          string      // ==, !=, <, <=, >, >= ("" when the conjunct isn't a comparison)
	left, right cel.Program
	leftText    string
}

// CompileError is a postcondition that didn't compile: Line and Column are 1-based (0 when
// unknown); Message is a sentence for the connector config's field error.
type CompileError struct {
	Line, Column int
	Message      string
}

func (e *CompileError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, e.Message)
	}
	return e.Message
}

// knownRoots are the variables a postcondition may use; anything else gets a hint.
var knownRoots = []string{"args", "request", "result", "observation"}

// CompilePostcondition parses, type-checks and cost-checks a postcondition. It must return a bool,
// and its estimated cost must stay under CostLimit.
func CompilePostcondition(src string) (*Postcondition, error) {
	if strings.TrimSpace(src) == "" {
		return nil, &CompileError{Message: "The postcondition is empty."}
	}
	if len([]rune(src)) > MaxExprRunes {
		return nil, &CompileError{Message: "The postcondition is longer than 2,048 characters."}
	}
	env, err := postEnv()
	if err != nil {
		return nil, err
	}
	parsed, iss := env.Parse(src)
	if iss.Err() != nil {
		return nil, issueError(iss, "")
	}
	if e := unknownRoot(parsed.NativeRep()); e != nil {
		return nil, e
	}
	checked, iss := env.Check(parsed)
	if iss.Err() != nil {
		return nil, issueError(iss, "")
	}
	// Exactly bool: every variable is a dyn map, so a bare field (args.id) checks as dyn and
	// would only fail at run time, as a gap on every call.
	if out := checked.OutputType(); !out.IsExactType(types.BoolType) {
		return nil, &CompileError{Message: "The postcondition must be true or false. Compare a field, like observation.body.status == args.status."}
	}
	est, err := env.EstimateCost(checked, sizeHints{})
	if err != nil {
		return nil, &CompileError{Message: "Deedglass couldn't estimate what this condition costs. Simplify it."}
	}
	if est.Max > CostLimit {
		return nil, &CompileError{Message: fmt.Sprintf("This condition could cost up to %s units per call; the limit is %s. Compare fewer fields, or use expect.",
			thousands(est.Max), thousands(CostLimit))}
	}
	prog, err := program(env, checked)
	if err != nil {
		return nil, err
	}
	pc := &Postcondition{Source: src, prog: prog}
	pc.ops = conjuncts(env, checked.NativeRep())
	return pc, nil
}

func program(env *cel.Env, a *cel.Ast) (cel.Program, error) {
	return env.Program(a, cel.EvalOptions(cel.OptOptimize), cel.CostLimit(CostLimit), cel.InterruptCheckFrequency(InterruptEvery))
}

// unknownRoot names a variable that isn't one of the four, with a hint (request.status → args.status).
func unknownRoot(a *celast.AST) *CompileError {
	info := a.SourceInfo()
	// Macros (all, exists, map, filter) expand to comprehensions whose loop and accumulator
	// variables (x, @result) are identifiers too; they are bound, not roots.
	bound := map[string]bool{}
	for _, e := range celast.MatchDescendants(celast.NavigateAST(a), celast.KindMatcher(celast.ComprehensionKind)) {
		c := e.AsComprehension()
		bound[c.IterVar()], bound[c.AccuVar()] = true, true
		if c.HasIterVar2() {
			bound[c.IterVar2()] = true
		}
	}
	for _, e := range celast.MatchDescendants(celast.NavigateAST(a), celast.KindMatcher(celast.IdentKind)) {
		name := e.AsIdent()
		known := bound[name]
		for _, k := range knownRoots {
			known = known || name == k
		}
		if known {
			continue
		}
		loc := info.GetStartLocation(e.ID())
		return &CompileError{Line: loc.Line(), Column: loc.Column() + 1,
			Message: fmt.Sprintf("%s isn't a variable. Use args, request, result or observation.", name)}
	}
	// request has only tool, args and mode: request.status is the common slip for args.status.
	for _, e := range celast.MatchDescendants(celast.NavigateAST(a), celast.KindMatcher(celast.SelectKind)) {
		sel := e.AsSelect()
		if sel.Operand().Kind() == celast.IdentKind && sel.Operand().AsIdent() == "request" {
			switch sel.FieldName() {
			case "tool", "args", "mode":
				continue
			}
			loc := info.GetStartLocation(sel.Operand().ID()) // where "request" starts, not the dot
			return &CompileError{Line: loc.Line(), Column: loc.Column() + 1,
				Message: fmt.Sprintf("request.%s isn't a field. Did you mean args.%s?", sel.FieldName(), sel.FieldName())}
		}
	}
	return nil
}

func issueError(iss *cel.Issues, _ string) error {
	errs := iss.Errors()
	if len(errs) == 0 {
		return &CompileError{Message: "The postcondition isn't valid CEL."}
	}
	e := errs[0]
	msg := strings.TrimSuffix(e.Message, ".")
	// The parser's list of expected tokens ("expecting {'[', '!', …}") helps no one reading a
	// config error, and carries an exclamation mark into the copy.
	if i := strings.Index(msg, " expecting "); i > 0 {
		msg = msg[:i]
	}
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	return &CompileError{Line: e.Location.Line(), Column: e.Location.Column() + 1, Message: msg + "."}
}

// conjuncts splits the top-level && chain and prepares programs for each comparison's operands.
func conjuncts(env *cel.Env, a *celast.AST) []conjunct {
	var parts []celast.Expr
	var walk func(e celast.Expr)
	walk = func(e celast.Expr) {
		if e.Kind() == celast.CallKind && e.AsCall().FunctionName() == "_&&_" {
			for _, x := range e.AsCall().Args() {
				walk(x)
			}
			return
		}
		parts = append(parts, e)
	}
	walk(a.Expr())
	if len(parts) > maxDiffOps {
		parts = parts[:maxDiffOps]
	}
	ops := map[string]string{"_==_": "==", "_!=_": "!=", "_<_": "<", "_<=_": "<=", "_>_": ">", "_>=_": ">="}
	var out []conjunct
	for _, p := range parts {
		c := conjunct{}
		txt, err := parser.Unparse(p, a.SourceInfo())
		if err != nil {
			continue
		}
		if c.whole, err = compileSub(env, txt); err != nil {
			continue
		}
		if p.Kind() == celast.CallKind {
			call := p.AsCall()
			if op, ok := ops[call.FunctionName()]; ok && len(call.Args()) == 2 {
				lt, err1 := parser.Unparse(call.Args()[0], a.SourceInfo())
				rt, err2 := parser.Unparse(call.Args()[1], a.SourceInfo())
				if err1 == nil && err2 == nil {
					l, err3 := compileSub(env, lt)
					r, err4 := compileSub(env, rt)
					if err3 == nil && err4 == nil {
						c.op, c.left, c.right, c.leftText = op, l, r, lt
					}
				}
			}
		}
		out = append(out, c)
	}
	return out
}

func compileSub(env *cel.Env, src string) (cel.Program, error) {
	a, iss := env.Compile(src)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	return program(env, a)
}

// postVars are the four variables, as plain maps.
func postVars(s Subject, o Observation) map[string]any {
	args, _ := decode(s.Args).(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	out := decode(s.Result.Output)
	meta := s.Result.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	return map[string]any{
		"args":        args,
		"request":     map[string]any{"tool": s.Tool, "args": args, "mode": s.Mode},
		"result":      map[string]any{"output": out, "meta": jsonish(meta)},
		"observation": jsonish(observationRoot(o)),
	}
}

// jsonish round-trips a value through JSON so CEL sees only maps, lists, strings, numbers and bools.
func jsonish(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return toCEL(decode(b))
}

// toCEL turns json.Number into int64 or float64, which CEL's dyn values understand.
func toCEL(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, w := range x {
			x[k] = toCEL(w)
		}
		return x
	case []any:
		for i, w := range x {
			x[i] = toCEL(w)
		}
		return x
	}
	return v
}

// errPostcondition is an evaluation that didn't produce a bool (an error, the cost limit).
var errPostcondition = errors.New("witness: the postcondition couldn't be evaluated")

// Eval runs the postcondition. held reports the result; diffs name the comparisons that were false.
func (p *Postcondition) Eval(s Subject, o Observation) (held bool, diffs []FieldDiff, err error) {
	vars := postVars(s, o)
	for _, k := range []string{"args", "result"} {
		if m, ok := vars[k].(map[string]any); ok {
			vars[k] = toCEL(m)
		}
	}
	if r, ok := vars["request"].(map[string]any); ok {
		r["args"] = vars["args"]
	}
	out, _, err := p.prog.Eval(vars)
	if err != nil {
		return false, nil, fmt.Errorf("%w: %s", errPostcondition, celMessage(err))
	}
	b, ok := out.(types.Bool)
	if !ok {
		return false, nil, fmt.Errorf("%w: it returned %s, not true or false", errPostcondition, out.Type().TypeName())
	}
	if b {
		return true, nil, nil
	}
	for _, c := range p.ops {
		if c.whole == nil {
			continue
		}
		// The whole condition already evaluated to false (CEL's && absorbs an error next to a
		// false), so a conjunct that errors here explains nothing and doesn't change the result.
		v, _, err := c.whole.Eval(vars)
		if err != nil {
			continue
		}
		if bv, ok := v.(types.Bool); ok && bool(bv) {
			continue
		}
		if c.op == "" {
			continue
		}
		l, _, err1 := c.left.Eval(vars)
		r, _, err2 := c.right.Eval(vars)
		if err1 != nil || err2 != nil {
			continue
		}
		diffs = append(diffs, FieldDiff{Path: c.leftText, Expected: native(r), Observed: native(l)})
	}
	return false, diffs, nil
}

func native(v ref.Val) any {
	x, err := v.ConvertToNative(anyType)
	if err != nil {
		return fmt.Sprint(v.Value())
	}
	return x
}

func celMessage(err error) string {
	s := err.Error()
	if strings.Contains(s, "cost limit") || strings.Contains(s, "actual cost limit exceeded") {
		return "the cost limit of 100,000 was reached"
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// sizeHints bound the cost estimate: strings 4 KiB, lists and maps 256 entries.
type sizeHints struct{}

func (sizeHints) EstimateSize(n checker.AstNode) *checker.SizeEstimate {
	if s := n.ComputedSize(); s != nil {
		return s
	}
	max := uint64(hintDyn)
	if t := n.Type(); t != nil {
		switch t.Kind() {
		case types.StringKind, types.BytesKind:
			max = hintString
		case types.ListKind, types.MapKind:
			max = hintList
		}
	}
	return &checker.SizeEstimate{Min: 0, Max: max}
}

func (sizeHints) EstimateCallCost(string, string, *checker.AstNode, []checker.AstNode) *checker.CallEstimate {
	return nil
}

func thousands(n uint64) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// The postcondition cache: compiled programs by (connector config hash, tool).
var pcCache sync.Map // string → *Postcondition

// cachedPostcondition compiles src once per (hash, tool); an empty hash isn't cached.
func cachedPostcondition(hash, tool, src string) (*Postcondition, error) {
	if hash == "" {
		return CompilePostcondition(src)
	}
	key := hash + "\x00" + tool + "\x00" + src
	if v, ok := pcCache.Load(key); ok {
		return v.(*Postcondition), nil
	}
	pc, err := CompilePostcondition(src)
	if err != nil {
		return nil, err
	}
	v, _ := pcCache.LoadOrStore(key, pc)
	return v.(*Postcondition), nil
}
