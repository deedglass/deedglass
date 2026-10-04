package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/sqlguard"
)

// Tool names (unqualified; a connector named postgres serves postgres.query, …).
const (
	ToolQuery    = "query"
	ToolExecute  = "execute"
	ToolCall     = "call"
	ToolDescribe = "describe"
)

// Connector is the Postgres connector type. One value serves every org's postgres connectors: it
// holds their pools, keyed by connector id.
type Connector struct {
	pools    *pools
	log      *slog.Logger
	describe sync.Map // connector id + config hash → describeEntry
	hooks    hooks
}

// hooks are test seams (integration tests only).
type hooks struct {
	skipAdmission bool                            // run a statement that admission would refuse
	beforeCommit  func(ctx context.Context) error // runs inside the transaction, before COMMIT
}

var (
	_ connector.Connector = (*Connector)(nil)
	_ connector.Enricher  = (*Connector)(nil)
)

// New returns the Postgres connector type.
func New(log *slog.Logger) *Connector {
	if log == nil {
		log = slog.Default()
	}
	return &Connector{pools: newPools(log), log: log}
}

// Close closes every pool (server shutdown).
func (c *Connector) Close() { c.pools.close() }

// Forget drops a connector's pool and cached description (deleted, disabled or reconfigured).
func (c *Connector) Forget(id string) {
	c.pools.forget(id)
	c.describe.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), id+"/") {
			c.describe.Delete(k)
		}
		return true
	})
}

// Type implements connector.Connector.
func (c *Connector) Type() string { return Type }

const configSchema = `{"type":"object","required":["allowed_schemas"],"properties":{
"mode":{"type":"string","enum":["sql","controlled_functions"]},
"allowed_schemas":{"type":"array","items":{"type":"string"},"description":"the read scope and search_path, like [public]"},
"writable_tables":{"type":"array","items":{"type":"string"},"description":"schema.table the agent may INSERT, UPDATE and DELETE"},
"row_limit":{"type":"integer","minimum":1,"maximum":10000},
"max_result_bytes":{"type":"integer","minimum":1024,"maximum":16777216},
"max_rows_affected":{"type":"integer","minimum":1,"maximum":1000000},
"statement_timeout_ms":{"type":"integer","minimum":100,"maximum":60000},
"lock_timeout_ms":{"type":"integer","minimum":100,"maximum":60000},
"max_connections":{"type":"integer","minimum":1,"maximum":16},
"witness":{"type":"object","properties":{"enabled":{"type":"boolean"},"watched_tables":{"type":"array","items":{"type":"string"}},"sensitive_columns":{"type":"array","items":{"type":"string"}},"auto_columns":{"type":"object","description":"table → columns a trigger maintains, like {public.orders: [updated_at]}"},"max_rows":{"type":"object","description":"table → the most rows one action may change"},"strict_triggers":{"type":"boolean"},"l2_timeout":{"type":"string","description":"how long an action waits for Postgres's change log, 1s to 60s (10s)"},"diff_rows":{"type":"integer","minimum":1,"maximum":100}}},
"functions":{"type":"array","items":{"type":"object","required":["name"]}},
"throwaway":{"type":"boolean"}}}`

// Info implements connector.Connector.
func (c *Connector) Info() connector.TypeInfo {
	return connector.TypeInfo{Object: "connector_type", Type: Type, Title: "PostgreSQL",
		Description: "Agent SQL in a transaction Deedglass controls: one statement per action, run as the least-privilege role deedglass_agent, with a ledger row and a logical marker committed with the change for the witness.",
		Levels:      []string{"L0", "L1", "L2"},
		CredentialKinds: []connector.CredentialKind{
			{Kind: "dsn", Fields: []connector.CredentialField{{Name: "dsn", Secret: true, Help: "postgres:// for the deedglass_agent role.", PrefixHint: "postgres://"},
				{Name: "witness_dsn", Secret: true, Help: "postgres:// for the deedglass_witness role (L2)."}}},
		},
		ConfigSchema: json.RawMessage(configSchema), DocsURL: "https://docs.deedglass.com/connectors/postgres",
		Summary: "Agent SQL in a controlled transaction, witnessed by Postgres's own change log.",
		Status:  "stable", Icon: "database", Modes: []string{}, ToolsDynamic: false, Tools: catalogTools(),
		Setup: []connector.SetupStep{
			{Title: "Create the connector", Command: "deedglass connector create --type postgres --name pg-main --config pg-main.yaml"},
			{Title: "Prepare the database", Detail: "Review the plan, then apply it in one transaction.", Command: "deedglass connector postgres init --connector pg-main --apply"},
			{Title: "Check it", Command: "deedglass connector postgres check --connector pg-main"},
		},
		Limits: []string{
			"One statement per action, run as deedglass_agent.",
			"L2 needs logical replication (wal_level logical) and a replication slot.",
			"No DDL, COPY, DO blocks or session settings from agents.",
		}}
}

