-- 0017_connectors_vault.sql: what S16's connector framework and vault need beyond 0004. The
-- connectors table gains a description, the normalized config's hash and the last test result;
-- the credentials envelope gets S16's exact bounds (0004 already has its shape: one row per
-- connector, AES-256-GCM envelope, names and a hint in the clear, never a value).

ALTER TABLE deedglass.connectors
  ADD COLUMN description text NOT NULL DEFAULT '' CHECK (pg_catalog.length(description) <= 500),
  ADD COLUMN config_hash deedglass.digest,
  ADD COLUMN health jsonb NOT NULL DEFAULT '{"status": "unknown", "tested_at": null}'
    CHECK (pg_catalog.jsonb_typeof(health) = 'object' AND pg_catalog.pg_column_size(health) <= 8192),
  ADD COLUMN updated_by text CHECK (pg_catalog.length(updated_by) <= 64);
-- The config is at most 64 KiB of JSON (S16 §3.4).
ALTER TABLE deedglass.connectors
  ADD CONSTRAINT connectors_config_size CHECK (pg_catalog.pg_column_size(config) <= 70000);
REVOKE ALL ON deedglass.connectors FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.connectors TO deedglass_app;
GRANT UPDATE (name, config, status, updated_at, deleted_at, description, config_hash, health, updated_by)
  ON deedglass.connectors TO deedglass_app;

-- Credentials: S16's bounds. The ciphertext is 12 (nonce) + 2..16,384 (plaintext) + 16 (tag).
UPDATE deedglass.credentials SET hint = '' WHERE hint IS NULL;
UPDATE deedglass.credentials SET updated_by = 'operator' WHERE updated_by IS NULL;
ALTER TABLE deedglass.credentials
  ALTER COLUMN hint SET DEFAULT '',
  ALTER COLUMN hint SET NOT NULL,
  ALTER COLUMN updated_by SET NOT NULL,
  ALTER COLUMN version DROP DEFAULT,
  ADD CONSTRAINT credentials_ciphertext_len CHECK (pg_catalog.octet_length(ciphertext) BETWEEN 30 AND 16412),
  ADD CONSTRAINT credentials_field_names CHECK (pg_catalog.cardinality(field_names) BETWEEN 1 AND 16);
CREATE INDEX credentials_kek ON deedglass.credentials (org_id, kek_id);
REVOKE ALL ON deedglass.credentials FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.credentials TO deedglass_app;

-- DELETE /v1/connectors/{id} answers 409 while an action on the connector is executing.
CREATE INDEX actions_connector_executing ON deedglass.actions (org_id, connector) WHERE state = 'executing';
