package fhirbench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/corpus/bench"
)

func TestEffectNormalEqualString(t *testing.T) {
	a := Effect{Changes: map[string]map[string]int{"Observation": {"create": 1, "update": 0}, "Patient": {}}, Differs: []string{"Observation.note", "Observation.id", "Observation.note"}}
	b := Effect{Changes: map[string]map[string]int{"Observation": {"create": 1}}, Differs: []string{"Observation.id", "Observation.note"}, Dangling: map[string]int{"x": 0}}
	if !Equal(a, b) {
		t.Fatalf("%+v and %+v should be equal", a.Normal(), b.Normal())
	}
	if got := a.String(); got != "create 1 Observation; Observation.id, Observation.note not as the agent believes" {
		t.Fatalf("String: %q", got)
	}
	if got := (Effect{}).String(); got != "nothing" {
		t.Fatalf("empty: %q", got)
	}
	full := Effect{Changes: map[string]map[string]int{"Observation": {"delete": 1}}, Overwrote: map[string]int{"Observation": 1},
		Dangling: map[string]int{"Observation.subject": 1}, HistoryKept: map[string]int{"Observation": 1}}
	for _, want := range []string{"delete 1 Observation", "overwrote 1 Observation version", "1 reference in Observation.subject", "1 deleted Observation still readable"} {
		if !strings.Contains(full.String(), want) {
			t.Errorf("%q has no %q", full.String(), want)
		}
	}
	if Equal(full, Effect{Changes: full.Changes}) {
		t.Fatal("the extra dimensions must count")
	}
}

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func TestFacts(t *testing.T) {
	sent := map[string]any{"resourceType": "Observation", "id": "mine", "status": "final", "identifier": []any{map[string]any{"system": "s", "value": "1"}},
		"valueQuantity": map[string]any{"value": 72}, "note": []any{map[string]any{"text": "n"}}, "meta": map[string]any{"author": map[string]any{"reference": "Practitioner/a"}},
		"subject": map[string]any{"reference": "Patient/p1"}, "text": map[string]any{"status": "generated", "div": "<div/>"}}
	stored := map[string]any{"resourceType": "Observation", "id": "srv-1", "status": "final", "identifier": []any{map[string]any{"system": "s", "value": "1"}},
		"valueQuantity": map[string]any{"value": 72.0}, "meta": map[string]any{"versionId": "v1", "author": map[string]any{"reference": "ClientApplication/c"}},
		"subject": map[string]any{"reference": "Patient/p1"}, "extra": true}
	run := &Run{Steps: []StepRecord{
		{Session: "agent", Request: Request{Method: "POST", Path: "Observation", Body: raw(sent)},
			Versions: []Version{{Type: "Observation", ID: "srv-1", VersionID: "v1", Op: "create", Content: raw(stored)}}},
		{Session: "other", Request: Request{Method: "PUT", Path: "Observation/srv-1"}, Versions: []Version{{Type: "Observation", ID: "srv-1", VersionID: "v2", Op: "update", Content: raw(stored)}}},
		{Session: "agent", Request: Request{Method: "PUT", Path: "Observation/srv-1", Body: raw(stored)}, Read: "v1", Prior: "v2",
			Versions: []Version{{Type: "Observation", ID: "srv-1", VersionID: "v3", Op: "update", Content: raw(stored)}}},
		{Session: "agent", Request: Request{Method: "DELETE", Path: "Observation/srv-1"},
			Versions: []Version{{Type: "Observation", ID: "srv-1", VersionID: "v4", Op: "delete", Content: raw(map[string]any{"resourceType": "Observation", "id": "srv-1"})}}},
	}, Lookups: Lookups{Exists: map[string]bool{"Patient/p1": false}, EarlierVersions: map[string]int{"Observation/srv-1": 3}}}
	got := Facts(run)
	want := Effect{Changes: map[string]map[string]int{"Observation": {"create": 1, "update": 1, "delete": 1}},
		Differs:   []string{"Observation.id", "Observation.meta.author", "Observation.note"},
		Overwrote: map[string]int{"Observation": 1}, Dangling: map[string]int{"Observation.subject": 2}, HistoryKept: map[string]int{"Observation": 1}}
	if !Equal(got, want) {
		t.Fatalf("Facts:\n got %s\nwant %s", got.String(), want.String())
	}
}

