# Witness reasons

Every Witness verdict lists its reasons: what each check found, or what it couldn't find. This page is generated from `internal/witness/catalog.go` by `go generate ./internal/witness`; never edit it by hand. A pass agrees with what the tool reported, a violation contradicts it, a gap is evidence Deedglass needed and couldn't get, and a note is context that never changes a verdict. The messages below are shown with sample values.

| Code | Class | Level | Message | What to do next |
|---|---|---|---|---|
| `edge_reported_success` | pass | L0 | `The edge reported success. Deedglass did not run this call itself.` | Nothing. Proxy-mode calls stay at L0 unless the system of record is witnessed (L2). |
| `internal_error` | gap | any | `The witness hit an internal error, so the action isn't marked verified.` | Report it with the action id; it is a bug in Deedglass. |
| `level_below_required` | gap | none | `The policy asks for L1. The strongest clean evidence was L0.` | Fix what kept the stronger level from running (the other reasons say what), or lower verify: in the policy. |
| `no_checks` | gap | none | `No check could run for this action.` | Check that the tool reports a result. |
| `outcome_unknown` | gap | L0 | `Deedglass couldn't tell whether the tool ran (timeout). It may have.` | Check the target system for the change, and the connector's timeout. |
| `postcondition_error` | gap | L1 | `The postcondition couldn't be evaluated: no such key: status.` | Fix the postcondition in the connector config; guard optional fields with has(). |
| `postcondition_failed` | violation | L1 | `orders.update_status reported success, but orders.get says status is "pending", not "shipped".` | Read the diff, then check the target system: the write isn't there as the tool said. |
| `postcondition_held` | pass | L1 | `Postcondition held: observation.body.status == args.status.` | Nothing. |
| `readback_absent` | violation | L1 | `orders.update_status reported success, but orders.get found nothing at /v1/orders/42.` | Check the target system: the record the tool said it wrote isn't there. |
| `readback_failed` | gap | L1 | `The read-back orders.get failed (upstream_error), so the result couldn't be confirmed.` | Check the connector (deedglass connector test) and the target's health. |
| `readback_matched` | pass | L1 | `Read back orders.get: status is "shipped".` | Nothing. |
| `readback_not_found` | gap | L1 | `orders.get found nothing at /v1/orders/42. It may not be visible yet.` | If the target is eventually consistent, set readback.consistency to eventual. |
| `readback_skipped` | note | L1 | `No read-back: the statement isn't a single-row update by primary key.` | Nothing. Add a read-back, or witness the connector at L2. |
| `readback_timeout` | gap | L1 | `The read-back orders.get took longer than 10 s.` | Check the target's latency, or raise witness.readback_timeout. |
| `response_schema_invalid` | gap | L0 | `The response didn't match the tool's output schema at body.status, so Deedglass can't read it with confidence.` | Check the tool's output schema against what the service returns now. |
| `response_schema_valid` | pass | L0 | `The response matched the tool's output schema.` | Nothing. |
| `row_missing` | gap | L1 | `Row id = 42 in public.orders isn't there any more, so a read-back can't confirm it.` | Check whether another process deleted the row. |
| `row_superseded` | gap | L1 | `Another transaction changed row id = 42 in public.orders after this action, so a read-back can't confirm it.` | Nothing to fix. L2 (the change stream) sees each transaction on its own. |
| `row_written` | pass | L1 | `Postgres shows row id = 42 in public.orders was last written by this action's transaction.` | Nothing. |
| `schema_skipped` | note | L0 | `The response was too large to check against the output schema.` | Nothing. Responses over 256 KiB are kept as a digest only. |
| `tool_reported_success` | pass | L0 | `The tool reported success.` | Nothing. Add a read-back to reach L1. |
| `value_mismatch` | violation | L1, L2 | `Postgres has status = "cancelled" on row id = 42 in public.orders. The statement set "shipped".` | Look for a trigger, rule or default that rewrites the column. |
| `witness_gave_up` | gap | any | `The witness stopped after 8 attempts: the target is unreachable.` | Check the connector and the server log for the error. |

## Reserved for S20 and S21

These codes are taken for the decoder and L2. They aren't used yet.

- `l2_timeout`
- `rows_outside_footprint`
- `column_outside_set`
- `replica_identity_missing`
- `forged_marker`
- `decoder_degraded`
- `row_count_mismatch`
- `rows_witnessed`
