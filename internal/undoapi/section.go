package undoapi

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

// The approval context section "undo" (S23 §3.6): the undone action, rows, tables, counts, the
// first 20 steps (sensitive values as •••), the plan digest and whether allow_triggers is on.
// approve.SectionFunc is pure (no I/O), so the run handler, which has just previewed the plan,
// leaves it here keyed by the plan digest; Prepare runs moments later on the same instance.

// MaxSectionSteps is how many steps the approval context lists.
const MaxSectionSteps = 20

// Section is the "undo" context section.
type Section struct {
	Summary       string        `json:"summary"`
	Undoes        string        `json:"undoes"`
	Rows          int           `json:"rows"`
	Tables        []string      `json:"tables"`
	Counts        undo.Counts   `json:"counts"`
	Steps         []PreviewStep `json:"steps"`
	StepsMore     int           `json:"steps_more"`
	PlanDigest    string        `json:"plan_digest"`
	AllowTriggers bool          `json:"allow_triggers"`
	Reason        string        `json:"reason"`
}

type sectionEntry struct {
	s  Section
	at time.Time
}

var (
	sectionMu    sync.Mutex
	sectionCache = map[string]sectionEntry{} // plan digest → section, for a few minutes
)

const sectionTTL = 10 * time.Minute

// remember keeps a previewed plan's section for the approval that follows.
func remember(digest string, s Section) {
	sectionMu.Lock()
	defer sectionMu.Unlock()
	now := time.Now()
	for k, e := range sectionCache {
		if now.Sub(e.at) > sectionTTL {
			delete(sectionCache, k)
		}
	}
	if len(sectionCache) > 1000 {
		for k := range sectionCache {
			delete(sectionCache, k)
			break
		}
	}
	sectionCache[digest] = sectionEntry{s: s, at: now}
}

func recall(digest string) (Section, bool) {
	sectionMu.Lock()
	defer sectionMu.Unlock()
	e, ok := sectionCache[digest]
	if !ok || time.Since(e.at) > sectionTTL {
		return Section{}, false
	}
	return e.s, true
}

var registerOnce sync.Once

// RegisterSection registers the "undo" approval context section once per process.
func RegisterSection() {
	registerOnce.Do(func() {
		approve.RegisterSection("undo", func(tool, connectorType string) bool {
			return strings.HasSuffix(tool, "."+undoTool) && connectorType == "postgres"
		}, sectionFor)
		// S50: a session undo's approval lists the same things, naming the session.
		approve.RegisterSection("undo_session", func(tool, connectorType string) bool {
			return isSessionTool(tool) && connectorType == "postgres"
		}, sessionSectionFor)
	})
}

// sessionSectionFor is sectionFor for "<connector>.undo_session".
func sessionSectionFor(in gate.ApprovalInput) (json.RawMessage, error) {
	var a SessionArgs
	if err := json.Unmarshal(in.Args, &a); err != nil {
		return nil, err
	}
	s, ok := recall(a.PlanDigest)
	if !ok {
		s = Section{Undoes: a.Session, Tables: []string{}, Steps: []PreviewStep{}, PlanDigest: a.PlanDigest}
	}
	s.AllowTriggers, s.Reason = a.AllowTriggers, a.Reason
	if s.Summary != "" {
		s.Summary = strings.Replace(s.Summary, "{agent}", in.AgentName, 1)
	}
	return json.Marshal(s)
}

// RememberSession leaves a session plan's section for the approval that follows (S50).
func RememberSession(p *undo.Plan, session, person string) {
	s := sectionOf(p, session, person)
	s.Summary = undo.ApprovalSummary("{agent}", person, p, session)
	remember(p.Digest, s)
}

// PreviewStepsOf is the first limit steps as a person reads them, and how many more there are.
func PreviewStepsOf(p *undo.Plan, limit int) ([]PreviewStep, int) { return previewSteps(p, limit) }

// sectionFor builds the section from the action's arguments and the remembered plan; without one
// (another instance, or a restart in between) it lists what the arguments say.
func sectionFor(in gate.ApprovalInput) (json.RawMessage, error) {
	var a Args
	if err := json.Unmarshal(in.Args, &a); err != nil {
		return nil, err
	}
	s, ok := recall(a.PlanDigest)
	if !ok {
		s = Section{Undoes: a.Undoes, Tables: []string{}, Steps: []PreviewStep{}, PlanDigest: a.PlanDigest}
	}
	s.AllowTriggers, s.Reason = a.AllowTriggers, a.Reason
	if s.Summary != "" {
		// "console (for Arjun) wants to put back 301 rows in public.orders, undoing act_01j9zw6k…."
		s.Summary = strings.Replace(s.Summary, "{agent}", in.AgentName, 1)
	}
	return json.Marshal(s)
}

// sectionOf builds the section a run leaves for its approval.
func sectionOf(p *undo.Plan, undoes, person string) Section {
	steps, more := previewSteps(p, MaxSectionSteps)
	s := Section{Undoes: undoes, Rows: p.Rows, Tables: p.TableNames(), Counts: p.Counts, Steps: steps, StepsMore: more, PlanDigest: p.Digest}
	s.Summary = undo.ApprovalSummary("{agent}", person, p, undoes)
	return s
}
