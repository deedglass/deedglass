package policy

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The case-input format (S12 §3.8): one parser for `deedglass policy test` (YAML cases files),
// POST /v1/policies/evaluate and POST /v1/policies/test (JSON), and the S26 editor. A case input is
// a generic value (maps, lists, strings, whole numbers, decimals, booleans) read from YAML or JSON;
// BuildInput checks every key and turns it into an Input.
//
//	tool: postgres.query                       # or {name, connector, connector_type, side_effect, default_schema}
//	sql: "SELECT 1"                            # shorthand for args.sql
//	args: {amount: 2000}
//	agent: {id, name, labels, status, budgets: {calls_per_minute, calls_per_day, spend_per_day}}
//	org: {id, timezone, stopped}
//	user: {ref, sub, iss}
//	session: {ref}
//	request: {mode, intent_signed, bytes, digest}
//	time: 2026-10-04T10:00:00Z
//	usage: {calls_minute: 60, refunds-per-day: 49000, currency: usd}
//	anomaly: {active: true}

// DefaultCaseTime is the decision time of a case that names none.
var DefaultCaseTime = time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)

// Local defaults for an input that names no agent or org. They are well-formed ids no database
// holds.
const (
	LocalAgentID = "agt_00000000000000000000000000"
	LocalOrgID   = "org_00000000000000000000000000"
)

// KnownConnectorTypes are inferred from a tool name's prefix (docs/POLICY_LANGUAGE.md §4.4).
var KnownConnectorTypes = map[string]bool{"postgres": true, "rest": true, "mcp": true, "mcp_http": true, "sdk": true,
	"github": true, "slack": true, "stripe": true, "gmail": true, "notion": true, "fake": true}

// InputProblem is one thing wrong with a case input. Path is relative to the input ("agent.lables").
type InputProblem struct {
	Path    string
	Message string // one sentence, ends with a period
	Hint    string
}

func (p *InputProblem) Error() string { return p.Message }

var (
	inputKeys       = []string{"tool", "sql", "args", "agent", "org", "user", "session", "request", "time", "usage", "anomaly"}
	toolKeys        = []string{"name", "connector", "connector_type", "side_effect", "default_schema", "type", "op"}
	agentKeys       = []string{"id", "name", "labels", "status", "budgets"}
	agentBudgetKeys = []string{"calls_per_minute", "calls_per_day", "spend_per_day"}
	orgKeys         = []string{"id", "timezone", "stopped"}
	userKeys        = []string{"ref", "sub", "iss"}
	sessionKeys     = []string{"ref"}
	requestKeys     = []string{"mode", "intent_signed", "bytes", "digest", "http"}
	httpKeys        = []string{"method", "path", "host"}
	anomalyKeys     = []string{"active"}
	usageKeys       = []string{"calls_minute", "calls_hour", "calls_today", "spend_today", "currency", "budgets"}
	budgetIDRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// MergeCaseInput deep-merges over into base (maps merge key by key; anything else is replaced) and
// returns a new value. The test runner merges a file's defaults into each case.
func MergeCaseInput(base, over any) any {
	bm, ok1 := base.(map[string]any)
	om, ok2 := over.(map[string]any)
	if !ok1 || !ok2 {
		if over == nil {
			return base
		}
		return over
	}
	out := make(map[string]any, len(bm)+len(om))
	for k, v := range bm {
		out[k] = v
	}
	for k, v := range om {
		if b, ok := out[k]; ok {
			out[k] = MergeCaseInput(b, v)
		} else {
			out[k] = v
		}
	}
	return out
}

// NormalizeJSONValue turns json.Number values (a decoder with UseNumber) into int64 or float64, so
// JSON and YAML inputs look the same to BuildInput.
func NormalizeJSONValue(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = NormalizeJSONValue(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = NormalizeJSONValue(e)
		}
		return x
	}
	return v
}

