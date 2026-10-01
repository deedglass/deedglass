package policy

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/checker"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
)

// factTypes maps each structured variable to its Go struct. args is map(string, dyn).
var factTypes = map[string]reflect.Type{
	"tool":    reflect.TypeFor[ToolFacts](),
	"request": reflect.TypeFor[RequestFacts](),
	"sql":     reflect.TypeFor[SQLFacts](),
	"agent":   reflect.TypeFor[AgentFacts](),
	"user":    reflect.TypeFor[UserFacts](),
	"session": reflect.TypeFor[SessionFacts](),
	"org":     reflect.TypeFor[OrgFacts](),
	"time":    reflect.TypeFor[TimeFacts](),
	"usage":   reflect.TypeFor[UsageFacts](),
	"anomaly": reflect.TypeFor[AnomalyFacts](),
}

// celFields returns the CEL field names of a facts struct, in declaration order.
func celFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("cel"), ",")[0]
		if tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	return out
}

var (
	envOnce sync.Once
	envV1   *cel.Env
	envErr  error
)

// Env returns the one shared CEL environment of policy/v1. It is safe for concurrent use.
func Env() (*cel.Env, error) {
	envOnce.Do(func() { envV1, envErr = newEnv() })
	return envV1, envErr
}

func newEnv() (*cel.Env, error) {
	opts := []cel.EnvOption{
		ext.NativeTypes(
			reflect.TypeFor[ToolFacts](), reflect.TypeFor[RequestFacts](), reflect.TypeFor[SQLFacts](),
			reflect.TypeFor[AgentFacts](), reflect.TypeFor[UserFacts](), reflect.TypeFor[SessionFacts](),
			reflect.TypeFor[OrgFacts](), reflect.TypeFor[TimeFacts](), reflect.TypeFor[UsageFacts](),
			reflect.TypeFor[AnomalyFacts](),
			ext.ParseStructTag("cel")),
		cel.Variable("args", cel.MapType(cel.StringType, cel.DynType)),
		cel.CrossTypeNumericComparisons(true),
		cel.DefaultUTCTimeZone(true),
		cel.EnableMacroCallTracking(),
		cel.ParserRecursionLimit(MaxParserRecursion),
		cel.ParserExpressionSizeLimit(MaxExprChars),
		cel.ASTValidators(
			cel.ValidateDurationLiterals(),
			cel.ValidateTimestampLiterals(),
			cel.ValidateHomogeneousAggregateLiterals(),
			cel.ValidateComprehensionNestingLimit(MaxNesting),
			constantArgs{},
		),
		ext.Strings(ext.StringsVersion(extStringsVersion)),
		ext.Sets(ext.SetsVersion(extSetsVersion)),
		ext.Lists(ext.ListsVersion(extListsVersion)),
		cel.Function("glob",
			cel.Overload("glob_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType,
				cel.BinaryBinding(celGlob))),
		cel.Function("minor",
			cel.Overload("minor_string", []*cel.Type{cel.StringType}, cel.IntType,
				cel.UnaryBinding(celMinor))),
	}
	for _, name := range Variables {
		if t, ok := factTypes[name]; ok {
			opts = append(opts, cel.Variable(name, cel.ObjectType("policy."+t.Name())))
		}
	}
	return cel.NewEnv(opts...)
}

func celGlob(v, p ref.Val) ref.Val {
	s, ok1 := v.(types.String)
	pat, ok2 := p.(types.String)
	if !ok1 || !ok2 {
		return types.NewErr("glob needs two strings")
	}
	g, ok := CompileGlob(string(pat))
	if !ok {
		return types.NewErr("glob: invalid pattern")
	}
	return types.Bool(g.Match(string(s)))
}

func celMinor(v ref.Val) ref.Val {
	s, ok := v.(types.String)
	if !ok {
		return types.NewErr("minor needs a string")
	}
	m, prob := ParseMoney(string(s))
	if prob != nil {
		return types.NewErr("minor: %s", prob.Message)
	}
	return types.Int(m.Amount)
}

// validator issue messages carry their diagnostic code: "dg:<code>|<message>|<hint>".
func dgIssue(code, msg, hint string) string { return "dg:" + code + "|" + msg + "|" + hint }

// constantArgs requires constant patterns for glob() and matches(), and a constant, valid amount
// for minor(). Patterns come from the policy author, never from an agent's arguments.
type constantArgs struct{}

func (constantArgs) Name() string { return "deedglass.constant_args" }

