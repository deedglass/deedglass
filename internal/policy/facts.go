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
	// S16: the connector type again under its short name, and the tool's unqualified name
	// ("update_status" in "orders.update_status"). Empty in inputs recorded before S16.
	Type string `json:"type,omitempty" cel:"type"`
	Op   string `json:"op,omitempty" cel:"op"`
}

// HTTPFacts is the HTTP request a REST connector will send (S16): request.http.
type HTTPFacts struct {
	Method string `json:"method" cel:"method"`
	Path   string `json:"path" cel:"path"` // the templated path, with the values filled in
	Host   string `json:"host" cel:"host"`
}

// GitHubFacts is what the GitHub connector knows about a call before the decision (S29):
// request.github. Enriched is false when the lookup failed; starter rules then treat the branch
// as protected.
type GitHubFacts struct {
	Repo             string `json:"repo" cel:"repo"`                     // "owner/name"
	Owner            string `json:"owner" cel:"owner"`                   //
	Name             string `json:"name" cel:"name"`                     //
	DefaultBranch    string `json:"default_branch" cel:"default_branch"` // the repository's default branch
	Branch           string `json:"branch" cel:"branch"`                 // the branch written (a commit's branch, else the default)
	Base             string `json:"base" cel:"base"`                     // a pull request's base
	Head             string `json:"head" cel:"head"`                     // a pull request's head
	Path             string `json:"path" cel:"path"`                     // a file's path
	ProtectedBranch  bool   `json:"protected_branch" cel:"protected_branch"`
	IsDefaultBranch  bool   `json:"is_default_branch" cel:"is_default_branch"`
	TouchesWorkflows bool   `json:"touches_workflows" cel:"touches_workflows"` // path under .github/workflows/ or .github/actions/
	Enriched         bool   `json:"enriched" cel:"enriched"`
}

// SlackFacts is what the Slack connector knows about a call before the decision (S29):
// request.slack. Channels are compared by id, so a renamed channel keeps its rules.
type SlackFacts struct {
	ChannelID    string `json:"channel_id" cel:"channel_id"`
	ChannelName  string `json:"channel_name" cel:"channel_name"` // without the #
	IsPrivate    bool   `json:"is_private" cel:"is_private"`
	IsIM         bool   `json:"is_im" cel:"is_im"`
	IsExtShared  bool   `json:"is_ext_shared" cel:"is_ext_shared"`
	Broadcast    bool   `json:"broadcast" cel:"broadcast"`         // <!channel>, <!here> or <!everyone> in the text
	UserMentions int64  `json:"user_mentions" cel:"user_mentions"` // how many <@U…> mentions the text holds
	Enriched     bool   `json:"enriched" cel:"enriched"`
}

// StripeFacts is what the Stripe connector read from Stripe before the decision (S30):
// request.stripe. Amounts are minor units of Currency, from Stripe's own objects, never from the
// agent's arguments. Enriched is false when the read failed; the starter sends those refunds to a
// person.
type StripeFacts struct {
	Charge           string `json:"charge" cel:"charge"`
	PaymentIntent    string `json:"payment_intent" cel:"payment_intent"`
	Customer         string `json:"customer" cel:"customer"`
	Currency         string `json:"currency" cel:"currency"`               // lowercase ISO 4217
	ChargeAmount     int64  `json:"charge_amount" cel:"charge_amount"`     // what the charge was for
	AmountRefunded   int64  `json:"amount_refunded" cel:"amount_refunded"` // refunded so far
	Refundable       int64  `json:"refundable" cel:"refundable"`           // charge_amount − amount_refunded
	Disputed         bool   `json:"disputed" cel:"disputed"`
	ChargeAgeDays    int64  `json:"charge_age_days" cel:"charge_age_days"`
	AmountDisplay    string `json:"amount_display" cel:"amount_display"` // args.amount for people: "$120.00"
	Subscription     string `json:"subscription" cel:"subscription"`
	Status           string `json:"status" cel:"status"`                         // the subscription's
	CurrentPeriodEnd int64  `json:"current_period_end" cel:"current_period_end"` // Unix seconds
	AmountPerPeriod  int64  `json:"amount_per_period" cel:"amount_per_period"`
	Interval         string `json:"interval" cel:"interval"`
	Enriched         bool   `json:"enriched" cel:"enriched"`
}

// EmailFacts is what the Gmail connector knows about a draft before the decision (S30):
// request.email. Domains are lowercase; internal domains come from the connector's config.
type EmailFacts struct {
	RecipientCount  int64    `json:"recipient_count" cel:"recipient_count"` // to + cc + bcc
	Domains         []string `json:"domains" cel:"domains"`
	ExternalDomains []string `json:"external_domains" cel:"external_domains"`
	HasExternal     bool     `json:"has_external" cel:"has_external"`
	HasBcc          bool     `json:"has_bcc" cel:"has_bcc"`
	IsReply         bool     `json:"is_reply" cel:"is_reply"`
	Enriched        bool     `json:"enriched" cel:"enriched"`
}

// MailFacts is what the ops build's outbound-mail connector counts in a message before the
// decision (S40): request.mail. The policy reads these, never the sender's own counts.
type MailFacts struct {
	From             string `json:"from" cel:"from"`
	FromDomain       string `json:"from_domain" cel:"from_domain"`
	ToDomain         string `json:"to_domain" cel:"to_domain"`
	Words            int64  `json:"words" cel:"words"`
	Links            int64  `json:"links" cel:"links"`
	BareDomains      int64  `json:"bare_domains" cel:"bare_domains"`
	Questions        int64  `json:"questions" cel:"questions"`
	HasHTML          bool   `json:"has_html" cel:"has_html"`
	Step             int64  `json:"step" cel:"step"`
	Threaded         bool   `json:"threaded" cel:"threaded"`
	UnsubscribeHTTPS bool   `json:"unsubscribe_https" cel:"unsubscribe_https"`
	Enriched         bool   `json:"enriched" cel:"enriched"`
}

