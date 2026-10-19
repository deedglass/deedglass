package approve

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/phi"
	"github.com/deedglass/deedglass/internal/vault"
)

// The context snapshot (S22 §3.5): exactly what the person countersigns. It is written once, in the
// transaction that pauses the action, and never changes (a trigger refuses it); every decision
// carries its digest, sha256 of its JCS form. It holds names and ids, never an email address.

// SnapshotVersion is the snapshot's v.
const SnapshotVersion = 1

// Limits of the snapshot.
const (
	MaxArgsBytes    = 8 << 10  // args longer than this are cut, with args_note
	MaxSectionBytes = 8 << 10  // one RegisterSection section
	MaxRecent       = 10       // the agent's last sealed actions
	maxContextBytes = 32 << 10 // approvals.context_max_bytes default
)

// Snapshot is the context snapshot, v1.
type Snapshot struct {
	V          int                        `json:"v"`
	Summary    string                     `json:"summary"`
	Action     SnapAction                 `json:"action"`
	Agent      SnapAgent                  `json:"agent"`
	OnBehalfOf *string                    `json:"on_behalf_of"`
	Policy     SnapPolicy                 `json:"policy"`
	Args       json.RawMessage            `json:"args"`
	ArgsNote   *string                    `json:"args_note"`
	SQL        *SnapSQL                   `json:"sql"`
	Sections   map[string]json.RawMessage `json:"sections"`
	// Schedule (S40): the window an approved action runs in, and its sentence; context_digest binds it.
	Schedule    *gate.ApprovalSchedule `json:"schedule,omitempty"`
	RequestedAt string                 `json:"requested_at"`
	ExpiresAt   string                 `json:"expires_at"`
}

// SnapAction is the action being paused.
type SnapAction struct {
	ID            string `json:"id"`
	Tool          string `json:"tool"`
	Connector     string `json:"connector"`
	ConnectorType string `json:"connector_type"`
	SideEffect    string `json:"side_effect"`
	Mode          string `json:"mode"`
	RequestDigest string `json:"request_digest"`
}

// SnapAgent is the agent asking, with its recent record.
type SnapAgent struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Status string            `json:"status"`
	Labels map[string]string `json:"labels"`
	Recent []SnapRecent      `json:"recent"`
}

// SnapRecent is one of the agent's last sealed actions.
type SnapRecent struct {
	Action  string  `json:"action"`
	Tool    string  `json:"tool"`
	Status  string  `json:"status"`
	Level   *string `json:"level"`
	Verdict *string `json:"verdict"`
	At      string  `json:"at"`
}

// SnapPolicy is the rule that asks for a person.
type SnapPolicy struct {
	ID        *string  `json:"id"`
	Name      *string  `json:"name"`
	Version   *int     `json:"version"`
	VersionID *string  `json:"version_id"`
	RuleID    string   `json:"rule_id"`
	Reason    string   `json:"reason"`
	Verify    string   `json:"verify"`
	Required  int      `json:"required"`
	Approvers []string `json:"approvers"`
}

// SnapSQL are S11's SQL facts (never re-parsed here) and the connector's estimate.
type SnapSQL struct {
	Kind     string        `json:"kind"`
	Tables   []string      `json:"tables"`
	Writes   bool          `json:"writes"`
	HasWhere bool          `json:"has_where"`
	Estimate *SnapEstimate `json:"estimate"`
}

