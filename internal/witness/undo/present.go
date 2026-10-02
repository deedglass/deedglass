package undo

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Every string a person reads about an undo (S23 §5): the checks, the plan summary, the notes, the
// CLI lines, the approval context and the Console strings S25 renders. Vocabulary: undo, put back,
// restore, plan, preview, check, witness, deed. Never "rollback" for a user-facing undo (a rollback
// is what Postgres does to a failed transaction), never "revert".

// Finding is one reason a check doesn't pass: its check id, the sentence, and a fix when there is
// one (a sentence, or SQL to copy).
type Finding struct {
	Check  string
	Text   string
	Fix    string
	FixSQL string
	Warn   bool // a warning, not a failure (allow_triggers, row-level security)
}

// Check ids, in the order the preview lists them (S23 §3.3).
const (
	CheckEvidence        = "evidence"
	CheckConnector       = "connector"
	CheckAge             = "age"
	CheckNotUndone       = "not_undone"
	CheckRowLimit        = "row_limit"
	CheckReplicaIdentity = "replica_identity"
	CheckPrimaryKey      = "primary_key"
	CheckColumns         = "columns"
	CheckTriggers        = "triggers"
	CheckCascades        = "cascades"
	CheckUnchanged       = "unchanged"
	CheckPrivileges      = "privileges"
)

// CheckOrder is every check id in order.
var CheckOrder = []string{CheckEvidence, CheckConnector, CheckAge, CheckNotUndone, CheckRowLimit, CheckReplicaIdentity,
	CheckPrimaryKey, CheckColumns, CheckTriggers, CheckCascades, CheckUnchanged, CheckPrivileges}

// CheckTitles are the checks' titles (API, Console).
var CheckTitles = map[string]string{
	CheckEvidence:        "L2 evidence",
	CheckConnector:       "Connector",
	CheckAge:             "Age",
	CheckNotUndone:       "Not undone",
	CheckRowLimit:        "Row limit",
	CheckReplicaIdentity: "Replica identity",
	CheckPrimaryKey:      "Primary key",
	CheckColumns:         "Columns",
	CheckTriggers:        "Triggers",
	CheckCascades:        "Cascades",
	CheckUnchanged:       "Rows unchanged since",
	CheckPrivileges:      "Privileges",
}

// cliLabels are the checks' labels in the CLI's list.
var cliLabels = map[string]string{
	CheckEvidence:        "evidence",
	CheckConnector:       "connector",
	CheckAge:             "age",
	CheckNotUndone:       "not undone",
	CheckRowLimit:        "row limit",
	CheckReplicaIdentity: "replica identity",
	CheckPrimaryKey:      "primary key",
	CheckColumns:         "columns",
	CheckTriggers:        "triggers",
	CheckCascades:        "cascades",
	CheckUnchanged:       "unchanged",
	CheckPrivileges:      "privileges",
}

// DocURL is where the checks are explained.
const DocURL = "https://docs.deedglass.com/undo#the-twelve-checks"

// --- evidence problems ---

var (
	// ProblemNoFrame: no transaction was bound to the action.
	ProblemNoFrame = Finding{Check: CheckEvidence,
		Text: "Undo needs L2 evidence from Postgres. Deedglass has no committed transaction for this action, so it doesn't know every row it changed."}
	problemNotL2 = "Undo needs L2 evidence from Postgres. This action was witnessed at %s, so Deedglass doesn't know every row it changed."
)

// ProblemNotWitnessedAt is the evidence failure for an action witnessed below L2 (or not at all).
func ProblemNotWitnessedAt(level string) Finding {
	if level == "" {
		return ProblemNoFrame
	}
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf(problemNotL2, level)}
}

// ProblemNotBound: the only frames naming the action aren't a clean binding.
func ProblemNotBound(class string) Finding {
	why := map[string]string{
		"forged_marker":   "a marker without a ledger row",
		"marker_count":    "more than one marker or ledger row",
		"marker_mismatch": "a marker and a ledger row that disagree",
		"publication_gap": "a change log with a gap",
	}[class]
	if why == "" {
		why = "a transaction Deedglass can't attribute"
	}
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("Postgres's change log shows %s for this action, so it can't be undone from that evidence.", why)}
}

// ProblemManyFrames: more than one transaction names the action.
func ProblemManyFrames(n int) Finding {
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("%d transactions name this action, so Deedglass can't tell which one to undo.", n)}
}

// ProblemTruncate: the transaction truncated a table.
func ProblemTruncate(table string) Finding {
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("The transaction truncated %s. Undo can't put back a TRUNCATE; restore from a backup.", table)}
}

