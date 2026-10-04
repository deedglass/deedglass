package sim

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	simfiles "github.com/deedglass/deedglass/examples/sim"
	"github.com/deedglass/deedglass/internal/policy"
)

// Scenario is one parsed, checked scenario file (S27 §3.3). Build it with Load, Parse or Default.
type Scenario struct {
	V         int
	Org       Org
	Policies  Policies
	Approvers []string
	Agents    []Agent
	Incidents []Incident
	Source    string // the file name, for messages

	enforce *policy.Compiled // compiled once by Parse; Plan compiles it when nil
}

// Org is the simulated org.
type Org struct {
	Slug, Name, Timezone string
	Location             *time.Location
}

// Policies are the policy files the scenario names, read relative to the scenario file.
type Policies struct {
	EnforceFile   string
	Enforce       []byte
	ShadowFile    string // "" when there is no shadow policy
	Shadow        []byte // nil when there is no shadow policy
	ShadowFromDay int    // 0 when there is no shadow policy
}

// Agent is one simulated agent.
type Agent struct {
	Name             string
	HourFrom, HourTo int // org-local, from < to, 0 to 24
	PerDay           int
	UserRefs         *UserRefs // nil when the agent acts for no one
	Tools            []ToolSpec
	Outcomes         Outcomes
	Levels           Levels
	Approvals        Approvals
	Line             int
}

// UserRefs is the pool of on-behalf-of refs an agent draws from: Pool refs "<Prefix><4 digits>".
type UserRefs struct {
	Pool   int
	Prefix string
}

// ToolSpec is one tool an agent calls, with its relative weight.
type ToolSpec struct {
	Tool   string // <connector>.<tool>
	Weight float64
	Args   []ArgSpec // nil: realistic generated arguments for the tool
	SQL    []string  // postgres tools only; {id} and {sku} are filled in
	Line   int
}

// ArgSpec is one argument: a fixed value (string, int64, float64 or bool) or a lognormal draw.
type ArgSpec struct {
	Key       string
	Value     any
	Lognormal *Lognormal
}

// Lognormal is a lognormal distribution given by its median and sigma (of the log).
type Lognormal struct {
	Median, Sigma float64
}

// Outcomes are the fractions of an agent's calls that end each way. They add up to 1.
type Outcomes struct {
	Verified, Unverifiable, Failed float64
}

// Levels are the fractions of an agent's calls the scripted Witness takes to each level.
type Levels struct {
	L0, L1, L2 float64
}

// Approvals script how people answer when the policy asks for an approval.
type Approvals struct {
	Approve             float64 // the fraction approved; the rest are denied
	WaitMedian, WaitP95 time.Duration
}

// Incident kinds.
const (
	KindMismatch       = "mismatch"
	KindFirstUse       = "first_use"
	KindSpike          = "spike"
	KindOffHours       = "off_hours"
	KindNewUserRefs    = "new_user_refs"
	KindBudgetPressure = "budget_pressure"
)

// Incident is one scripted event on top of the baseline traffic.
type Incident struct {
	Kind   string
	Days   []int // 1-based; a single day: is a list of one
	Hour   int   // org-local hour inside the day; unused by budget_pressure
	Agent  string
	Tool   string  // mismatch, first_use, spike
	Table  string  // mismatch
	Count  int     // mismatch, first_use, off_hours, new_user_refs
	Factor float64 // spike
	Budget string  // budget_pressure
	Target float64 // budget_pressure, in (0, 1]
	Line   int
}

// Limits of a scenario.
const (
	MaxDays      = 90
	MaxAgents    = 50
	MaxTools     = 50
	MaxIncidents = 200
	MaxPerDay    = 100000
	MaxCount     = 10000
	MaxPool      = 9000
)

// Connectors are the connector types a simulated tool may use.
var Connectors = []string{"postgres", "stripe", "gmail", "slack", "notion", "github"}

// IncidentKinds lists every incident kind.
var IncidentKinds = []string{KindMismatch, KindFirstUse, KindSpike, KindOffHours, KindNewUserRefs, KindBudgetPressure}

// Defaults for keys a scenario leaves out.
var (
	DefaultOutcomes  = Outcomes{Verified: 1}
	DefaultLevels    = Levels{L0: 1}
	DefaultApprovals = Approvals{Approve: 1, WaitMedian: 5 * time.Minute, WaitP95: 30 * time.Minute}
)

// Error is a problem in a scenario file, at a line.
type Error struct {
	File string
	Line int // 0: the whole file
	Msg  string
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg)
	}
	return e.File + ": " + e.Msg
}

// Load reads a scenario file and the policy files it names (relative to the scenario file).
func Load(path string) (*Scenario, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{File: path, Msg: fmt.Sprintf("can't read the scenario (%v); check the path and try again.", unwrapPath(err))}
	}
	dir := filepath.Dir(path)
	return Parse(path, src, func(name string) ([]byte, error) {
		if filepath.IsAbs(name) {
			return os.ReadFile(name)
		}
		return os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	})
}

// Default returns the embedded default scenario, examples/sim/acme.yaml. It panics if the
// embedded files don't parse; a test pins that they do.
func Default() *Scenario {
	sc, err := Parse("acme.yaml", simfiles.AcmeScenario, simfiles.FS.ReadFile)
	if err != nil {
		panic("sim: the embedded default scenario does not parse: " + err.Error())
	}
	return sc
}

