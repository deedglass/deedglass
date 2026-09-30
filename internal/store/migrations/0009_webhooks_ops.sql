-- 0009_webhooks_ops.sql: webhooks, webhook deliveries, the waitlist.

-- webhooks: the signing secret is stored encrypted (S22) ------------------------------------------
CREATE TABLE deedglass.webhooks (
  id deedglass.whk_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  url text NOT NULL CHECK (url ~ '^https?://' AND pg_catalog.length(url) <= 2048),
  description text CHECK (pg_catalog.length(description) <= 200),
  events text[] NOT NULL CHECK (pg_catalog.cardinality(events) >= 1),
  secret_ciphertext bytea NOT NULL,
  secret_nonce bytea NOT NULL CHECK (pg_catalog.octet_length(secret_nonce) = 12),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  disabled_reason text CHECK (pg_catalog.length(disabled_reason) <= 500),
  created_by text CHECK (pg_catalog.length(created_by) <= 64),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id)
);
CREATE TRIGGER webhooks_touch BEFORE UPDATE ON deedglass.webhooks FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.webhooks ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.webhooks FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.webhooks FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.webhooks TO deedglass_app;

-- webhook_deliveries ----------------------------------------------------------------------------
CREATE TABLE deedglass.webhook_deliveries (
  id deedglass.whd_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  webhook_id deedglass.whk_id NOT NULL,
  event_type text NOT NULL CHECK (event_type ~ '^[a-z_]+\.[a-z_]+$'),
  event_id deedglass.evt_id NOT NULL,
  payload_digest deedglass.digest NOT NULL,
  attempt integer NOT NULL DEFAULT 1 CHECK (attempt >= 1),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'failed', 'dead')),
  response_status integer CHECK (response_status BETWEEN 100 AND 599),
  response_ms integer CHECK (response_ms >= 0),
  next_attempt_at timestamptz,
  last_error text CHECK (pg_catalog.length(last_error) <= 2000),
  delivered_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, webhook_id) REFERENCES deedglass.webhooks (org_id, id)
);
CREATE INDEX webhook_deliveries_due ON deedglass.webhook_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX webhook_deliveries_webhook ON deedglass.webhook_deliveries (org_id, webhook_id, created_at DESC);
ALTER TABLE deedglass.webhook_deliveries ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.webhook_deliveries FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.webhook_deliveries FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.webhook_deliveries FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.webhook_deliveries TO deedglass_app;

-- waitlist (global): anyone may add themselves; only system scope reads it ------------------------
CREATE TABLE deedglass.waitlist (
  id deedglass.wl_id PRIMARY KEY,
  email text NOT NULL UNIQUE CHECK (email = pg_catalog.lower(email) AND pg_catalog.length(email) BETWEEN 3 AND 254
    AND email ~ '^[^@[:space:]]+@[^@[:space:]]+$'),
  source text CHECK (pg_catalog.length(source) <= 64),
  note text CHECK (pg_catalog.length(note) <= 500),
  invited_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now()
);
ALTER TABLE deedglass.waitlist ENABLE ROW LEVEL SECURITY;
CREATE POLICY insert_any ON deedglass.waitlist FOR INSERT TO deedglass_app WITH CHECK (true);
CREATE POLICY system_scope ON deedglass.waitlist FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.waitlist FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.waitlist TO deedglass_app;
