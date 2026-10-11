package setup

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
)

// CheckInput is what the readiness checks read besides the catalogs.
type CheckInput struct {
	State       *State
	Desired     Desired
	Witness     bool   // witness.enabled
	ConnectorID string // con_…: names the slot; "" in local mode without a connector
	Connector   string // the connector's name, for the fix lines
	Prod        bool
	Host        string // the agent DSN's host
	TLS         bool   // the agent connection uses TLS
	Pooler      bool   // the agent DSN goes through a transaction pooler
	WitnessDSN  *WitnessInfo
	Probe       *ProbeResult // nil: --probe wasn't asked for
}

// WitnessInfo is what check learned from the witness DSN.
type WitnessInfo struct {
	Configured bool
	User       string
	Pooler     bool
	Connected  bool
	Err        string // why the connection failed (redacted)
}

// ProbeResult is check --probe: begin_action in a transaction that is rolled back.
type ProbeResult struct {
	BeginAction   bool   // begin_action wrote the ledger row and the marker
	DirectRefused bool   // deedglass_agent's own INSERT into the ledger got 42501
	Detail        string // what went wrong
}

const docBase = "https://docs.deedglass.com/connectors/postgres"

func doc(anchor string) string { return docBase + "#" + anchor }

// Readiness is the summary line: "Ready for Proven checks · 8 passed · 2 notes · 0 failed".
func Readiness(checks []connector.Check, witness bool) (ready bool, line string) {
	pass, notes, fail := 0, 0, 0
	for _, c := range checks {
		switch c.Status {
		case connector.CheckPass:
			pass++
		case connector.CheckFail:
			fail++
		default:
			notes++
		}
	}
	counts := fmt.Sprintf("%d passed · %d %s · %d failed", pass, notes, plural(notes, "note", "notes"), fail)
	switch {
	case fail > 0:
		return false, fmt.Sprintf("Not ready for Proven checks · %d %s to fix · %s", fail, plural(fail, "item", "items"), counts)
	case !witness:
		return false, "Ready for L0 · the witness is off in this connector's config · " + counts
	}
	return true, "Ready for Proven checks · " + counts
}