// ProblemRelationChanged: DDL in the original transaction.
func ProblemRelationChanged(table string) Finding {
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("The transaction changed the definition of %s. Undo can't put back schema changes.", table)}
}

// ProblemImagesTruncated: the decoder kept only the first images.
func ProblemImagesTruncated(total, kept int) Finding {
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("The transaction changed %s rows and Deedglass kept the images of %s, so it can't put every row back.",
		thousands(total), thousands(kept))}
}

// ProblemNoRelation: a change names a table the frame doesn't describe.
func ProblemNoRelation(table string) Finding {
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("The evidence has no description of %s, so its rows can't be planned.", table)}
}

// ProblemOldImage: the table didn't log full old rows when the action ran.
func ProblemOldImage(table, identity string) Finding {
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("%s didn't log full old rows when the action ran (replica identity %s), so the values before it are unknown.",
		table, identityWord(identity)), FixSQL: fmt.Sprintf("ALTER TABLE %s REPLICA IDENTITY FULL;", fixTable(table))}
}

// ProblemToast: an unchanged TOAST value with no old value to take it from.
func ProblemToast(table string) Finding {
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("A row in %s has a large value Postgres didn't resend and the old image doesn't hold, so it can't be restored exactly.", table)}
}

// ProblemSensitive: a value the plan must write was stored only as a digest.
func ProblemSensitive(table, column string) Finding {
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("%s.%s is marked sensitive, so Deedglass kept only its digest and can't put the old value back.", table, column)}
}

// ProblemKeyUnreadable: a row's key isn't plain text in the evidence.
func ProblemKeyUnreadable(table string) Finding {
	return Finding{Check: CheckPrimaryKey, Text: fmt.Sprintf("A row in %s has a primary key Deedglass can't read from the evidence (NULL, sensitive or not text), so it can't point at that row safely.", table)}
}

// ProblemInconsistent: the change sequence contradicts itself.
func ProblemInconsistent(table string) Finding {
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("The changes to %s don't add up row by row, so Deedglass won't plan from them.", table)}
}

// ProblemCycle: rows swapped keys (or depend on each other in a circle), so no order of single-row
// statements puts them back.
func ProblemCycle(tables []string) Finding {
	return Finding{Check: CheckEvidence, Text: fmt.Sprintf("Rows in %s swapped keys or depend on each other in a circle, so undo can't put them back one row at a time. Restore from a backup.",
		tablesText(tables))}
}

// ProblemNoPrimaryKey: the table had no primary key.
func ProblemNoPrimaryKey(table string) Finding {
	return Finding{Check: CheckPrimaryKey, Text: fmt.Sprintf("%s has no primary key, so Deedglass can't point at one row safely.", table)}
}

// fixTable quotes "schema.table" for fix SQL.
func fixTable(t string) string {
	schema, name, ok := strings.Cut(t, ".")
	if !ok {
		return quoteIdent(t)
	}
	return fixName(schema, name)
}

func identityWord(id string) string {
	switch id {
	case "f":
		return "full"
	case "n":
		return "nothing"
	case "i":
		return "index"
	}
	return "default"
}

// --- the other checks' sentences ---

// Connector check.
const (
	MsgConnectorDeleted   = "The %q connector was deleted. Undo runs through the connector that made the change."
	MsgConnectorInactive  = "The %q connector is %s. Undo runs through the connector that made the change; turn it back on first."
	MsgConnectorNoCreds   = "The %q connector has no credentials. Set them in Console › Connectors, then preview again."
	MsgConnectorNotPG     = "Undo works for Postgres writes Deedglass witnessed at L2. This action ran through %s."
	MsgConnectorProxy     = "This action ran at the edge (proxy mode), so no Deedglass marker sits in its transaction. Undo works for actions the Gate ran."
	MsgConnectorNoWitness = "Witness is off for the %q connector, so the undo itself couldn't be witnessed. Turn on witness in the connector's config."
	MsgConnectorOK        = "%s (native Postgres) · active"
)

// Age check.
const (
	MsgAgeTooOld  = "This action is %s old; undo works for %s while the row images are kept."
	MsgAgePruned  = "The row images of this action were pruned on %s. Restore from a backup."
	MsgNotSealed  = "This action isn't sealed yet. Wait for its verdict, then preview again."
	MsgNotWritten = "This action didn't change anything in Postgres, so there is nothing to undo."
)

// Not-undone check.
const (
	MsgUndoneBy    = "%s already undid this action at %s."
	MsgUndoPending = "An undo of this action is already waiting: %s."
	MsgUndoRunning = "An undo of this action is already running: %s."
)