// Parse checks a scenario strictly: an unknown key anywhere, a wrong type or a value out of range
// is an error with its line. readFile reads the policy files the scenario names.
func Parse(name string, src []byte, readFile func(name string) ([]byte, error)) (*Scenario, error) {
	p := &parser{file: name}
	root, err := p.document(src)
	if err != nil {
		return nil, err
	}
	sc := &Scenario{Source: name}
	if err := p.scenario(root, sc, readFile); err != nil {
		return nil, err
	}
	return sc, nil
}

func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// ---- the YAML layer ----

type parser struct{ file string }

func (p *parser) errf(line int, format string, args ...any) error {
	return &Error{File: p.file, Line: line, Msg: fmt.Sprintf(format, args...)}
}

var yamlLine = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

// document reads exactly one YAML document and refuses anchors, aliases and custom tags.
func (p *parser) document(src []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, p.errf(0, "the scenario is empty; start from examples/sim/acme.yaml.")
		}
		msg := strings.TrimSpace(err.Error())
		if m := yamlLine.FindStringSubmatch(msg); m != nil {
			n, _ := strconv.Atoi(m[1])
			return nil, p.errf(n, "the YAML can't be read (%s); check the syntax on this line.", m[2])
		}
		return nil, p.errf(0, "the YAML can't be read (%s); check the syntax.", strings.TrimPrefix(msg, "yaml: "))
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, p.errf(extra.Line, "the file holds more than one YAML document; keep one scenario per file.")
	} else if !errors.Is(err, io.EOF) {
		return nil, p.errf(0, "the YAML after the first document can't be read; keep one scenario per file.")
	}
	if len(doc.Content) == 0 {
		return nil, p.errf(0, "the scenario is empty; start from examples/sim/acme.yaml.")
	}
	if err := p.plain(doc.Content[0]); err != nil {
		return nil, err
	}
	return doc.Content[0], nil
}

func (p *parser) plain(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return p.errf(n.Line, "anchors and aliases are not supported; write the value out in full.")
	}
	if n.Tag != "" && !strings.HasPrefix(n.Tag, "!!") {
		return p.errf(n.Line, "the tag %s is not supported; remove it.", n.Tag)
	}
	for _, c := range n.Content {
		if err := p.plain(c); err != nil {
			return err
		}
	}
	return nil
}

func in(where string) string {
	if where == "" {
		return "at the top level"
	}
	return "in " + where
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// fields is a checked mapping: every key known, none twice.
type fields struct {
	node *yaml.Node
	vals map[string]*yaml.Node
	keys map[string]*yaml.Node
}

func (f fields) get(k string) *yaml.Node { return f.vals[k] }

func (p *parser) mapping(n *yaml.Node, where string, allowed, required []string) (fields, error) {
	f := fields{node: n, vals: map[string]*yaml.Node{}, keys: map[string]*yaml.Node{}}
	if n.Kind != yaml.MappingNode {
		what := where
		if what == "" {
			what = "the scenario"
		}
		return f, p.errf(n.Line, "%s must be a mapping of keys to values; see examples/sim/acme.yaml for the layout.", what)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
			return f, p.errf(k.Line, "a key %s must be plain text; check the indentation.", in(where))
		}
		if _, dup := f.vals[k.Value]; dup {
			return f, p.errf(k.Line, "key %q appears twice %s; keep one of them.", k.Value, in(where))
		}
		if !slices.Contains(allowed, k.Value) {
			if s := suggest(k.Value, allowed); s != "" {
				return f, p.errf(k.Line, "unknown key %q %s; did you mean %q?", k.Value, in(where), s)
			}
			return f, p.errf(k.Line, "unknown key %q %s; use %s.", k.Value, in(where), orList(allowed))
		}
		f.vals[k.Value], f.keys[k.Value] = v, k
	}
	for _, r := range required {
		if _, ok := f.vals[r]; !ok {
			what := where
			if what == "" {
				return f, p.errf(n.Line, "the scenario has no %s; add it (see examples/sim/acme.yaml).", r)
			}
			return f, p.errf(n.Line, "%s has no %s; add it.", what, r)
		}
	}
	return f, nil
}

func (p *parser) str(n *yaml.Node, path string) (string, error) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", p.errf(n.Line, "%s must be text; quote it if it looks like a number.", path)
	}
	return n.Value, nil
}

func (p *parser) integer(n *yaml.Node, path string, lo, hi int) (int, error) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
		return 0, p.errf(n.Line, "%s must be a whole number from %d to %s; write it without quotes.", path, lo, commas(hi))
	}
	var v int64
	if err := n.Decode(&v); err != nil || v < int64(lo) || v > int64(hi) {
		return 0, p.errf(n.Line, "%s is %s; use a whole number from %d to %s.", path, n.Value, lo, commas(hi))
	}
	return int(v), nil
}

func (p *parser) number(n *yaml.Node, path string) (float64, error) {
	if n.Kind != yaml.ScalarNode || (n.Tag != "!!int" && n.Tag != "!!float") {
		return 0, p.errf(n.Line, "%s must be a number; write it without quotes.", path)
	}
	var v float64
	if err := n.Decode(&v); err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, p.errf(n.Line, "%s must be a finite number; write it like 0.5.", path)
	}
	return v, nil
}

