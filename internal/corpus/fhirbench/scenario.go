package fhirbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Scenario is one directory of corpus/fhir/scenarios: scenario.json and expected.json.
type Scenario struct {
	ID    string `json:"-"`
	Dir   string `json:"-"`
	Title string `json:"title"`
	// Story is the scenario in two or three sentences, for the README and the results.
	Story string `json:"story"`
	// Servers are the FHIR servers the scenario runs on ("medplum", "hapi").
	Servers []string `json:"servers"`
	// Behaviour names what the server does and why that's documented: standard FHIR semantics or
	// the vendor's own documentation. Sources back it.
	Behaviour string   `json:"behaviour"`
	Sources   []Source `json:"sources"`
	Setup     Setup    `json:"setup"`
	Steps     []Step   `json:"steps"`
	// AgentReports is what the agent says it did, written before any run.
	AgentReports AgentReport `json:"agent_reports"`
	// Expected holds, per server, what the server stored with no gateway in front, as the runner
	// read it from the server's database (make fhir-bench writes it with -write-expected, and
	// checks it on every run of a subject that doesn't stand in front of the calls).
	Expected map[string]Expected `json:"-"`
}

// Source backs a scenario's behaviour.
type Source struct {
	Says string `json:"says"`
	URL  string `json:"url"`
}

// Setup is what the runner does before the steps, as the server's admin.
type Setup struct {
	// ProjectFeatures are Medplum Project.features for the run (transaction-bundles).
	ProjectFeatures []string `json:"project_features,omitempty"`
	// CheckReferencesOnWrite is Medplum's Project.checkReferencesOnWrite for the run.
	CheckReferencesOnWrite bool `json:"check_references_on_write,omitempty"`
	// ReadonlyFields are, per resource type, the fields the agent's client may not write (a
	// Medplum AccessPolicy's readonlyFields). Empty: the agent's client has no AccessPolicy.
	ReadonlyFields map[string][]string `json:"readonly_fields,omitempty"`
	// Seed are requests another client sends before the steps.
	Seed []Request `json:"seed,omitempty"`
}