// Row limit check.
const (
	MsgRowLimit   = "This undo touches %s rows; the limit is %s. Restore from a backup, or raise undo.max_rows."
	MsgRowLimitOK = "%s of %s"
	MsgNothing    = "The action's transaction left every row as it found it (rows it created it also removed, rows it updated kept their values), so there is nothing to put back."
)

// Fix sentences.
const (
	FixFailing     = "Fix the failing check, then preview again."
	FixTriggers    = "Undo would fire this trigger again."
	FixAllowTrig   = "An admin can pass allow_triggers with a reason; the L2 witness then shows what the trigger did."
	FixReplica     = "Set REPLICA IDENTITY FULL so the undo itself can be witnessed."
	FixPrivileges  = "Grant the privilege, or add the table to writable_tables and run deedglass connector postgres init again."
	FixRowsChanged = "Preview the undo again; the plan follows the rows as they are now."
)

// Notes.
const (
	NoteSequences = "Sequences are not rewound."
	NoteSensitive = "Values in columns marked sensitive show as ••• and are compared by their digest."
	NoteNoop      = "Some rows were created and removed in the same transaction; they need no step."
	NoteGenerated = "Generated columns are recomputed by Postgres, never written."
	NoteSameValue = "Some rows were updated to the values they already had; they need no step."
)

// --- the summary and the plan sentence ---