// BuildInput turns a case input into an Input. Unset parts get the local defaults: agent
// LocalAgentID named "agent" and active, org LocalOrgID in UTC, the time DefaultCaseTime, request
// mode gateway, usage zero. The caller may then resolve a real agent (the API) and fill local
// usage (FillLocalUsage).
func BuildInput(v any) (Input, *InputProblem) {
	var in Input
	in.V = InputVersion
	m, ok := v.(map[string]any)
	if !ok {
		return in, &InputProblem{Message: "The input must be a mapping of keys, like {tool: postgres.query, sql: \"SELECT 1\"}."}
	}
	if p := checkKeys("", m, inputKeys); p != nil {
		return in, p
	}
	// tool
	switch t := m["tool"].(type) {
	case nil:
		return in, &InputProblem{Path: "tool", Message: "The input needs a tool, like postgres.query.", Hint: "Add tool: postgres.query."}
	case string:
		if t == "" {
			return in, &InputProblem{Path: "tool", Message: "The tool name is empty."}
		}
		in.Tool.Name = t
		prefix, _, _ := strings.Cut(t, ".")
		in.Tool.Connector = prefix
		if KnownConnectorTypes[prefix] {
			in.Tool.ConnectorType = prefix
		}
	case map[string]any:
		if p := checkKeys("tool", t, toolKeys); p != nil {
			return in, p
		}
		for k, dst := range map[string]*string{"name": &in.Tool.Name, "connector": &in.Tool.Connector,
			"connector_type": &in.Tool.ConnectorType, "side_effect": &in.Tool.SideEffect, "default_schema": &in.Tool.DefaultSchema,
			"type": &in.Tool.Type, "op": &in.Tool.Op} {
			if p := str(t, k, "tool."+k, dst); p != nil {
				return in, p
			}
		}
		if in.Tool.Name == "" {
			return in, &InputProblem{Path: "tool.name", Message: "The tool needs a name, like postgres.query."}
		}
		prefix, _, _ := strings.Cut(in.Tool.Name, ".")
		if _, set := t["connector"]; !set {
			in.Tool.Connector = prefix
		}
		if _, set := t["connector_type"]; !set && KnownConnectorTypes[prefix] {
			in.Tool.ConnectorType = prefix
		}
	default:
		return in, wrongType("tool", "a tool name or a mapping", t)
	}
	if in.Tool.SideEffect == "" {
		in.Tool.SideEffect = "unknown"
	}
	// S16: tool.type defaults to the connector type and tool.op to the name after the first dot.
	if in.Tool.Type == "" {
		in.Tool.Type = in.Tool.ConnectorType
	}
	if in.Tool.Op == "" {
		_, in.Tool.Op, _ = strings.Cut(in.Tool.Name, ".")
	}
	// args and the sql shorthand
	args := map[string]any{}
	switch a := m["args"].(type) {
	case nil:
	case map[string]any:
		args = a
	default:
		return in, wrongType("args", "a mapping", a)
	}
	if s, set := m["sql"]; set {
		q, ok := s.(string)
		if !ok {
			return in, wrongType("sql", "a string", s)
		}
		if _, dup := args["sql"]; dup {
			return in, &InputProblem{Path: "sql", Message: "The input sets both sql and args.sql.", Hint: "Keep one of them."}
		}
		args = MergeCaseInput(args, map[string]any{"sql": q}).(map[string]any)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return in, &InputProblem{Path: "args", Message: "The arguments can't be written as JSON: " + strings.TrimSuffix(err.Error(), ".") + "."}
	}
	in.Args = raw
	// agent
	in.Agent.ID, in.Agent.Name, in.Agent.Status = LocalAgentID, "agent", StatusActive
	in.Agent.Labels = map[string]string{}
	if a, set := m["agent"]; set {
		am, ok := a.(map[string]any)
		if !ok {
			return in, wrongType("agent", "a mapping", a)
		}
		if p := checkKeys("agent", am, agentKeys); p != nil {
			return in, p
		}
		for k, dst := range map[string]*string{"id": &in.Agent.ID, "name": &in.Agent.Name, "status": &in.Agent.Status} {
			if p := str(am, k, "agent."+k, dst); p != nil {
				return in, p
			}
		}
		if in.Agent.ID != LocalAgentID && !agentIDRE.MatchString(in.Agent.ID) {
			return in, &InputProblem{Path: "agent.id", Message: fmt.Sprintf("%q isn't an agent id.", clipN(in.Agent.ID, 40)), Hint: "Agent ids look like agt_01jb8m2x7k4q9r3t5v6w8y0z1a."}
		}
		switch in.Agent.Status {
		case StatusActive, StatusSuspended, StatusStopped:
		default:
			return in, &InputProblem{Path: "agent.status", Message: "The agent status must be active, suspended or stopped."}
		}
		if l, set := am["labels"]; set {
			lm, ok := l.(map[string]any)
			if !ok {
				return in, wrongType("agent.labels", "a mapping", l)
			}
			for k, v := range lm {
				s, ok := scalarString(v)
				if !ok {
					return in, wrongType("agent.labels."+k, "a string", v)
				}
				in.Agent.Labels[k] = s
			}
		}
		if b, set := am["budgets"]; set {
			bm, ok := b.(map[string]any)
			if !ok {
				return in, wrongType("agent.budgets", "a mapping", b)
			}
			if p := checkKeys("agent.budgets", bm, agentBudgetKeys); p != nil {
				return in, p
			}
			ab := &AgentBudgets{}
			for k, dst := range map[string]**int64{"calls_per_minute": &ab.CallsPerMinute, "calls_per_day": &ab.CallsPerDay} {
				if x, set := bm[k]; set {
					n, p := wholeNumber("agent.budgets."+k, x)
					if p != nil {
						return in, p
					}
					*dst = &n
				}
			}
			if x, set := bm["spend_per_day"]; set {
				mny, p := moneyValue("agent.budgets.spend_per_day", x)
				if p != nil {
					return in, p
				}
				ab.SpendPerDay = &mny
			}
			in.Agent.Budgets = ab
		}
	}
	// org
	in.Org.ID = LocalOrgID
	if o, set := m["org"]; set {
		om, ok := o.(map[string]any)
		if !ok {
			return in, wrongType("org", "a mapping", o)
		}
		if p := checkKeys("org", om, orgKeys); p != nil {
			return in, p
		}
		if p := str(om, "id", "org.id", &in.Org.ID); p != nil {
			return in, p
		}
		if p := str(om, "timezone", "org.timezone", &in.Org.Timezone); p != nil {
			return in, p
		}
		if in.Org.Timezone != "" {
			if _, err := LoadLocation(in.Org.Timezone); err != nil {
				return in, &InputProblem{Path: "org.timezone", Message: fmt.Sprintf("%q isn't an IANA time zone.", clipN(in.Org.Timezone, 40)), Hint: "Use a name like Asia/Kolkata or UTC."}
			}
		}
		if p := boolean(om, "stopped", "org.stopped", &in.Org.Stopped); p != nil {
			return in, p
		}
	}
	// user, session, request, anomaly
	if u, set := m["user"]; set {
		um, ok := u.(map[string]any)
		if !ok {
			return in, wrongType("user", "a mapping", u)
		}
		if p := checkKeys("user", um, userKeys); p != nil {
			return in, p
		}
		for k, dst := range map[string]*string{"ref": &in.User.Ref, "sub": &in.User.Sub, "iss": &in.User.Iss} {
			if p := str(um, k, "user."+k, dst); p != nil {
				return in, p
			}
		}
	}
	if s, set := m["session"]; set {
		sm, ok := s.(map[string]any)
		if !ok {
			return in, wrongType("session", "a mapping", s)
		}
		if p := checkKeys("session", sm, sessionKeys); p != nil {
			return in, p
		}
		if p := str(sm, "ref", "session.ref", &in.Session.Ref); p != nil {
			return in, p
		}
	}
	in.Request.Mode = "gateway"
	if r, set := m["request"]; set {
		rm, ok := r.(map[string]any)
		if !ok {
			return in, wrongType("request", "a mapping", r)
		}
		if p := checkKeys("request", rm, requestKeys); p != nil {
			return in, p
		}
		if p := str(rm, "mode", "request.mode", &in.Request.Mode); p != nil {
			return in, p
		}
		if h, set := rm["http"]; set {
			hm, ok := h.(map[string]any)
			if !ok {
				return in, wrongType("request.http", "a mapping", h)
			}
			if p := checkKeys("request.http", hm, httpKeys); p != nil {
				return in, p
			}
			for k, dst := range map[string]*string{"method": &in.Request.HTTP.Method, "path": &in.Request.HTTP.Path, "host": &in.Request.HTTP.Host} {
				if p := str(hm, k, "request.http."+k, dst); p != nil {
					return in, p
				}
			}
		}
		if in.Request.Mode != "gateway" && in.Request.Mode != "proxy" {
			return in, &InputProblem{Path: "request.mode", Message: "The request mode must be gateway or proxy."}
		}
		if p := boolean(rm, "intent_signed", "request.intent_signed", &in.Request.IntentSigned); p != nil {
			return in, p
		}
		if p := str(rm, "digest", "request.digest", &in.Request.Digest); p != nil {
			return in, p
		}
		if x, set := rm["bytes"]; set {
			n, p := wholeNumber("request.bytes", x)
			if p != nil {
				return in, p
			}
			in.Request.Bytes = n
		}
	}
	if in.Request.Bytes == 0 {
		in.Request.Bytes = int64(len(in.Args))
	}
	if a, set := m["anomaly"]; set {
		am, ok := a.(map[string]any)
		if !ok {
			return in, wrongType("anomaly", "a mapping", a)
		}
		if p := checkKeys("anomaly", am, anomalyKeys); p != nil {
			return in, p
		}
		if p := boolean(am, "active", "anomaly.active", &in.Anomaly.Active); p != nil {
			return in, p
		}
	}
	// time
	in.Time = DefaultCaseTime
	if t, set := m["time"]; set {
		var s string
		switch x := t.(type) {
		case string:
			s = x
		case time.Time:
			s = x.Format(time.RFC3339Nano)
		default:
			return in, wrongType("time", "an RFC 3339 time", t)
		}
		at, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return in, &InputProblem{Path: "time", Message: fmt.Sprintf("%q isn't an RFC 3339 time.", clipN(s, 40)), Hint: "Write it like 2026-10-04T10:00:00+05:30."}
		}
		in.Time = at.UTC()
	}
	// usage
	if u, set := m["usage"]; set {
		um, ok := u.(map[string]any)
		if !ok {
			return in, wrongType("usage", "a mapping", u)
		}
		keys := sortedKeys(um)
		for _, k := range keys {
			x := um[k]
			path := "usage." + k
			switch k {
			case "currency":
				s, ok := x.(string)
				if !ok {
					return in, wrongType(path, "a currency code", x)
				}
				in.Usage.Currency = strings.ToLower(s)
			case "budgets":
				bm, ok := x.(map[string]any)
				if !ok {
					return in, wrongType(path, "a mapping of budget ids", x)
				}
				for _, id := range sortedKeys(bm) {
					n, p := wholeNumber(path+"."+id, bm[id])
					if p != nil {
						return in, p
					}
					setBudgetUsage(&in.Usage, id, n)
				}
			default:
				n, p := wholeNumber(path, x)
				if p != nil {
					return in, p
				}
				switch k {
				case "calls_minute":
					in.Usage.CallsMinute = n
				case "calls_hour":
					in.Usage.CallsHour = n
				case "calls_today":
					in.Usage.CallsToday = n
				case "spend_today":
					in.Usage.SpendToday = n
				default:
					if !budgetIDRE.MatchString(k) {
						return in, unknownKey("usage", k, usageKeys)
					}
					setBudgetUsage(&in.Usage, k, n)
				}
			}
		}
	}
	return in, nil
}

