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

// Reserved are codes S20 and S21 will register for the decoder and L2. They are listed so nobody
// takes them for something else.
var Reserved = []Code{"l2_timeout", "rows_outside_footprint", "column_outside_set", "replica_identity_missing",
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
