// Package setup prepares a customer database for the Postgres connector (S17, contract §4): the
// embedded static SQL (schema deedglass, the ledger, deedglass.begin_action), a catalog-driven
// planner that diffs the database against a connector's config and emits ordered, reviewable SQL,
// the readiness checks behind `deedglass connector postgres check`, provider detection, and
// client-side SCRAM-SHA-256 verifiers (so a generated password never reaches Postgres or its logs).
//
// Invariants:
//   - Every catalog read is parameterized. The planner is the one place SQL text is built, from
//     identifiers that passed ValidIdent and exist in the catalogs, quoted with pgx.Identifier.
//   - A plan never contains a password or a verifier; Apply adds the ALTER ROLE … PASSWORD
//     statements for the roles it creates (or rotates) inside the same transaction.
//   - The body of deedglass.begin_action is pinned: BeginActionBodySHA256 is recomputed from the
//     embedded file by a test, and check fails when the live pg_proc.prosrc differs.
//   - The slot is never created here (S20's witness creates it); the publication always is, first.
package setup

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
)

//go:embed sql/20_schema.sql
var schemaSQL string

// BeginActionBodySHA256 is "sha256:" + hex(sha256(prosrc)) of deedglass.begin_action as
// sql/20_schema.sql defines it: the text between the $fn$ delimiters, which Postgres stores as
// pg_proc.prosrc. TestBeginActionHash recomputes it from the file.
const BeginActionBodySHA256 = "sha256:2f3f33adfdbfc3428b8321b499707b07c3c77cff2d01a6f33adb8d7ad6067bd4"

// Fixed names in the customer database (contract §4).
const (
	Schema           = "deedglass"
	RoleOwner        = "deedglass_owner"
	RoleAgent        = "deedglass_agent"
	RoleWitness      = "deedglass_witness"
	Publication      = "deedglass_pub"
	Ledger           = "deedglass.ledger"
	BeginActionSig   = "deedglass.begin_action(text,text,text,text,text)"
	MarkerPrefix     = "deedglass"
	SlotPrefix       = "deedglass_"
	ControlPlaneMark = "deedglass.schema_migrations"
)

// SlotName is the witness's replication slot for a connector: "deedglass_" plus the connector id
// without its con_ prefix, lowercased (contract §4; S20 creates it).
func SlotName(connectorID string) string {
	return SlotPrefix + strings.ToLower(strings.TrimPrefix(connectorID, "con_"))
}

// SchemaSQL is the embedded file, verbatim.
func SchemaSQL() string { return schemaSQL }

// Section returns one "-- @section name" block of the static SQL (schema, ledger, begin_action),
// without its marker line and without trailing blank lines.
func Section(name string) string {
	marker := "-- @section " + name + "\n"
	i := strings.Index(schemaSQL, marker)
	if i < 0 {
		panic("setup: no section " + name)
	}
	rest := schemaSQL[i+len(marker):]
	if j := strings.Index(rest, "\n-- @section "); j >= 0 {
		rest = rest[:j+1]
	}
	return strings.TrimRight(rest, "\n") + "\n"
}

// BeginActionBody is the function body as pg_proc.prosrc stores it.
func BeginActionBody() string {
	const open, closing = "AS $fn$", "$fn$;"
	i := strings.Index(schemaSQL, open)
	if i < 0 {
		panic("setup: no AS $fn$ in 20_schema.sql")
	}
	body := schemaSQL[i+len(open):]
	j := strings.Index(body, closing)
	if j < 0 {
		panic("setup: no closing $fn$; in 20_schema.sql")
	}
	return body[:j]
}

// SHA256 is "sha256:" + lowercase hex of sha256(s).
func SHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// ShortHash is "sha256:2f3f…7bd4" for display (brand §12: first 4 and last 4 of the hex).
func ShortHash(h string) string {
	hexPart := strings.TrimPrefix(h, "sha256:")
	if len(hexPart) < 12 {
		return h
	}
	return fmt.Sprintf("sha256:%s…%s", hexPart[:4], hexPart[len(hexPart)-4:])
}