// Checks runs the readiness table (S17 §3.10) over in. It reads only in; nothing is written.
func Checks(in CheckInput) []connector.Check {
	st := in.State
	initFix := "Run deedglass connector postgres init"
	if in.Connector != "" {
		initFix += " --connector " + in.Connector
	}
	initFix += ", review the plan, then apply it."
	var out []connector.Check
	add := func(id, title string, s connector.CheckStatus, detail, fix, fixSQL, url string) {
		out = append(out, connector.Check{ID: id, Title: title, Status: s, Detail: detail, Fix: fix, FixSQL: fixSQL, DocURL: url})
	}
	// version
	switch v := st.VersionNum; {
	case v >= 160000:
		add("version", "version", connector.CheckPass, "PostgreSQL "+st.Version, "", "", "")
	case v >= 140000:
		add("version", "version", connector.CheckWarn, "PostgreSQL "+st.Version+" · untested (Deedglass is tested on 16, 17 and 18)", "Upgrade to PostgreSQL 16 or later.", "", doc("versions"))
	default:
		add("version", "version", connector.CheckFail, "PostgreSQL "+st.Version+" · too old", "Upgrade to PostgreSQL 16 or later.", "", doc("versions"))
	}
	// wal_level
	prov := in.Desired.Provider
	if wl := st.Settings["wal_level"]; wl == "logical" {
		add("wal_level", "wal_level", connector.CheckPass, "logical", "", "", "")
	} else {
		fix, fixSQL := WALLevelFix(prov)
		add("wal_level", "wal_level", connector.CheckFail, wl+" · L2 needs logical", fix, fixSQL, DocURL(prov))
	}
	// slot_capacity
	maxSlots, _ := st.SettingInt("max_replication_slots")
	maxSenders, _ := st.SettingInt("max_wal_senders")
	freeSlots, freeSenders := maxSlots-len(st.Slots), maxSenders-st.SendersUsed
	free := min(freeSlots, freeSenders)
	detail := fmt.Sprintf("%d of %d slots used · %d of %d senders", len(st.Slots), maxSlots, st.SendersUsed, maxSenders)
	switch {
	case free >= 2:
		add("slot_capacity", "slot capacity", connector.CheckPass, detail, "", "", "")
	case free == 1:
		add("slot_capacity", "slot capacity", connector.CheckWarn, detail+" · one free", "Raise max_replication_slots and max_wal_senders by 2, then restart Postgres.", "", doc("slot-capacity"))
	default:
		add("slot_capacity", "slot capacity", connector.CheckFail, detail+" · none free", "Raise max_replication_slots and max_wal_senders by 2 (or drop an unused slot), then restart Postgres.", "", doc("slot-capacity"))
	}
	// wal_keep_cap
	if n, ok := st.SettingBytes("max_slot_wal_keep_size"); ok {
		if n < 0 {
			add("wal_keep_cap", "wal keep cap", connector.CheckWarn, "unlimited · set max_slot_wal_keep_size = '4GB' so a stalled witness can't fill the disk",
				"Set max_slot_wal_keep_size = '4GB', then reload Postgres. A slot that falls further behind is invalidated instead of filling the disk.",
				"ALTER SYSTEM SET max_slot_wal_keep_size = '4GB';\nSELECT pg_catalog.pg_reload_conf();", doc("wal-keep-cap"))
		} else {
			add("wal_keep_cap", "wal keep cap", connector.CheckPass, sizeText(n), "", "", "")
		}
	}
	// idle_slot_timeout (PG 18)
	if v, ok := st.Settings["idle_replication_slot_timeout"]; ok {
		secs := durationSeconds(v, st.SettingUnits["idle_replication_slot_timeout"])
		switch {
		case secs == 0:
			add("idle_slot_timeout", "idle slot timeout", connector.CheckPass, "off", "", "", "")
		case secs >= 86400:
			add("idle_slot_timeout", "idle slot timeout", connector.CheckPass, durationText(secs), "", "", "")
		default:
			add("idle_slot_timeout", "idle slot timeout", connector.CheckWarn, durationText(secs)+" · a witness that is down longer loses its slot",
				"Set idle_replication_slot_timeout to 0 (off) or at least 1 day, then reload Postgres.", "ALTER SYSTEM SET idle_replication_slot_timeout = 0;\nSELECT pg_catalog.pg_reload_conf();", doc("idle-slot-timeout"))
		}
	}
	// schema
	l := st.Ledger
	var gaps []string
	switch {
	case !st.SchemaExists:
		gaps = append(gaps, "schema deedglass is missing")
	case !l.Exists:
		gaps = append(gaps, "deedglass.ledger is missing")
	default:
		if st.SchemaOwner != RoleOwner {
			gaps = append(gaps, "schema owner is "+st.SchemaOwner)
		}
		if !ledgerShapeOK(l.Columns) {
			gaps = append(gaps, "the ledger's columns differ")
		}
		if !l.RLS || !l.ForceRLS {
			gaps = append(gaps, "row level security isn't forced")
		}
		if l.ReplIdent != "f" {
			gaps = append(gaps, "replica identity isn't full")
		}
		if l.Owner != RoleOwner {
			gaps = append(gaps, "the ledger's owner is "+l.Owner)
		}
	}
	if len(gaps) == 0 {
		add("schema", "schema deedglass", connector.CheckPass, "ledger · RLS forced · replica identity full", "", "", "")
	} else {
		add("schema", "schema deedglass", connector.CheckFail, strings.Join(gaps, " · "), initFix, "", doc("schema"))
	}
	// begin_action
	ba := st.BeginAction
	switch {
	case !ba.Exists:
		add("begin_action", "begin_action", connector.CheckFail, "missing", initFix, "", doc("begin-action"))
	case ba.BodySHA256 != BeginActionBodySHA256:
		add("begin_action", "begin_action", connector.CheckFail, "body changed: expected "+ShortHash(BeginActionBodySHA256)+", found "+ShortHash(ba.BodySHA256),
			"Someone replaced deedglass.begin_action. Find out who, then run init to restore it.", "", doc("begin-action"))
	case !ba.SecDef || ba.Config != "search_path=pg_catalog, pg_temp, TimeZone=UTC" || ba.Owner != RoleOwner:
		add("begin_action", "begin_action", connector.CheckFail, fmt.Sprintf("owner %s · security definer %t · settings %q", ba.Owner, ba.SecDef, ba.Config), initFix, "", doc("begin-action"))
	case !ba.AgentExec || ba.PublicExec:
		add("begin_action", "begin_action", connector.CheckFail, "grants differ: only deedglass_agent may execute it", initFix, "", doc("begin-action"))
	default:
		add("begin_action", "begin_action", connector.CheckPass, "security definer · search_path pinned · body "+ShortHash(ba.BodySHA256), "", "", "")
	}
	out = append(out, agentRole(in, initFix))
	out = append(out, witnessRole(in, initFix))
	out = append(out, watchedTables(in, initFix)...)
	out = append(out, publication(in, initFix))
	out = append(out, slot(in))
	// tls
	switch {
	case in.TLS:
		add("tls", "tls", connector.CheckPass, "on", "", "", "")
	case IsLocalHost(in.Host):
		add("tls", "tls", connector.CheckWarn, "off · local host", "Use sslmode=verify-full for any database that isn't on this machine.", "", doc("tls"))
	case in.Prod:
		add("tls", "tls", connector.CheckFail, "off · remote host", "Connect with sslmode=verify-full (or verify-ca) and the server's CA.", "", doc("tls"))
	default:
		add("tls", "tls", connector.CheckWarn, "off · remote host (dev)", "Connect with sslmode=verify-full (or verify-ca) and the server's CA. Prod refuses this connection.", "", doc("tls"))
	}
	// pooler
	switch {
	case in.WitnessDSN != nil && in.WitnessDSN.Pooler:
		add("pooler", "pooler", connector.CheckFail, "witness_dsn goes through a transaction pooler", "Logical replication needs a direct connection. Point witness_dsn at the database's direct port (5432), not the pooler (6543).", "", doc("pooler"))
	case in.Pooler:
		add("pooler", "pooler", connector.CheckWarn, "dsn goes through a transaction pooler · statements run without server-side type checks", "Prefer a direct connection for dsn. Through a pooler Deedglass skips DISCARD ALL (the pooler resets sessions) and sends parameters as text.", "", doc("pooler"))
	default:
		add("pooler", "pooler", connector.CheckPass, "direct", "", "", "")
	}
	// emit_message_acl
	if st.EmitPublicExec {
		add("emit_message_acl", "marker ACL", connector.CheckWarn, "PUBLIC may call pg_logical_emit_message (the default) · a forged marker has no ledger row, so the witness flags it",
			"Optional hardening, as a superuser: revoke it from PUBLIC and grant it to deedglass_owner.", EmitHardenSQL(st.VersionNum), doc("marker-acl"))
	} else {
		add("emit_message_acl", "marker ACL", connector.CheckPass, "revoked from PUBLIC", "", "", "")
	}
	// probe
	if p := in.Probe; p != nil {
		switch {
		case p.BeginAction && p.DirectRefused:
			add("probe", "probe", connector.CheckPass, "ledger and marker written and rolled back · a direct ledger insert got 42501", "", "", "")
		default:
			add("probe", "probe", connector.CheckFail, p.Detail, initFix, "", doc("probe"))
		}
	}
	return out
}

