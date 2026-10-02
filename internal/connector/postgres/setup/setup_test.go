package setup

import (
	"bytes"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
)

var update = flag.Bool("update", false, "rewrite the planner goldens in testdata/plans")

// TestBeginActionHash recomputes the pinned body hash from the embedded file.
func TestBeginActionHash(t *testing.T) {
	if got := SHA256(BeginActionBody()); got != BeginActionBodySHA256 {
		t.Fatalf("BeginActionBodySHA256 = %s, the file hashes to %s (a changed body is new semantics: update the constant and say so in the report)", BeginActionBodySHA256, got)
	}
	disk, err := os.ReadFile("sql/20_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != SchemaSQL() {
		t.Fatal("the embedded SQL differs from sql/20_schema.sql")
	}
}

func TestSections(t *testing.T) {
	for _, name := range []string{"schema", "ledger", "begin_action"} {
		s := Section(name)
		if strings.Contains(s, "@section") || strings.TrimSpace(s) == "" {
			t.Errorf("section %s: %q", name, s)
		}
	}
	if !strings.Contains(Section("begin_action"), "$fn$") || strings.Contains(Section("ledger"), "begin_action") {
		t.Error("sections are cut in the wrong place")
	}
	// Static SQL rules (CLAUDE.md rule 4).
	all := SchemaSQL()
	for _, banned := range []string{"EXCEPTION WHEN", "EXECUTE format", "EXECUTE '"} {
		if strings.Contains(all, banned) {
			t.Errorf("the static SQL holds %q", banned)
		}
	}
	if !strings.Contains(all, "SET search_path = pg_catalog, pg_temp") {
		t.Error("begin_action's search_path isn't pinned")
	}
	if strings.Count(Section("ledger"), "SET LOCAL ROLE deedglass_owner") != 1 || strings.Count(Section("ledger"), "RESET ROLE") != 1 {
		t.Error("the ledger section must create as deedglass_owner and reset")
	}
}

func TestSlotName(t *testing.T) {
	got := SlotName("con_01K67C04KXEVYEX3PJYQE0T45D")
	if got != "deedglass_01k67c04kxevyex3pjyqe0t45d" || len(got) != 36 {
		t.Fatalf("SlotName = %q (%d)", got, len(got))
	}
}

func TestSCRAMVerifier(t *testing.T) {
	pw, err := GeneratePassword(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) != PasswordLen || !regexp.MustCompile(`^[0-9A-Za-z]{32}$`).MatchString(pw) {
		t.Fatalf("password %q", pw)
	}
	seen := map[string]bool{}
	for range 1000 {
		p, _ := GeneratePassword(nil)
		if seen[p] {
			t.Fatal("a generated password repeated")
		}
		seen[p] = true
	}
	salt := bytes.Repeat([]byte{7}, 16)
	v, err := scramWithSalt("pencil", salt, 4096)
	if err != nil {
		t.Fatal(err)
	}
	// Recompute independently from RFC 5802's definitions.
	salted, _ := pbkdf2.Key(sha256.New, "pencil", salt, 4096, 32)
	mac := func(k []byte, m string) []byte { h := hmac.New(sha256.New, k); h.Write([]byte(m)); return h.Sum(nil) }
	stored := sha256.Sum256(mac(salted, "Client Key"))
	want := "SCRAM-SHA-256$4096:" + base64.StdEncoding.EncodeToString(salt) + "$" +
		base64.StdEncoding.EncodeToString(stored[:]) + ":" + base64.StdEncoding.EncodeToString(mac(salted, "Server Key"))
	if v != want {
		t.Fatalf("verifier\n got %s\nwant %s", v, want)
	}
	if !regexp.MustCompile(`^SCRAM-SHA-256\$4096:[A-Za-z0-9+/]{22}==\$[A-Za-z0-9+/]{43}=:[A-Za-z0-9+/]{43}=$`).MatchString(v) {
		t.Fatalf("verifier shape %q", v)
	}
	if strings.Contains(v, "pencil") {
		t.Fatal("the verifier holds the password")
	}
	v1, _ := SCRAMVerifier(pw, nil)
	v2, _ := SCRAMVerifier(pw, nil)
	if v1 == v2 {
		t.Fatal("two verifiers of one password share a salt")
	}
	if _, err := SCRAMVerifier("tab\there", nil); err == nil {
		t.Fatal("a non-printable password was accepted")
	}
}

