-- 0010_ledger.sql: what the Ledger (S06) needs beyond 0005/0006: the execution status, list
-- indexes, size limits on event data, and the admin-event limits the deed format sets.

-- actions.execution_status: the deed's execution.status (contract §6). NULL until the tool ran.
-- executing_at is when execution started; executed_at or failed_at is when it finished.
ALTER TABLE deedglass.actions
  ADD COLUMN execution_status text CHECK (execution_status IN ('succeeded', 'failed', 'unknown'));
GRANT UPDATE (execution_status) ON deedglass.actions TO deedglass_app;

-- List shapes (GET /v1/actions): the org time index and the agent index exist since 0005.
CREATE INDEX actions_verdict_time ON deedglass.actions (org_id, witness_verdict, created_at DESC, id DESC);
CREATE INDEX actions_tool_prefix  ON deedglass.actions (org_id, tool pg_catalog.text_pattern_ops);
CREATE INDEX actions_org_open     ON deedglass.actions (org_id, state) WHERE state <> 'sealed';

-- action_events: small, structured data only (ledger.EventData); payloads never go here, which is
-- why an append-only table can coexist with retention.
ALTER TABLE deedglass.action_events
  ADD CONSTRAINT action_events_data_size CHECK (pg_catalog.pg_column_size(data) <= 2048);
CREATE INDEX action_events_action ON deedglass.action_events (org_id, action_id, seq);

-- admin_events: the deed's admin.reason limit (280 characters) and a 16 KiB cap on data, whose
-- digest is all the deed carries.
ALTER TABLE deedglass.admin_events
  ADD CONSTRAINT admin_events_reason_len CHECK (pg_catalog.char_length(reason) <= 280),
  ADD CONSTRAINT admin_events_data_size CHECK (pg_catalog.pg_column_size(data) <= 16384);

-- deeds: list by kind (GET /v1/deeds?kind=) without scanning the chain.
CREATE INDEX deeds_org_kind_seq ON deedglass.deeds (org_id, kind, seq);