func (p *parser) fraction(n *yaml.Node, path string) (float64, error) {
	v, err := p.number(n, path)
	if err != nil {
		return 0, err
	}
	if v < 0 || v > 1 {
		return 0, p.errf(n.Line, "%s is %s; use a fraction from 0 to 1.", path, n.Value)
	}
	return v, nil
}

func (p *parser) seq(n *yaml.Node, path, example string) ([]*yaml.Node, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, p.errf(n.Line, "%s must be a list, like %s.", path, example)
	}
	return n.Content, nil
}

func (p *parser) duration(n *yaml.Node, path string) (time.Duration, error) {
	s, err := p.str(n, path)
	if err != nil {
		return 0, err
	}
	d, perr := time.ParseDuration(s)
	if perr != nil || d <= 0 {
		return 0, p.errf(n.Line, "%s %q is not a duration; write it like \"20m\" or \"2h\".", path, s)
	}
	if d > 7*24*time.Hour {
		return 0, p.errf(n.Line, "%s %q is longer than 7 days; use a shorter wait.", path, s)
	}
	return d, nil
}

// ---- the scenario ----

var (
	slugRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	hoursRe    = regexp.MustCompile(`^(\d{2})-(\d{2})$`)
	toolRe     = regexp.MustCompile(`^([a-z][a-z0-9_]*)\.([a-z][a-z0-9_]*)$`)
	argKeyRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	prefixRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)
	tableRe    = regexp.MustCompile(`^[a-z_][a-z0-9_]*\.[a-z_][a-z0-9_]*$`)
	approverRe = regexp.MustCompile(`^[a-z0-9._%+-]+@([a-z0-9-]+\.)+test$`)
	holeRe     = regexp.MustCompile(`\{[^{}]*\}`)
)

var (
	topKeys      = []string{"v", "org", "policies", "approvers", "agents", "incidents"}
	orgKeys      = []string{"slug", "name", "timezone"}
	policyKeys   = []string{"enforce", "shadow"}
	shadowKeys   = []string{"file", "from_day"}
	agentKeys    = []string{"name", "hours", "per_day", "user_refs", "tools", "outcomes", "levels", "approvals"}
	userRefKeys  = []string{"pool", "prefix"}
	toolKeys     = []string{"tool", "weight", "args", "sql"}
	outcomeKeys  = []string{"verified", "unverifiable", "failed"}
	levelKeys    = []string{"L0", "L1", "L2"}
	approvalKeys = []string{"approve", "wait"}
	waitKeys     = []string{"median", "p95"}
	incidentKeys = []string{"day", "days", "hour", "agent", "kind", "tool", "table", "count", "factor", "budget", "target"}
	// kindFields are the kind-specific keys each incident kind needs; the others don't apply.
	kindFields = map[string][]string{
		KindMismatch:       {"tool", "table", "count"},
		KindFirstUse:       {"tool", "count"},
		KindSpike:          {"tool", "factor"},
		KindOffHours:       {"count"},
		KindNewUserRefs:    {"count"},
		KindBudgetPressure: {"budget", "target"},
	}
	kindSpecific = []string{"tool", "table", "count", "factor", "budget", "target"}
)

func (p *parser) scenario(root *yaml.Node, sc *Scenario, readFile func(string) ([]byte, error)) error {
	top, err := p.mapping(root, "", topKeys, []string{"v", "org", "policies", "approvers", "agents"})
	if err != nil {
		return err
	}
	vn := top.get("v")
	if vn.Kind != yaml.ScalarNode || vn.Tag != "!!int" || vn.Value != "1" {
		return p.errf(vn.Line, "v is %s, and this Deedglass reads scenario format 1; set v: 1.", vn.Value)
	}
	sc.V = 1
	if err := p.org(top.get("org"), &sc.Org); err != nil {
		return err
	}
	if err := p.policies(top.get("policies"), sc, readFile); err != nil {
		return err
	}
	if err := p.approvers(top.get("approvers"), sc); err != nil {
		return err
	}
	items, err := p.seq(top.get("agents"), "agents", "[ { name: support-bot, hours: \"09-19\", per_day: 200, tools: [...] } ]")
	if err != nil {
		return err
	}
	if len(items) == 0 || len(items) > MaxAgents {
		return p.errf(top.get("agents").Line, "agents lists %d agents; list 1 to %d.", len(items), MaxAgents)
	}
	for i, n := range items {
		a, err := p.agent(n, fmt.Sprintf("agents[%d]", i))
		if err != nil {
			return err
		}
		if slices.ContainsFunc(sc.Agents, func(b Agent) bool { return b.Name == a.Name }) {
			return p.errf(n.Line, "agents[%d] is named %q like an earlier agent; give each agent its own name.", i, a.Name)
		}
		sc.Agents = append(sc.Agents, a)
	}
	if n := top.get("incidents"); n != nil {
		items, err := p.seq(n, "incidents", "[ { day: 5, hour: 11, agent: ops-agent, kind: spike, tool: postgres.query, factor: 4 } ]")
		if err != nil {
			return err
		}
		if len(items) > MaxIncidents {
			return p.errf(n.Line, "incidents lists %d incidents; list at most %d.", len(items), MaxIncidents)
		}
		for i, n := range items {
			inc, err := p.incident(n, fmt.Sprintf("incidents[%d]", i), sc)
			if err != nil {
				return err
			}
			sc.Incidents = append(sc.Incidents, inc)
		}
	}
	return nil
}

