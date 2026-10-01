package policy

import (
	"encoding/json"

	"github.com/gowebpki/jcs"
)

// The normalized document (docs/POLICY_LANGUAGE.md §9): every default filled in, nothing about
// how the file was written. Its JCS bytes are what version_hash covers.
type normDoc struct {
	Lang        string       `json:"lang"`
	CEL         normCEL      `json:"cel"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Defaults    normDefaults `json:"defaults"`
	Budgets     []normBudget `json:"budgets"`
	Rules       []normRule   `json:"rules"`
}

type normCEL struct {
	Lists   int `json:"lists"`
	Sets    int `json:"sets"`
	Strings int `json:"strings"`
}

type normDefaults struct {
	Decision Outcome      `json:"decision"`
	Reason   string       `json:"reason"`
	Verify   Level        `json:"verify"`
	Approval normApproval `json:"approval"`
	Notify   []string     `json:"notify"`
}

type normApproval struct {
	Approvers []string `json:"approvers"`
	Required  int      `json:"required"`
	ExpiresIn int64    `json:"expires_in"`
}

type normMatch struct {
	Tool          []string          `json:"tool,omitempty"`
	ConnectorType []string          `json:"connector_type,omitempty"`
	SideEffect    []string          `json:"side_effect,omitempty"`
	Agent         []string          `json:"agent,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type normBudget struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Match       normMatch `json:"match"`
	Per         string    `json:"per"`
	Window      string    `json:"window"`
	MaxCalls    *int64    `json:"max_calls,omitempty"`
	MaxSpend    *Money    `json:"max_spend,omitempty"`
	Measure     *string   `json:"measure,omitempty"`
}

type normRule struct {
	ID          string        `json:"id"`
	Description string        `json:"description"`
	Match       normMatch     `json:"match"`
	When        string        `json:"when"`
	Decision    Outcome       `json:"decision"`
	Reason      string        `json:"reason"`
	Approval    *normApproval `json:"approval,omitempty"`
	Notify      []string      `json:"notify,omitempty"`
	Verify      Level         `json:"verify,omitempty"`
}

func normApprovalOf(a ApprovalSpec) normApproval {
	ap := append([]string{}, a.Approvers...)
	return normApproval{Approvers: sortUnique(ap), Required: a.Required, ExpiresIn: a.ExpiresIn}
}

func normMatchOf(m Matcher) normMatch {
	var n normMatch
	for _, g := range m.Tool {
		n.Tool = append(n.Tool, g.src)
	}
	for _, g := range m.ConnectorType {
		n.ConnectorType = append(n.ConnectorType, g.src)
	}
	n.SideEffect = m.SideEffect
	var agents []string
	for _, g := range m.AgentNames {
		agents = append(agents, g.src)
	}
	agents = append(agents, m.AgentIDs...)
	n.Agent = sortUnique(agents)
	if len(m.Labels) > 0 {
		n.Labels = map[string]string{}
		for _, l := range m.Labels {
			n.Labels[l.Key] = l.Value
		}
	}
	return n
}

// normalize builds the normalized document and returns JCS(document).
func normalize(p *draft) ([]byte, error) {
	doc := normDoc{
		Lang: Lang, CEL: normCEL{Lists: extListsVersion, Sets: extSetsVersion, Strings: extStringsVersion},
		Name: p.name, Description: p.description,
		Defaults: normDefaults{Decision: p.defaults.Decision, Reason: p.defaults.Reason, Verify: p.defaults.Verify,
			Approval: normApprovalOf(p.defaults.Approval), Notify: append([]string{}, p.defaults.Notify...)},
		Budgets: []normBudget{}, Rules: []normRule{},
	}
	for _, d := range p.budgets {
		b := d.b
		nb := normBudget{ID: b.ID, Description: b.Description, Match: normMatchOf(b.Match), Per: b.Per, Window: b.Window}
		if b.MaxSpend != nil {
			m := *b.MaxSpend
			nb.MaxSpend = &m
		} else {
			x := b.MaxCalls
			nb.MaxCalls = &x
		}
		if b.Measure != nil {
			s := b.MeasureCanonical
			nb.Measure = &s
		}
		doc.Budgets = append(doc.Budgets, nb)
	}
	for _, d := range p.rules {
		r := d.r
		nr := normRule{ID: r.ID, Description: r.Description, Match: normMatchOf(r.Match), When: r.WhenCanonical,
			Decision: r.Decision, Reason: r.Reason, Verify: r.Verify}
		if r.Approval != nil {
			a := normApprovalOf(*r.Approval)
			nr.Approval = &a
		}
		if r.Decision != Allow {
			nr.Notify = append([]string{}, r.Notify...)
			if nr.Notify == nil {
				nr.Notify = []string{}
			}
		}
		doc.Rules = append(doc.Rules, nr)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return jcs.Transform(b)
}
