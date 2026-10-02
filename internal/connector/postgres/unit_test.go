package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/ids"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/values.golden")

// TestValuesGolden covers every mapping of S17 §3.8 (text format in, JSON out).
func TestValuesGolden(t *testing.T) {
	type row struct {
		name string
		oid  uint32
		in   *string
	}
	s := func(v string) *string { return &v }
	rows := []row{
		{"int2", pgtype.Int2OID, s("-32768")},
		{"int4", pgtype.Int4OID, s("42")},
		{"int8 small", pgtype.Int8OID, s("9007199254740991")},
		{"int8 2^53", pgtype.Int8OID, s("9007199254740992")},
		{"int8 -2^53", pgtype.Int8OID, s("-9007199254740992")},
		{"int8 max", pgtype.Int8OID, s("9223372036854775807")},
		{"numeric 0.1", pgtype.NumericOID, s("0.1")},
		{"numeric NaN", pgtype.NumericOID, s("NaN")},
		{"float8", pgtype.Float8OID, s("1.5")},
		{"float8 tiny", pgtype.Float8OID, s("1e-300")},
		{"float8 NaN", pgtype.Float8OID, s("NaN")},
		{"float8 Infinity", pgtype.Float8OID, s("Infinity")},
		{"float4 -Infinity", pgtype.Float4OID, s("-Infinity")},
		{"bool true", pgtype.BoolOID, s("t")},
		{"bool false", pgtype.BoolOID, s("f")},
		{"text", pgtype.TextOID, s(`he said "hi"` + "\n")},
		{"varchar", pgtype.VarcharOID, s("paid")},
		{"uuid", pgtype.UUIDOID, s("6f1c2d3e-0000-4000-8000-00000000d001")},
		{"inet", pgtype.InetOID, s("10.0.0.1/32")},
		{"enum (unknown oid)", 99999, s("shipped")},
		{"bytea", pgtype.ByteaOID, s(`\xdeadbeef`)},
		{"date", pgtype.DateOID, s("2026-10-06")},
		{"date infinity", pgtype.DateOID, s("infinity")},
		{"timestamptz", pgtype.TimestamptzOID, s("2026-10-06 11:12:00.123456+00")},
		{"timestamptz whole second", pgtype.TimestamptzOID, s("2026-10-06 11:12:00+00")},
		{"timestamptz offset", pgtype.TimestamptzOID, s("2026-10-06 16:42:00+05:30")},
		{"timestamptz infinity", pgtype.TimestamptzOID, s("infinity")},
		{"timestamp", pgtype.TimestampOID, s("2026-10-06 11:12:00.5")},
		{"interval", pgtype.IntervalOID, s("1 day 02:00:00")},
		{"json", pgtype.JSONOID, s(`{"a": [1, 2.5, "x"], "big": 12345678901234567890}`)},
		{"jsonb", pgtype.JSONBOID, s(`{"a": 1, "b": {"c": null}}`)},
		{"int4[]", pgtype.Int4ArrayOID, s("{1,2,NULL,4}")},
		{"text[] quoted", pgtype.TextArrayOID, s(`{plain,"with space","quote \" and \\ backslash",NULL,"NULL"}`)},
		{"int8[][]", pgtype.Int8ArrayOID, s("{{1,2},{9223372036854775807,4}}")},
		{"int4[] empty", pgtype.Int4ArrayOID, s("{}")},
		{"int4[] bounds", pgtype.Int4ArrayOID, s("[0:1]={5,6}")},
		{"numeric[]", pgtype.NumericArrayOID, s("{0.1,NaN}")},
		{"null", pgtype.TextOID, nil},
	}
	var sb strings.Builder
	for _, r := range rows {
		var raw []byte
		if r.in != nil {
			raw = []byte(*r.in)
		}
		got, err := ToJSON(r.oid, raw)
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		if !json.Valid(got) {
			t.Fatalf("%s: not JSON: %s", r.name, got)
		}
		in := "NULL"
		if r.in != nil {
			in = *r.in
		}
		b, _ := json.Marshal(in)
		sb.WriteString(r.name + " (" + TypeName(r.oid) + ")\t" + string(b) + "\t→ " + string(got) + "\n")
	}
	path := filepath.Join("testdata", "values.golden")
	if *updateGolden {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/connector/postgres -run TestValuesGolden -update)", err)
	}
	if sb.String() != string(want) {
		t.Fatalf("values differ from the golden:\n%s", sb.String())
	}
}