func (p *parser) org(n *yaml.Node, o *Org) error {
	f, err := p.mapping(n, "org", orgKeys, orgKeys)
	if err != nil {
		return err
	}
	if o.Slug, err = p.str(f.get("slug"), "org.slug"); err != nil {
		return err
	}
	if !slugRe.MatchString(o.Slug) {
		return p.errf(f.get("slug").Line, "org.slug %q must be 2 to 63 lowercase letters, digits and dashes, starting with a letter; use something like \"acme-sim\".", clip(o.Slug))
	}
	if o.Name, err = p.str(f.get("name"), "org.name"); err != nil {
		return err
	}
	if strings.TrimSpace(o.Name) == "" || len([]rune(o.Name)) > 100 {
		return p.errf(f.get("name").Line, "org.name must be 1 to 100 characters; write a short display name.")
	}
	if o.Timezone, err = p.str(f.get("timezone"), "org.timezone"); err != nil {
		return err
	}
	loc, lerr := time.LoadLocation(o.Timezone)
	if lerr != nil || o.Timezone == "" || o.Timezone == "Local" {
		return p.errf(f.get("timezone").Line, "org.timezone %q is not a known IANA time zone; use a name like \"Asia/Kolkata\" or \"UTC\".", clip(o.Timezone))
	}
	o.Location = loc
	return nil
}

func (p *parser) policies(n *yaml.Node, sc *Scenario, readFile func(string) ([]byte, error)) error {
	f, err := p.mapping(n, "policies", policyKeys, []string{"enforce"})
	if err != nil {
		return err
	}
	if sc.Policies.EnforceFile, err = p.str(f.get("enforce"), "policies.enforce"); err != nil {
		return err
	}
	c, src, err := p.policyFile(f.get("enforce"), "policies.enforce", sc.Policies.EnforceFile, readFile)
	if err != nil {
		return err
	}
	sc.Policies.Enforce, sc.enforce = src, c
	if sn := f.get("shadow"); sn != nil {
		sf, err := p.mapping(sn, "policies.shadow", shadowKeys, shadowKeys)
		if err != nil {
			return err
		}
		if sc.Policies.ShadowFile, err = p.str(sf.get("file"), "policies.shadow.file"); err != nil {
			return err
		}
		if sc.Policies.ShadowFromDay, err = p.integer(sf.get("from_day"), "policies.shadow.from_day", 1, MaxDays); err != nil {
			return err
		}
		if _, sc.Policies.Shadow, err = p.policyFile(sf.get("file"), "policies.shadow.file", sc.Policies.ShadowFile, readFile); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) policyFile(n *yaml.Node, path, name string, readFile func(string) ([]byte, error)) (*policy.Compiled, []byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil, p.errf(n.Line, "%s is empty; name a policy file, like \"acme-sim.v1.yaml\".", path)
	}
	if readFile == nil {
		return nil, nil, p.errf(n.Line, "%s %q can't be read here; load the scenario from a file with sim.Load.", path, clip(name))
	}
	src, err := readFile(name)
	if err != nil {
		return nil, nil, p.errf(n.Line, "%s %q can't be read (%v); check the path, relative to the scenario file.", path, clip(name), unwrapPath(err))
	}
	c, diags := policy.Compile(src, policy.Options{File: name})
	if c == nil {
		first := "it has errors"
		for _, d := range diags {
			if d.Severity == policy.SevError {
				first = fmt.Sprintf("line %d: %s", d.Line, strings.TrimSuffix(d.Message, "."))
				break
			}
		}
		return nil, nil, p.errf(n.Line, "%s %q does not compile (%s); run deedglass policy lint on it to see every problem.", path, clip(name), first)
	}
	return c, src, nil
}

func (p *parser) approvers(n *yaml.Node, sc *Scenario) error {
	items, err := p.seq(n, "approvers", "[priya@example.test]")
	if err != nil {
		return err
	}
	if len(items) == 0 || len(items) > 32 {
		return p.errf(n.Line, "approvers lists %d people; list 1 to 32 addresses under .test.", len(items))
	}
	for i, it := range items {
		path := fmt.Sprintf("approvers[%d]", i)
		s, err := p.str(it, path)
		if err != nil {
			return err
		}
		if !approverRe.MatchString(s) {
			return p.errf(it.Line, "%s %q must be a lowercase address under the reserved .test domain, like \"priya@example.test\"; change the domain.", path, clip(s))
		}
		if slices.Contains(sc.Approvers, s) {
			return p.errf(it.Line, "%s %q is listed twice; keep one of them.", path, s)
		}
		sc.Approvers = append(sc.Approvers, s)
	}
	return nil
}

