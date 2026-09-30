-- 0004_connectors.sql: connectors and their encrypted credentials (one row per connector, S16).

CREATE TABLE deedglass.connectors (
  id deedglass.con_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  type text NOT NULL CHECK (type ~ '^[a-z][a-z0-9_]{1,31}$'),
  name text NOT NULL CHECK (name ~ '^[a-z][a-z0-9-]{1,31}$'),   -- the <connector> in <connector>.<tool>
  config jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(config) = 'object'),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'error')),
  created_by text CHECK (pg_catalog.length(created_by) <= 64),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  deleted_at timestamptz,
  UNIQUE (org_id, id)
);
CREATE UNIQUE INDEX connectors_live_name ON deedglass.connectors (org_id, name) WHERE deleted_at IS NULL;
CREATE TRIGGER connectors_touch BEFORE UPDATE ON deedglass.connectors FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.connectors ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.connectors FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.connectors FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.connectors TO deedglass_app;
GRANT UPDATE (name, config, status, updated_at, deleted_at) ON deedglass.connectors TO deedglass_app;

-- credentials: S16's envelope. Only field names and a short hint are stored in the clear.
CREATE TABLE deedglass.credentials (
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  connector_id deedglass.con_id NOT NULL,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  kind text NOT NULL CHECK (kind IN ('bearer', 'header', 'basic', 'query', 'dsn', 'fields')),
  field_names text[] NOT NULL DEFAULT '{}',
  hint text CHECK (pg_catalog.length(hint) <= 120),
  alg text NOT NULL DEFAULT 'A256GCM-ENV-1' CHECK (alg = 'A256GCM-ENV-1'),
  kek_id text NOT NULL CHECK (kek_id ~ '^kek:[0-9a-f]{12}$'),
  wrapped_dek bytea NOT NULL CHECK (pg_catalog.octet_length(wrapped_dek) = 60),
  ciphertext bytea NOT NULL CHECK (pg_catalog.octet_length(ciphertext) BETWEEN 1 AND 65536),
  updated_by text CHECK (pg_catalog.length(updated_by) <= 64),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  rotated_at timestamptz,
  PRIMARY KEY (org_id, connector_id),
  FOREIGN KEY (org_id, connector_id) REFERENCES deedglass.connectors (org_id, id)
);
CREATE TRIGGER credentials_touch BEFORE UPDATE ON deedglass.credentials FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.credentials ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.credentials FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.credentials FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.credentials TO deedglass_app;