// EmitHardenSQL is the superuser-only hardening `check` suggests: pg_logical_emit_message (its text
// and bytea forms; PG 17 added a flush argument) revoked from PUBLIC and granted to
// deedglass_owner, so begin_action keeps working.
func EmitHardenSQL(versionNum int) string {
	sigs := []string{"boolean, text, text", "boolean, text, bytea"}
	if versionNum >= 170000 {
		sigs = []string{"boolean, text, text, boolean", "boolean, text, bytea, boolean"}
	}
	var lines []string
	for _, sig := range sigs {
		// deedglass_owner emits the marker (begin_action); deedglass_witness emits the decoder's
		// heartbeat (S20), a non-transactional message that shows the slot is moving.
		lines = append(lines, "REVOKE EXECUTE ON FUNCTION pg_catalog.pg_logical_emit_message("+sig+") FROM PUBLIC;",
			"GRANT EXECUTE ON FUNCTION pg_catalog.pg_logical_emit_message("+sig+") TO deedglass_owner, deedglass_witness;")
	}
	return strings.Join(lines, "\n")
}

func agentRole(in CheckInput, initFix string) connector.Check {
	st, d := in.State, in.Desired
	c := connector.Check{ID: "agent_role", Title: "agent role", DocURL: doc("agent-role")}
	rs := st.Roles[RoleAgent]
	if !rs.Exists {
		c.Status, c.Detail, c.Fix = connector.CheckFail, "deedglass_agent is missing", initFix
		return c
	}
	var unsafe []string
	for _, m := range rs.MemberOf {
		if contains(UnsafeRoles, m) {
			unsafe = append(unsafe, m)
		}
	}
	if rs.Super || rs.BypassRLS || rs.Replication || rs.CreateRole || rs.CreateDB || len(unsafe) > 0 {
		what := []string{}
		for _, x := range []struct {
			on   bool
			name string
		}{{rs.Super, "SUPERUSER"}, {rs.BypassRLS, "BYPASSRLS"}, {rs.Replication, "REPLICATION"}, {rs.CreateRole, "CREATEROLE"}, {rs.CreateDB, "CREATEDB"}} {
			if x.on {
				what = append(what, x.name)
			}
		}
		what = append(what, unsafe...)
		c.Status, c.Detail = connector.CheckFail, "superuser-class: "+strings.Join(what, ", ")
		c.Fix = "Deedglass refuses to run agent SQL with this role. " + initFix
		return c
	}
	writable := map[string]bool{}
	for _, t := range d.WritableTables {
		writable[t] = true
	}
	var badWrites, missing []string
	for _, name := range sortedKeys(st.Tables) {
		t := st.Tables[name]
		for _, p := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			if t.HasAgent(p) && !(writable[name] && (p == "INSERT" || p == "UPDATE" || p == "DELETE")) {
				badWrites = append(badWrites, name+" "+strings.ToLower(p))
			}
		}
		if contains(d.AllowedSchemas, schemaOf(name)) && !t.HasAgent("SELECT") {
			missing = append(missing, name+" select")
		}
	}
	for _, t := range d.WritableTables {
		ts := st.Tables[t]
		if !ts.HasAgent("INSERT") || !ts.HasAgent("UPDATE") || !ts.HasAgent("DELETE") {
			missing = append(missing, t+" write")
		}
	}
	for _, t := range st.AgentExtraWrites {
		badWrites = append(badWrites, t)
	}
	switch {
	case len(st.Ledger.AgentPrivs) > 0:
		c.Status, c.Detail, c.Fix = connector.CheckFail, "has ledger access ("+strings.ToLower(strings.Join(st.Ledger.AgentPrivs, ", "))+")", initFix
	case len(badWrites) > 0:
		c.Status, c.Detail, c.Fix = connector.CheckFail, "writes outside writable_tables: "+clipList(badWrites, 4), initFix
	case len(missing) > 0:
		c.Status, c.Detail, c.Fix = connector.CheckFail, "missing grants: "+clipList(missing, 4), initFix
	case len(rs.MemberOf) > 0:
		c.Status, c.Detail, c.Fix = connector.CheckWarn, "member of "+strings.Join(rs.MemberOf, ", "), initFix
	case len(st.AgentExtraReads) > 0:
		c.Status, c.Detail = connector.CheckWarn, "extra read grants: "+clipList(st.AgentExtraReads, 4)
		c.Fix = "deedglass_agent can read tables outside allowed_schemas. Revoke them, or add their schema to allowed_schemas."
	default:
		n := len(d.WritableTables)
		c.Status, c.Detail = connector.CheckPass, fmt.Sprintf("least privilege · %d writable %s · no ledger access", n, plural(n, "table", "tables"))
	}
	return c
}