func TestParseConfig(t *testing.T) {
	good := `{"allowed_schemas":["public"],"writable_tables":["public.orders","public.refunds"],
		"witness":{"enabled":true,"watched_tables":["public.orders","public.refunds","public.customers"],"sensitive_columns":["public.customers.email"]},
		"functions":[{"name":"public.ship_order","args":[{"name":"p_order_id","type":"bigint"}],"side_effect":"write"}],"throwaway":true}`
	c, errs := ParseConfig(json.RawMessage(good))
	if len(errs) > 0 {
		t.Fatalf("good config refused: %v", errs)
	}
	if c.Mode != ModeSQL || c.RowLimit != 500 || c.MaxResultBytes != 4<<20 || c.MaxRowsAffected != 1000 || c.StatementTimeoutMS != 5000 || c.LockTimeoutMS != 2000 || c.MaxConnections != 4 {
		t.Fatalf("defaults: %+v", c)
	}
	if strings.Join(c.Witness.WatchedTables, ",") != "public.customers,public.orders,public.refunds" {
		t.Fatalf("watched tables aren't sorted: %v", c.Witness.WatchedTables)
	}
	if c.Functions[0].Signature() != "public.ship_order(bigint)" {
		t.Fatalf("signature %s", c.Functions[0].Signature())
	}
	again, errs := ParseConfig(c.Normalize())
	if len(errs) > 0 || string(again.Normalize()) != string(c.Normalize()) {
		t.Fatal("normalizing isn't idempotent")
	}
	bad := []struct {
		cfg, param, msg string
	}{
		{`{}`, "config.allowed_schemas", "at least one schema"},
		{`{"allowed_schemas":["Public"]}`, "config.allowed_schemas[0]", "lowercase"},
		{`{"allowed_schemas":["deedglass"]}`, "config.allowed_schemas[0]", "never lets agents read it"},
		{`{"allowed_schemas":["public","public"]}`, "config.allowed_schemas[1]", "twice"},
		{`{"allowed_schemas":["public"],"mode":"anything"}`, "config.mode", "sql or controlled_functions"},
		{`{"allowed_schemas":["public"],"writable_tables":["orders"]}`, "config.writable_tables[0]", "schema.table"},
		{`{"allowed_schemas":["public"],"writable_tables":["billing.invoices"]}`, "config.writable_tables[0]", "isn't in allowed_schemas"},
		{`{"allowed_schemas":["public"],"writable_tables":["deedglass.ledger"]}`, "config.writable_tables[0]", "can't be in schema deedglass"},
		{`{"allowed_schemas":["public"],"row_limit":10001}`, "config.row_limit", "between 1 and 10000"},
		{`{"allowed_schemas":["public"],"statement_timeout_ms":60001}`, "config.statement_timeout_ms", "between 100 and 60000"},
		{`{"allowed_schemas":["public"],"statement_timeout_ms":1000,"lock_timeout_ms":2000}`, "config.lock_timeout_ms", "at most statement_timeout_ms"},
		{`{"allowed_schemas":["public"],"max_connections":17}`, "config.max_connections", "between 1 and 16"},
		{`{"allowed_schemas":["public"],"writable_tables":["public.orders"],"witness":{"enabled":true,"watched_tables":[]}}`, "config.witness.watched_tables", "must include every writable table"},
		{`{"allowed_schemas":["public"],"witness":{"enabled":true,"watched_tables":["public.orders"],"sensitive_columns":["public.customers.email"]}}`, "config.witness.sensitive_columns[0]", "isn't a watched table"},
		{`{"allowed_schemas":["public"],"witness":{"enabled":true,"autocolumns":["x"]}}`, "config", `"autocolumns" isn't a postgres config key`},
		// S21: L2's settings.
		{`{"allowed_schemas":["public"],"witness":{"enabled":true,"auto_columns":["x"]}}`, "config", "can't be read"},
		{`{"allowed_schemas":["public"],"witness":{"enabled":true,"watched_tables":["public.orders"],"auto_columns":{"public.refunds":["updated_at"]}}}`, "config.witness.auto_columns.public.refunds", "isn't a watched table"},
		{`{"allowed_schemas":["public"],"witness":{"enabled":true,"watched_tables":["public.orders"],"auto_columns":{"public.orders":["Updated At"]}}}`, "config.witness.auto_columns.public.orders[0]", "lowercase column name"},
		{`{"allowed_schemas":["public"],"witness":{"enabled":true,"watched_tables":["public.orders"],"max_rows":{"public.orders":0}}}`, "config.witness.max_rows.public.orders", "between 1 and"},
		{`{"allowed_schemas":["public"],"witness":{"enabled":true,"l2_timeout":"90s"}}`, "config.witness.l2_timeout", "between 1s and 60s"},
		{`{"allowed_schemas":["public"],"witness":{"enabled":true,"diff_rows":500}}`, "config.witness.diff_rows", "between 1 and 100"},
		{`{"allowed_schemas":["public"],"witness":{"max_rows":{"public.orders":1}}}`, "config.witness.max_rows.public.orders", "isn't a watched table"},
		{`{"allowed_schemas":["public"],"witness":{"l2_timeout":"5s"}}`, "config.witness.enabled", "must be true"},
		{`{"allowed_schemas":["public"],"functions":[{"name":"public.f","writes":[{"table":"orders","ops":["update"]}]}]}`, "config.functions[0].writes[0].table", "schema.table"},
		{`{"allowed_schemas":["public"],"functions":[{"name":"public.f","writes":[{"table":"public.orders","ops":["merge"]}]}]}`, "config.functions[0].writes[0].ops", "only insert, update and delete"},
		{`{"allowed_schemas":["public"],"rowlimit":5}`, "config", `"rowlimit" isn't a postgres config key`},
		{`{"allowed_schemas":["public"],"functions":[{"name":"ship_order"}]}`, "config.functions[0].name", "schema.function"},
		{`{"allowed_schemas":["public"],"functions":[{"name":"public.f","args":[{"name":"a","type":"text; drop"}]}]}`, "config.functions[0].args[0].type", "Postgres type name"},
		{`{"allowed_schemas":["public"],"functions":[{"name":"public.f","side_effect":"maybe"}]}`, "config.functions[0].side_effect", "read, write or destructive"},
		{`[]`, "config", "can't be read"},
	}
	for _, b := range bad {
		_, errs := ParseConfig(json.RawMessage(b.cfg))
		found := false
		for _, e := range errs {
			if e.Param == b.param && strings.Contains(e.Message, b.msg) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: got %v, want %s: …%s…", b.cfg, errs, b.param, b.msg)
		}
	}
}