func TestValidIdent(t *testing.T) {
	for _, ok := range []string{"public", "orders", "_x", "a$b", strings.Repeat("a", 63)} {
		if !ValidIdent(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "Public", "1x", "a-b", "a b", `a"b`, "a;b", strings.Repeat("a", 64), "ä"} {
		if ValidIdent(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, ok := SplitQualified("public.orders", 2); !ok {
		t.Error("public.orders refused")
	}
	for _, bad := range []string{"orders", "public.orders.x", "public.", ".orders", "public.Orders"} {
		if _, ok := SplitQualified(bad, 2); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"bigint", "text", "numeric(10,2)", "timestamp with time zone", "int4[]"} {
		if !ValidType(ok) {
			t.Errorf("type %q refused", ok)
		}
	}
	for _, bad := range []string{"text; drop", "int)", "a  b", "Text", "text --"} {
		if ValidType(bad) {
			t.Errorf("type %q accepted", bad)
		}
	}
	if got := QuoteFunction("public.ship_order(bigint)"); got != `"public"."ship_order"(bigint)` {
		t.Errorf("QuoteFunction = %s", got)
	}
}

// demo is the Acme demo's config (examples/demo/connectors/postgres.yaml).
func demoDesired() Desired {
	return Desired{AllowedSchemas: []string{"public"}, WritableTables: []string{"public.orders", "public.refunds"},
		WatchedTables: []string{"public.customers", "public.orders", "public.refunds"}, Functions: []string{"public.ship_order(bigint)"}, Provider: ProviderLocal}
}

func demoTables() map[string]TableState {
	return map[string]TableState{
		"public.customers":   {Name: "public.customers", Kind: "r", ReplIdent: "d", Persistence: "p", HasPK: true},
		"public.order_items": {Name: "public.order_items", Kind: "r", ReplIdent: "d", Persistence: "p", HasPK: true},
		"public.orders":      {Name: "public.orders", Kind: "r", ReplIdent: "d", Persistence: "p", HasPK: true},
		"public.products":    {Name: "public.products", Kind: "r", ReplIdent: "d", Persistence: "p", HasPK: true},
		"public.refunds":     {Name: "public.refunds", Kind: "r", ReplIdent: "d", Persistence: "p", HasPK: true},
	}
}

// fresh is the demo database before init: no roles, no schema, no publication.
func fresh() *State {
	return &State{Database: "acme", VersionNum: 180006, Version: "18.6", CurrentUser: "postgres", CurrentSuper: true,
		Roles: map[string]RoleState{RoleOwner: {}, RoleAgent: {}, RoleWitness: {}}, SchemasFound: map[string]bool{"public": true, "deedglass": false},
		SchemaUsage: map[string]bool{}, Tables: demoTables(), Functions: map[string]FuncState{"public.ship_order(bigint)": {Signature: "public.ship_order(bigint)", Exists: true, SecDef: true}},
		Settings:     map[string]string{"wal_level": "logical", "max_replication_slots": "10", "max_wal_senders": "10", "max_slot_wal_keep_size": "1024"},
		SettingUnits: map[string]string{"max_slot_wal_keep_size": "MB"}, EmitPublicExec: true}
}

// complete is the demo database after init.
func complete() *State {
	st := fresh()
	wset := map[string]string{"timezone": "UTC", "datestyle": "ISO, YMD", "intervalstyle": "postgres", "bytea_output": "hex", "extra_float_digits": "3"}
	st.Roles = map[string]RoleState{
		RoleOwner:   {Exists: true},
		RoleAgent:   {Exists: true, Login: true, Settings: map[string]string{"statement_timeout": "5s", "lock_timeout": "2s", "idle_in_transaction_session_timeout": "10s"}},
		RoleWitness: {Exists: true, Login: true, Replication: true, Settings: wset},
	}
	st.SchemaExists, st.SchemaOwner = true, RoleOwner
	st.SchemasFound["deedglass"] = true
	st.SchemaUsage = map[string]bool{"deedglass_agent/deedglass": true, "deedglass_witness/deedglass": true, "deedglass_agent/public": true, "deedglass_witness/public": true}
	st.Ledger = LedgerState{Exists: true, Owner: RoleOwner, ReplIdent: "f", RLS: true, ForceRLS: true, Columns: LedgerColumns, Policies: []string{"owner_all", "witness_read"}, WitnessSelect: true}
	st.BeginAction = BeginActionState{Exists: true, Owner: RoleOwner, SecDef: true, Config: "search_path=pg_catalog, pg_temp, TimeZone=UTC", BodySHA256: BeginActionBodySHA256, AgentExec: true}
	for name, t := range st.Tables {
		t.AgentPrivs = []string{"SELECT"}
		if name == "public.orders" || name == "public.refunds" {
			t.AgentPrivs = []string{"SELECT", "INSERT", "UPDATE", "DELETE"}
		}
		if name != "public.order_items" && name != "public.products" {
			t.ReplIdent, t.WitnessSelect = "f", true
		}
		st.Tables[name] = t
	}
	f := st.Functions["public.ship_order(bigint)"]
	f.AgentExec = true
	st.Functions["public.ship_order(bigint)"] = f
	st.Publication = PublicationState{Exists: true, Publish: []string{"insert", "update", "delete", "truncate"},
		Tables: []string{"deedglass.ledger", "public.customers", "public.orders", "public.refunds"}, GenCols: GenColsStored}
	return st
}

func TestPlannerGoldens(t *testing.T) {
	cases := map[string]func() (*State, Desired){
		"fresh": func() (*State, Desired) { return fresh(), demoDesired() },
		"fresh_rds": func() (*State, Desired) {
			st := fresh()
			st.CurrentSuper, st.CurrentCreate, st.Settings["wal_level"] = false, true, "replica"
			st.ProviderRoles = []string{"rds_replication", "rds_superuser"}
			d := demoDesired()
			d.Provider = ProviderRDS
			return st, d
		},
		"partial": func() (*State, Desired) {
			// roles exist (V0 left them, with old settings), no schema, wal_level replica
			st := fresh()
			st.Roles = map[string]RoleState{RoleOwner: {Exists: true}, RoleAgent: {Exists: true, Login: true, Settings: map[string]string{"statement_timeout": "5s", "idle_in_transaction_session_timeout": "5s"}},
				RoleWitness: {Exists: true, Login: true, Replication: true}}
			st.Settings["wal_level"] = "replica"
			return st, demoDesired()
		},
		"complete": func() (*State, Desired) { return complete(), demoDesired() },
		"table_added": func() (*State, Desired) {
			d := demoDesired()
			d.WritableTables = append(d.WritableTables, "public.order_items")
			d.WatchedTables = append(d.WatchedTables, "public.order_items")
			return complete(), d
		},
		"table_removed": func() (*State, Desired) {
			d := demoDesired()
			d.WritableTables = []string{"public.orders"}
			d.WatchedTables = []string{"public.customers", "public.orders"}
			return complete(), d
		},
		"function_tampered": func() (*State, Desired) {
			st := complete()
			st.BeginAction.BodySHA256 = "sha256:" + strings.Repeat("ab", 32)
			st.BeginAction.Owner = "postgres"
			return st, demoDesired()
		},
		"unsafe_agent": func() (*State, Desired) {
			st := complete()
			a := st.Roles[RoleAgent]
			a.Super, a.MemberOf = true, []string{"pg_write_all_data"}
			st.Roles[RoleAgent] = a
			t := st.Tables["public.products"]
			t.AgentPrivs = []string{"SELECT", "UPDATE", "TRUNCATE"}
			st.Tables["public.products"] = t
			st.Publication.RowFilters = []string{"public.orders"}
			return st, demoDesired()
		},
		"control_plane": func() (*State, Desired) {
			st := fresh()
			st.ControlPlane, st.Database = true, "deedglass"
			return st, demoDesired()
		},
		"fresh_pg17": func() (*State, Desired) {
			// before PG 18 there is no publish_generated_columns
			st := fresh()
			st.VersionNum, st.Version = 170011, "17.11"
			return st, demoDesired()
		},
		"pg18_gencols": func() (*State, Desired) {
			// an S17-era publication on PG 18: stored generated columns aren't published (DL-037)
			st := complete()
			st.Publication.GenCols = GenColsNone
			return st, demoDesired()
		},
		"missing_table": func() (*State, Desired) {
			d := demoDesired()
			d.WritableTables = append(d.WritableTables, "public.orderz")
			return fresh(), d
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			st, d := mk()
			p := Build(st, d)
			var sb strings.Builder
			for _, c := range p.Changes {
				sb.WriteString(c.Op + " " + c.Summary + "\n")
			}
			for _, n := range p.Notes {
				sb.WriteString("○ " + n.Text + "\n")
			}
			for _, b := range p.Blockers {
				sb.WriteString("✕ " + b + "\n")
			}
			if p.Empty() && len(p.Blockers) == 0 {
				sb.WriteString("Nothing to change.\n")
			}
			sb.WriteString("plan " + p.SHA256() + "\n\n")
			sb.WriteString(p.Body())
			got := sb.String()
			if strings.Contains(got, "SCRAM-SHA-256") || strings.Contains(got, "PASSWORD") {
				t.Fatal("a plan holds a password clause")
			}
			path := filepath.Join("testdata", "plans", name+".sql")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/connector/postgres/setup -run TestPlannerGoldens -update)", err)
			}
			if got != string(want) {
				t.Fatalf("plan %s differs from its golden:\n%s", name, got)
			}
			// Determinism: the same inputs give the same plan.
			if Build(st, d).SHA256() != p.SHA256() {
				t.Fatal("the planner isn't deterministic")
			}
		})
	}
	if !Build(complete(), demoDesired()).Empty() {
		t.Fatal("a complete database must plan Nothing to change.")
	}
}