func (constantArgs) Validate(_ *cel.Env, _ cel.ValidatorConfig, a *ast.AST, iss *cel.Issues) {
	root := ast.NavigateAST(a)
	for _, e := range ast.MatchDescendants(root, ast.KindMatcher(ast.CallKind)) {
		call := e.AsCall()
		args := call.Args()
		switch call.FunctionName() {
		case "glob":
			if len(args) == 2 {
				if s, ok := constString(args[1]); !ok {
					iss.ReportErrorAtID(args[1].ID(), "%s", dgIssue("cel_constant_required", "`glob()` needs a constant pattern.",
						"Patterns come from the policy, never from an agent's arguments: write it in quotes, like `glob(tool.name, \"stripe.*\")`."))
				} else if _, ok := CompileGlob(s); !ok || s == "" {
					iss.ReportErrorAtID(args[1].ID(), "%s", dgIssue("invalid_glob", fmt.Sprintf("`%s` is not a valid pattern.", clip(s)),
						"Patterns use `*` and `?` only, with letters, digits and _ . : -."))
				}
			}
		case "minor":
			if len(args) == 1 {
				if s, ok := constString(args[0]); !ok {
					iss.ReportErrorAtID(args[0].ID(), "%s", dgIssue("cel_constant_required", "`minor()` needs a constant amount.",
						`Write the amount in quotes, like minor("50.00 usd").`))
				} else if _, prob := ParseMoney(s); prob != nil {
					iss.ReportErrorAtID(args[0].ID(), "%s", dgIssue(prob.Code, prob.Message, prob.Hint))
				}
			}
		case "matches":
			var pat ast.Expr
			switch {
			case call.IsMemberFunction() && len(args) == 1:
				pat = args[0]
			case !call.IsMemberFunction() && len(args) == 2:
				pat = args[1]
			}
			if pat == nil {
				continue
			}
			s, ok := constString(pat)
			switch {
			case !ok:
				iss.ReportErrorAtID(pat.ID(), "%s", dgIssue("cel_regex", "The pattern in `matches()` must be a constant string.",
					"Patterns come from the policy, never from an agent's arguments: write the pattern in quotes."))
			case runeLen(s) > MaxRegex:
				iss.ReportErrorAtID(pat.ID(), "%s", dgIssue("cel_regex",
					fmt.Sprintf("The pattern in `matches()` is %s characters; the limit is %s.", thousands(runeLen(s)), thousands(MaxRegex)),
					"Shorten it, or split the condition."))
			default:
				if _, err := regexp.Compile(s); err != nil {
					iss.ReportErrorAtID(pat.ID(), "%s", dgIssue("cel_regex",
						fmt.Sprintf("The pattern in `matches()` is not valid RE2: %s.", re2Reason(err)),
						"Fix the pattern; RE2 has no backreferences or lookarounds."))
				}
			}
		}
	}
}

func constString(e ast.Expr) (string, bool) {
	if e.Kind() != ast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().(types.String)
	return string(s), ok
}

func re2Reason(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "error parsing regexp: ")
	if i := strings.Index(msg, ": `"); i >= 0 {
		msg = msg[:i]
	}
	return msg
}

// sizeHints is the cost estimator (§8): CEL can't know how big a value is, so these hints bound it.
type sizeHints struct{}

var listHints = map[string]uint64{
	"sql.tables": hintTables, "sql.functions": hintFunctions, "sql.dangerous_functions": hintFunctions,
	"sql.write_functions": hintFunctions, "sql.unknown_functions": hintFunctions,
	"agent.labels": hintLabels, "usage.budgets": hintBudgets,
}

func (h sizeHints) EstimateSize(n checker.AstNode) *checker.SizeEstimate {
	if s := n.ComputedSize(); s != nil {
		return s
	}
	return &checker.SizeEstimate{Min: 0, Max: sizeHint(n.Path(), n.Type())}
}

// sizeHint is the largest size a value at this path can have for estimation: declared fact
// strings (names, ids, kinds, table and function names) are short; anything else is 262,144.
func sizeHint(p []string, t *types.Type) uint64 {
	if len(p) == 1 && p[0] == "args" {
		return hintArgs
	}
	if len(p) == 2 {
		if h, ok := listHints[p[0]+"."+p[1]]; ok {
			return h
		}
	}
	if t == nil {
		return hintDyn
	}
	switch t.Kind() {
	case types.StringKind, types.BytesKind:
		if len(p) >= 2 && p[0] != "args" {
			if len(p) == 3 && p[0] == "agent" && p[1] == "labels" {
				return hintLabelValue
			}
			return hintFactString
		}
		return hintString
	case types.ListKind, types.MapKind:
		return hintList
	}
	return hintDyn
}

func (h sizeHints) EstimateCallCost(fn, _ string, _ *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	switch fn {
	case "glob":
		// The matcher walks the value once per star at worst; patterns are at most 200 runes.
		sz := hintString
		if len(args) == 2 {
			if c := args[0].ComputedSize(); c != nil {
				sz = int(c.Max)
			} else {
				sz = int(sizeHint(args[0].Path(), args[0].Type()))
			}
		}
		return &checker.CallEstimate{CostEstimate: checker.CostEstimate{Min: 1, Max: 1 + uint64(sz)/10}}
	case "minor":
		return &checker.CallEstimate{CostEstimate: checker.CostEstimate{Min: 1, Max: 10}}
	}
	return nil
}
