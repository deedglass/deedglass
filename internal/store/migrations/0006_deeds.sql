-- 0006_deeds.sql: seal keys (instance-wide), deeds with the chain trigger, admin events.

-- seal_keys (global): one instance-wide set, published at /.well-known/deedglass/keys.json.
-- Public keys only; no RLS.
CREATE TABLE deedglass.seal_keys (
  id deedglass.sk_id PRIMARY KEY,
  public_key bytea NOT NULL UNIQUE CHECK (pg_catalog.octet_length(public_key) = 32),
  alg text NOT NULL DEFAULT 'Ed25519' CHECK (alg = 'Ed25519'),
  thumbprint deedglass.digest NOT NULL UNIQUE,                   -- sha256 of the raw 32-byte key (S04)
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired', 'revoked')),
  activated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  retired_at timestamptz,
  revoked_at timestamptz,
  revocation_reason text CHECK (pg_catalog.length(revocation_reason) <= 500),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  CHECK (thumbprint = 'sha256:' || pg_catalog.encode(pg_catalog.sha256(public_key), 'hex')),
  CHECK (status <> 'retired' OR retired_at IS NOT NULL),
  CHECK (status <> 'revoked' OR revoked_at IS NOT NULL)
);
REVOKE ALL ON deedglass.seal_keys FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.seal_keys TO deedglass_app;
GRANT UPDATE (status, retired_at, revoked_at, revocation_reason) ON deedglass.seal_keys TO deedglass_app;

-- deeds: append-only, one hash-linked chain per org ------------------------------------------------
CREATE TABLE deedglass.deeds (
  id deedglass.deed_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  seq bigint NOT NULL CHECK (seq >= 1),
  kind text NOT NULL CHECK (kind IN ('action', 'admin', 'amendment')),
  action_id deedglass.act_id,
  amends deedglass.deed_id,
  body jsonb NOT NULL,                                                         -- for queries
  body_jcs bytea NOT NULL CHECK (pg_catalog.octet_length(body_jcs) <= 65536),   -- the exact bytes that were hashed
  body_hash deedglass.digest NOT NULL,
  prev_hash deedglass.digest NOT NULL,
  seal text NOT NULL CHECK (seal ~ '^ed25519:[A-Za-z0-9_-]{86}$'),             -- 64-byte signature, base64url, no padding
  seal_key_id deedglass.sk_id NOT NULL REFERENCES deedglass.seal_keys (id),
  envelope bytea NOT NULL CHECK (pg_catalog.octet_length(envelope) <= 16384),  -- exact JCS(envelope), served verbatim
  issued_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, seq),
  UNIQUE (org_id, id),
  UNIQUE (org_id, prev_hash),                                                  -- no forks: one child per parent
  CHECK (body_hash = 'sha256:' || pg_catalog.encode(pg_catalog.sha256(body_jcs), 'hex')),
  CHECK ((kind = 'amendment') = (amends IS NOT NULL)),
  CHECK (kind <> 'action' OR action_id IS NOT NULL),
  FOREIGN KEY (org_id, action_id) REFERENCES deedglass.actions (org_id, id),
  FOREIGN KEY (org_id, amends) REFERENCES deedglass.deeds (org_id, id)
);
CREATE INDEX deeds_action ON deedglass.deeds (org_id, action_id) WHERE action_id IS NOT NULL;
CREATE INDEX deeds_org_time ON deedglass.deeds (org_id, issued_at DESC, seq DESC);

