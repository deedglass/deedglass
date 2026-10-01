package policy

import "time"

// The facts a condition can read (docs/POLICY_LANGUAGE.md §7.1). S11 fills exactly these
// structs. Every field carries an explicit `cel` tag, and nothing else is exposed to CEL
// (TestDeclaredFields lists them). Adding a field is additive; renaming one needs a new
// language version. A field S11 adds that CEL must not see is tagged `cel:"-"`.

// ToolFacts describes the tool being called.
type ToolFacts struct {
	Name          string `json:"name" cel:"name"`
	Connector     string `json:"connector" cel:"connector"`
	ConnectorType string `json:"connector_type" cel:"connector_type"`
	SideEffect    string `json:"side_effect" cel:"side_effect"`
}

// RequestFacts describes the request that carried the action.
type RequestFacts struct {
	Mode         string `json:"mode" cel:"mode"`
	Bytes        int64  `json:"bytes" cel:"bytes"`
	IntentSigned bool   `json:"intent_signed" cel:"intent_signed"`
	Digest       string `json:"digest" cel:"digest"`
}

// SQLFacts is what the S11 classifier reports about args.sql.
type SQLFacts struct {
	Present            bool     `json:"present" cel:"present"`
	Kind               string   `json:"kind" cel:"kind"`
	Category           string   `json:"category" cel:"category"`
	Kinds              []string `json:"kinds" cel:"kinds"`
	Statements         int64    `json:"statements" cel:"statements"`
	Multi              bool     `json:"multi" cel:"multi"`
	Writes             bool     `json:"writes" cel:"writes"`
	WriteKinds         []string `json:"write_kinds" cel:"write_kinds"`
	HasWhere           bool     `json:"has_where" cel:"has_where"`
	UnboundedWrite     bool     `json:"unbounded_write" cel:"unbounded_write"`
	WhereTrivial       bool     `json:"where_trivial" cel:"where_trivial"`
	HasReturning       bool     `json:"has_returning" cel:"has_returning"`
	CTEWrite           bool     `json:"cte_write" cel:"cte_write"`
	SelectInto         bool     `json:"select_into" cel:"select_into"`
	CopyProgram        bool     `json:"copy_program" cel:"copy_program"`
	ExplainAnalyze     bool     `json:"explain_analyze" cel:"explain_analyze"`
	LocksRows          bool     `json:"locks_rows" cel:"locks_rows"`
	Tables             []string `json:"tables" cel:"tables"`
	TablesComplete     bool     `json:"tables_complete" cel:"tables_complete"`
	Functions          []string `json:"functions" cel:"functions"`
	DangerousFunctions []string `json:"dangerous_functions" cel:"dangerous_functions"`
	WriteFunctions     []string `json:"write_functions" cel:"write_functions"`
	UnknownFunctions   []string `json:"unknown_functions" cel:"unknown_functions"`
	Ambiguous          bool     `json:"ambiguous" cel:"ambiguous"`
	Reasons            []string `json:"reasons" cel:"reasons"`
	Bytes              int64    `json:"bytes" cel:"bytes"`
}

// AgentFacts describes the calling agent.
type AgentFacts struct {
	ID     string            `json:"id" cel:"id"`
	Name   string            `json:"name" cel:"name"`
	Labels map[string]string `json:"labels" cel:"labels"`
	Status string            `json:"status" cel:"status"`
}

// UserFacts is the person the agent acts for (S09 on-behalf-of).
type UserFacts struct {
	Ref string `json:"ref" cel:"ref"`
	Sub string `json:"sub" cel:"sub"`
	Iss string `json:"iss" cel:"iss"`
}

// SessionFacts is the caller's session reference (S09).
type SessionFacts struct {
	Ref string `json:"ref" cel:"ref"`
}

// OrgFacts describes the org.
type OrgFacts struct {
	ID       string `json:"id" cel:"id"`
	Timezone string `json:"timezone" cel:"timezone"`
}

// TimeFacts is the decision instant in the org's time zone. It is an input, never a clock read.
type TimeFacts struct {
	Now     time.Time `json:"now" cel:"now"`
	Hour    int64     `json:"hour" cel:"hour"`
	Minute  int64     `json:"minute" cel:"minute"`
	Weekday string    `json:"weekday" cel:"weekday"`
	Date    string    `json:"date" cel:"date"`
}

// UsageFacts are the usage counters in their current windows, in minor units for money.
type UsageFacts struct {
	CallsMinute int64            `json:"calls_minute" cel:"calls_minute"`
	CallsHour   int64            `json:"calls_hour" cel:"calls_hour"`
	CallsToday  int64            `json:"calls_today" cel:"calls_today"`
	SpendToday  int64            `json:"spend_today" cel:"spend_today"`
	Currency    string           `json:"currency" cel:"currency"`
	Budgets     map[string]int64 `json:"budgets" cel:"budgets"`
}

// AnomalyFacts is filled by S27; until then Active is false.
type AnomalyFacts struct {
	Active bool `json:"active" cel:"active"`
}

// Variables lists the CEL variables of policy/v1 in documentation order.
var Variables = []string{"tool", "args", "request", "sql", "agent", "user", "session", "org", "time", "usage", "anomaly"}