func witnessRole(in CheckInput, initFix string) connector.Check {
	st := in.State
	c := connector.Check{ID: "witness_role", Title: "witness role", DocURL: doc("witness-role")}
	if !in.Witness {
		c.Status, c.Detail = connector.CheckSkip, "the witness is off in this connector's config"
		return c
	}
	rs := st.Roles[RoleWitness]
	if !rs.Exists {
		c.Status, c.Detail, c.Fix = connector.CheckFail, "deedglass_witness is missing", initFix
		return c
	}
	rds := in.Desired.Provider == ProviderRDS || in.Desired.Provider == ProviderAurora
	repl := rs.Replication || rds && contains(rs.MemberOf, "rds_replication")
	if !repl {
		c.Status, c.Detail, c.Fix = connector.CheckFail, "no replication", initFix
		if rds {
			c.FixSQL = "GRANT rds_replication TO deedglass_witness;"
		}
		return c
	}
	var unpinned []string
	for _, kv := range WitnessSettings {
		if rs.Settings[strings.ToLower(kv[0])] != kv[1] {
			unpinned = append(unpinned, kv[0])
		}
	}
	if rs.Super || rs.BypassRLS || rs.CreateRole || rs.CreateDB {
		c.Status, c.Detail, c.Fix = connector.CheckFail, "more than read-only: superuser-class attributes", initFix
		return c
	}
	if len(unpinned) > 0 {
		c.Status, c.Detail, c.Fix = connector.CheckFail, "settings not pinned: "+strings.Join(unpinned, ", "), initFix
		return c
	}
	if w := in.WitnessDSN; w != nil && w.Configured && w.User != "" && w.User != RoleWitness {
		c.Status, c.Detail = connector.CheckWarn, "witness_dsn connects as "+w.User+", a provider-wide role that can read and change more than it needs"
		c.Fix = "Use deedglass_witness where the provider allows a REPLICATION role (Supabase on PostgreSQL 17+; Neon only through its Console)."
		return c
	}
	if contains(rs.MemberOf, "neon_superuser") {
		c.Status, c.Detail = connector.CheckWarn, "replication through neon_superuser, which can also read and change data"
		c.Fix = "Neon grants REPLICATION only to roles made in its Console, through neon_superuser. Deedglass reads with it; it never writes."
		return c
	}
	if w := in.WitnessDSN; w != nil && w.Configured && !w.Connected && w.Err != "" {
		c.Status, c.Detail, c.Fix = connector.CheckFail, "witness_dsn can't connect: "+w.Err, "Store witness_dsn again: deedglass connector postgres init --connector "+in.Connector+" --rotate-passwords --apply"
		return c
	}
	how := "replication"
	if rds && !rs.Replication {
		how = "rds_replication"
	}
	c.Status, c.Detail = connector.CheckPass, how+" · pinned settings · read-only"
	if w := in.WitnessDSN; w == nil || !w.Configured {
		c.Detail += " · witness_dsn not stored yet"
	}
	return c
}