func TestChecksMatrix(t *testing.T) {
	base := func() CheckInput {
		return CheckInput{State: complete(), Desired: demoDesired(), Witness: true, ConnectorID: "con_01k67c04kxevyex3pjyqe0t45d", Connector: "postgres", Host: "127.0.0.1",
			WitnessDSN: &WitnessInfo{Configured: true, User: RoleWitness, Connected: true}}
	}
	status := func(cs []connector.Check, id string) connector.Check {
		for _, c := range cs {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("no check %s", id)
		return connector.Check{}
	}
	healthy := Checks(base())
	for _, c := range healthy {
		if c.Status == connector.CheckFail {
			t.Errorf("healthy: %s failed: %s", c.ID, c.Detail)
		}
	}
	if ready, line := Readiness(healthy, true); !ready || !strings.HasPrefix(line, "Ready for L2 · ") {
		t.Errorf("healthy readiness: %s", line)
	}
	cases := []struct {
		name   string
		mut    func(*CheckInput)
		id     string
		status connector.CheckStatus
		detail string
	}{
		{"wal_level replica", func(in *CheckInput) { in.State.Settings["wal_level"] = "replica" }, "wal_level", connector.CheckFail, "replica · L2 needs logical"},
		{"rds wal_level", func(in *CheckInput) { in.State.Settings["wal_level"] = "replica"; in.Desired.Provider = ProviderRDS }, "wal_level", connector.CheckFail, "replica"},
		{"ledger replica identity default", func(in *CheckInput) { in.State.Ledger.ReplIdent = "d" }, "schema", connector.CheckFail, "replica identity isn't full"},
		{"watched table not in publication", func(in *CheckInput) {
			in.State.Publication.Tables = []string{"deedglass.ledger", "public.customers", "public.orders"}
		}, "publication", connector.CheckFail, "missing public.refunds"},
		{"row filter", func(in *CheckInput) { in.State.Publication.RowFilters = []string{"public.orders"} }, "publication", connector.CheckFail, "row filters"},
		{"generated columns not published (PG 18)", func(in *CheckInput) { in.State.Publication.GenCols = GenColsNone }, "publication", connector.CheckFail, "stored generated columns"},
		{"virtual generated column (PG 18)", func(in *CheckInput) {
			t := in.State.Tables["public.orders"]
			t.VirtualGenerated = []string{"total_with_tax"}
			in.State.Tables["public.orders"] = t
		}, "witness.watched_tables", connector.CheckFail, "virtual generated columns (total_with_tax)"},
		{"agent superuser", func(in *CheckInput) { r := in.State.Roles[RoleAgent]; r.Super = true; in.State.Roles[RoleAgent] = r }, "agent_role", connector.CheckFail, "superuser-class: SUPERUSER"},
		{"agent member of pg_write_all_data", func(in *CheckInput) {
			r := in.State.Roles[RoleAgent]
			r.MemberOf = []string{"pg_write_all_data"}
			in.State.Roles[RoleAgent] = r
		}, "agent_role", connector.CheckFail, "pg_write_all_data"},
		{"agent writes elsewhere", func(in *CheckInput) {
			x := in.State.Tables["public.products"]
			x.AgentPrivs = []string{"SELECT", "UPDATE"}
			in.State.Tables["public.products"] = x
		}, "agent_role", connector.CheckFail, "writes outside writable_tables: public.products update"},
		{"agent ledger access", func(in *CheckInput) { in.State.Ledger.AgentPrivs = []string{"INSERT"} }, "agent_role", connector.CheckFail, "ledger access"},
		{"agent extra reads", func(in *CheckInput) { in.State.AgentExtraReads = []string{"billing.invoices"} }, "agent_role", connector.CheckWarn, "extra read grants"},
		{"witness without replication", func(in *CheckInput) {
			r := in.State.Roles[RoleWitness]
			r.Replication = false
			in.State.Roles[RoleWitness] = r
		}, "witness_role", connector.CheckFail, "no replication"},
		{"witness settings", func(in *CheckInput) {
			r := in.State.Roles[RoleWitness]
			r.Settings = nil
			in.State.Roles[RoleWitness] = r
		}, "witness_role", connector.CheckFail, "settings not pinned"},
		{"supabase postgres witness", func(in *CheckInput) { in.WitnessDSN.User = "postgres" }, "witness_role", connector.CheckWarn, "provider-wide role"},
		{"begin_action replaced", func(in *CheckInput) { in.State.BeginAction.BodySHA256 = "sha256:" + strings.Repeat("ab", 32) }, "begin_action", connector.CheckFail,
			"body changed: expected " + ShortHash(BeginActionBodySHA256) + ", found sha256:abab…abab"},
		{"unlogged watched table", func(in *CheckInput) {
			x := in.State.Tables["public.orders"]
			x.Persistence = "u"
			in.State.Tables["public.orders"] = x
		}, "witness.watched_tables", connector.CheckFail, "UNLOGGED"},
		{"no primary key", func(in *CheckInput) {
			x := in.State.Tables["public.refunds"]
			x.HasPK = false
			in.State.Tables["public.refunds"] = x
		}, "witness.watched_tables", connector.CheckWarn, "public.refunds has no primary key"},
		{"trigger on orders", func(in *CheckInput) {
			x := in.State.Tables["public.orders"]
			x.Triggers = []string{"orders_audit"}
			in.State.Tables["public.orders"] = x
		}, "witness.watched_tables", connector.CheckWarn, "orders_audit"},
		{"writable unwatched", func(in *CheckInput) { in.Desired.WatchedTables = []string{"public.customers", "public.orders"} }, "writable_watched", connector.CheckFail, "public.refunds"},
		{"no free slots", func(in *CheckInput) { in.State.Settings["max_replication_slots"] = "0" }, "slot_capacity", connector.CheckFail, "none free"},
		{"one free slot", func(in *CheckInput) { in.State.Settings["max_replication_slots"] = "1" }, "slot_capacity", connector.CheckWarn, "one free"},
		{"unlimited wal keep", func(in *CheckInput) { in.State.Settings["max_slot_wal_keep_size"] = "-1" }, "wal_keep_cap", connector.CheckWarn, "unlimited"},
		{"short idle timeout", func(in *CheckInput) {
			in.State.Settings["idle_replication_slot_timeout"] = "3600"
			in.State.SettingUnits["idle_replication_slot_timeout"] = "s"
		}, "idle_slot_timeout", connector.CheckWarn, "1 hour"},
		{"lost slot", func(in *CheckInput) {
			in.State.Slots = []SlotState{{Name: "deedglass_01k67c04kxevyex3pjyqe0t45d", WALStatus: "lost", Invalidation: "wal_removed"}}
		}, "slot", connector.CheckFail, "wal_removed"},
		{"active slot", func(in *CheckInput) {
			in.State.Slots = []SlotState{{Name: "deedglass_01k67c04kxevyex3pjyqe0t45d", Active: true, WALStatus: "reserved"}}
		}, "slot", connector.CheckPass, "active · reserved"},
		{"remote without tls in prod", func(in *CheckInput) { in.Host, in.Prod = "db.example.com", true }, "tls", connector.CheckFail, "off · remote host"},
		{"witness through a pooler", func(in *CheckInput) { in.WitnessDSN.Pooler = true }, "pooler", connector.CheckFail, "witness_dsn"},
		{"version 13", func(in *CheckInput) { in.State.VersionNum, in.State.Version = 130010, "13.10" }, "version", connector.CheckFail, "too old"},
		{"version 15", func(in *CheckInput) { in.State.VersionNum, in.State.Version = 150004, "15.4" }, "version", connector.CheckWarn, "untested"},
		{"probe refused", func(in *CheckInput) {
			in.Probe = &ProbeResult{BeginAction: true, Detail: "deedglass_agent could insert into the ledger directly"}
		}, "probe", connector.CheckFail, "directly"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base()
			c.mut(&in)
			got := status(Checks(in), c.id)
			if got.Status != c.status || !strings.Contains(got.Detail, c.detail) {
				t.Fatalf("%s: %s %q, want %s containing %q", c.id, got.Status, got.Detail, c.status, c.detail)
			}
			if got.Status == connector.CheckFail && got.Fix == "" {
				t.Fatalf("%s fails without a fix", c.id)
			}
		})
	}
}