func TestParseDSN(t *testing.T) {
	dev := connector.Env{Name: "dev", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
	prod := connector.Env{Name: "prod", Egress: &egress.Policy{Env: egress.EnvProd}}
	cases := []struct {
		dsn  string
		env  connector.Env
		err  string
		warn bool
		pool bool
	}{
		{"postgres://deedglass_agent:pw@127.0.0.1:5432/demo?sslmode=disable", dev, "", false, false},
		{"postgres://deedglass_agent:pw@127.0.0.1:5432/demo?sslmode=disable", prod, "", false, false}, // local host: TLS not required
		{"postgres://deedglass_agent:pw@db.example.com:5432/demo?sslmode=verify-full", prod, "", false, false},
		{"postgres://deedglass_agent:pw@db.example.com:5432/demo?sslmode=require", prod, "", true, false},
		{"postgres://deedglass_agent:pw@db.example.com:5432/demo?sslmode=disable", prod, "sslmode=disable isn't allowed in prod", false, false},
		{"postgres://deedglass_agent:pw@db.example.com:5432/demo", prod, "sslmode=prefer isn't allowed in prod", false, false},
		{"postgres://deedglass_agent:pw@db.example.com:5432/demo?sslmode=disable", dev, "", false, false},
		{"postgres://deedglass_agent:pw@db.example.com:6543/demo?sslmode=verify-full", prod, "", false, true},
		{"postgres://deedglass_agent:pw@db.example.com:5432/demo?sslmode=verify-full&pgbouncer=true", prod, "", false, true},
		{"host=/tmp user=deedglass_agent dbname=demo", prod, "Unix sockets are for dev only", false, false},
		{"host=/tmp user=deedglass_agent dbname=demo", dev, "", false, false},
		{"postgres://deedglass_agent:pw@127.0.0.1/demo?options=-c%20search_path%3Devil", dev, "sets options", false, false},
		{"postgres://deedglass_agent:pw@127.0.0.1/demo?search_path=evil", dev, "sets search_path", false, false},
		{"", dev, "empty", false, false},
	}
	for _, c := range cases {
		d, err := ParseDSN(c.dsn, c.env)
		switch {
		case c.err != "":
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s (%s): err %v, want %q", c.dsn, c.env.Name, err, c.err)
			}
			if err != nil && strings.Contains(err.Error(), ":pw@") {
				t.Errorf("%s: the error holds the password", c.dsn)
			}
		case err != nil:
			t.Errorf("%s (%s): %v", c.dsn, c.env.Name, err)
		default:
			if (d.Warning != "") != c.warn || d.Pooler != c.pool {
				t.Errorf("%s: warning %q pooler %v", c.dsn, d.Warning, d.Pooler)
			}
			if strings.Contains(d.String(), "pw") {
				t.Errorf("String() holds the password: %s", d.String())
			}
			if d.Config.DialFunc == nil || d.Config.LookupFunc == nil {
				t.Errorf("%s: the dial doesn't go through egress", c.dsn)
			}
		}
	}
}