func (p *parser) agent(n *yaml.Node, path string) (Agent, error) {
	a := Agent{Line: n.Line, Outcomes: DefaultOutcomes, Levels: DefaultLevels, Approvals: DefaultApprovals}
	f, err := p.mapping(n, path, agentKeys, []string{"name", "hours", "per_day", "tools"})
	if err != nil {
		return a, err
	}
	if a.Name, err = p.str(f.get("name"), path+".name"); err != nil {
		return a, err
	}
	if !slugRe.MatchString(a.Name) {
		return a, p.errf(f.get("name").Line, "%s.name %q must be 2 to 63 lowercase letters, digits and dashes, starting with a letter; use something like \"support-bot\".", path, clip(a.Name))
	}
	hn := f.get("hours")
	hs, err := p.str(hn, path+".hours")
	if err != nil {
		return a, err
	}
	m := hoursRe.FindStringSubmatch(hs)
	if m == nil {
		return a, p.errf(hn.Line, "%s.hours %q must be two org-local hours, like \"09-19\"; write both with two digits.", path, clip(hs))
	}
	a.HourFrom, _ = strconv.Atoi(m[1])
	a.HourTo, _ = strconv.Atoi(m[2])
	if a.HourTo > 24 || a.HourFrom >= a.HourTo {
		return a, p.errf(hn.Line, "%s.hours %q must start before it ends, within 00 to 24; write it like \"09-19\".", path, hs)
	}
	if a.PerDay, err = p.integer(f.get("per_day"), path+".per_day", 1, MaxPerDay); err != nil {
		return a, err
	}
	if un := f.get("user_refs"); un != nil {
		uf, err := p.mapping(un, path+".user_refs", userRefKeys, userRefKeys)
		if err != nil {
			return a, err
		}
		u := &UserRefs{}
		if u.Pool, err = p.integer(uf.get("pool"), path+".user_refs.pool", 1, MaxPool); err != nil {
			return a, err
		}
		if u.Prefix, err = p.str(uf.get("prefix"), path+".user_refs.prefix"); err != nil {
			return a, err
		}
		if !prefixRe.MatchString(u.Prefix) {
			return a, p.errf(uf.get("prefix").Line, "%s.user_refs.prefix %q must be 1 to 16 lowercase letters, digits and underscores, starting with a letter; use something like \"cust_\".", path, clip(u.Prefix))
		}
		a.UserRefs = u
	}
	tn := f.get("tools")
	items, err := p.seq(tn, path+".tools", "[ { tool: postgres.query, weight: 100 } ]")
	if err != nil {
		return a, err
	}
	if len(items) == 0 || len(items) > MaxTools {
		return a, p.errf(tn.Line, "%s.tools lists %d tools; list 1 to %d.", path, len(items), MaxTools)
	}
	for i, it := range items {
		t, err := p.toolSpec(it, fmt.Sprintf("%s.tools[%d]", path, i))
		if err != nil {
			return a, err
		}
		a.Tools = append(a.Tools, t)
	}
	if on := f.get("outcomes"); on != nil {
		v, err := p.fractions(on, path+".outcomes", outcomeKeys)
		if err != nil {
			return a, err
		}
		a.Outcomes = Outcomes{Verified: v[0], Unverifiable: v[1], Failed: v[2]}
	}
	if ln := f.get("levels"); ln != nil {
		v, err := p.fractions(ln, path+".levels", levelKeys)
		if err != nil {
			return a, err
		}
		a.Levels = Levels{L0: v[0], L1: v[1], L2: v[2]}
	}
	if an := f.get("approvals"); an != nil {
		af, err := p.mapping(an, path+".approvals", approvalKeys, nil)
		if err != nil {
			return a, err
		}
		if v := af.get("approve"); v != nil {
			if a.Approvals.Approve, err = p.fraction(v, path+".approvals.approve"); err != nil {
				return a, err
			}
		}
		if wn := af.get("wait"); wn != nil {
			wf, err := p.mapping(wn, path+".approvals.wait", waitKeys, waitKeys)
			if err != nil {
				return a, err
			}
			if a.Approvals.WaitMedian, err = p.duration(wf.get("median"), path+".approvals.wait.median"); err != nil {
				return a, err
			}
			if a.Approvals.WaitP95, err = p.duration(wf.get("p95"), path+".approvals.wait.p95"); err != nil {
				return a, err
			}
			if a.Approvals.WaitP95 < a.Approvals.WaitMedian {
				return a, p.errf(wf.get("p95").Line, "%s.approvals.wait.p95 (%s) is shorter than the median (%s); set p95 at or above the median.",
					path, wf.get("p95").Value, wf.get("median").Value)
			}
		}
	}
	return a, nil
}

// fractions reads a mapping of fractions that must add up to 1; a missing key is 0.
func (p *parser) fractions(n *yaml.Node, path string, keys []string) ([]float64, error) {
	f, err := p.mapping(n, path, keys, nil)
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(keys))
	sum := 0.0
	for i, k := range keys {
		if v := f.get(k); v != nil {
			if out[i], err = p.fraction(v, path+"."+k); err != nil {
				return nil, err
			}
			sum += out[i]
		}
	}
	if math.Abs(sum-1) > 1e-9 {
		return nil, p.errf(n.Line, "%s add up to %s; make %s add up to 1.", path, strconv.FormatFloat(math.Round(sum*1e6)/1e6, 'f', -1, 64), andList(keys))
	}
	return out, nil
}