// Request is one FHIR REST call. Path is relative to the FHIR base ("Observation",
// "Observation/${id:obs}", "" for a Bundle). Strings may hold ${nonce} (unique per run),
// ${id:<label>} and ${version:<label>} (the id and meta.versionId of the resource a labelled
// request wrote).
type Request struct {
	Label   string            `json:"label,omitempty"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// Step is one call in the scenario.
type Step struct {
	Request
	// Session is "agent" (through the subject) or "other" (another client, always direct).
	Session string `json:"session"`
	// Note says what happens, in a few words.
	Note string `json:"note,omitempty"`
	// LoseResponse: the server answers, but the agent's client never sees the answer (a dropped
	// connection), so it retries in the next step.
	LoseResponse bool `json:"lose_response,omitempty"`
	// Believed is what the agent believes the server stored after this step, one resource per
	// entry. Left out, it is the resource the step sends (each entry of a Bundle).
	Believed []json.RawMessage `json:"believed,omitempty"`
	// ReadAfter is the label of the request after which the agent read the resource it now
	// updates: when another writer stored a version since, the agent's update replaces a version
	// it didn't read.
	ReadAfter string `json:"read_after,omitempty"`
}

// AgentReport is the agent's own account.
type AgentReport struct {
	Summary string `json:"summary"`
	Effect  Effect `json:"effect"`
}

// Expected is what a server stored with no gateway, and the expected Deedglass outcome.
type Expected struct {
	Facts     Effect `json:"facts"`
	Deedglass string `json:"deedglass,omitempty"`
}

var reID = regexp.MustCompile(`^[0-9]{2}-[a-z0-9-]+$`)

// Load reads every scenario under dir/scenarios, sorted by id.
func Load(dir string) ([]*Scenario, error) {
	ents, err := os.ReadDir(filepath.Join(dir, "scenarios"))
	if err != nil {
		return nil, err
	}
	var out []*Scenario
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		s, err := LoadOne(filepath.Join(dir, "scenarios", e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) == 0 {
		return nil, errors.New("fhirbench: no scenarios under " + dir)
	}
	return out, nil
}

// LoadOne reads one scenario directory.
func LoadOne(d string) (*Scenario, error) {
	id := filepath.Base(d)
	if !reID.MatchString(id) {
		return nil, fmt.Errorf("fhirbench: scenario directory %q isn't NN-name", id)
	}
	b, err := os.ReadFile(filepath.Join(d, "scenario.json"))
	if err != nil {
		return nil, err
	}
	s := &Scenario{ID: id, Dir: d}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(s); err != nil {
		return nil, fmt.Errorf("fhirbench: %s/scenario.json: %w", id, err)
	}
	if err := s.check(); err != nil {
		return nil, fmt.Errorf("fhirbench: %s: %w", id, err)
	}
	s.Expected = map[string]Expected{}
	if b, err := os.ReadFile(filepath.Join(d, "expected.json")); err == nil {
		if err := json.Unmarshal(b, &s.Expected); err != nil {
			return nil, fmt.Errorf("fhirbench: %s/expected.json: %w", id, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func (s *Scenario) check() error {
	if s.Title == "" || s.Story == "" || s.Behaviour == "" {
		return errors.New("title, story and behaviour are required")
	}
	if len(s.Sources) == 0 {
		return errors.New("every scenario cites at least one source for the behaviour")
	}
	for _, src := range s.Sources {
		if !strings.HasPrefix(src.URL, "https://") || src.Says == "" {
			return fmt.Errorf("source %q needs says and an https url", src.URL)
		}
	}
	if len(s.Servers) == 0 {
		return errors.New("servers is empty")
	}
	agent := false
	for i, st := range s.Steps {
		switch st.Session {
		case "agent":
			agent = true
		case "other":
		default:
			return fmt.Errorf("step %d: session is agent or other, not %q", i+1, st.Session)
		}
		switch st.Method {
		case "POST", "PUT", "PATCH", "DELETE", "GET":
		default:
			return fmt.Errorf("step %d: method %q", i+1, st.Method)
		}
	}
	if !agent {
		return errors.New("no agent step")
	}
	return nil
}

// Diverges reports whether the agent's report differs from what the server stored with no
// gateway in front.
func (s *Scenario) Diverges(server string) (bool, error) {
	e, ok := s.Expected[server]
	if !ok {
		return false, fmt.Errorf("%s has no expected facts for %s (run make fhir-bench WRITE_EXPECTED=1 and review expected.json)", s.ID, server)
	}
	return !Equal(s.AgentReports.Effect, e.Facts), nil
}

var rePlace = regexp.MustCompile(`\$\{(nonce|id:[a-z0-9-]+|version:[a-z0-9-]+)\}`)

// Vars are the values ${…} placeholders take in one run.
type Vars struct {
	Nonce    string
	IDs      map[string]string
	Versions map[string]string
}

// Fill replaces the placeholders in s. An unknown label is an error.
func (v Vars) Fill(s string) (string, error) {
	var bad error
	out := rePlace.ReplaceAllStringFunc(s, func(m string) string {
		k := m[2 : len(m)-1]
		switch {
		case k == "nonce":
			return v.Nonce
		case strings.HasPrefix(k, "id:"):
			if x, ok := v.IDs[k[3:]]; ok {
				return x
			}
		case strings.HasPrefix(k, "version:"):
			if x, ok := v.Versions[k[8:]]; ok {
				return x
			}
		}
		bad = fmt.Errorf("placeholder %s has no value yet", m)
		return m
	})
	return out, bad
}

// FillRequest returns r with every placeholder filled.
func (v Vars) FillRequest(r Request) (Request, error) {
	out := r
	var err error
	if out.Path, err = v.Fill(r.Path); err != nil {
		return out, err
	}
	out.Headers = map[string]string{}
	for k, x := range r.Headers {
		if out.Headers[k], err = v.Fill(x); err != nil {
			return out, err
		}
	}
	if len(r.Body) > 0 {
		b, err := v.Fill(string(r.Body))
		if err != nil {
			return out, err
		}
		out.Body = json.RawMessage(b)
	}
	return out, nil
}