-- The chain: seq 1 links to the org's genesis value, seq n to the body_hash of seq n-1 (contract §7).
-- S04's issuer computes the same genesis in Go. Runs as the caller, under RLS: the predecessor of an
-- org's deed is visible only inside that org's transaction.
CREATE FUNCTION deedglass.deeds_chain_check() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
DECLARE want text;
BEGIN
  IF NEW.seq = 1 THEN
    want := 'sha256:' || pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to('deedglass/genesis' || E'\n' || NEW.org_id, 'UTF8')), 'hex');
  ELSE
    SELECT d.body_hash INTO want FROM deedglass.deeds AS d WHERE d.org_id = NEW.org_id AND d.seq = NEW.seq - 1;
    IF NOT FOUND THEN
      RAISE EXCEPTION USING ERRCODE = 'DG002',
        MESSAGE = pg_catalog.format('deedglass: deed seq %s has no predecessor in org %s', NEW.seq, NEW.org_id),
        HINT = 'Deeds are issued in order, one at a time per org (S04 holds the chain lock).';
    END IF;
  END IF;
  IF NEW.prev_hash IS DISTINCT FROM want THEN
    RAISE EXCEPTION USING ERRCODE = 'DG003',
      MESSAGE = pg_catalog.format('deedglass: deed seq %s does not link to seq %s', NEW.seq, NEW.seq - 1),
      HINT = 'prev_hash must be the body_hash of the previous deed (the genesis value for seq 1).';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION deedglass.deeds_chain_check() FROM PUBLIC;
CREATE TRIGGER deeds_chain BEFORE INSERT ON deedglass.deeds FOR EACH ROW EXECUTE FUNCTION deedglass.deeds_chain_check();
CREATE TRIGGER deeds_append_only BEFORE UPDATE OR DELETE ON deedglass.deeds FOR EACH ROW EXECUTE FUNCTION deedglass.forbid_mutation();
CREATE TRIGGER deeds_append_only_stmt BEFORE UPDATE OR DELETE OR TRUNCATE ON deedglass.deeds FOR EACH STATEMENT EXECUTE FUNCTION deedglass.forbid_mutation();
ALTER TABLE deedglass.deeds ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.deeds FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.deeds FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.deeds TO deedglass_app;

-- admin_events: append-only. Write the admin deed first, then the event that names it.
CREATE TABLE deedglass.admin_events (
  id deedglass.adm_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  actor_type text NOT NULL CHECK (actor_type IN ('user', 'agent', 'system')),
  actor_id text CHECK (pg_catalog.length(actor_id) <= 64),
  action text NOT NULL CHECK (action ~ '^[a-z_]+\.[a-z_]+$'),
  target_type text CHECK (pg_catalog.length(target_type) <= 64),
  target_id text CHECK (pg_catalog.length(target_id) <= 64),
  reason text CHECK (pg_catalog.length(reason) <= 2000),
  data jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(data) = 'object'),
  data_digest deedglass.digest,
  request_id deedglass.req_id,
  deed_id deedglass.deed_id,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, deed_id) REFERENCES deedglass.deeds (org_id, id),
  CHECK ((actor_type = 'system') = (actor_id IS NULL))
);
CREATE INDEX admin_events_org_time ON deedglass.admin_events (org_id, created_at DESC, id DESC);
CREATE TRIGGER admin_events_append_only BEFORE UPDATE OR DELETE ON deedglass.admin_events
  FOR EACH ROW EXECUTE FUNCTION deedglass.forbid_mutation();
CREATE TRIGGER admin_events_append_only_stmt BEFORE UPDATE OR DELETE OR TRUNCATE ON deedglass.admin_events
  FOR EACH STATEMENT EXECUTE FUNCTION deedglass.forbid_mutation();
ALTER TABLE deedglass.admin_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.admin_events FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.admin_events FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.admin_events TO deedglass_app;

-- Deed links from tables created earlier.
ALTER TABLE deedglass.actions ADD CONSTRAINT actions_deed_fk
  FOREIGN KEY (org_id, deed_id) REFERENCES deedglass.deeds (org_id, id);
ALTER TABLE deedglass.approvals ADD CONSTRAINT approvals_deed_fk
  FOREIGN KEY (org_id, deed_id) REFERENCES deedglass.deeds (org_id, id);
ALTER TABLE deedglass.policy_activations ADD CONSTRAINT policy_activations_deed_fk
  FOREIGN KEY (org_id, deed_id) REFERENCES deedglass.deeds (org_id, id);