func (p *parser) toolName(n *yaml.Node, path string) (string, error) {
	s, err := p.str(n, path)
	if err != nil {
		return "", err
	}
	m := toolRe.FindStringSubmatch(s)
	if m == nil {
		return "", p.errf(n.Line, "%s %q must be <connector>.<tool> in lowercase, like \"stripe.create_refund\"; fix the name.", path, clip(s))
	}
	if !slices.Contains(Connectors, m[1]) {
		if sg := suggest(m[1], Connectors); sg != "" {
			return "", p.errf(n.Line, "%s %q uses the connector %q; did you mean %q?", path, clip(s), m[1], sg+"."+m[2])
		}
		return "", p.errf(n.Line, "%s %q uses the connector %q; use %s.", path, clip(s), m[1], orList(Connectors))
	}
	return s, nil
}

func (p *parser) toolSpec(n *yaml.Node, path string) (ToolSpec, error) {
	t := ToolSpec{Line: n.Line}
	f, err := p.mapping(n, path, toolKeys, []string{"tool", "weight"})
	if err != nil {
		return t, err
	}
	if t.Tool, err = p.toolName(f.get("tool"), path+".tool"); err != nil {
		return t, err
	}
	wn := f.get("weight")
	if t.Weight, err = p.number(wn, path+".weight"); err != nil {
		return t, err
	}
	if t.Weight <= 0 || t.Weight > 1e6 {
		return t, p.errf(wn.Line, "%s.weight is %s; use a number above 0, like 30.", path, wn.Value)
	}
	isPG := connectorOf(t.Tool) == "postgres"
	if an := f.get("args"); an != nil {
		if an.Kind != yaml.MappingNode {
			return t, p.errf(an.Line, "%s.args must be a mapping of argument names to values, like { currency: usd }.", path)
		}
		t.Args = []ArgSpec{}
		for i := 0; i+1 < len(an.Content); i += 2 {
			k, v := an.Content[i], an.Content[i+1]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || !argKeyRe.MatchString(k.Value) {
				return t, p.errf(k.Line, "%s has the key %q; argument names are lowercase letters, digits and underscores.", path+".args", clip(k.Value))
			}
			if slices.ContainsFunc(t.Args, func(a ArgSpec) bool { return a.Key == k.Value }) {
				return t, p.errf(k.Line, "key %q appears twice in %s.args; keep one of them.", k.Value, path)
			}
			if isPG && k.Value == "sql" {
				return t, p.errf(k.Line, "%s.args.sql is set with sql: for postgres tools; move it to %s.sql.", path, path)
			}
			arg, err := p.argValue(v, path+".args."+k.Value)
			if err != nil {
				return t, err
			}
			arg.Key = k.Value
			t.Args = append(t.Args, arg)
		}
	}
	if sn := f.get("sql"); sn != nil {
		if !isPG {
			return t, p.errf(f.keys["sql"].Line, "%s.sql applies only to postgres tools; remove it, or use args for %s.", path, t.Tool)
		}
		items, err := p.seq(sn, path+".sql", `["SELECT id FROM public.orders WHERE id = {id}"]`)
		if err != nil {
			return t, err
		}
		if len(items) == 0 || len(items) > 32 {
			return t, p.errf(sn.Line, "%s.sql lists %d statements; list 1 to 32.", path, len(items))
		}
		for i, it := range items {
			sp := fmt.Sprintf("%s.sql[%d]", path, i)
			s, err := p.str(it, sp)
			if err != nil {
				return t, err
			}
			if strings.TrimSpace(s) == "" || len(s) > 4096 {
				return t, p.errf(it.Line, "%s must be 1 to 4,096 characters of SQL; write the statement out.", sp)
			}
			for _, h := range holeRe.FindAllString(s, -1) {
				if h != "{id}" && h != "{sku}" {
					return t, p.errf(it.Line, "%s has the placeholder %s; use {id} or {sku}.", sp, h)
				}
			}
			t.SQL = append(t.SQL, s)
		}
	}
	return t, nil
}

func (p *parser) argValue(n *yaml.Node, path string) (ArgSpec, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!str":
			return ArgSpec{Value: n.Value}, nil
		case "!!int":
			var v int64
			if err := n.Decode(&v); err != nil || v > policy.MaxMinor || v < -policy.MaxMinor {
				return ArgSpec{}, p.errf(n.Line, "%s is %s; use a whole number of at most 2^53 - 1.", path, n.Value)
			}
			return ArgSpec{Value: v}, nil
		case "!!float":
			v, err := p.number(n, path)
			return ArgSpec{Value: v}, err
		case "!!bool":
			var v bool
			_ = n.Decode(&v)
			return ArgSpec{Value: v}, nil
		}
		return ArgSpec{}, p.errf(n.Line, "%s has no value; write one, or remove the key.", path)
	case yaml.MappingNode:
		f, err := p.mapping(n, path, []string{"lognormal"}, []string{"lognormal"})
		if err != nil {
			return ArgSpec{}, err
		}
		lf, err := p.mapping(f.get("lognormal"), path+".lognormal", []string{"median", "sigma"}, []string{"median", "sigma"})
		if err != nil {
			return ArgSpec{}, err
		}
		med, err := p.number(lf.get("median"), path+".lognormal.median")
		if err != nil {
			return ArgSpec{}, err
		}
		if med < 1 || med > 1e12 {
			return ArgSpec{}, p.errf(lf.get("median").Line, "%s.lognormal.median is %s; use a number from 1 to 1e12.", path, lf.get("median").Value)
		}
		sig, err := p.number(lf.get("sigma"), path+".lognormal.sigma")
		if err != nil {
			return ArgSpec{}, err
		}
		if sig < 0 || sig > 3 {
			return ArgSpec{}, p.errf(lf.get("sigma").Line, "%s.lognormal.sigma is %s; use a number from 0 to 3.", path, lf.get("sigma").Value)
		}
		return ArgSpec{Lognormal: &Lognormal{Median: med, Sigma: sig}}, nil
	}
	return ArgSpec{}, p.errf(n.Line, "%s must be a plain value or { lognormal: { median, sigma } }; lists are not supported.", path)
}