func watchedTables(in CheckInput, initFix string) []connector.Check {
	st, d := in.State, in.Desired
	if !in.Witness {
		return []connector.Check{
			{ID: "witness.watched_tables", Title: "watched tables", Status: connector.CheckSkip, Detail: "the witness is off in this connector's config"},
			{ID: "writable_watched", Title: "writable watched", Status: connector.CheckSkip, Detail: "the witness is off in this connector's config"},
		}
	}
	w := connector.Check{ID: "witness.watched_tables", Title: "watched tables", DocURL: doc("watched-tables")}
	var fails, warns []string
	virtual := false
	pk := 0
	for _, t := range d.WatchedTables {
		ts, ok := st.Tables[t]
		switch {
		case !ok:
			fails = append(fails, t+" missing")
			continue
		case ts.Persistence == "u":
			fails = append(fails, t+" is UNLOGGED (its changes never reach the log)")
		case ts.ReplIdent != "f":
			fails = append(fails, t+" replica identity isn't full")
		}
		if len(ts.VirtualGenerated) > 0 {
			virtual = true
			fails = append(fails, t+" has virtual generated columns ("+strings.Join(ts.VirtualGenerated, ", ")+"); PG 18 refuses updates and deletes on a watched table that has one")
		}
		if ts.HasPK {
			pk++
		} else {
			warns = append(warns, t+" has no primary key")
		}
		if len(ts.Triggers) > 0 {
			warns = append(warns, t+" has triggers ("+strings.Join(ts.Triggers, ", ")+"); writes they make to unwatched tables are invisible to L2")
		}
		if ts.Kind == "p" {
			warns = append(warns, t+" is partitioned")
		}
	}
	n := len(d.WatchedTables)
	switch {
	case len(fails) > 0:
		w.Status, w.Detail, w.Fix = connector.CheckFail, strings.Join(fails, " · "), initFix
		if virtual {
			w.Fix = "Make each virtual generated column STORED (drop it and add it back with GENERATED ALWAYS AS (…) STORED), or remove the table from witness.watched_tables. Then run init again."
		}
	case len(warns) > 0:
		w.Status, w.Detail = connector.CheckWarn, strings.Join(warns, " · ")
		w.Fix = "Rows without a primary key can't be matched one to one; triggers that write elsewhere make actions UNVERIFIABLE, never MISMATCH."
	default:
		w.Status, w.Detail = connector.CheckPass, fmt.Sprintf("%d of %d · primary keys · replica identity full", n, n)
	}
	ww := connector.Check{ID: "writable_watched", Title: "writable watched", DocURL: doc("watched-tables")}
	var unwatched []string
	for _, t := range d.WritableTables {
		if !contains(d.WatchedTables, t) {
			unwatched = append(unwatched, t)
		}
	}
	if len(unwatched) > 0 {
		ww.Status, ww.Detail = connector.CheckFail, "writable but not watched: "+strings.Join(unwatched, ", ")
		ww.Fix = "Add them to witness.watched_tables, so every write an agent can make is witnessed."
	} else {
		k := len(d.WritableTables)
		ww.Status, ww.Detail = connector.CheckPass, fmt.Sprintf("%d writable %s, all watched", k, plural(k, "table", "tables"))
	}
	return []connector.Check{w, ww}
}

