package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Settings are the tunables that live only in deedglass.yaml, in sections (contract §11: gate.*,
// mcp.*, approvals.*, …, never new env vars). A section is a YAML mapping; nested mappings make
// dotted paths ("gate.worker.concurrency"). Unknown paths are problems, like unknown keys.
//
// S13 adds the gate section.
type Setting struct {
	Path    string // "gate.wait_timeout"
	Kind    SettingKind
	Default string
	Min     int64 // durations in milliseconds, integers as is
	Max     int64
	Doc     string
	Owner   string
}

// SettingKind is how a setting's value is parsed.
type SettingKind int

const (
	SettingDuration SettingKind = iota // Go duration: 10s, 10m
	SettingInt                         // a whole number
)

// Settings lists every file-only setting, in display order.
var Settings = []Setting{
	{Path: "gate.wait_timeout", Kind: SettingDuration, Default: "10s", Min: 0, Max: 25_000,
		Doc: "How long POST /v1/actions waits for a gateway action to finish before it answers 202 processing (at most 25s).", Owner: "S13"},
	{Path: "gate.execute_timeout", Kind: SettingDuration, Default: "30s", Min: 100, Max: 600_000,
		Doc: "How long the Gate lets a connector call run. Past it, the outcome is unknown and the deed says UNVERIFIABLE.", Owner: "S13"},
	{Path: "gate.edge_result_timeout", Kind: SettingDuration, Default: "10m", Min: 1_000, Max: 86_400_000,
		Doc: "Proxy mode: how long the edge has to report a result before the action is sealed UNVERIFIABLE (edge_lost).", Owner: "S13"},
	{Path: "gate.max_args_bytes", Kind: SettingInt, Default: "262144", Min: 1_024, Max: 262_144,
		Doc: "The largest args object POST /v1/actions accepts, in bytes (at most 256 KiB, the policy input limit).", Owner: "S13"},
	{Path: "gate.max_result_bytes", Kind: SettingInt, Default: "262144", Min: 1_024, Max: 1_048_576,
		Doc: "The largest tool result stored on the action, in bytes. Larger results are digested, and only the digest is kept.", Owner: "S13"},
	{Path: "gate.worker.concurrency", Kind: SettingInt, Default: "4", Min: 1, Max: 64,
		Doc: "Witness jobs one instance runs at once.", Owner: "S13"},
	{Path: "gate.worker.max_attempts", Kind: SettingInt, Default: "8", Min: 1, Max: 100,
		Doc: "Attempts per witness job that can't run before the Witness decides without it (witness_gave_up, listed as a gap).", Owner: "S13"},
	{Path: "witness.inline_budget", Kind: SettingDuration, Default: "1500ms", Min: 100, Max: 5_000,
		Doc: "The longest an L1 read-back may hold the request path. Past it, the worker finishes the read-back and the agent sees processing.", Owner: "S19"},
	{Path: "witness.readback_timeout", Kind: SettingDuration, Default: "10s", Min: 100, Max: 60_000,
		Doc: "The worker's limit for one read-back job; past it, the read-back is the gap readback_timeout.", Owner: "S19"},
	{Path: "witness.max_detail_bytes", Kind: SettingInt, Default: "65536", Min: 4_096, Max: 262_144,
		Doc: "The largest witness_detail (as JCS), in bytes. Beyond it, diff rows and evidence are cut and counted.", Owner: "S19"},
}

var settingByPath = func() map[string]Setting {
	m := make(map[string]Setting, len(Settings))
	for _, s := range Settings {
		m[s.Path] = s
	}
	return m
}()

// sections are the top-level deedglass.yaml keys that hold settings.
func sections() map[string]bool {
	m := map[string]bool{}
	for _, s := range Settings {
		head, _, _ := strings.Cut(s.Path, ".")
		m[head] = true
	}
	return m
}

// GateSettings are the gate.* settings, parsed.
type GateSettings struct {
	WaitTimeout       time.Duration
	ExecuteTimeout    time.Duration
	EdgeResultTimeout time.Duration
	MaxArgsBytes      int
	MaxResultBytes    int
	WorkerConcurrency int
	WorkerMaxAttempts int
}

// WitnessSettings are the witness.* settings, parsed (S19).
type WitnessSettings struct {
	InlineBudget    time.Duration
	ReadbackTimeout time.Duration
	MaxDetailBytes  int
}

// readSection flattens one section mapping into l.yaml under dotted paths.
func (l *loader) readSection(file, prefix string, n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		l.problem(fmt.Sprintf("%s:%d", file, n.Line), fmt.Sprintf("%q must be a mapping of settings.", prefix),
			"See deedglass.example.yaml for the "+prefix+" section.")
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		path := prefix + "." + k.Value
		if v.Kind == yaml.MappingNode {
			l.readSection(file, path, v)
			continue
		}
		if _, ok := settingByPath[path]; !ok {
			next := "See deedglass.example.yaml for every setting."
			if s := suggest(path, settingPaths()); s != "" {
				next = "Did you mean " + s + "?"
			}
			l.problem(fmt.Sprintf("%s:%d", file, k.Line), fmt.Sprintf("%q isn't a Deedglass setting.", path), next)
			continue
		}
		if _, dup := l.yaml[path]; dup {
			l.problem(fmt.Sprintf("%s:%d", file, k.Line), fmt.Sprintf("%q appears twice.", path), "Keep one.")
			continue
		}
		if v.Kind != yaml.ScalarNode {
			l.problem(fmt.Sprintf("%s:%d", file, v.Line), fmt.Sprintf("%q must be a single value.", path), "Write it on one line.")
			continue
		}
		l.yaml[path] = yamlValue{s: v.Value, line: v.Line}
	}
}