func setBudgetUsage(u *UsageSnapshot, id string, n int64) {
	if u.Budgets == nil {
		u.Budgets = map[string]int64{}
	}
	u.Budgets[id] = n
}

func checkKeys(path string, m map[string]any, known []string) *InputProblem {
	return checkKeysIn(path, "", m, known)
}

// checkKeysIn is checkKeys with the words for where the keys are ("expect", "this case").
func checkKeysIn(path, where string, m map[string]any, known []string) *InputProblem {
	for _, k := range sortedKeys(m) {
		ok := false
		for _, x := range known {
			ok = ok || x == k
		}
		if !ok {
			p := unknownKey(path, k, known)
			if where != "" {
				p.Message = fmt.Sprintf("Unknown key %q in %s.", clipN(k, 40), where)
			}
			return p
		}
	}
	return nil
}

func unknownKey(path, k string, known []string) *InputProblem {
	where := "the input"
	if path != "" {
		where = path
	}
	p := &InputProblem{Path: join(path, k), Message: fmt.Sprintf("Unknown key %q in %s.", clipN(k, 40), where)}
	if s := suggest(k, known); s != "" {
		p.Hint = fmt.Sprintf("Did you mean %q?", s)
	} else {
		p.Hint = "Use " + orList(known) + "."
	}
	return p
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func wrongType(path, want string, got any) *InputProblem {
	return &InputProblem{Path: path, Message: fmt.Sprintf("%s must be %s; found %s.", path, want, typeWord(got))}
}

func typeWord(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case string:
		return "a string"
	case int64, int, float64:
		return "a number"
	case bool:
		return "true or false"
	case map[string]any:
		return "a mapping"
	case []any:
		return "a list"
	}
	return "an unsupported value"
}

