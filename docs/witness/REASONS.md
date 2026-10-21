# Witness reasons

Every Witness verdict lists its reasons: what each check found, or what it couldn't find. This page is generated from `internal/witness/catalog.go` by `go generate ./internal/witness`; never edit it by hand. A pass agrees with what the tool reported, a violation contradicts it, a gap is evidence Deedglass needed and couldn't get, and a note is context that never changes a verdict. The messages below are shown with sample values.

| Code | Class | Level | Message | What to do next |
|---|---|---|---|---|
| `any_writer` | note | L2 | `The Debezium feed from MySQL doesn't carry this action's id, so changes from any writer to these tables are compared as this action's.` | Write the ledger row in the action's own transaction so the feed names the action (docs/WITNESS_DEBEZIUM.md). |
| `decoder_halted` | gap | L2 | `The decoder for this connector has stopped (publication_missing), so Postgres's change log wasn't read.` | Run deedglass decoder status; it names the cause and the fix. |
| `edge_reported_success` | pass | L0 | `The edge reported success. Deedglass did not run this call itself.` | Nothing. Proxy-mode calls stay at L0 unless the system of record is witnessed (L2). |
| `feed_incomplete` | gap | L2 | `The Debezium feed from MySQL holds only part of transaction 3e11fa47-71ca-11e1-9e33-c80aa9429562:23 (it starts at event 49 of 301), so Deedglass can't compare it.` | Turn on provide.transaction.metadata, then replay the feed from before the transaction began (docs/WITNESS_DEBEZIUM.md). |
| `feed_no_match` | gap | L2 | `The Debezium feed from MySQL holds no transaction this action could have written.` | Check that the feed covers the time of the action and every table it writes. |
| `fhir_conditional_matched` | violation | L1 | `fhir.create reported success, but the conditional create matched an existing resource ("Observation/1001 version 1"), so this action created nothing.` | Read the existing resource. The agent's report of a new one is wrong; check what it did next with that id. |
| `fhir_extra_versions` | violation | L2 | `This action's transaction committed 2 resource versions, and the call reported 1. Also written: Patient/ghost-3 version 1.` | Read the resources it names: the server created or changed them on its own (a placeholder reference target, an interceptor). |
| `fhir_field_changed` | violation | L1 | `fhir.update reported success, but Observation/1001 version 2 shows status as "preliminary", not "final".` | Read the diff, then check the server: a profile, a default or an interceptor stored something else. |
| `fhir_history_no_match` | gap | L1 | `The server's history since the call shows 0 versions carrying this action's token, not exactly one, so Deedglass can't tell whether the write happened.` | Check the resource's history on the server. Turn on request ids (HAPI: store_meta_source_information) so the next one can be found. |
| `fhir_no_new_version` | note | L1 | `The server kept version "2": the update changed nothing, so it wrote no new version.` | Nothing. The record shows the version that was already there. |
| `fhir_schema_unknown` | gap | L2 | `The FHIR server's tables have a layout Deedglass doesn't know (public.hfj_res_ver), so its change log isn't read. The read-back (L1) still runs.` | Check deedglass decoder status. Upgrade Deedglass for this server version, or turn witness off for this connector. |
| `footprint_matched` | pass | L2 | `Postgres committed 1 row in public.orders, as the statement said.` | Nothing. |
| `footprint_unknown` | gap | L2 | `The statement is too complex for Deedglass to predict which rows it touches (UPDATE … FROM), so L2 can't compare.` | Use a single-table statement with a key in WHERE, or a controlled function that declares what it writes. |
| `internal_error` | gap | any | `The witness hit an internal error, so the action isn't marked verified.` | Report it with the action id; it is a bug in Deedglass. |
| `keys_unknown` | gap | L2 | `Deedglass couldn't match rows in public.orders to the statement's keys (the table has no primary key).` | Give the table a primary key, or raise decoder.max_images_per_tx for large statements. |
| `l2_timeout` | gap | L2 | `Postgres's change log didn't show this action within 10s.` | Check deedglass decoder status. If the change log shows it later, an amendment deed records the result. |
| `large_object_not_replicated` | gap | L2 | `The server stored Observation/1001 version 1 outside the row (a large object), and Postgres's change log doesn't carry it, so its content can't be compared.` | Nothing to fix for this action; the read-back (L1) still checks it. Rows written by HAPI before 7.0 keep their text in a large object. |
| `ledger_mismatch` | violation | L2 | `The ledger row in Postgres doesn't match what Deedglass authorized (sql_digest).` | Treat the execution path as compromised: the statement that ran isn't the one the policy allowed. |
| `level_below_required` | gap | none | `The policy asks for L1. The strongest clean evidence was L0.` | Fix what kept the stronger level from running (the other reasons say what), or lower verify: in the policy. |
| `marker_count` | violation | L2 | `This action's transaction in Postgres holds 2 ledger rows and 1 markers, not one of each.` | Treat the database as tampered with: something wrote Deedglass's ledger outside begin_action. |
| `marker_mismatch` | violation | L2 | `This action's transaction in Postgres doesn't match its own ledger row (the marker names another action).` | Treat the database as tampered with: the ledger row and the marker disagree. |
| `missing_change` | violation | L2 | `The agent reported 1 row changed in public.orders, but Postgres committed 0.` | Check the target: the change the tool reported isn't in Postgres's change log. |
| `multiple_transactions` | violation | L2 | `2 transactions in Postgres claim this action. Only one can be real.` | Treat the database as tampered with: a ledger row and marker for this action were written again. |
| `no_checks` | gap | none | `No check could run for this action.` | Check that the tool reports a result. |
| `no_evidence` | gap | L2 | `This action's ledger row is in Postgres, but its transaction never appeared in the change log.` | Run deedglass decoder status: the slot may have been recreated after the action committed. |
| `old_image_missing` | gap | L2 | `public.orders doesn't keep full old rows (REPLICA IDENTITY DEFAULT), so Deedglass can't tell which columns changed.` | Run ALTER TABLE … REPLICA IDENTITY FULL on the table (docs/POSTGRES_DEPLOYMENT.md has the cost note). |
| `out_of_band` | note | L2 | `Another writer changed Observation/1001 while this action's call was open (version 3). That change isn't counted as this action's.` | Nothing for this action. Check who else writes this resource; the decoder counts these as unattributed. |
| `outcome_unknown` | gap | L0 | `Deedglass couldn't tell whether the tool ran (timeout). It may have.` | Check the target system for the change, and the connector's timeout. |
| `postcondition_error` | gap | L1 | `The postcondition couldn't be evaluated: no such key: status.` | Fix the postcondition in the connector config; guard optional fields with has(). |
| `postcondition_failed` | violation | L1 | `orders.update_status reported success, but orders.get says status is "pending", not "shipped".` | Read the diff, then check the target system: the write isn't there as the tool said. |
| `postcondition_held` | pass | L1 | `Postcondition held: observation.body.status == args.status.` | Nothing. |
| `publication_gap` | gap | L2 | `The publication changed while this action ran, so the change log may be missing its rows.` | Nothing to fix for this action. Change deedglass_pub only while no agent writes. |
| `readback_absent` | violation | L1 | `orders.update_status reported success, but orders.get found nothing at /v1/orders/42.` | Check the target system: the record the tool said it wrote isn't there. |
| `readback_failed` | gap | L1 | `The read-back orders.get failed (upstream_error), so the result couldn't be confirmed.` | Check the connector (deedglass connector test) and the target's health. |
| `readback_matched` | pass | L1 | `Read back orders.get: status is "shipped".` | Nothing. |
| `readback_not_found` | gap | L1 | `orders.get found nothing at /v1/orders/42. It may not be visible yet.` | If the target is eventually consistent, set readback.consistency to eventual. |
| `readback_skipped` | note | L1 | `No read-back: the statement isn't a single-row update by primary key.` | Nothing. Add a read-back, or witness the connector at L2. |
| `readback_timeout` | gap | L1 | `The read-back orders.get took longer than 10 s.` | Check the target's latency, or raise witness.readback_timeout. |
| `relation_changed` | gap | L2 | `The structure of public.orders changed in this transaction, so the comparison may be incomplete.` | Nothing to fix. Run schema changes outside agent actions. |
| `request_id_reused` | note | L2 | `Another transaction carried this action's request token (Observation/1002 version 1). It isn't counted as this action's.` | Treat it as a forged or replayed request id: find the client that sent it. The action is judged on its own transaction only. |
| `response_schema_invalid` | gap | L0 | `The response didn't match the tool's output schema at body.status, so Deedglass can't read it with confidence.` | Check the tool's output schema against what the service returns now. |
| `response_schema_valid` | pass | L0 | `The response matched the tool's output schema.` | Nothing. |
| `row_count_exceeded` | violation | L2 | `Postgres changed 301 rows in public.orders. The agent reported 1.` | Read the diff: a trigger, rule or cascade changed rows the statement didn't name. |
| `row_limit_exceeded` | violation | L2 | `Postgres changed 301 rows in public.orders; this connector allows at most 1 per action.` | Read the diff, then check why one action changed this many rows. |
| `row_missing` | gap | L1 | `Row id = 42 in public.orders isn't there any more, so a read-back can't confirm it.` | Check whether another process deleted the row. |
| `row_superseded` | gap | L1 | `Another transaction changed row id = 42 in public.orders after this action, so a read-back can't confirm it.` | Nothing to fix. L2 (the change stream) sees each transaction on its own. |
| `row_written` | pass | L1 | `Postgres shows row id = 42 in public.orders was last written by this action's transaction.` | Nothing. |
| `schema_skipped` | note | L0 | `The response was too large to check against the output schema.` | Nothing. Responses over 256 KiB are kept as a digest only. |
| `slot_dropped` | gap | L2 | `The replication slot was dropped while this action waited, so its change log can't be read.` | Nothing to fix for this action. The next sync creates a new slot for later ones. |
| `slot_invalidated` | gap | L2 | `Postgres invalidated the replication slot (wal_removed), so this action's change log is gone.` | Read deedglass decoder status, raise max_slot_wal_keep_size if WAL ran out, then drop and recreate the slot. |
| `tool_reported_success` | pass | L0 | `The tool reported success.` | Nothing. Add a read-back to reach L1. |
| `triggers_present` | note | L2 | `public.orders has triggers (orders_ship_open_batch). Their writes to watched tables are checked; writes elsewhere aren't visible.` | Nothing. Set witness.strict_triggers to treat unwatched trigger writes as a gap. |
| `triggers_unwatched` | gap | L2 | `public.orders has triggers that may write tables Deedglass doesn't watch.` | Watch every table the triggers write, or turn off witness.strict_triggers. |
| `undo_restored` | pass | L2 | `Restored 301 rows in public.orders exactly as planned.` | Nothing. |
| `unexpected_column` | violation | L2 | `Postgres changed total_cents on 1 row in public.orders. The statement set only status.` | If a trigger maintains the column on purpose, list it in the connector's witness.auto_columns. |
| `unexpected_key` | violation | L2 | `300 changed rows in public.orders are outside the rows the statement named (id = 42).` | Read the diff: the unexpected rows are listed first after the expected ones. |
| `unexpected_operation` | violation | L2 | `Postgres deleted 1 row in public.orders. The statement only updates rows.` | Look for a trigger or rule that deletes, inserts or truncates on this table. |
| `unexpected_table` | violation | L2 | `Postgres also changed 1 row in public.audit, which the statement doesn't touch.` | Look for a trigger or rule on the statement's table that writes this one. |
| `unwatched_table` | gap | L2 | `public.invoices isn't watched, so Postgres's change log doesn't show it.` | Add the table to witness.watched_tables and run deedglass connector postgres init again. |
| `value_mismatch` | violation | L1, L2 | `Postgres has status = "cancelled" on row id = 42 in public.orders. The statement set "shipped".` | Look for a trigger, rule or default that rewrites the column. |
| `version_field_changed` | violation | L2 | `The server's database committed Observation/1001 version 2 with status different from what fhir.update sent.` | Read the paths, then check the server: a profile, a default or an interceptor stored something else. |
| `version_matched` | pass | L2 | `The server's database committed Observation/1001 version 2 in this action's transaction, as fhir.update sent it.` | Nothing. |
| `version_tag_mismatch` | gap | L2 | `The version the server reported (Observation/1001 version 2) carries another request's mark, so Deedglass can't tie that transaction to this action.` | Check that nothing between Deedglass and the server rewrites X-Request-ID, and that witness.author names this connector's client. |
| `witness_gave_up` | gap | any | `The witness stopped after 8 attempts: the target is unreachable.` | Check the connector and the server log for the error. |

## Reserved

These codes were set aside before L2 chose its names. They stay unused, so they never mean something else.

- `rows_outside_footprint`
- `column_outside_set`
- `replica_identity_missing`
- `forged_marker`
- `decoder_degraded`
- `row_count_mismatch`
- `rows_witnessed`