func TestCheckCopy(t *testing.T) {
	in := CheckInput{State: fresh(), Desired: demoDesired(), Witness: true, Host: "db.example.com", Prod: true}
	in.State.Settings["wal_level"] = "replica"
	in.State.Settings["max_slot_wal_keep_size"] = "-1"
	for _, c := range Checks(in) {
		for _, s := range []string{c.Detail, c.Fix} {
			if strings.Contains(s, "—") || strings.Contains(s, "!") {
				t.Errorf("%s: %q breaks the copy rules", c.ID, s)
			}
		}
		if c.Status == connector.CheckFail && !strings.HasSuffix(strings.TrimSpace(c.Fix), ".") {
			t.Errorf("%s: the fix %q isn't a sentence", c.ID, c.Fix)
		}
	}
}

func TestDetect(t *testing.T) {
	st := fresh()
	cases := []struct {
		host string
		mut  func(*State)
		want Provider
	}{
		{"127.0.0.1", nil, ProviderLocal},
		{"localhost", nil, ProviderLocal},
		{"::1", nil, ProviderLocal},
		{"/var/run/postgresql", nil, ProviderLocal},
		{"db.example.com", nil, ProviderOther},
		{"x.abc.us-east-1.rds.amazonaws.com", nil, ProviderRDS},
		{"x.cluster-abc.us-east-1.rds.amazonaws.com", nil, ProviderAurora},
		{"10.0.0.5", func(s *State) { s.Settings["rds.logical_replication"] = "off" }, ProviderRDS},
		{"10.0.0.5", func(s *State) { s.AuroraVersion = true }, ProviderAurora},
		{"db.abcd.supabase.co", nil, ProviderSupabase},
		{"10.0.0.5", func(s *State) { s.ProviderRoles = []string{"supabase_admin"} }, ProviderSupabase},
		{"ep-x.us-east-2.aws.neon.tech", nil, ProviderNeon},
		{"10.0.0.5", func(s *State) { s.NeonSettings = true }, ProviderNeon},
		{"10.0.0.5", func(s *State) { s.ProviderRoles = []string{"cloudsqlsuperuser"} }, ProviderCloudSQL},
		{"x.postgres.database.azure.com", nil, ProviderAzure},
	}
	for _, c := range cases {
		s := *st
		s.Settings = map[string]string{"wal_level": "logical"}
		if c.mut != nil {
			c.mut(&s)
		}
		if got := Detect(c.host, &s); got != c.want {
			t.Errorf("Detect(%s) = %s, want %s", c.host, got, c.want)
		}
	}
}
