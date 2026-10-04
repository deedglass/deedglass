package policy

import (
	"cel.dev/cel-go/common/ast"
)

// ReadsAnomaly reports whether any rule's when or any budget's measure reads the anomaly variable
// (S27 §3.8). Insights uses it to say so when a policy reads anomaly.active while the org's anomaly
// facts are off (the policy then always sees false). It walks the checked expressions, so a
// mention inside a string literal doesn't count. A nil policy reads nothing.
func ReadsAnomaly(c *Compiled) bool {
	if c == nil {
		return false
	}
	env, err := Env()
	if err != nil {
		return false
	}
	reads := func(src string) bool {
		if src == "" || src == "true" {
			return false
		}
		a, iss := env.Compile(src)
		if iss.Err() != nil {
			return false
		}
		root := ast.NavigateAST(a.NativeRep())
		return len(ast.MatchDescendants(root, func(e ast.NavigableExpr) bool {
			return e.Kind() == ast.IdentKind && e.AsIdent() == "anomaly"
		})) > 0
	}
	for i := range c.Rules {
		if reads(c.Rules[i].WhenCanonical) {
			return true
		}
	}
	for i := range c.Budgets {
		if reads(c.Budgets[i].MeasureCanonical) {
			return true
		}
	}
	return false
}
