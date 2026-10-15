package securitypack

import (
	"fmt"
	"sort"
)

// Aliases replace the org's agent, connector and rule names with stable aliases (agent-1,
// connector-2, rules-1) across a whole pack, numbered in creation order (ids are time-ordered),
// so the same setup gives the same aliases in the next pack. The mapping goes only to
// aliases.private.json. The starter name default-deny is Deedglass's, not the org's, and stays.
type Aliases struct {
	byKey   map[string]string
	Entries []AliasEntry `json:"aliases"`
}

// AliasEntry is one row of aliases.private.json.
type AliasEntry struct {
	Kind  string `json:"kind"` // agent | connector | rules
	Alias string `json:"alias"`
	ID    string `json:"id"`
	Name  string `json:"name"`
}

// NewAliases numbers every name in f.
func NewAliases(f *Facts) *Aliases {
	a := &Aliases{byKey: map[string]string{}, Entries: []AliasEntry{}}
	type item struct{ id, name string }
	add := func(kind, prefix string, items []item) {
		sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })
		n := 0
		for _, it := range items {
			key := kind + "\x00" + it.name
			if _, seen := a.byKey[key]; seen || it.name == "" || (kind == "rules" && it.name == DefaultDenyName) {
				continue
			}
			n++
			alias := fmt.Sprintf("%s-%d", prefix, n)
			a.byKey[key] = alias
			a.Entries = append(a.Entries, AliasEntry{Kind: kind, Alias: alias, ID: it.id, Name: it.name})
		}
	}
	var agents, conns, pols []item
	for _, x := range f.Agents {
		agents = append(agents, item{x.ID, x.Name})
	}
	for _, x := range f.Connectors {
		conns = append(conns, item{x.ID, x.Name})
	}
	for _, v := range []*ActiveVersion{f.Rules.Enforce, f.Rules.Shadow} {
		if v != nil {
			pols = append(pols, item{v.PolicyID, v.Name})
		}
	}
	add("agent", "agent", agents)
	add("connector", "connector", conns)
	add("rules", "rules", pols)
	return a
}

// Name is the alias for a name of kind, or the name itself when it has none.
func (a *Aliases) Name(kind, name string) string {
	if a == nil {
		return name
	}
	if v, ok := a.byKey[kind+"\x00"+name]; ok {
		return v
	}
	return name
}

// Apply replaces the names in f.
func (a *Aliases) Apply(f *Facts) {
	for i := range f.Agents {
		f.Agents[i].Name = a.Name("agent", f.Agents[i].Name)
	}
	for i := range f.Connectors {
		f.Connectors[i].Name = a.Name("connector", f.Connectors[i].Name)
	}
	for _, v := range []*ActiveVersion{f.Rules.Enforce, f.Rules.Shadow} {
		if v != nil {
			v.Name = a.Name("rules", v.Name)
		}
	}
}