// catalogTools is the catalog's tool list: the default config's tools, with L2 on the writes.
func catalogTools() []connector.ToolInfo {
	cfg, _ := ParseConfig(json.RawMessage(`{"mode":"sql"}`))
	ts := connector.CatalogTools(Type, toolSpecs(cfg))
	for i := range ts {
		if ts[i].SideEffect != connector.SideEffectRead {
			ts[i].Levels = []string{"L0", "L1", "L2"}
			if ts[i].ReadBack == "" {
				ts[i].ReadBack = "Postgres's change log shows exactly the rows the statement changed (L2); the row read-back confirms them (L1)."
			}
		}
	}
	return ts
}

// Validate implements connector.Connector.
func (c *Connector) Validate(_ context.Context, _ connector.Env, raw json.RawMessage) (json.RawMessage, []connector.FieldError) {
	cfg, errs := ParseConfig(raw)
	if len(errs) > 0 {
		return nil, errs
	}
	return cfg.Normalize(), nil
}

// state is an instance's parsed config and tool specs.
type state struct {
	cfg   Config
	specs []connector.ToolSpec
}

func parse(inst *connector.Instance) (*state, error) {
	v, err := inst.Parsed(func() (any, error) {
		cfg, errs := ParseConfig(inst.Config)
		if len(errs) > 0 {
			return nil, fmt.Errorf("postgres: the stored config is invalid: %s", errs[0].Error())
		}
		return &state{cfg: cfg, specs: toolSpecs(cfg)}, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*state), nil
}

// The tools' input schemas (connector/schema subset; additionalProperties false everywhere).
const (
	queryInput = `{"type":"object","additionalProperties":false,"required":["sql"],"properties":{
"sql":{"type":"string","maxLength":65536,"description":"One read-only SQL statement (SELECT). Use $1, $2 … for values."},
"params":{"type":"array","items":{},"description":"Values for $1, $2 …: strings, integers, booleans or null; at most 100."},
"limit":{"type":"integer","minimum":1,"maximum":%d,"description":"Return at most this many rows (default and maximum %d)."}}}`
	executeInput = `{"type":"object","additionalProperties":false,"required":["sql"],"properties":{
"sql":{"type":"string","maxLength":65536,"description":"One INSERT, UPDATE, DELETE or MERGE. Use $1, $2 … for values."},
"params":{"type":"array","items":{},"description":"Values for $1, $2 …: strings, integers, booleans or null; at most 100."}}}`
	callInput = `{"type":"object","additionalProperties":false,"required":["function"],"properties":{
"function":{"type":"string","enum":%s,"description":"An allow-listed function."},
"args":{"type":"object","description":"The function's named arguments."}}}`
	describeInput = `{"type":"object","additionalProperties":false,"properties":{
"schema":{"type":"string","maxLength":63,"description":"Only this schema."},
"table":{"type":"string","maxLength":63,"description":"Only this table."}}}`
)

func toolSpecs(cfg Config) []connector.ToolSpec {
	specs := []connector.ToolSpec{{Name: ToolQuery, SideEffect: connector.SideEffectRead, Idempotent: true,
		Description: fmt.Sprintf("Run one read-only SQL statement against %s and get up to %d rows back. Read-only: it never commits.", strings.Join(cfg.AllowedSchemas, ", "), cfg.RowLimit),
		InputSchema: json.RawMessage(fmt.Sprintf(queryInput, cfg.RowLimit, cfg.RowLimit))}}
	if cfg.Mode == ModeSQL {
		desc := "Run one INSERT, UPDATE, DELETE or MERGE in a transaction Deedglass controls, as a least-privilege role."
		if len(cfg.WritableTables) > 0 {
			desc += " Writable tables: " + strings.Join(cfg.WritableTables, ", ") + "."
		}
		specs = append(specs, connector.ToolSpec{Name: ToolExecute, SideEffect: connector.SideEffectWrite, Description: desc,
			InputSchema: json.RawMessage(executeInput)})
	}
	if len(cfg.Functions) > 0 {
		names := make([]string, len(cfg.Functions))
		side := connector.SideEffectRead
		var desc []string
		for i, f := range cfg.Functions {
			names[i] = f.Name
			if s := connector.SideEffect(f.SideEffect); s.Rank() > side.Rank() {
				side = s
			}
			d := f.Signature()
			if f.Description != "" {
				d += ": " + f.Description
			}
			desc = append(desc, d)
		}
		enum, _ := json.Marshal(names)
		specs = append(specs, connector.ToolSpec{Name: ToolCall, SideEffect: side,
			Description: "Call an allow-listed function with named arguments: " + strings.Join(desc, "; "),
			InputSchema: json.RawMessage(fmt.Sprintf(callInput, enum))})
	}
	specs = append(specs, connector.ToolSpec{Name: ToolDescribe, SideEffect: connector.SideEffectRead, Idempotent: true,
		Description: "List the tables the agent can read: columns, types, primary keys, row estimates, and which tables are writable and watched.",
		InputSchema: json.RawMessage(describeInput)})
	// S23: the internal undo tool; never listed to agents and never resolved for them.
	return append(specs, undoSpec())
}

// Tools implements connector.Connector.
func (c *Connector) Tools(_ context.Context, inst *connector.Instance) ([]connector.ToolSpec, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	return append([]connector.ToolSpec(nil), st.specs...), nil
}

// sqlArgs are query and execute's arguments.
type sqlArgs struct {
	SQL    string            `json:"sql"`
	Params []json.RawMessage `json:"params"`
	Limit  int               `json:"limit"`
}

func decodeArgs(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return connector.ErrValidation("args", "can't be read")
	}
	return nil
}