func str(m map[string]any, k, path string, dst *string) *InputProblem {
	v, set := m[k]
	if !set {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return wrongType(path, "a string", v)
	}
	*dst = s
	return nil
}

func boolean(m map[string]any, k, path string, dst *bool) *InputProblem {
	v, set := m[k]
	if !set {
		return nil
	}
	b, ok := v.(bool)
	if !ok {
		return wrongType(path, "true or false", v)
	}
	*dst = b
	return nil
}

func scalarString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case int64:
		return fmt.Sprint(x), true
	case bool:
		return fmt.Sprint(x), true
	}
	return "", false
}

func wholeNumber(path string, v any) (int64, *InputProblem) {
	switch x := v.(type) {
	case int64:
		if x < 0 {
			return 0, &InputProblem{Path: path, Message: fmt.Sprintf("%s must not be negative.", path)}
		}
		return x, nil
	case int:
		return wholeNumber(path, int64(x))
	case float64:
		if x == math.Trunc(x) && x >= 0 && x <= MaxMinor {
			return int64(x), nil
		}
	}
	return 0, &InputProblem{Path: path, Message: fmt.Sprintf("%s must be a whole number; found %s.", path, typeWord(v)),
		Hint: "Money is in minor units: 12000 is $120.00."}
}

func moneyValue(path string, v any) (Money, *InputProblem) {
	switch x := v.(type) {
	case string:
		m, p := ParseMoney(x)
		if p != nil {
			return Money{}, &InputProblem{Path: path, Message: p.Message, Hint: p.Hint}
		}
		return m, nil
	case map[string]any:
		if pr := checkKeys(path, x, []string{"amount", "currency"}); pr != nil {
			return Money{}, pr
		}
		n, pr := wholeNumber(path+".amount", x["amount"])
		if pr != nil {
			return Money{}, pr
		}
		c, _ := x["currency"].(string)
		c = strings.ToLower(c)
		if _, ok := CurrencyExponent(c); !ok {
			return Money{}, &InputProblem{Path: path + ".currency", Message: fmt.Sprintf("%q isn't an ISO 4217 currency.", clipN(c, 10))}
		}
		return Money{Amount: n, Currency: c}, nil
	}
	return Money{}, wrongType(path, `money, like "50.00 usd"`, v)
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
