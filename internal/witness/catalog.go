package witness

import "sort"

//go:generate go test -run TestReasonsDoc -update .

// Reason codes registered by S19. S20 and S21 add theirs to the same table; the page
// docs/witness/REASONS.md is generated from it (go generate ./internal/witness).
const (
	CodeToolReportedSuccess   Code = "tool_reported_success"
	CodeEdgeReportedSuccess   Code = "edge_reported_success"
	CodeResponseSchemaValid   Code = "response_schema_valid"
	CodeResponseSchemaInvalid Code = "response_schema_invalid"
	CodeSchemaSkipped         Code = "schema_skipped"
	CodeOutcomeUnknown        Code = "outcome_unknown"
	CodeReadbackMatched       Code = "readback_matched"
	CodePostconditionHeld     Code = "postcondition_held"
	CodePostconditionFailed   Code = "postcondition_failed"
	CodeReadbackAbsent        Code = "readback_absent"
	CodeReadbackNotFound      Code = "readback_not_found"
	CodeReadbackFailed        Code = "readback_failed"
	CodeReadbackTimeout       Code = "readback_timeout"
	CodePostconditionError    Code = "postcondition_error"
	CodeRowWritten            Code = "row_written"
	CodeValueMismatch         Code = "value_mismatch"
	CodeRowSuperseded         Code = "row_superseded"
	CodeRowMissing            Code = "row_missing"
	CodeReadbackSkipped       Code = "readback_skipped"
	CodeWitnessGaveUp         Code = "witness_gave_up"
	CodeNoChecks              Code = "no_checks"
	CodeLevelBelowRequired    Code = "level_below_required"
	CodeInternalError         Code = "internal_error"
)

// L2 reason codes (S21): what Postgres's change log showed, compared with what the statement said.
const (
	CodeFootprintMatched     Code = "footprint_matched"
	CodeRowCountExceeded     Code = "row_count_exceeded"
	CodeMissingChange        Code = "missing_change"
	CodeUnexpectedTable      Code = "unexpected_table"
	CodeUnexpectedOperation  Code = "unexpected_operation"
	CodeUnexpectedKey        Code = "unexpected_key"
	CodeUnexpectedColumn     Code = "unexpected_column"
	CodeRowLimitExceeded     Code = "row_limit_exceeded"
	CodeMarkerCount          Code = "marker_count"
	CodeMarkerMismatch       Code = "marker_mismatch"
	CodeMultipleTransactions Code = "multiple_transactions"
	CodeLedgerMismatch       Code = "ledger_mismatch"
	CodeOldImageMissing      Code = "old_image_missing"
	CodeKeysUnknown          Code = "keys_unknown"
	CodeRelationChanged      Code = "relation_changed"
	CodeFootprintUnknown     Code = "footprint_unknown"
	CodeUnwatchedTable       Code = "unwatched_table"
	CodeL2Timeout            Code = "l2_timeout"
	CodeSlotInvalidated      Code = "slot_invalidated"
	CodeSlotDropped          Code = "slot_dropped"
	CodeDecoderHalted        Code = "decoder_halted"
	CodeNoEvidence           Code = "no_evidence"
	CodePublicationGap       Code = "publication_gap"
	CodeTriggersPresent      Code = "triggers_present"
	CodeTriggersUnwatched    Code = "triggers_unwatched"
)

// L1 for FHIR servers (HC-P02, ADR-105 §6): the traps a FHIR write's read-back names.
const (
	CodeFHIRConditionalMatched Code = "fhir_conditional_matched"
	CodeFHIRFieldChanged       Code = "fhir_field_changed"
	CodeFHIRNoNewVersion       Code = "fhir_no_new_version"
	CodeFHIRHistoryNoMatch     Code = "fhir_history_no_match"
)

// L2 for FHIR servers on Postgres (HC-P05, ADR-108): the server's own transaction, bound by the
// version it reported to the action.
const (
	CodeVersionMatched           Code = "version_matched"
	CodeVersionFieldChanged      Code = "version_field_changed"
	CodeFHIRExtraVersions        Code = "fhir_extra_versions"
	CodeOutOfBand                Code = "out_of_band"
	CodeRequestIDReused          Code = "request_id_reused"
	CodeVersionTagMismatch       Code = "version_tag_mismatch"
	CodeLargeObjectNotReplicated Code = "large_object_not_replicated"
	CodeFHIRSchemaUnknown        Code = "fhir_schema_unknown"
)

// L2 for an undo (S23): the exact footprint of a plan, row by row.
const (
	CodeUndoRestored Code = "undo_restored"
)