// Enrich implements connector.Enricher: the classifier's default schema (the first allowed
// schema, so unqualified names resolve as the search_path will) and a raised side effect:
// destructive for a DELETE (or a MERGE that may delete), the function's own for postgres.call.
// The SQL facts themselves are derived by policy from args.sql with S11's names unchanged.
func (c *Connector) Enrich(_ context.Context, inst *connector.Instance, t connector.ToolSpec, args json.RawMessage) (map[string]any, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"default_schema": st.cfg.AllowedSchemas[0]}
	switch t.Name {
	case ToolExecute:
		var a sqlArgs
		if json.Unmarshal(args, &a) == nil {
			f := sqlguard.Analyze(a.SQL, sqlguard.Options{DefaultSchema: st.cfg.AllowedSchemas[0], MaxBytes: MaxSQLBytes})
			for _, k := range f.WriteKinds {
				if k == "delete" {
					out["side_effect"] = string(connector.SideEffectDestructive)
				}
			}
			if f.Kind == "delete" || f.Kind == "merge" {
				out["side_effect"] = string(connector.SideEffectDestructive)
			}
		}
	case ToolCall:
		var a struct {
			Function string `json:"function"`
		}
		if json.Unmarshal(args, &a) == nil {
			if f, ok := st.cfg.Function(a.Function); ok {
				out["side_effect"] = f.SideEffect
			}
		}
	}
	return out, nil
}

// Execute implements connector.Connector.
func (c *Connector) Execute(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Result, error) {
	st, err := parse(inst)
	if err != nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: "This connector's config can't be read. An admin can check Console › Connectors.", Err: err}
	}
	switch call.Tool.Name {
	case ToolQuery:
		return c.query(ctx, inst, st, call)
	case ToolExecute:
		if st.cfg.Mode != ModeSQL {
			return connector.Result{}, refused("postgres.execute is off for this connector (mode controlled_functions). Use postgres.call.")
		}
		return c.execute(ctx, inst, st, call)
	case ToolCall:
		return c.callFunction(ctx, inst, st, call)
	case ToolDescribe:
		return c.describeTool(ctx, inst, st, call)
	case ToolUndo:
		// The undo runs only through the undo executor (internal/undoapi), which plans it from
		// evidence and calls RunUndo; nothing reaches it with arguments alone.
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "tool_unknown",
			AgentMessage: "The Postgres connector has no tool by that name."}
	}
	return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "tool_unknown", AgentMessage: "The Postgres connector has no tool by that name."}
}

// entry opens the credential and returns the connector's pool. The credential's fields are zeroed
// before it returns; the pool keeps its parsed config.
func (c *Connector) entry(ctx context.Context, inst *connector.Instance, st *state) (*poolEntry, error) {
	sec, err := inst.Secret(ctx)
	if err != nil {
		return nil, err
	}
	defer sec.Destroy()
	if sec.Kind() != "dsn" {
		return nil, &connector.Error{Kind: connector.KindConfig, Code: connector.CodeCredentialsMissing,
			AgentMessage: connector.CredentialsMissingMessage(inst.Name),
			AdminDetail:  fmt.Sprintf("The stored credential is %s; a postgres connector needs kind dsn.", sec.Kind())}
	}
	raw := sec.Field("dsn")
	if raw == "" {
		return nil, connector.ErrCredentialsMissing(inst.Name)
	}
	e, err := c.pools.get(ctx, inst, st.cfg, raw)
	if err != nil {
		var de *DSNError
		if errors.As(err, &de) {
			return nil, &connector.Error{Kind: connector.KindConfig, Code: connector.CodeConnectorUnavailable,
				AgentMessage: "This connector's database address can't be used, so nothing ran. An admin can check Console › Connectors.",
				AdminDetail:  "dsn: " + de.Message, Err: err}
		}
		if ce := connector.AsError(err); ce != nil {
			return nil, ce
		}
		return nil, connectErr(err, inst.Name)
	}
	return e, nil
}

// SQLDigest is "sha256:" + hex(sha256(JCS({"params":[…],"sql":"…"}))) (S17 §3.5; S21 recomputes
// it). params must be JCS-safe: integers ≤ 2^53−1, strings, booleans, null.
func SQLDigest(sql string, params []any) (string, error) {
	if params == nil {
		params = []any{}
	}
	return deed.DigestValue(map[string]any{"params": params, "sql": sql})
}

// CallDigest is the digest of a postgres.call: JCS({"args":{…},"function":"public.ship_order"}).
func CallDigest(function string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	return deed.DigestValue(map[string]any{"args": args, "function": function})
}