func TestParams(t *testing.T) {
	got, err := Params([]json.RawMessage{json.RawMessage(`42`), json.RawMessage(`"x"`), json.RawMessage(`true`), json.RawMessage(`null`), json.RawMessage(`-9007199254740991`)})
	if err != nil || got[0] != int64(42) || got[1] != "x" || got[2] != true || got[3] != nil {
		t.Fatalf("Params = %v, %v", got, err)
	}
	for _, bad := range []string{`1.5`, `9007199254740992`, `{"a":1}`, `[1]`, `1e3`} {
		if _, err := Params([]json.RawMessage{json.RawMessage(bad)}); err == nil {
			t.Errorf("param %s accepted", bad)
		}
	}
}

func TestDigests(t *testing.T) {
	d1, _ := SQLDigest("UPDATE orders SET status = 'shipped' WHERE id = $1", []any{int64(42)})
	d2, _ := SQLDigest("UPDATE orders SET status = 'shipped' WHERE id = $1", []any{int64(42)})
	d3, _ := SQLDigest("UPDATE orders SET status = 'shipped' WHERE id = $1", []any{"42"})
	if d1 != d2 || d1 == d3 || !strings.HasPrefix(d1, "sha256:") || len(d1) != 71 {
		t.Fatalf("SQLDigest: %s %s %s", d1, d2, d3)
	}
	// The pinned value: S21 recomputes exactly this.
	if d1 != "sha256:"+sha256hex(`{"params":[42],"sql":"UPDATE orders SET status = 'shipped' WHERE id = $1"}`) {
		t.Fatalf("SQLDigest isn't JCS of {params, sql}: %s", d1)
	}
	c, _ := CallDigest("public.ship_order", map[string]any{"p_order_id": int64(42)})
	if c != "sha256:"+sha256hex(`{"args":{"p_order_id":42},"function":"public.ship_order"}`) {
		t.Fatalf("CallDigest: %s", c)
	}
}

func TestCallStatement(t *testing.T) {
	f := Function{Name: "public.ship_order", Args: []FunctionArg{{Name: "p_order_id", Type: "bigint"}, {Name: "p_note", Type: "text"}}}
	st, dig, err := CallStatement(f, map[string]json.RawMessage{"p_order_id": json.RawMessage(`42`), "p_note": json.RawMessage(`"fragile"`)})
	if err != nil {
		t.Fatal(err)
	}
	if st.SQL != `SELECT * FROM "public"."ship_order"("p_order_id" => $1::bigint, "p_note" => $2::text)` || len(st.Params) != 2 || dig["p_order_id"] != int64(42) {
		t.Fatalf("CallStatement: %s %v %v", st.SQL, st.Params, dig)
	}
	if _, _, err := CallStatement(f, map[string]json.RawMessage{"p_order_id": json.RawMessage(`42`)}); err == nil {
		t.Fatal("a missing argument was accepted")
	}
	if _, _, err := CallStatement(f, map[string]json.RawMessage{"p_order_id": json.RawMessage(`42`), "p_note": json.RawMessage(`"x"`), "evil": json.RawMessage(`"); DROP TABLE x; --"`)}); err == nil {
		t.Fatal("an unknown argument was accepted")
	}
}