// Entry is one catalog row: the class every reason with this code has, the levels it is used at,
// its sentence, what a person does next, and sample values for the tests and the generated page.
type Entry struct {
	Code     Code
	Class    Class
	Levels   string // "L0", "L1", "L1, L2", "any", "none" (the claim itself)
	Template string // {name} placeholders; rendered by R
	Next     string // what to do next, for docs/witness/REASONS.md
	Sample   map[string]any
}

var entries = []Entry{
	{CodeToolReportedSuccess, Pass, "L0", `The tool reported success.`,
		"Nothing. Add a read-back to reach L1.", nil},
	{CodeEdgeReportedSuccess, Pass, "L0", `The edge reported success. Deedglass did not run this call itself.`,
		"Nothing. Proxy-mode calls stay at L0 unless the system of record is witnessed (L2).", nil},
	{CodeResponseSchemaValid, Pass, "L0", `The response matched the tool's output schema.`,
		"Nothing.", nil},
	{CodeResponseSchemaInvalid, Gap, "L0", `The response didn't match the tool's output schema at {path}, so Deedglass can't read it with confidence.`,
		"Check the tool's output schema against what the service returns now.", map[string]any{"path": "body.status"}},
	{CodeSchemaSkipped, Note, "L0", `The response was too large to check against the output schema.`,
		"Nothing. Responses over 256 KiB are kept as a digest only.", nil},
	{CodeOutcomeUnknown, Gap, "L0", `Deedglass couldn't tell whether the tool ran ({cause}). It may have.`,
		"Check the target system for the change, and the connector's timeout.", map[string]any{"cause": "timeout"}},
	{CodeReadbackMatched, Pass, "L1", `Read back {readback}: {fields}.`,
		"Nothing.", map[string]any{"readback": "orders.get", "fields": `status is "shipped"`}},
	{CodePostconditionHeld, Pass, "L1", `Postcondition held: {expr}.`,
		"Nothing.", map[string]any{"expr": "observation.body.status == args.status"}},
	{CodePostconditionFailed, Violation, "L1", `{tool} reported success, but {readback} says {path} is {observed}, not {expected}.`,
		"Read the diff, then check the target system: the write isn't there as the tool said.",
		map[string]any{"tool": "orders.update_status", "readback": "orders.get", "path": "status", "observed": "pending", "expected": "shipped"}},
	{CodeReadbackAbsent, Violation, "L1", `{tool} reported success, but {readback} found nothing at {target}.`,
		"Check the target system: the record the tool said it wrote isn't there.",
		map[string]any{"tool": "orders.update_status", "readback": "orders.get", "target": "/v1/orders/42"}},
	{CodeReadbackNotFound, Gap, "L1", `{readback} found nothing at {target}. It may not be visible yet.`,
		"If the target is eventually consistent, set readback.consistency to eventual.",
		map[string]any{"readback": "orders.get", "target": "/v1/orders/42"}},
	{CodeReadbackFailed, Gap, "L1", `The read-back {readback} failed ({error_code}), so the result couldn't be confirmed.`,
		"Check the connector (deedglass connector test) and the target's health.",
		map[string]any{"readback": "orders.get", "error_code": "upstream_error"}},
	{CodeReadbackTimeout, Gap, "L1", `The read-back {readback} took longer than {budget}.`,
		"Check the target's latency, or raise witness.readback_timeout.", map[string]any{"readback": "orders.get", "budget": "10 s"}},
	{CodePostconditionError, Gap, "L1", `The postcondition couldn't be evaluated: {error}.`,
		"Fix the postcondition in the connector config; guard optional fields with has().",
		map[string]any{"error": "no such key: status"}},
	{CodeRowWritten, Pass, "L1", `Postgres shows row {key} in {table} was last written by this action's transaction.`,
		"Nothing.", map[string]any{"key": "id = 42", "table": "public.orders"}},
	{CodeValueMismatch, Violation, "L1, L2", `Postgres has {column} = {observed} on row {key} in {table}. The statement set {expected}.`,
		"Look for a trigger, rule or default that rewrites the column.",
		map[string]any{"column": "status", "observed": "cancelled", "key": "id = 42", "table": "public.orders", "expected": "shipped"}},
	{CodeRowSuperseded, Gap, "L1", `Another transaction changed row {key} in {table} after this action, so a read-back can't confirm it.`,
		"Nothing to fix. L2 (the change stream) sees each transaction on its own.", map[string]any{"key": "id = 42", "table": "public.orders"}},
	{CodeRowMissing, Gap, "L1", `Row {key} in {table} isn't there any more, so a read-back can't confirm it.`,
		"Check whether another process deleted the row.", map[string]any{"key": "id = 42", "table": "public.orders"}},
	{CodeReadbackSkipped, Note, "L1", `No read-back: {why}.`,
		"Nothing. Add a read-back, or witness the connector at L2.", map[string]any{"why": "the statement isn't a single-row update by primary key"}},
	{CodeWitnessGaveUp, Gap, "any", `The witness stopped after {attempts} attempts: {last_error}.`,
		"Check the connector and the server log for the error.", map[string]any{"attempts": 8, "last_error": "the target is unreachable"}},
	{CodeNoChecks, Gap, "none", `No check could run for this action.`,
		"Check that the tool reports a result.", nil},
	{CodeLevelBelowRequired, Gap, "none", `The policy asks for {required}. The strongest clean evidence was {reached}.`,
		"Fix what kept the stronger level from running (the other reasons say what), or lower verify: in the policy.",
		map[string]any{"required": "L1", "reached": "L0"}},
	{CodeInternalError, Gap, "any", `The witness hit an internal error, so the action isn't marked verified.`,
		"Report it with the action id; it is a bug in Deedglass.", nil},

	// L2 (S21): Postgres's change log against the statement's footprint.
	{CodeFootprintMatched, Pass, "L2", `Postgres committed {observed} {rows} in {table}, as the statement said.`,
		"Nothing.", map[string]any{"observed": 1, "rows": "row", "table": "public.orders"}},
	{CodeUndoRestored, Pass, "L2", `Restored {observed} {rows} in {table} exactly as planned.`,
		"Nothing.", map[string]any{"observed": 301, "rows": "rows", "table": "public.orders"}},
	{CodeRowCountExceeded, Violation, "L2", `Postgres changed {observed} rows in {table}. The agent reported {expected}.`,
		"Read the diff: a trigger, rule or cascade changed rows the statement didn't name.",
		map[string]any{"observed": 301, "table": "public.orders", "expected": 1}},
	{CodeMissingChange, Violation, "L2", `The agent reported {expected} {rows} changed in {table}, but Postgres committed {observed}.`,
		"Check the target: the change the tool reported isn't in Postgres's change log.",
		map[string]any{"expected": 1, "rows": "row", "table": "public.orders", "observed": 0}},
	{CodeUnexpectedTable, Violation, "L2", `Postgres also changed {n} {rows} in {table}, which the statement doesn't touch.`,
		"Look for a trigger or rule on the statement's table that writes this one.",
		map[string]any{"n": 1, "rows": "row", "table": "public.audit"}},
	{CodeUnexpectedOperation, Violation, "L2", `Postgres {op_past} {n} {rows} in {table}. The statement only {expected_op}.`,
		"Look for a trigger or rule that deletes, inserts or truncates on this table.",
		map[string]any{"op_past": "deleted", "n": 1, "rows": "row", "table": "public.orders", "expected_op": "updates rows"}},
	{CodeUnexpectedKey, Violation, "L2", `{n} changed {rows} in {table} {are} outside the rows the statement named ({keys}).`,
		"Read the diff: the unexpected rows are listed first after the expected ones.",
		map[string]any{"n": 300, "rows": "rows", "table": "public.orders", "are": "are", "keys": "id = 42"}},
	{CodeUnexpectedColumn, Violation, "L2", `Postgres changed {column} on {n} {rows} in {table}. The statement set only {columns}.`,
		"If a trigger maintains the column on purpose, list it in the connector's witness.auto_columns.",
		map[string]any{"column": "total_cents", "n": 1, "rows": "row", "table": "public.orders", "columns": "status"}},
	{CodeRowLimitExceeded, Violation, "L2", `Postgres changed {observed} rows in {table}; this connector allows at most {max} per action.`,
		"Read the diff, then check why one action changed this many rows.",
		map[string]any{"observed": 301, "table": "public.orders", "max": 1}},
	{CodeMarkerCount, Violation, "L2", `This action's transaction in Postgres holds {ledger} ledger rows and {markers} markers, not one of each.`,
		"Treat the database as tampered with: something wrote Deedglass's ledger outside begin_action.",
		map[string]any{"ledger": 2, "markers": 1}},
	{CodeMarkerMismatch, Violation, "L2", `This action's transaction in Postgres doesn't match its own ledger row ({why}).`,
		"Treat the database as tampered with: the ledger row and the marker disagree.",
		map[string]any{"why": "the marker names another action"}},
	{CodeMultipleTransactions, Violation, "L2", `{n} transactions in Postgres claim this action. Only one can be real.`,
		"Treat the database as tampered with: a ledger row and marker for this action were written again.",
		map[string]any{"n": 2}},
	{CodeLedgerMismatch, Violation, "L2", `The ledger row in Postgres doesn't match what Deedglass authorized ({field}).`,
		"Treat the execution path as compromised: the statement that ran isn't the one the policy allowed.",
		map[string]any{"field": "sql_digest"}},
	{CodeOldImageMissing, Gap, "L2", `{table} doesn't keep full old rows (REPLICA IDENTITY {identity}), so Deedglass can't tell which columns changed.`,
		"Run ALTER TABLE … REPLICA IDENTITY FULL on the table (docs/POSTGRES_DEPLOYMENT.md has the cost note).",
		map[string]any{"table": "public.orders", "identity": "DEFAULT"}},
	{CodeKeysUnknown, Gap, "L2", `Deedglass couldn't match rows in {table} to the statement's keys ({why}).`,
		"Give the table a primary key, or raise decoder.max_images_per_tx for large statements.",
		map[string]any{"table": "public.orders", "why": "the table has no primary key"}},
	{CodeRelationChanged, Gap, "L2", `The structure of {table} changed in this transaction, so the comparison may be incomplete.`,
		"Nothing to fix. Run schema changes outside agent actions.", map[string]any{"table": "public.orders"}},
	{CodeFootprintUnknown, Gap, "L2", `The statement is too complex for Deedglass to predict which rows it touches ({why}), so L2 can't compare.`,
		"Use a single-table statement with a key in WHERE, or a controlled function that declares what it writes.",
		map[string]any{"why": "UPDATE … FROM"}},
	{CodeUnwatchedTable, Gap, "L2", `{table} isn't watched, so Postgres's change log doesn't show it.`,
		"Add the table to witness.watched_tables and run deedglass connector postgres init again.",
		map[string]any{"table": "public.invoices"}},
	{CodeL2Timeout, Gap, "L2", `Postgres's change log didn't show this action within {timeout}.`,
		"Check deedglass decoder status. If the change log shows it later, an amendment deed records the result.",
		map[string]any{"timeout": "10s"}},
	{CodeSlotInvalidated, Gap, "L2", `Postgres invalidated the replication slot ({why}), so this action's change log is gone.`,
		"Read deedglass decoder status, raise max_slot_wal_keep_size if WAL ran out, then drop and recreate the slot.",
		map[string]any{"why": "wal_removed"}},
	{CodeSlotDropped, Gap, "L2", `The replication slot was dropped while this action waited, so its change log can't be read.`,
		"Nothing to fix for this action. The next sync creates a new slot for later ones.", nil},
	{CodeDecoderHalted, Gap, "L2", `The decoder for this connector has stopped ({why}), so Postgres's change log wasn't read.`,
		"Run deedglass decoder status; it names the cause and the fix.", map[string]any{"why": "publication_missing"}},
	{CodeNoEvidence, Gap, "L2", `This action's ledger row is in Postgres, but its transaction never appeared in the change log.`,
		"Run deedglass decoder status: the slot may have been recreated after the action committed.", nil},
	{CodePublicationGap, Gap, "L2", `The publication changed while this action ran, so the change log may be missing its rows.`,
		"Nothing to fix for this action. Change deedglass_pub only while no agent writes.", nil},
	{CodeTriggersPresent, Note, "L2", `{table} has triggers ({names}). Their writes to watched tables are checked; writes elsewhere aren't visible.`,
		"Nothing. Set witness.strict_triggers to treat unwatched trigger writes as a gap.",
		map[string]any{"table": "public.orders", "names": "orders_ship_open_batch"}},
	{CodeFHIRConditionalMatched, Violation, "L1", `{tool} reported success, but the conditional create matched an existing resource ({observed}), so this action created nothing.`,
		"Read the existing resource. The agent's report of a new one is wrong; check what it did next with that id.",
		map[string]any{"tool": "fhir.create", "observed": "Observation/1001 version 1"}},
	{CodeFHIRFieldChanged, Violation, "L1", `{tool} reported success, but {readback} shows {path} as {observed}, not {expected}.`,
		"Read the diff, then check the server: a profile, a default or an interceptor stored something else.",
		map[string]any{"tool": "fhir.update", "readback": "Observation/1001 version 2", "path": "status", "observed": "preliminary", "expected": "final"}},
	{CodeFHIRNoNewVersion, Note, "L1", `The server kept version {observed}: the update changed nothing, so it wrote no new version.`,
		"Nothing. The record shows the version that was already there.", map[string]any{"observed": "2"}},
	{CodeFHIRHistoryNoMatch, Gap, "L1", `The server's history since the call shows {n} versions carrying this action's token, not exactly one, so Deedglass can't tell whether the write happened.`,
		"Check the resource's history on the server. Turn on request ids (HAPI: store_meta_source_information) so the next one can be found.",
		map[string]any{"n": "0"}},
	// L2 for FHIR servers on Postgres (HC-P05, ADR-108).
	{CodeVersionMatched, Pass, "L2", `The server's database committed {resource} version {version} in this action's transaction, as {tool} sent it.`,
		"Nothing.", map[string]any{"resource": "Observation/1001", "version": "2", "tool": "fhir.update"}},
	{CodeVersionFieldChanged, Violation, "L2", `The server's database committed {resource} version {version} with {path} different from what {tool} sent.`,
		"Read the paths, then check the server: a profile, a default or an interceptor stored something else.",
		map[string]any{"resource": "Observation/1001", "version": "2", "path": "status", "tool": "fhir.update"}},
	{CodeFHIRExtraVersions, Violation, "L2", `This action's transaction committed {n} resource versions, and the call reported 1. Also written: {resources}.`,
		"Read the resources it names: the server created or changed them on its own (a placeholder reference target, an interceptor).",
		map[string]any{"n": 2, "resources": "Patient/ghost-3 version 1"}},
	{CodeOutOfBand, Note, "L2", `Another writer changed {resource} while this action's call was open (version {version}). That change isn't counted as this action's.`,
		"Nothing for this action. Check who else writes this resource; the decoder counts these as unattributed.",
		map[string]any{"resource": "Observation/1001", "version": "3"}},
	{CodeRequestIDReused, Note, "L2", `Another transaction carried this action's request token ({resource} version {version}). It isn't counted as this action's.`,
		"Treat it as a forged or replayed request id: find the client that sent it. The action is judged on its own transaction only.",
		map[string]any{"resource": "Observation/1002", "version": "1"}},
	{CodeVersionTagMismatch, Gap, "L2", `The version the server reported ({resource} version {version}) carries another request's mark, so Deedglass can't tie that transaction to this action.`,
		"Check that nothing between Deedglass and the server rewrites X-Request-ID, and that witness.author names this connector's client.",
		map[string]any{"resource": "Observation/1001", "version": "2"}},
	{CodeLargeObjectNotReplicated, Gap, "L2", `The server stored {resource} version {version} outside the row ({why}), and Postgres's change log doesn't carry it, so its content can't be compared.`,
		"Nothing to fix for this action; the read-back (L1) still checks it. Rows written by HAPI before 7.0 keep their text in a large object.",
		map[string]any{"resource": "Observation/1001", "version": "1", "why": "a large object"}},
	{CodeFHIRSchemaUnknown, Gap, "L2", `The FHIR server's tables have a layout Deedglass doesn't know ({table}), so its change log isn't read. The read-back (L1) still runs.`,
		"Check deedglass decoder status. Upgrade Deedglass for this server version, or turn witness off for this connector.",
		map[string]any{"table": "public.hfj_res_ver"}},
	{CodeTriggersUnwatched, Gap, "L2", `{table} has triggers that may write tables Deedglass doesn't watch.`,
		"Watch every table the triggers write, or turn off witness.strict_triggers.", map[string]any{"table": "public.orders"}},
}

var catalog = func() map[Code]Entry {
	m := make(map[Code]Entry, len(entries))
	for _, e := range entries {
		if _, dup := m[e.Code]; dup {
			panic("witness: duplicate catalog code " + string(e.Code))
		}
		m[e.Code] = e
	}
	return m
}()

// Reserved are codes S19 set aside for the decoder and L2 before S21 chose its names (S21 uses
// l2_timeout; the rest stay unused). They are listed so nobody takes them for something else.
var Reserved = []Code{"rows_outside_footprint", "column_outside_set", "replica_identity_missing",
	"forged_marker", "decoder_degraded", "row_count_mismatch", "rows_witnessed"}

// Lookup returns a code's catalog entry.
func Lookup(c Code) (Entry, bool) {
	e, ok := catalog[c]
	return e, ok
}

// Catalog lists every entry, sorted by code.
func Catalog() []Entry {
	out := append([]Entry(nil), entries...)
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}