func TestBelievedAndMatch(t *testing.T) {
	b := raw(map[string]any{"resourceType": "Bundle", "type": "batch", "entry": []any{
		map[string]any{"resource": map[string]any{"resourceType": "Observation", "identifier": []any{map[string]any{"system": "s", "value": "a"}}, "status": "final"}, "request": map[string]any{"method": "POST", "url": "Observation"}},
		map[string]any{"resource": map[string]any{"resourceType": "Observation", "identifier": []any{map[string]any{"system": "s", "value": "b"}}, "status": "final"}, "request": map[string]any{"method": "POST", "url": "Observation"}},
	}})
	st := StepRecord{Session: "agent", Request: Request{Method: "POST", Path: "", Body: b}}
	if n := len(believed(st)); n != 2 {
		t.Fatalf("believed: %d", n)
	}
	v := Version{Type: "Observation", ID: "x", Op: "create", Content: raw(map[string]any{"resourceType": "Observation", "identifier": []any{map[string]any{"system": "s", "value": "b"}}, "status": "amended"})}
	m := match(st, v)
	if m == nil || differs("POST", v, m)[0] != "Observation.status" {
		t.Fatalf("match/differs: %v", m)
	}
	if got := Nominal(st.Request); len(got) != 2 || got[0] != [2]string{"Observation", "create"} {
		t.Fatalf("Nominal: %v", got)
	}
	if got := Nominal(Request{Method: "PUT", Path: "Patient?identifier=x|1"}); len(got) != 1 || got[0] != [2]string{"Patient", "update"} {
		t.Fatalf("Nominal conditional update: %v", got)
	}
	if got := Nominal(Request{Method: "GET", Path: "Patient/1"}); got != nil {
		t.Fatalf("Nominal read: %v", got)
	}
}

func TestSameJSON(t *testing.T) {
	if !SameJSON(map[string]any{"a": json.Number("72")}, map[string]any{"a": 72.0}) {
		t.Fatal("72 and 72.0")
	}
	if SameJSON([]any{1.0, 2.0}, []any{2.0, 1.0}) {
		t.Fatal("arrays compare in order")
	}
}

