// Package fhirbench is the FHIR write-integrity benchmark (corpus/fhir/BENCHMARK.md): the
// healthcare version of the corpus benchmark (internal/corpus/bench). Each scenario is a short
// sequence of FHIR R4 REST calls against a real FHIR server on synthetic data. The runner reads
// what the server stored from the server's own database (its version tables), never from the
// server's answers or from a subject, and scores each subject the way the corpus benchmark does:
// did it flag the difference between what the agent reports and what was stored, did it report
// what was stored, and did it leave a record a third party can check.
//
// The benchmark checks what was stored against what the agent reported. It never judges whether
// clinical content is right.
package fhirbench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Effect is what the server stored on the agent's behalf in one run.
type Effect struct {
	// Changes are the resource versions the agent's calls stored, per resource type and kind
	// (create, update, delete).
	Changes map[string]map[string]int `json:"changes"`
	// Differs are the elements ("Observation.note") of a version the agent's calls stored that
	// differ from what the agent believes it stored (by default, the resource it sent).
	Differs []string `json:"differs,omitempty"`
	// Overwrote counts, per type, versions another writer stored that the agent's update
	// replaced without the agent having read them (a lost update).
	Overwrote map[string]int `json:"overwrote,omitempty"`
	// Dangling counts, per element ("Observation.subject"), references in what the agent stored
	// to resources that don't exist.
	Dangling map[string]int `json:"dangling,omitempty"`
	// HistoryKept counts, per type, resources the agent deleted whose earlier versions can still
	// be read from the server's history.
	HistoryKept map[string]int `json:"history_kept,omitempty"`
}

// Normal drops zero counts and empty maps and sorts Differs, so two effects compare by content.
func (e Effect) Normal() Effect {
	out := Effect{Changes: map[string]map[string]int{}}
	for t, ops := range e.Changes {
		for op, n := range ops {
			if n != 0 {
				if out.Changes[t] == nil {
					out.Changes[t] = map[string]int{}
				}
				out.Changes[t][op] = n
			}
		}
	}
	seen := map[string]bool{}
	for _, d := range e.Differs {
		if !seen[d] {
			seen[d] = true
			out.Differs = append(out.Differs, d)
		}
	}
	sort.Strings(out.Differs)
	for _, src := range []struct {
		in  map[string]int
		out *map[string]int
	}{{e.Overwrote, &out.Overwrote}, {e.Dangling, &out.Dangling}, {e.HistoryKept, &out.HistoryKept}} {
		for k, n := range src.in {
			if n != 0 {
				if *src.out == nil {
					*src.out = map[string]int{}
				}
				(*src.out)[k] = n
			}
		}
	}
	return out
}

// Equal reports whether two effects have the same content.
func Equal(a, b Effect) bool {
	x, _ := json.Marshal(a.Normal())
	y, _ := json.Marshal(b.Normal())
	return bytes.Equal(x, y)
}

// String is one line: "create 1 Observation; Observation.note stored differently".
func (e Effect) String() string {
	n := e.Normal()
	var parts []string
	for _, t := range keys(n.Changes) {
		for _, op := range []string{"create", "update", "delete"} {
			if c := n.Changes[t][op]; c > 0 {
				parts = append(parts, fmt.Sprintf("%s %d %s", op, c, t))
			}
		}
	}
	if len(n.Differs) > 0 {
		parts = append(parts, strings.Join(n.Differs, ", ")+" not as the agent believes")
	}
	for _, t := range keys(n.Overwrote) {
		parts = append(parts, fmt.Sprintf("overwrote %d %s version the agent hadn't read", n.Overwrote[t], t))
	}
	for _, f := range keys(n.Dangling) {
		parts = append(parts, fmt.Sprintf("%d reference in %s to a resource that doesn't exist", n.Dangling[f], f))
	}
	for _, t := range keys(n.HistoryKept) {
		parts = append(parts, fmt.Sprintf("%d deleted %s still readable in history", n.HistoryKept[t], t))
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, "; ")
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// add adds n to m[a][b].
func add(m map[string]map[string]int, a, b string, n int) {
	if m[a] == nil {
		m[a] = map[string]int{}
	}
	m[a][b] += n
}

func inc(m *map[string]int, k string) {
	if *m == nil {
		*m = map[string]int{}
	}
	(*m)[k]++
}