func settingPaths() []string {
	out := make([]string, len(Settings))
	for i, s := range Settings {
		out[i] = s.Path
	}
	return out
}

// parseSettings resolves every setting (deedglass.yaml, else its default) into c.
func (l *loader) parseSettings(c *Config) {
	durs := map[string]*time.Duration{
		"gate.wait_timeout": &c.Gate.WaitTimeout, "gate.execute_timeout": &c.Gate.ExecuteTimeout,
		"gate.edge_result_timeout": &c.Gate.EdgeResultTimeout,
		"witness.inline_budget":    &c.Witness.InlineBudget, "witness.readback_timeout": &c.Witness.ReadbackTimeout,
	}
	ints := map[string]*int{
		"gate.max_args_bytes": &c.Gate.MaxArgsBytes, "gate.max_result_bytes": &c.Gate.MaxResultBytes,
		"gate.worker.concurrency": &c.Gate.WorkerConcurrency, "gate.worker.max_attempts": &c.Gate.WorkerMaxAttempts,
		"witness.max_detail_bytes": &c.Witness.MaxDetailBytes,
	}
	for _, s := range Settings {
		v, src := s.Default, SourceDefault
		if y, ok := l.yaml[s.Path]; ok && y.s != "" {
			v, src = y.s, SourceYAML
		}
		l.raw[s.Path], l.src[s.Path] = v, src
		switch s.Kind {
		case SettingDuration:
			d, err := time.ParseDuration(v)
			if err != nil || d.Milliseconds() < s.Min || d.Milliseconds() > s.Max {
				l.problem(s.Path, fmt.Sprintf("must be a duration from %s to %s (got %q).",
					time.Duration(s.Min)*time.Millisecond, time.Duration(s.Max)*time.Millisecond, v), "For example "+s.Path+": "+s.Default+".")
				continue
			}
			*durs[s.Path] = d
		case SettingInt:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < s.Min || n > s.Max {
				l.problem(s.Path, fmt.Sprintf("must be a whole number from %d to %d (got %q).", s.Min, s.Max, v),
					"For example "+s.Path+": "+s.Default+".")
				continue
			}
			*ints[s.Path] = int(n)
		}
	}
}

// SettingsMarkdown is the settings table of docs/CONFIG.md (TestSettingsDocs keeps them equal).
func SettingsMarkdown() string {
	var b strings.Builder
	b.WriteString("| Setting | Default | Range | What it does | Added |\n|---|---|---|---|---|\n")
	for _, s := range Settings {
		rng := fmt.Sprintf("%d to %d", s.Min, s.Max)
		if s.Kind == SettingDuration {
			rng = fmt.Sprintf("%s to %s", time.Duration(s.Min)*time.Millisecond, time.Duration(s.Max)*time.Millisecond)
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s |\n", s.Path, s.Default, rng, s.Doc, s.Owner)
	}
	return b.String()
}
