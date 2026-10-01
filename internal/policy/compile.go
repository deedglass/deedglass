package policy

import (
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/internal/deed"
)

// Options configures Compile.
type Options struct {
	File  string // used in messages only
	Voice bool   // opt-in brand-voice warnings (reason_style)
}

// Compile reads, checks and compiles a policy. It is deterministic and never panics: an internal
// failure becomes internal_compiler_error. A nil *Compiled means the policy was not accepted; the
// diagnostics (errors and warnings, sorted by position) say why.
func Compile(src []byte, opts Options) (*Compiled, []Diagnostic) {
	c, out, _ := compile(src, opts)
	return c, out
}

func compile(src []byte, opts Options) (c *Compiled, out []Diagnostic, p *draft) {
	d := &diags{file: opts.File}
	defer func() {
		if r := recover(); r != nil {
			c = nil
			d.list = append(d.list, Diagnostic{Severity: SevError, Code: "internal_compiler_error", File: opts.File,
				Message: "Deedglass could not compile this policy (an internal error), so it was not accepted.",
				Hint:    "Report it with the file; it is a bug in Deedglass.", DocURL: DocBase + "internal_compiler_error"})
			out = finish(d.list)
		}
	}()
	s := newSource(src)
	root := readYAML(s, d)
	if root == nil {
		return nil, finish(d.list), nil
	}
	p = validate(s, d, root)
	env, err := Env()
	if err != nil {
		panic(fmt.Sprintf("policy: CEL environment: %v", err))
	}
	for _, b := range p.budgets {
		if b.measure == nil || badTag(b.measure) {
			continue
		}
		if x, ok := compileExpr(env, s, d, b.measure, b.path+".measure", b.b.ID, exprMeasure); ok {
			b.b.Measure, b.b.MeasureSource, b.b.MeasureCanonical, b.b.MeasureCostMax = x.prog, b.measure.Value, x.canonical, x.cost
		}
	}
	for _, r := range p.rules {
		r.r.WhenCanonical = "true"
		if r.when == nil {
			continue
		}
		if badTag(r.when) {
			continue // already reported as yaml_tag
		}
		if x, ok := compileExpr(env, s, d, r.when, r.path+".when", r.r.ID, exprWhen); ok {
			r.r.When, r.r.WhenSource, r.r.WhenCanonical, r.r.WhenCostMax = x.prog, r.when.Value, x.canonical, x.cost
		}
	}
	warn(s, d, p, opts)
	list := finish(d.list)
	if d.hasErrors() {
		return nil, list, p
	}
	norm, err := normalize(p)
	if err != nil {
		panic(fmt.Sprintf("policy: normalize: %v", err))
	}
	c = &Compiled{
		Name: p.name, Description: p.description, Lang: Lang,
		VersionHash: deed.Digest(norm), SourceHash: deed.Digest(src), Normalized: norm,
		Defaults: p.defaults,
	}
	for _, b := range p.budgets {
		c.Budgets = append(c.Budgets, b.b)
	}
	for _, r := range p.rules {
		c.Rules = append(c.Rules, r.r)
	}
	_, c.Warnings = split(list)
	return c, list, p
}

func badTag(n *yaml.Node) bool { return n.Style&yaml.TaggedStyle != 0 && !coreTags[n.Tag] }
