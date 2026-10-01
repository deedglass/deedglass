package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cel.dev/cel-go/interpreter"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/sqlguard"
)

// Input is everything one evaluation reads (S11 §3.1). The gate builds one per action (S13) and
// stores it canonically as actions.policy_input. Only raw facts are stored: the SQL facts and the
// time parts are derived at evaluation, so a replay under a newer classifier reflects it.
type Input struct {
	V       int             `json:"v"`
	Org     OrgInput        `json:"org"`
	Agent   AgentInput      `json:"agent"`
	User    UserFacts       `json:"user"`
	Session SessionFacts    `json:"session"`
	Tool    ToolInput       `json:"tool"`
	Args    json.RawMessage `json:"args"`
	Request RequestFacts    `json:"request"`
	Time    time.Time       `json:"time"`
	Usage   UsageSnapshot   `json:"usage"`
	Anomaly AnomalyFacts    `json:"anomaly"`
}

// InputVersion is the input format.
const InputVersion = 1

// OrgInput is the org as the input carries it: the CEL-visible facts plus Stop.
type OrgInput struct {
	OrgFacts
	Stopped bool `json:"stopped"`
}

// AgentInput is the agent as the input carries it: the CEL-visible facts plus S08's budgets,
// which CEL never sees.
type AgentInput struct {
	AgentFacts
	Budgets *AgentBudgets `json:"budgets,omitempty"`
}

// ToolInput is the tool as the input carries it: the CEL-visible facts plus the connector's
// default schema for the SQL classifier.
type ToolInput struct {
	ToolFacts
	DefaultSchema string `json:"default_schema,omitempty"`
}

// Limits of the input.
const (
	MaxArgsBytes = 256 << 10 // the args object, as JSON
	MaxArgsDepth = 32
)

// Agent statuses (S08).
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusStopped   = "stopped"
)

// Digest is "sha256:" + hex(sha256(JCS(in))), so a Console trace can be matched to a stored action.
func (in Input) Digest() (string, error) { return deed.DigestValue(in) }

var (
	emptyStrings = []string{}
	emptyLabels  = map[string]string{}
	emptyBudgets = map[string]int64{}
	weekdays     = [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
)

// Prepared is an input made ready for evaluation: the decoded args, the derived facts and the CEL
// activation. Build it once per input; it is read-only afterwards.
type Prepared struct {
	in   Input
	loc  *time.Location
	act  activation
	sqlv string // sqlguard.Version when the SQL was classified
}

// Facts returns the derived facts.
func (p *Prepared) Facts() DerivedFacts { return DerivedFacts{SQL: p.act.sql, Time: p.act.time} }

// Prepare decodes and derives everything evaluation needs, once.
func Prepare(in Input) (*Prepared, error) {
	if in.V != 0 && in.V != InputVersion {
		return nil, fmt.Errorf("input version %d is not supported", in.V)
	}
	if in.Time.IsZero() {
		return nil, errors.New("the decision time is missing")
	}
	switch in.Agent.Status {
	case "", StatusActive, StatusSuspended, StatusStopped:
	default:
		return nil, fmt.Errorf("agent status %q is not active, suspended or stopped", clipName(in.Agent.Status))
	}
	loc, err := LoadLocation(in.Org.Timezone)
	if err != nil {
		return nil, fmt.Errorf("time zone %q is not a known IANA zone", clipName(in.Org.Timezone))
	}
	args, err := decodeArgs(in.Args)
	if err != nil {
		return nil, err
	}
	p := &Prepared{in: in, loc: loc}
	p.act.tool = in.Tool.ToolFacts
	p.act.args = args
	p.act.request = in.Request
	p.act.agent = in.Agent.AgentFacts
	if p.act.agent.Labels == nil {
		p.act.agent.Labels = emptyLabels
	}
	p.act.user = in.User
	p.act.session = in.Session
	p.act.org = in.Org.OrgFacts
	p.act.usage = in.Usage
	if p.act.usage.Budgets == nil {
		p.act.usage.Budgets = emptyBudgets
	}
	p.act.anomaly = in.Anomaly
	lt := in.Time.In(loc)
	p.act.time = TimeFacts{Now: lt, Hour: int64(lt.Hour()), Minute: int64(lt.Minute()),
		Weekday: weekdays[lt.Weekday()], Date: lt.Format("2006-01-02")}
	p.act.sql = noSQL()
	if in.Tool.ConnectorType == "postgres" {
		if s, ok := args["sql"].(string); ok {
			f := sqlguard.Analyze(s, sqlguard.Options{DefaultSchema: in.Tool.DefaultSchema})
			p.act.sql = sqlFacts(f)
			p.sqlv = sqlguard.Version
		}
	}
	return p, nil
}

func noSQL() SQLFacts {
	return SQLFacts{Kind: "none", Category: sqlguard.CatNone, Kinds: emptyStrings, WriteKinds: emptyStrings,
		TablesComplete: true, Tables: emptyStrings, Functions: emptyStrings, DangerousFunctions: emptyStrings,
		WriteFunctions: emptyStrings, UnknownFunctions: emptyStrings, Reasons: emptyStrings}
}

func sqlFacts(f sqlguard.Facts) SQLFacts {
	return SQLFacts{
		Present: f.Present, Kind: f.Kind, Category: f.Category, Kinds: f.Kinds, Statements: int64(f.Statements),
		Multi: f.Multi, Writes: f.Writes, WriteKinds: f.WriteKinds, HasWhere: f.HasWhere,
		UnboundedWrite: f.UnboundedWrite, WhereTrivial: f.WhereTrivial, HasReturning: f.HasReturning,
		CTEWrite: f.CTEWrite, SelectInto: f.SelectInto, CopyProgram: f.CopyProgram, ExplainAnalyze: f.ExplainAnalyze,
		LocksRows: f.LocksRows, Tables: f.Tables, TablesComplete: f.TablesComplete, Functions: f.Functions,
		DangerousFunctions: f.DangerousFunctions, WriteFunctions: f.WriteFunctions, UnknownFunctions: f.UnknownFunctions,
		Ambiguous: f.Ambiguous, Reasons: f.Reasons, Bytes: int64(f.Bytes),
	}
}

// decodeArgs decodes the args object: integers become int64, other numbers float64, depth ≤ 32.
func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return map[string]any{}, nil
	}
	if len(raw) > MaxArgsBytes {
		return nil, fmt.Errorf("the arguments are %s bytes; the limit is %s", thousands(len(raw)), thousands(MaxArgsBytes))
	}
	if raw[0] != '{' {
		return nil, errors.New("the arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := decodeValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw[dec.InputOffset():])) > 0 {
		return nil, errors.New("the arguments have data after the JSON object")
	}
	m, _ := v.(map[string]any)
	return m, nil
}