// period ends a sentence, unless it already ends with an ellipsis (a short id).
func period(s string) string {
	if strings.HasSuffix(s, "…") || strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

// Summary is the preview's one sentence (API summary, CLI second line).
func Summary(p *Plan, action string) string {
	if p == nil || p.Rows == 0 {
		return period("Nothing to put back for " + ShortID(action))
	}
	return period(fmt.Sprintf("Put back %s in %s to the values Postgres recorded before %s", rowsText(p.Rows), tablesText(p.TableNames()), ShortID(action)))
}

// SummaryOf is the preview's sentence from its numbers, with subject naming the action ("this
// action" in the CLI, the short id in the API).
func SummaryOf(rows int, tables []string, subject string) string {
	if rows == 0 {
		return period("Nothing to put back for " + subject)
	}
	return period(fmt.Sprintf("Put back %s in %s to the values Postgres recorded before %s", rowsText(rows), tablesText(tables), subject))
}

// Headline is "301 rows in public.orders · 301 updates".
func Headline(p *Plan) string {
	if p == nil {
		return ""
	}
	parts := []string{rowsText(p.Rows) + " in " + tablesText(p.TableNames())}
	for _, op := range []Op{OpUpdate, OpInsert, OpDelete} {
		if n := p.Counts.Of(op); n > 0 {
			parts = append(parts, thousands(n)+" "+opPlural(op, n))
		}
	}
	return strings.Join(parts, " · ")
}

// ApprovalSummary is the approval's summary line: "console (for Arjun) wants to put back 301 rows
// in public.orders, undoing act_01j9zw6k…".
func ApprovalSummary(agent, person string, p *Plan, undoes string) string {
	who := agent
	if person != "" {
		who += " (for " + person + ")"
	}
	if p == nil {
		return period(fmt.Sprintf("%s wants to undo %s", who, ShortID(undoes)))
	}
	return period(fmt.Sprintf("%s wants to put back %s in %s, undoing %s", who, rowsText(p.Rows), tablesText(p.TableNames()), ShortID(undoes)))
}

// ResultText is the executed undo's line: "301 rows restored in public.orders".
func ResultText(rows int, tables []string) string {
	return fmt.Sprintf("%s restored in %s", rowsText(rows), tablesText(tables))
}

// WitnessText is the undo's L2 VERIFIED detail.
func WitnessText(rows int, tables []string) string {
	return fmt.Sprintf("Restored %s in %s exactly as planned.", rowsText(rows), tablesText(tables))
}

func opPlural(op Op, n int) string {
	w := map[Op]string{OpUpdate: "update", OpInsert: "insert", OpDelete: "delete"}[op]
	if n == 1 {
		return w
	}
	return w + "s"
}

func rowsText(n int) string {
	if n == 1 {
		return "1 row"
	}
	return thousands(n) + " rows"
}

func tablesText(ts []string) string {
	switch len(ts) {
	case 0:
		return "no table"
	case 1:
		return ts[0]
	case 2:
		return ts[0] + " and " + ts[1]
	}
	return strings.Join(ts[:len(ts)-1], ", ") + " and " + ts[len(ts)-1]
}

// ShortID is an id as the brand shows it in sentences: the prefix and 8 characters, then "…".
func ShortID(id string) string {
	p, rest, ok := strings.Cut(id, "_")
	if !ok || len(rest) <= 8 {
		return id
	}
	return p + "_" + rest[:8] + "…"
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Thousands writes n with thousands separators (1,000).
func Thousands(n int) string { return thousands(n) }

// DurationWords is a duration as a sentence says it: "6 min", "9 days", "7 days".
func DurationWords(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	}
	days := int(d.Hours() / 24)
	return fmt.Sprintf("%d days", days)
}

// --- Console strings for S25 (S23 §5.2). S25 renders them; nothing else spells them. ---
const (
	ConsolePanelTitle      = "Undo"
	ConsoleEligible        = "Put these rows back the way Postgres recorded them before this action."
	ConsolePreviewButton   = "Preview undo"
	ConsoleSheetTitle      = "Undo %s?" // the short action id
	ConsoleColNow          = "Now"
	ConsoleColAfter        = "After undo"
	ConsoleReasonLabel     = "Why are you undoing this? The approver and the Ledger read it."
	ConsoleRequestButton   = "Request undo"
	ConsoleUnavailable     = "Undo isn't available"
	ConsoleCopyFix         = "Copy fix"
	ConsoleUndoneChip      = "Undone · %s · %s" // the undo action's short id, its time
	ConsoleUndoesLine      = "This undoes %s."  // the original's short id
	ConsoleNotEligible     = "Undo works for Postgres writes Deedglass witnessed at L2."
	ConsoleAllowTriggers   = "Fire triggers again (admins only)"
	ConsoleAllowTriggersOn = "Triggers on these tables will fire again. The witness shows what they did."
)

// --- API error messages (internal/httpx/errors.go holds the catalog defaults) ---

// MsgUnavailable is undo_unavailable's message.
func MsgUnavailable(action string, failed []string) string {
	n := len(failed)
	word := "checks"
	if n == 1 {
		word = "check"
	}
	return fmt.Sprintf("Undo isn't available for %s: %d %s failed (%s). Run deedglass undo preview %s for the fix.",
		ShortID(action), n, word, strings.Join(failed, ", "), action)
}

// MsgStale is undo_plan_stale's message (also the executor's failure message).
const MsgStale = "The rows changed since you previewed this undo. Preview it again and use the new plan."

// MsgAlreadyUndone is already_undone's message for an applied undo.
func MsgAlreadyUndone(action, by, at string) string {
	return period(fmt.Sprintf("%s was already undone by %s at %s", ShortID(action), ShortID(by), at))
}

// MsgAlreadyWaiting is already_undone's message while another undo is pending or running.
func MsgAlreadyWaiting(action, by string) string {
	return period(fmt.Sprintf("An undo of %s is already waiting for approval: %s", ShortID(action), ShortID(by)))
}

// MsgReason is validation_failed's message for a missing reason.
const MsgReason = "Say why you're undoing this. The approver and the Ledger read it."

// MsgReasonLong is validation_failed's message for a reason over 280 characters.
const MsgReasonLong = "Keep the reason to 280 characters. The approver reads it on a phone."

// MsgAgentKey is insufficient_scope's message for an agent key.
const MsgAgentKey = "Agent keys can't undo actions. A person starts an undo from the Console or with an admin key."

// MsgAllowTriggersRole is insufficient_role's message for allow_triggers below admin.
const MsgAllowTriggersRole = "Only an admin can let an undo fire triggers again."

// Conflict messages (undo_conflict, the action's execution.error_message).
const (
	MsgConflictRow    = "Row %s %s changed after the plan was made (%s). Nothing was changed. Preview the undo again."
	MsgConflictExists = "Row %s %s exists again since the plan was made. Nothing was changed. Preview the undo again."
	MsgConflictGone   = "Row %s %s is gone since the plan was made. Nothing was changed. Preview the undo again."
	MsgConflictLock   = "%s %s is locked by another transaction. Nothing was changed. Try again in a moment."
	MsgConflictCount  = "A step of the undo changed %d rows in %s instead of exactly 1, so Postgres rolled it back. Nothing was changed. Preview the undo again."
	MsgConflictFailed = "Postgres refused a step of the undo (%s), so it rolled the whole undo back. Nothing was changed."
	MsgConflictPlan   = "The catalog changed since the plan was made (%s). Nothing was changed. Preview the undo again."
)

// KeyText writes a key as "id=42" (columns in the given order).
func KeyText(k Key, cols []string) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		parts = append(parts, c+"="+k[c])
	}
	return strings.Join(parts, " ")
}

// ValueText is a value as a person reads it: 'text', NULL, •••.
func ValueText(v Value) string {
	switch {
	case v.D != "":
		return Hidden
	case v.N:
		return "NULL"
	case v.X != "":
		return `'\x` + v.X + `'`
	case v.T != nil:
		return "'" + *v.T + "'"
	}
	return "?"
}

// Hidden is how a sensitive value is shown everywhere.
const Hidden = "•••"
