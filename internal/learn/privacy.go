package learn

// Phrasing privacy (S28 §3.7). PhrasingInput is the ONLY type that crosses to the phrasing
// client. It carries the generator's kind, product-defined tool and connector type names,
// decisions, numbers, and the template text with a {placeholder} for every name an org chose
// (agents, rules, tables, budgets, policies, its own tools). It never carries arguments, SQL,
// results, on-behalf-of or session refs, emails, org or user names, action ids, the YAML, the diff
// or the evidence JSON. The placeholders' values stay here (Values is never serialized), and the
// reply is resolved locally.

// PhrasingInput is the allow-listed request content.
type PhrasingInput struct {
	Kind              string            `json:"kind"`
	Generator         string            `json:"generator"`
	Tool              string            `json:"tool,omitempty"`
	ConnectorType     string            `json:"connector_type,omitempty"`
	Before            string            `json:"before,omitempty"`
	After             string            `json:"after,omitempty"`
	WindowDays        int               `json:"window_days"`
	Refs              map[string]string `json:"refs"` // placeholder → kind (agent, rule, table, budget, policy, tool)
	Numbers           []Number          `json:"numbers"`
	TemplateTitle     string            `json:"template_title"`
	TemplateRationale string            `json:"template_rationale"`

	values map[string]string // placeholder → the org's name; local only
}

// PhrasingInputKeys are the JSON keys PhrasingInput may send (the privacy test pins them).
var PhrasingInputKeys = []string{"kind", "generator", "tool", "connector_type", "before", "after", "window_days", "refs", "numbers",
	"template_title", "template_rationale"}

// Values are the placeholders' local values.
func (in *PhrasingInput) Values() map[string]string { return in.values }

// BuildPhrasingInput is the phrasing request for a proposal.
func BuildPhrasingInput(p *Proposal, windowDays int) *PhrasingInput {
	refs := p.Refs
	if refs == nil {
		refs = NewRefs()
	}
	tool := ""
	if p.Tool != "" {
		tool = refs.toolRef(p.Tool)
	}
	ct := p.ConnectorType
	if ct != "" && !productConnectors[ct] {
		ct = ""
	}
	in := &PhrasingInput{Kind: p.Kind, Generator: p.Generator, Tool: tool, ConnectorType: ct, Before: p.Before, After: p.After,
		WindowDays: windowDays, Refs: map[string]string{}, Numbers: append([]Number{}, p.Numbers...),
		TemplateTitle: p.TemplateTitle, TemplateRationale: p.TemplateRationale, values: map[string]string{}}
	for k, v := range refs.Kinds {
		in.Refs[k] = v
	}
	for k, v := range refs.Values {
		in.values[k] = v
	}
	return in
}