func decodeValue(dec *json.Decoder, depth int) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, errors.New("the arguments are not valid JSON")
	}
	switch t := tok.(type) {
	case json.Delim:
		if depth >= MaxArgsDepth {
			return nil, fmt.Errorf("the arguments nest deeper than %d levels", MaxArgsDepth)
		}
		switch t {
		case '{':
			m := map[string]any{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, errors.New("the arguments are not valid JSON")
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("the arguments are not valid JSON")
				}
				if _, dup := m[k]; dup {
					return nil, fmt.Errorf("the arguments repeat the key %q", clipName(k))
				}
				v, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			if _, err := dec.Token(); err != nil {
				return nil, errors.New("the arguments are not valid JSON")
			}
			return m, nil
		case '[':
			l := []any{}
			for dec.More() {
				v, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, errors.New("the arguments are not valid JSON")
			}
			return l, nil
		}
		return nil, errors.New("the arguments are not valid JSON")
	case json.Number:
		s := string(t)
		if !strings.ContainsAny(s, ".eE") {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return i, nil
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("the number %s is out of range", clipName(s))
		}
		return f, nil
	default:
		return t, nil // string, bool or nil
	}
}

// activation is the one CEL activation of an evaluation, reused by every rule. It resolves the
// eleven variables of policy/v1 without a map.
type activation struct {
	tool    ToolFacts
	args    map[string]any
	request RequestFacts
	sql     SQLFacts
	agent   AgentFacts
	user    UserFacts
	session SessionFacts
	org     OrgFacts
	time    TimeFacts
	usage   UsageFacts
	anomaly AnomalyFacts
}

func (a *activation) ResolveName(name string) (any, bool) {
	switch name {
	case "tool":
		return &a.tool, true
	case "args":
		return a.args, true
	case "request":
		return &a.request, true
	case "sql":
		return &a.sql, true
	case "agent":
		return &a.agent, true
	case "user":
		return &a.user, true
	case "session":
		return &a.session, true
	case "org":
		return &a.org, true
	case "time":
		return &a.time, true
	case "usage":
		return &a.usage, true
	case "anomaly":
		return &a.anomaly, true
	}
	return nil, false
}

func (a *activation) Parent() interpreter.Activation { return nil }