func TestToolsAndEnrich(t *testing.T) {
	cfg := `{"allowed_schemas":["public"],"writable_tables":["public.orders"],"witness":{"enabled":true,"watched_tables":["public.orders"]},
		"functions":[{"name":"public.ship_order","args":[{"name":"p_order_id","type":"bigint"}]},{"name":"public.stats","side_effect":"read"}]}`
	c := New(nil)
	norm, errs := c.Validate(t.Context(), connector.Env{}, json.RawMessage(cfg))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	inst := connector.NewInstance(ids.New[ids.ConnectorKind](), ids.New[ids.OrgKind](), "postgres", Type, norm, "sha256:x", connector.Env{Name: "dev"}, nil)
	tools, err := c.Tools(t.Context(), inst)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name+"/"+string(tl.SideEffect))
		if !connector.ValidToolName(Type, tl.Name) {
			t.Errorf("tool name %s", tl.Name)
		}
		if _, err := json.Marshal(tl.InputSchema); err != nil || !json.Valid(tl.InputSchema) {
			t.Errorf("%s: input schema isn't JSON", tl.Name)
		}
		if err := connector.ValidateArgs(tl.InputSchema, json.RawMessage(`{}`)); err != nil && tl.Name == ToolDescribe {
			t.Errorf("describe refuses {}: %v", err)
		}
	}
	if strings.Join(names, " ") != "query/read execute/write call/write describe/read" {
		t.Fatalf("tools: %v", names)
	}
	spec := tools[1]
	m, _ := c.Enrich(t.Context(), inst, spec, json.RawMessage(`{"sql":"DELETE FROM orders WHERE id = 1"}`))
	if m["side_effect"] != "destructive" || m["default_schema"] != "public" {
		t.Fatalf("Enrich(delete) = %v", m)
	}
	m, _ = c.Enrich(t.Context(), inst, spec, json.RawMessage(`{"sql":"UPDATE orders SET status = 'x' WHERE id = 1"}`))
	if _, raised := m["side_effect"]; raised {
		t.Fatalf("Enrich(update) raised: %v", m)
	}
	m, _ = c.Enrich(t.Context(), inst, tools[2], json.RawMessage(`{"function":"public.stats"}`))
	if m["side_effect"] != "read" {
		t.Fatalf("Enrich(call stats) = %v", m)
	}
	// controlled_functions has no execute
	norm2, _ := c.Validate(t.Context(), connector.Env{}, json.RawMessage(`{"mode":"controlled_functions","allowed_schemas":["public"]}`))
	inst2 := connector.NewInstance(ids.New[ids.ConnectorKind](), ids.New[ids.OrgKind](), "pg", Type, norm2, "sha256:y", connector.Env{Name: "dev"}, nil)
	tools2, _ := c.Tools(t.Context(), inst2)
	if len(tools2) != 2 || tools2[0].Name != ToolQuery || tools2[1].Name != ToolDescribe {
		t.Fatalf("controlled_functions tools: %+v", tools2)
	}
	// no credential: credentials_missing, nothing sent
	_, err = c.Execute(t.Context(), inst, connector.Call{Tool: tools[0], Args: json.RawMessage(`{"sql":"SELECT 1"}`)})
	if ce := connector.AsError(err); ce == nil || ce.Kind != connector.KindCredentialsMissing {
		t.Fatalf("no credentials: %v", err)
	}
}

func TestStatementErrorCopy(t *testing.T) {
	for _, msg := range []string{msgMulti, msgQueryKind, msgExecuteKind, msgSchema, msgFunction, msgClass, msgUnclassified, msgNotReady,
		msgRoleUnsafe, msgRoleNotAgent, msgRows, msgTimeout, msgLock, msgCommitLost, msgReadOnly, msgInternal} {
		if strings.Contains(msg, "—") || strings.Contains(msg, "!") || !strings.HasSuffix(msg, ".") {
			t.Errorf("copy rules: %q", msg)
		}
	}
	if got := thousands(1204); got != "1,204" {
		t.Errorf("thousands = %s", got)
	}
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