// SnapEstimate is the connector's estimate: rows and method, or method none with the reason.
type SnapEstimate struct {
	Rows   *int64 `json:"rows,omitempty"`
	Method string `json:"method"`
	TookMS *int64 `json:"took_ms,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// SectionFunc builds one extra context section from the action (pure: no I/O, ≤ 8 KiB). It returns
// nil to add nothing.
type SectionFunc func(in gate.ApprovalInput) (json.RawMessage, error)

type section struct {
	name  string
	match func(tool, connectorType string) bool
	fn    SectionFunc
}

var (
	sectionsMu sync.RWMutex
	sections   []section
)

// RegisterSection adds an extra context section for the tools match accepts (S23 registers
// "undo"; S30 registers Stripe). Registering a name twice panics.
func RegisterSection(name string, match func(tool, connectorType string) bool, fn SectionFunc) {
	sectionsMu.Lock()
	defer sectionsMu.Unlock()
	for _, s := range sections {
		if s.name == name {
			panic("approve: context section " + name + " registered twice")
		}
	}
	sections = append(sections, section{name: name, match: match, fn: fn})
	sort.Slice(sections, func(i, j int) bool { return sections[i].name < sections[j].name })
}

func buildSections(in gate.ApprovalInput) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	sectionsMu.RLock()
	defer sectionsMu.RUnlock()
	for _, s := range sections {
		if !s.match(in.Tool, in.ConnectorType) {
			continue
		}
		b, err := s.fn(in)
		if err != nil || len(b) == 0 || len(b) > MaxSectionBytes || !json.Valid(b) {
			continue
		}
		out[s.name] = b
	}
	return out
}

// sectionSummary is the first section's own "summary" (sections in name order), "" when none has
// one. A section that knows the action better than the SQL facts (S23's undo) says it plainly.
func sectionSummary(secs map[string]json.RawMessage) string {
	names := make([]string, 0, len(secs))
	for n := range secs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		var x struct {
			Summary string `json:"summary"`
		}
		if json.Unmarshal(secs[n], &x) == nil && x.Summary != "" && utf8.RuneCountInString(x.Summary) <= 280 {
			return x.Summary
		}
	}
	return ""
}

// redactArgs returns the arguments as a person may see them: string values that look like a
// secret are scrubbed (S16's redaction), and JSON over MaxArgsBytes is cut, with a note.
func redactArgs(args json.RawMessage) (json.RawMessage, *string) {
	if len(args) == 0 {
		return json.RawMessage(`null`), nil
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return json.RawMessage(`null`), nil
	}
	v = scrubValue(v)
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`), nil
	}
	if len(b) <= MaxArgsBytes {
		return b, nil
	}
	cut := b[:MaxArgsBytes]
	for !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}
	s, _ := json.Marshal(string(cut))
	note := fmt.Sprintf("Showing the first 8 KiB of %d KiB. The full request is in Console › Actions.", (len(b)+1023)/1024)
	return s, &note
}

func scrubValue(v any) any {
	switch x := v.(type) {
	case string:
		return vault.Scrub(x)
	case []any:
		for i := range x {
			x[i] = scrubValue(x[i])
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = scrubValue(x[k])
		}
		return x
	}
	return v
}

// PHINote is the args note in a PHI-mode org's approval snapshot.
const PHINote = "PHI mode is on, so argument values are shown as digests. Ids the connector names are shown as they are."

// phiSnapshot makes a snapshot fit for a PHI-mode org (HC-P09): the arguments keep their names
// and the connector's identifier arguments, every other value is a digest, and the connector's
// sections (which can quote the request) are left out. The snapshot never changes once stored
// (DG006), so this happens before it is written.
func phiSnapshot(snap *Snapshot, in gate.ApprovalInput) {
	var ids []string
	if k, ok := in.Exec.(connector.PHIKeeping); ok {
		ids = k.PHIKeep(in.Call.Tool).Args
	}
	snap.Args = phi.MinimizeArgs(in.Args, ids)
	if len(snap.Args) == 0 {
		snap.Args = json.RawMessage(`null`)
	}
	note := PHINote
	snap.ArgsNote = &note
	snap.Sections = map[string]json.RawMessage{}
}

// digestOf is sha256 of the snapshot's JCS form.
func digestOf(b []byte) (string, error) { return deed.DigestJSON(b) }

// fitContext keeps the snapshot under max bytes: the recent actions go first, then the sections,
// then the arguments (with a note). It returns the JSON.
func fitContext(s *Snapshot, max int) ([]byte, error) {
	if max <= 0 {
		max = maxContextBytes
	}
	for step := 0; ; step++ {
		b, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		if len(b) <= max {
			return b, nil
		}
		switch step {
		case 0:
			s.Agent.Recent = []SnapRecent{}
		case 1:
			s.Sections = map[string]json.RawMessage{}
		case 2:
			note := "The arguments are too large to show here. The full request is in Console › Actions."
			s.Args, s.ArgsNote = json.RawMessage(`null`), &note
		default:
			return nil, errors.New("approve: the context snapshot is too large even without arguments")
		}
	}
}

// parseSnapshot reads a stored snapshot (nil when the approval has none: S13's stub rows).
func parseSnapshot(b []byte) (*Snapshot, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// summaryFacts are the summary's inputs from a snapshot.
func (s *Snapshot) summaryFacts() SummaryFacts {
	f := SummaryFacts{Tool: s.Action.Tool}
	if s.SQL != nil && s.SQL.Kind != "" && s.SQL.Kind != "unknown" {
		f.SQLKind, f.Tables = s.SQL.Kind, s.SQL.Tables
		if e := s.SQL.Estimate; e != nil && e.Rows != nil {
			f.Rows, f.Estimated = *e.Rows, true
		}
	}
	return f
}

// sqlText is the statement in the arguments ("" when the tool takes none), for the page, the CLI
// and (approvals.email_detail: full) the email.
func (s *Snapshot) sqlText() string {
	var a struct {
		SQL string `json:"sql"`
	}
	if json.Unmarshal(s.Args, &a) != nil {
		return ""
	}
	return strings.TrimSpace(a.SQL)
}

// formatTime is the snapshot's time format: RFC 3339, milliseconds, UTC.
func formatTime(t time.Time) string { return deed.FormatTime(t) }