// FHIRFacts is the FHIR request the fhir connector will send (HC-P02, ADR-105 §4): request.fhir.
// Codes are system|code pairs from the resource's code and category only, never free text.
// Enriched is false when the arguments don't build a request; rules that allow on request.fhir
// then don't match.
type FHIRFacts struct {
	Interaction  string   `json:"interaction" cel:"interaction"`     // read, vread, search, history, create, update, patch, delete
	ResourceType string   `json:"resource_type" cel:"resource_type"` // one of the connector's allowed types
	ID           string   `json:"id" cel:"id"`                       // "" for create, search and a type's history
	Conditional  bool     `json:"conditional" cel:"conditional"`     // a create with If-None-Exist
	IfMatch      bool     `json:"if_match" cel:"if_match"`           // an update, patch or delete with If-Match
	Codes        []string `json:"codes" cel:"codes"`
	Profiles     []string `json:"profiles" cel:"profiles"` // meta.profile URLs
	Enriched     bool     `json:"enriched" cel:"enriched"`
	// HC-P15 (ADR-119): what a create or update's resource is in India's national profiles (the
	// NRCeS FHIR IG for ABDM 6.5.0, which carries the NHCX profiles). Read from meta.profile and,
	// for a Bundle, from what it holds; zero for reads, deletes and patches.
	BundleType      string   `json:"bundle_type,omitempty" cel:"bundle_type"`           // Bundle.type; "" when not a Bundle
	EntryTypes      []string `json:"entry_types,omitempty" cel:"entry_types"`           // the resource types a Bundle holds, nested ones too, sorted
	Record          string   `json:"record,omitempty" cel:"record"`                     // ABDM record: OPConsultRecord, DischargeSummaryRecord, …
	RecordStatus    string   `json:"record_status,omitempty" cel:"record_status"`       // that record's Composition.status
	NHCX            string   `json:"nhcx,omitempty" cel:"nhcx"`                         // ClaimBundle, CoverageEligibilityRequestBundle, …
	ProfileMismatch bool     `json:"profile_mismatch,omitempty" cel:"profile_mismatch"` // the NHCX profile names another kind than the bundle holds
	Claims          int64    `json:"claims,omitempty" cel:"claims"`                     // Claim resources in the request
	ClaimUse        string   `json:"claim_use,omitempty" cel:"claim_use"`               // the first Claim's use: claim, preauthorization, predetermination, other
	ClaimUses       []string `json:"claim_uses,omitempty" cel:"claim_uses"`             // every Claim's use, sorted
	ClaimAmount     int64    `json:"claim_amount,omitempty" cel:"claim_amount"`         // the largest Claim.total in minor units; -1 when unknown
	ClaimCurrency   string   `json:"claim_currency,omitempty" cel:"claim_currency"`     // lowercase ISO 4217
}

// RequestFacts describes the request that carried the action.
type RequestFacts struct {
	Mode         string `json:"mode" cel:"mode"`
	Bytes        int64  `json:"bytes" cel:"bytes"`
	IntentSigned bool   `json:"intent_signed" cel:"intent_signed"`
	Digest       string `json:"digest" cel:"digest"`
	// HTTP is set by the REST connector's enrichment (S16); empty fields otherwise.
	HTTP HTTPFacts `json:"http,omitzero" cel:"http"`
	// GitHub and Slack are set by those connectors' enrichment (S29); zero for every other tool.
	GitHub GitHubFacts `json:"github,omitzero" cel:"github"`
	Slack  SlackFacts  `json:"slack,omitzero" cel:"slack"`
	// Stripe and Email are those connectors' facts (S30); zero for every other tool.
	Stripe StripeFacts `json:"stripe,omitzero" cel:"stripe"`
	Email  EmailFacts  `json:"email,omitzero" cel:"email"`
	// Mail is the outbound-mail connector's facts (S40); zero for every other tool.
	Mail MailFacts `json:"mail,omitzero" cel:"mail"`
	// FHIR is the fhir connector's facts (HC-P02); zero for every other tool.
	FHIR FHIRFacts `json:"fhir,omitzero" cel:"fhir"`
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
	// PHIMode is the org's PHI mode (HC-P09) at decision time (HC-P15, ADR-119), so a pack can
	// refuse clinical calls while it is off.
	PHIMode bool `json:"phi_mode,omitempty" cel:"phi_mode"`
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

// AnomalyFacts are what Insights' anomaly detection says about the agent at decision time (S27
// §3.8). They are filled only when the org opts in (anomaly facts in Insights settings); otherwise
// Active is false and the rest empty. They are part of the stored input, so a replay sees exactly
// what the decision saw. When detection is stale or unreadable for an opted-in org, Active is true
// with the kind detector_stale (fail closed).
type AnomalyFacts struct {
	Active   bool     `json:"active" cel:"active"`
	Kinds    []string `json:"kinds,omitempty" cel:"kinds"`       // the active kinds, sorted: ["volume_spike"], or ["detector_stale"]
	Severity string   `json:"severity,omitempty" cel:"severity"` // "", info, warn or high: the highest of the active ones
}

// Variables lists the CEL variables of policy/v1 in documentation order.
var Variables = []string{"tool", "args", "request", "sql", "agent", "user", "session", "org", "time", "usage", "anomaly"}