func publication(in CheckInput, initFix string) connector.Check {
	st := in.State
	c := connector.Check{ID: "publication", Title: "publication", DocURL: doc("publication")}
	p := st.Publication
	want := sortedUnique(PublicationTables(in.Desired))
	have := sortedUnique(p.Tables)
	var missing, extra []string
	for _, t := range want {
		if !contains(have, t) {
			missing = append(missing, t)
		}
	}
	for _, t := range have {
		if !contains(want, t) {
			extra = append(extra, t)
		}
	}
	switch {
	case !p.Exists:
		c.Status, c.Detail, c.Fix = connector.CheckFail, "deedglass_pub is missing", initFix
	case p.AllTables:
		c.Status, c.Detail, c.Fix = connector.CheckFail, "deedglass_pub is FOR ALL TABLES", initFix
	case len(p.RowFilters) > 0 || len(p.ColLists) > 0:
		c.Status, c.Detail, c.Fix = connector.CheckFail, "row filters or column lists on "+strings.Join(sortedUnique(append(append([]string{}, p.RowFilters...), p.ColLists...)), ", "), initFix
	case len(missing) > 0:
		c.Status, c.Detail, c.Fix = connector.CheckFail, "missing "+strings.Join(missing, ", "), initFix
	case strings.Join(p.Publish, ", ") != "insert, update, delete, truncate":
		c.Status, c.Detail, c.Fix = connector.CheckFail, "publishes "+strings.Join(p.Publish, ", "), initFix
	case st.VersionNum >= 180000 && p.GenCols != GenColsStored:
		c.Status, c.Detail, c.Fix = connector.CheckFail, "doesn't publish stored generated columns, so PG 18 refuses updates and deletes on watched tables that have one", initFix
	case len(extra) > 0 || len(p.Schemas) > 0:
		c.Status, c.Detail, c.Fix = connector.CheckWarn, "extra tables: "+strings.Join(append(extra, p.Schemas...), ", "), initFix
	default:
		c.Status, c.Detail = connector.CheckPass, fmt.Sprintf("deedglass_pub · %d tables · insert, update, delete, truncate", len(want))
	}
	return c
}

