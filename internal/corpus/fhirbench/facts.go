package fhirbench

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Run is everything the runner saw in one scenario played through one subject. It is kept in
// the results file, so the facts and the baseline subjects can be recomputed from it later
// (TestGoldenReplay) without a server.
type Run struct {
	Steps []StepRecord `json:"steps"`
	// Lookups are what the runner read from the server's database after the steps.
	Lookups Lookups `json:"lookups"`
}

// StepRecord is one step as it ran.
type StepRecord struct {
	Session string  `json:"session"`
	Request Request `json:"request"` // placeholders filled
	// Saw is what the agent's (or the other writer's) client was told. Lost: it was told nothing.
	Saw Exchange `json:"saw"`
	// Via is "direct" or the subject that carried the call.
	Via string `json:"via"`
	// Versions are the resource versions this step stored, read from the server's database.
	Versions []Version `json:"versions"`
	// Believed is what the agent believes this step stored (placeholders filled).
	Believed []json.RawMessage `json:"believed,omitempty"`
	// Read is the version the agent read before this update (from read_after), and Prior the
	// version that was current just before the step ran; both "" when the step has no read_after.
	Read  string `json:"read,omitempty"`
	Prior string `json:"prior,omitempty"`
}

// Exchange is a call's answer as the client saw it.
type Exchange struct {
	Status   int             `json:"status"`
	Location string          `json:"location,omitempty"`
	ETag     string          `json:"etag,omitempty"`
	Body     json.RawMessage `json:"body,omitempty"`
	Lost     bool            `json:"lost,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// Version is one stored resource version.
type Version struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	VersionID string          `json:"version_id"`
	Op        string          `json:"op"` // create, update, delete
	Content   json.RawMessage `json:"content"`
}

// Lookups are database reads after the steps.
type Lookups struct {
	// Exists says, per "Type/id" referenced by what the agent stored, whether that resource
	// exists (and isn't deleted).
	Exists map[string]bool `json:"exists,omitempty"`
	// EarlierVersions counts, per "Type/id" the agent deleted, the versions before the delete
	// whose content the server still holds.
	EarlierVersions map[string]int `json:"earlier_versions,omitempty"`
}

var reRef = regexp.MustCompile(`^([A-Z][A-Za-z]+)/([A-Za-z0-9\-.]{1,64})$`)

// References lists the relative references ("Patient/123") in a resource, by top-level element,
// leaving out meta.
func References(content json.RawMessage) map[string][]string {
	var m map[string]any
	if json.Unmarshal(content, &m) != nil {
		return nil
	}
	out := map[string][]string{}
	for k, v := range m {
		if k == "meta" || k == "id" || k == "resourceType" {
			continue
		}
		walkRefs(v, func(r string) { out[k] = append(out[k], r) })
	}
	return out
}

func walkRefs(v any, f func(string)) {
	switch t := v.(type) {
	case map[string]any:
		if r, ok := t["reference"].(string); ok && reRef.MatchString(r) {
			f(r)
		}
		for _, x := range t {
			walkRefs(x, f)
		}
	case []any:
		for _, x := range t {
			walkRefs(x, f)
		}
	}
}

// Facts is what the server stored on the agent's behalf in a run: computed from the versions the
// agent's steps stored and the lookups, never from what a subject or the server's answers say.
func Facts(r *Run) Effect {
	e := Effect{Changes: map[string]map[string]int{}}
	for _, st := range r.Steps {
		if st.Session != "agent" {
			continue
		}
		for _, v := range st.Versions {
			add(e.Changes, v.Type, v.Op, 1)
			switch v.Op {
			case "delete":
				if r.Lookups.EarlierVersions[v.Type+"/"+v.ID] > 0 {
					inc(&e.HistoryKept, v.Type)
				}
				continue
			}
			for field, refs := range References(v.Content) {
				for _, ref := range refs {
					if ok, known := r.Lookups.Exists[ref]; known && !ok {
						inc(&e.Dangling, v.Type+"."+field)
					}
				}
			}
			if b := match(st, v); b != nil {
				e.Differs = append(e.Differs, differs(st.Request.Method, v, b)...)
			}
		}
		if st.Read != "" && st.Prior != "" && st.Read != st.Prior {
			typ := strings.SplitN(st.Request.Path, "/", 2)[0]
			inc(&e.Overwrote, typ)
		}
	}
	return e.Normal()
}

// believed is what the agent believes a step stored: the step's believed list, or else what it
// sent (each entry's resource for a Bundle; nothing for a PATCH or a DELETE).
func believed(st StepRecord) []map[string]any {
	var raws []json.RawMessage
	switch {
	case len(st.Believed) > 0:
		raws = st.Believed
	case st.Request.Method == "POST" || st.Request.Method == "PUT":
		var m map[string]any
		if json.Unmarshal(st.Request.Body, &m) != nil {
			return nil
		}
		if m["resourceType"] == "Bundle" {
			es, _ := m["entry"].([]any)
			var out []map[string]any
			for _, x := range es {
				if e, ok := x.(map[string]any); ok {
					if r, ok := e["resource"].(map[string]any); ok {
						out = append(out, r)
					}
				}
			}
			return out
		}
		return []map[string]any{m}
	}
	var out []map[string]any
	for _, r := range raws {
		var m map[string]any
		if json.Unmarshal(r, &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// match finds what the agent believes about a stored version: the believed resource with the same
// first identifier, else the one with the same id, else the only one of that type.
func match(st StepRecord, v Version) map[string]any {
	var got map[string]any
	if json.Unmarshal(v.Content, &got) != nil {
		return nil
	}
	bs := believed(st)
	key := func(m map[string]any) string {
		ids, _ := m["identifier"].([]any)
		if len(ids) == 0 {
			return ""
		}
		id, _ := ids[0].(map[string]any)
		s, _ := id["system"].(string)
		val, _ := id["value"].(string)
		return s + "|" + val
	}
	var sameType []map[string]any
	for _, b := range bs {
		if b["resourceType"] != v.Type {
			continue
		}
		sameType = append(sameType, b)
		if k := key(b); k != "" && k == key(got) {
			return b
		}
	}
	for _, b := range sameType {
		if id, _ := b["id"].(string); id != "" && id == v.ID {
			return b
		}
	}
	if len(sameType) == 1 {
		return sameType[0]
	}
	return nil
}

// metaKept are the meta elements compared when the agent set them; the rest of meta is the
// server's.
var metaKept = []string{"author", "tag", "security", "profile"}

// differs lists the top-level elements of a stored version that aren't what the agent believes:
// missing or with another value. Elements only the server has are allowed. id counts only for a
// create (POST) that named one; meta only for the elements in metaKept the agent set.
func differs(method string, v Version, b map[string]any) []string {
	var got map[string]any
	if json.Unmarshal(v.Content, &got) != nil {
		return nil
	}
	var out []string
	for k, want := range b {
		switch k {
		case "resourceType":
			continue
		case "id":
			if method == "POST" && got["id"] != want {
				out = append(out, v.Type+".id")
			}
			continue
		case "text":
			if t, ok := want.(map[string]any); ok && t["status"] == "generated" {
				continue
			}
		case "meta":
			wm, _ := want.(map[string]any)
			gm, _ := got["meta"].(map[string]any)
			for _, mk := range metaKept {
				if w, ok := wm[mk]; ok && !SameJSON(w, gm[mk]) {
					out = append(out, v.Type+".meta."+mk)
				}
			}
			continue
		}
		if !SameJSON(want, got[k]) {
			out = append(out, v.Type+"."+k)
		}
	}
	sort.Strings(out)
	return out
}

// SameJSON compares two JSON values by content: numbers by value (72 equals 72.0), objects by
// keys and values, arrays in order.
func SameJSON(a, b any) bool {
	return bytes.Equal(canon(a), canon(b))
}

func canon(v any) []byte {
	b, _ := json.Marshal(normalNumbers(v))
	return b
}

func normalNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normalNumbers(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalNumbers(x)
		}
		return out
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return t.String()
		}
		return f
	case int:
		return float64(t)
	}
	return v
}