func (p *parser) incident(n *yaml.Node, path string, sc *Scenario) (Incident, error) {
	inc := Incident{Line: n.Line}
	f, err := p.mapping(n, path, incidentKeys, []string{"agent", "kind"})
	if err != nil {
		return inc, err
	}
	kn := f.get("kind")
	if inc.Kind, err = p.str(kn, path+".kind"); err != nil {
		return inc, err
	}
	if !slices.Contains(IncidentKinds, inc.Kind) {
		if s := suggest(inc.Kind, IncidentKinds); s != "" {
			return inc, p.errf(kn.Line, "%s.kind %q is not an incident kind; did you mean %q?", path, clip(inc.Kind), s)
		}
		return inc, p.errf(kn.Line, "%s.kind %q is not an incident kind; use %s.", path, clip(inc.Kind), orList(IncidentKinds))
	}
	an := f.get("agent")
	if inc.Agent, err = p.str(an, path+".agent"); err != nil {
		return inc, err
	}
	ai := slices.IndexFunc(sc.Agents, func(a Agent) bool { return a.Name == inc.Agent })
	if ai < 0 {
		names := make([]string, len(sc.Agents))
		for i, a := range sc.Agents {
			names[i] = a.Name
		}
		if s := suggest(inc.Agent, names); s != "" {
			return inc, p.errf(an.Line, "%s.agent %q is not one of the agents; did you mean %q?", path, clip(inc.Agent), s)
		}
		return inc, p.errf(an.Line, "%s.agent %q is not one of the agents; use %s.", path, clip(inc.Agent), orList(names))
	}
	ag := sc.Agents[ai]
	// Which kind-specific keys apply.
	need := kindFields[inc.Kind]
	for _, k := range kindSpecific {
		if f.get(k) != nil && !slices.Contains(need, k) {
			return inc, p.errf(f.keys[k].Line, "%s.%s does not apply to a %s incident; remove it.", path, k, inc.Kind)
		}
	}
	for _, k := range need {
		if f.get(k) == nil {
			return inc, p.errf(n.Line, "%s is a %s incident with no %s; add %s.", path, inc.Kind, k, andList(need))
		}
	}
	// day or days
	dn, dsn := f.get("day"), f.get("days")
	switch {
	case dn != nil && dsn != nil:
		return inc, p.errf(f.keys["days"].Line, "%s has both day and days; keep one of them.", path)
	case dn != nil:
		d, err := p.integer(dn, path+".day", 1, MaxDays)
		if err != nil {
			return inc, err
		}
		inc.Days = []int{d}
	case dsn != nil:
		items, err := p.seq(dsn, path+".days", "[2, 4, 6]")
		if err != nil {
			return inc, err
		}
		if len(items) == 0 {
			return inc, p.errf(dsn.Line, "%s.days is empty; list at least one day, like [2, 4, 6].", path)
		}
		for i, it := range items {
			d, err := p.integer(it, fmt.Sprintf("%s.days[%d]", path, i), 1, MaxDays)
			if err != nil {
				return inc, err
			}
			if slices.Contains(inc.Days, d) {
				return inc, p.errf(it.Line, "%s.days lists day %d twice; keep one of them.", path, d)
			}
			inc.Days = append(inc.Days, d)
		}
		slices.Sort(inc.Days)
	default:
		return inc, p.errf(n.Line, "%s has no day; add day: <n> or days: [<n>, ...].", path)
	}
	// hour
	hn := f.get("hour")
	if inc.Kind == KindBudgetPressure {
		if hn != nil {
			return inc, p.errf(f.keys["hour"].Line, "%s.hour does not apply to a budget_pressure incident, which spreads over the agent's hours; remove it.", path)
		}
	} else {
		if hn == nil {
			return inc, p.errf(n.Line, "%s has no hour; add the org-local hour, 0 to 23.", path)
		}
		if inc.Hour, err = p.integer(hn, path+".hour", 0, 23); err != nil {
			return inc, err
		}
	}
	if tn := f.get("tool"); tn != nil {
		if inc.Tool, err = p.toolName(tn, path+".tool"); err != nil {
			return inc, err
		}
	}
	if cn := f.get("count"); cn != nil {
		if inc.Count, err = p.integer(cn, path+".count", 1, MaxCount); err != nil {
			return inc, err
		}
	}
	switch inc.Kind {
	case KindMismatch:
		if connectorOf(inc.Tool) != "postgres" {
			return inc, p.errf(f.get("tool").Line, "%s.tool %q can't show a mismatch, because the Witness compares table changes for postgres tools only; use postgres.execute.", path, inc.Tool)
		}
		tb := f.get("table")
		if inc.Table, err = p.str(tb, path+".table"); err != nil {
			return inc, err
		}
		if !tableRe.MatchString(inc.Table) {
			return inc, p.errf(tb.Line, "%s.table %q must be a schema-qualified table, like \"public.inventory\"; add the schema.", path, clip(inc.Table))
		}
	case KindFirstUse:
		if slices.ContainsFunc(ag.Tools, func(t ToolSpec) bool { return t.Tool == inc.Tool }) {
			return inc, p.errf(f.get("tool").Line, "%s.tool %q is already one of %s's tools; pick a tool the agent has never used.", path, inc.Tool, ag.Name)
		}
	case KindSpike:
		fn := f.get("factor")
		if inc.Factor, err = p.number(fn, path+".factor"); err != nil {
			return inc, err
		}
		if inc.Factor <= 1 || inc.Factor > 100 {
			return inc, p.errf(fn.Line, "%s.factor is %s; use a number above 1 and at most 100, like 9.", path, fn.Value)
		}
	case KindOffHours:
		if inc.Hour >= ag.HourFrom && inc.Hour < ag.HourTo {
			return inc, p.errf(hn.Line, "%s.hour %d is inside %s's hours (%02d-%02d); pick an hour outside them.", path, inc.Hour, ag.Name, ag.HourFrom, ag.HourTo)
		}
	case KindNewUserRefs:
		if ag.UserRefs == nil {
			return inc, p.errf(an.Line, "%s needs user_refs on %s to make new ones; add user_refs to the agent.", path, ag.Name)
		}
	case KindBudgetPressure:
		tg := f.get("target")
		if inc.Target, err = p.number(tg, path+".target"); err != nil {
			return inc, err
		}
		if inc.Target <= 0 || inc.Target > 1 {
			return inc, p.errf(tg.Line, "%s.target is %s; use a fraction above 0 and at most 1, like 0.95.", path, tg.Value)
		}
		bn := f.get("budget")
		if inc.Budget, err = p.str(bn, path+".budget"); err != nil {
			return inc, err
		}
		if err := p.pressureBudget(bn, path, inc.Budget, ag.Name, sc); err != nil {
			return inc, err
		}
	}
	return inc, nil
}