func slot(in CheckInput) connector.Check {
	c := connector.Check{ID: "slot", Title: "slot", DocURL: doc("slot")}
	if !in.Witness {
		c.Status, c.Detail = connector.CheckSkip, "the witness is off in this connector's config"
		return c
	}
	if in.ConnectorID == "" {
		c.Status, c.Detail = connector.CheckSkip, "not checked · no connector id in local mode (pass --connector)"
		return c
	}
	name := SlotName(in.ConnectorID)
	for _, s := range in.State.Slots {
		if s.Name != name {
			continue
		}
		switch {
		case s.WALStatus == "lost" || s.Invalidation != "":
			c.Status, c.Detail = connector.CheckFail, name+" · "+firstNonEmpty(s.Invalidation, "lost")
			c.Fix = "The slot was invalidated, so changes were lost to the witness. Drop it with deedglass decoder drop; the witness creates a new one and reports the gap."
		case !s.Active:
			c.Status, c.Detail = connector.CheckWarn, name+" · inactive · WAL is kept until the witness reconnects"
		case s.WALStatus == "extended":
			c.Status, c.Detail = connector.CheckWarn, name+" · extended · the witness is behind"
		default:
			c.Status, c.Detail = connector.CheckPass, name+" · active · "+s.WALStatus
		}
		return c
	}
	c.Status, c.Detail = connector.CheckSkip, "not created yet · the witness creates "+name+" on first start"
	return c
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func clipList(s []string, n int) string {
	if len(s) <= n {
		return strings.Join(s, ", ")
	}
	return strings.Join(s[:n], ", ") + fmt.Sprintf(" and %d more", len(s)-n)
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GB", n>>30)
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}

func durationSeconds(v, unit string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return -1
	}
	switch unit {
	case "min":
		return n * 60
	case "h":
		return n * 3600
	case "ms":
		return n / 1000
	case "d":
		return n * 86400
	}
	return n
}

func durationText(s int64) string {
	switch {
	case s >= 86400 && s%86400 == 0:
		return fmt.Sprintf("%d %s", s/86400, plural(int(s/86400), "day", "days"))
	case s >= 3600 && s%3600 == 0:
		return fmt.Sprintf("%d %s", s/3600, plural(int(s/3600), "hour", "hours"))
	case s >= 60 && s%60 == 0:
		return fmt.Sprintf("%d min", s/60)
	}
	return fmt.Sprintf("%d s", s)
}