func TestVars(t *testing.T) {
	v := Vars{Nonce: "n1", IDs: map[string]string{"obs": "o-1"}, Versions: map[string]string{"obs": "v-1"}}
	r, err := v.FillRequest(Request{Method: "PUT", Path: "Observation/${id:obs}", Headers: map[string]string{"If-Match": `W/"${version:obs}"`}, Body: raw(map[string]any{"id": "${id:obs}", "v": "${nonce}-x"})})
	if err != nil || r.Path != "Observation/o-1" || r.Headers["If-Match"] != `W/"v-1"` || !strings.Contains(string(r.Body), `"n1-x"`) {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := v.Fill("${id:nope}"); err == nil {
		t.Fatal("an unknown label must fail")
	}
}

func TestScenariosLoad(t *testing.T) {
	ss, err := Load(filepath.Join("..", "..", "..", "corpus", "fhir"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) < 12 || len(ss) > 20 {
		t.Fatalf("%d scenarios; the benchmark has 12 to 20", len(ss))
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "..", "corpus", "fhir", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	diverging, controls := 0, 0
	for _, s := range ss {
		if !strings.Contains(string(readme), "`"+s.ID+"`") {
			t.Errorf("corpus/fhir/README.md doesn't list %s", s.ID)
		}
		for _, srv := range s.Servers {
			d, err := s.Diverges(srv)
			if err != nil {
				t.Fatal(err)
			}
			if d {
				diverging++
			} else {
				controls++
			}
		}
	}
	if controls == 0 || diverging == 0 {
		t.Fatalf("%d diverging, %d controls: the benchmark needs both", diverging, controls)
	}
}

// fakeServer is a FHIR server in memory: enough of create, update and delete to drive Play.
type fakeServer struct {
	versions []Version
	n        int
}

func (f *fakeServer) Name() string    { return "fake" }
func (f *fakeServer) Version() string { return "0" }
func (f *fakeServer) Prepare(context.Context, *Scenario) (Env, func(), error) {
	return Env{Server: "fake"}, func() {}, nil
}
func (f *fakeServer) Do(_ context.Context, _ Env, _ bool, r Request) (Exchange, error) {
	f.n++
	typ, id, _ := strings.Cut(r.Path, "/")
	var m map[string]any
	_ = json.Unmarshal(r.Body, &m)
	op := map[string]string{"POST": "create", "PUT": "update", "DELETE": "delete"}[r.Method]
	if op == "create" {
		id = fmt.Sprintf("srv-%d", f.n)
	}
	if m == nil {
		m = map[string]any{"resourceType": typ}
	}
	m["id"] = id
	vid := fmt.Sprintf("v%d", f.n)
	m["meta"] = map[string]any{"versionId": vid}
	f.versions = append(f.versions, Version{Type: typ, ID: id, VersionID: vid, Op: op, Content: raw(m)})
	return Exchange{Status: map[string]int{"create": 201}[op] + 200*btoi(op != "create"), Body: raw(m)}, nil
}
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
func (f *fakeServer) Mark(context.Context) (any, error) { return len(f.versions), nil }
func (f *fakeServer) Since(_ context.Context, m any) ([]Version, error) {
	return append([]Version(nil), f.versions[m.(int):]...), nil
}
func (f *fakeServer) Current(_ context.Context, _, id string) (string, error) {
	cur := ""
	for _, v := range f.versions {
		if v.ID == id {
			cur = v.VersionID
		}
	}
	return cur, nil
}
func (f *fakeServer) Lookup(context.Context, []Version) (Lookups, error) { return Lookups{}, nil }

func TestPlayOnAFakeServer(t *testing.T) {
	s := &Scenario{ID: "99-fake", Title: "t", Story: "s", Behaviour: "b", Servers: []string{"fake"},
		Setup: Setup{Seed: []Request{{Label: "obs", Method: "POST", Path: "Observation", Body: raw(map[string]any{"resourceType": "Observation", "status": "preliminary"})}}},
		Steps: []Step{
			{Session: "other", Request: Request{Method: "PUT", Path: "Observation/${id:obs}", Body: raw(map[string]any{"resourceType": "Observation", "status": "amended"})}},
			{Session: "agent", ReadAfter: "obs", Request: Request{Method: "PUT", Path: "Observation/${id:obs}", Body: raw(map[string]any{"resourceType": "Observation", "status": "final"})}},
		},
		AgentReports: AgentReport{Summary: "marked it final", Effect: Effect{Changes: map[string]map[string]int{"Observation": {"update": 1}}}},
		Expected:     map[string]Expected{"fake": {Facts: Effect{Changes: map[string]map[string]int{"Observation": {"update": 1}}, Overwrote: map[string]int{"Observation": 1}}}},
	}
	for _, subj := range []Subject{None{}, HTTPStatus{}} {
		r, err := Play(t.Context(), &fakeServer{}, subj, s, "n")
		if err != nil {
			t.Fatal(err)
		}
		if !Equal(r.Stored, s.Expected["fake"].Facts) {
			t.Fatalf("%s stored %s", subj.Info().Name, r.Stored.String())
		}
		want := Score{Diverges: true, Flag: bench.FlagNone}
		if r.Score != want {
			t.Fatalf("%s: %+v", subj.Info().Name, r.Score)
		}
		if r.Run.Steps[1].Read != "v1" || r.Run.Steps[1].Prior != "v2" {
			t.Fatalf("read %q prior %q", r.Run.Steps[1].Read, r.Run.Steps[1].Prior)
		}
	}
}