func (p *parser) pressureBudget(n *yaml.Node, path, id, agent string, sc *Scenario) error {
	file := sc.Policies.EnforceFile
	i := slices.IndexFunc(sc.enforce.Budgets, func(b policy.Budget) bool { return b.ID == id })
	if i < 0 {
		ids := make([]string, len(sc.enforce.Budgets))
		for j, b := range sc.enforce.Budgets {
			ids[j] = b.ID
		}
		if s := suggest(id, ids); s != "" {
			return p.errf(n.Line, "%s.budget %q is not a budget in %s; did you mean %q?", path, clip(id), file, s)
		}
		return p.errf(n.Line, "%s.budget %q is not a budget in %s; add it to the policy, or name one of its max_spend budgets.", path, clip(id), file)
	}
	b := sc.enforce.Budgets[i]
	if b.MaxSpend == nil {
		return p.errf(n.Line, "%s.budget %q counts calls, not money; name a max_spend budget.", path, id)
	}
	if b.Window != policy.WindowDay {
		return p.errf(n.Line, "%s.budget %q resets every %s; name a budget with window: day.", path, id, b.Window)
	}
	if !b.Match.Match(policy.ToolFacts{Name: refundTool, Connector: "stripe", ConnectorType: "stripe", SideEffect: "write"},
		policy.AgentFacts{ID: policy.LocalAgentID, Name: agent, Labels: map[string]string{}, Status: policy.StatusActive}) {
		return p.errf(n.Line, "%s.budget %q does not cover %s by %s; name a budget that counts refunds.", path, id, refundTool, agent)
	}
	return nil
}

// ---- small helpers ----

func connectorOf(tool string) string {
	c, _, _ := strings.Cut(tool, ".")
	return c
}

func clip(s string) string {
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "..."
	}
	return s
}

func commas(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func orList(xs []string) string  { return list(xs, "or") }
func andList(xs []string) string { return list(xs, "and") }

func list(xs []string, conj string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " " + conj + " " + xs[len(xs)-1]
}

// suggest returns the closest option to word, or "" when none is close: an edit distance of at
// most a third of the word (at least 2), or the same stem before the first underscore or dash.
func suggest(word string, options []string) string {
	best, bestD := "", math.MaxInt
	limit := max(2, len(word)/3)
	stem := func(s string) string {
		if i := strings.IndexAny(s, "_-"); i >= 3 {
			return s[:i]
		}
		return ""
	}
	for _, o := range options {
		d := levenshtein(strings.ToLower(word), strings.ToLower(o))
		if d > limit && (stem(word) == "" || stem(word) != stem(o)) {
			continue
		}
		if d < bestD {
			best, bestD = o, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
