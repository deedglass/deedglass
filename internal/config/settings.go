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
	Enum    []string // SettingEnum: the allowed values
}

// SettingKind is how a setting's value is parsed.
type SettingKind int

const (
	SettingDuration SettingKind = iota // Go duration: 10s, 10m
	SettingInt                         // a whole number
	SettingBool                        // true or false
	SettingEnum                        // one of Enum
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
	{Path: "decoder.enabled", Kind: SettingBool, Default: "true",
		Doc: "Run the decoder on this server. A connector opts in with witness.enabled: true.", Owner: "S20"},
	{Path: "decoder.transport", Kind: SettingEnum, Default: "peek", Enum: []string{"peek"},
		Doc: "How the decoder reads a slot: peek (SQL peek, then advance). Streaming is planned.", Owner: "S20"},
	{Path: "decoder.poll_active", Kind: SettingDuration, Default: "100ms", Min: 10, Max: 1_000,
		Doc: "How often a runner reads its slot while an action waits for L2 evidence.", Owner: "S20"},
	{Path: "decoder.poll_idle", Kind: SettingDuration, Default: "1s", Min: 100, Max: 10_000,
		Doc: "How often a runner reads its slot otherwise. It doubles (to 5s) while a read takes more than a quarter of it.", Owner: "S20"},
	{Path: "decoder.batch_changes", Kind: SettingInt, Default: "5000", Min: 100, Max: 100_000,
		Doc: "Rows one read asks for (upto_nchanges). Postgres returns whole transactions, so a larger one comes in full.", Owner: "S20"},
	{Path: "decoder.max_images_per_tx", Kind: SettingInt, Default: "1000", Min: 0, Max: 10_000,
		Doc: "Row changes per transaction whose before and after images are kept. Counts and the digest cover every change.", Owner: "S20"},
	{Path: "decoder.heartbeat_interval", Kind: SettingDuration, Default: "10s", Min: 1_000, Max: 60_000,
		Doc: "How often a runner writes a heartbeat message to the watched database.", Owner: "S20"},
	{Path: "decoder.heartbeat_deadline", Kind: SettingDuration, Default: "60s", Min: 5_000, Max: 600_000,
		Doc: "With no heartbeat read back within this time, the runner is degraded (stalled).", Owner: "S20"},
	{Path: "decoder.lease", Kind: SettingDuration, Default: "15s", Min: 5_000, Max: 120_000,
		Doc: "How long an instance holds a connector's slot without renewing; another instance takes over after it.", Owner: "S20"},
	{Path: "decoder.reconcile_interval", Kind: SettingDuration, Default: "60s", Min: 5_000, Max: 3_600_000,
		Doc: "How often a runner checks that every ledger row has a frame; one without is recorded as missing.", Owner: "S20"},
	{Path: "decoder.retention", Kind: SettingDuration, Default: "168h", Min: 3_600_000, Max: 31_536_000_000,
		Doc: "How long decoded frames (row images included) are kept. Unattributed and forged-marker frames stay 30 days.", Owner: "S20"},
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

// DecoderSettings are the decoder.* settings, parsed (S20).
type DecoderSettings struct {
	Enabled           bool
	Transport         string
	PollActive        time.Duration
	PollIdle          time.Duration
	BatchChanges      int
	MaxImagesPerTx    int
	HeartbeatInterval time.Duration
	HeartbeatDeadline time.Duration
	Lease             time.Duration
	ReconcileInterval time.Duration
	Retention         time.Duration
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
		"decoder.poll_active": &c.Decoder.PollActive, "decoder.poll_idle": &c.Decoder.PollIdle,
		"decoder.heartbeat_interval": &c.Decoder.HeartbeatInterval, "decoder.heartbeat_deadline": &c.Decoder.HeartbeatDeadline,
		"decoder.lease": &c.Decoder.Lease, "decoder.reconcile_interval": &c.Decoder.ReconcileInterval,
		"decoder.retention": &c.Decoder.Retention,
	}
	ints := map[string]*int{
		"gate.max_args_bytes": &c.Gate.MaxArgsBytes, "gate.max_result_bytes": &c.Gate.MaxResultBytes,
		"gate.worker.concurrency": &c.Gate.WorkerConcurrency, "gate.worker.max_attempts": &c.Gate.WorkerMaxAttempts,
		"witness.max_detail_bytes": &c.Witness.MaxDetailBytes,
		"decoder.batch_changes":    &c.Decoder.BatchChanges, "decoder.max_images_per_tx": &c.Decoder.MaxImagesPerTx,
	}
	bools := map[string]*bool{"decoder.enabled": &c.Decoder.Enabled}
	enums := map[string]*string{"decoder.transport": &c.Decoder.Transport}
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
		case SettingBool:
			switch strings.ToLower(v) {
			case "true":
				*bools[s.Path] = true
			case "false":
				*bools[s.Path] = false
			default:
				l.problem(s.Path, fmt.Sprintf("must be true or false (got %q).", v), "For example "+s.Path+": "+s.Default+".")
			}
		case SettingEnum:
			if !slicesContains(s.Enum, v) {
				l.problem(s.Path, fmt.Sprintf("must be one of %s (got %q).", strings.Join(s.Enum, ", "), v), "For example "+s.Path+": "+s.Default+".")
				continue
			}
			*enums[s.Path] = v
		}
	}
}

func slicesContains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// SettingsMarkdown is the settings table of docs/CONFIG.md (TestSettingsDocs keeps them equal).
func SettingsMarkdown() string {
	var b strings.Builder
	b.WriteString("| Setting | Default | Range | What it does | Added |\n|---|---|---|---|---|\n")
	for _, s := range Settings {
		rng := fmt.Sprintf("%d to %d", s.Min, s.Max)
		switch s.Kind {
		case SettingDuration:
			rng = fmt.Sprintf("%s to %s", time.Duration(s.Min)*time.Millisecond, time.Duration(s.Max)*time.Millisecond)
		case SettingBool:
			rng = "true or false"
		case SettingEnum:
			rng = strings.Join(s.Enum, ", ")
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s |\n", s.Path, s.Default, rng, s.Doc, s.Owner)
	}
	return b.String()
}
