-- 0025_suggestions_v2.sql: what S28's suggestions need (generators, the accept saga, phrasing).
--
-- 0008 created suggestions with row-level security and a foreign key from policy_versions. Nothing
-- wrote to it before S28, so this migration tightens it in place. It first refuses, with a clear
-- message, if the table already holds rows: a forward-only migration never guesses what to do with
-- data it didn't expect.
DO $refuse$
BEGIN
  IF EXISTS (SELECT 1 FROM deedglass.suggestions) THEN
    RAISE EXCEPTION 'migration 0025 expects suggestions to be empty (nothing wrote to it before S28)'
      USING HINT = 'Move the rows aside, apply the migration, and write them back in the new shape.';
  END IF;
END
$refuse$;

ALTER TABLE deedglass.suggestions
  -- which generator wrote it, and the version of its thresholds
  ADD COLUMN generator text NOT NULL CHECK (generator ~ '^[a-z_]+\.[a-z_]+$' AND pg_catalog.length(generator) <= 64),
  ADD COLUMN generator_version text NOT NULL CHECK (pg_catalog.length(generator_version) BETWEEN 1 AND 16),
  -- sha256(JCS{generator, policy_id, target, pattern}): one open suggestion per change
  ADD COLUMN fingerprint deedglass.digest NOT NULL,
  -- the enforced version the suggestion was written against
  ADD COLUMN policy_id deedglass.pol_id NOT NULL,
  ADD COLUMN base_version_id deedglass.pv_id NOT NULL,
  ADD COLUMN base_version_hash deedglass.digest NOT NULL,
  ADD COLUMN target_rule_id text NULL CHECK (pg_catalog.length(target_rule_id) <= 64),
  -- the patched policy (NULL for an advisory, which changes nothing)
  ADD COLUMN candidate_source text NULL CHECK (pg_catalog.octet_length(candidate_source) <= 262144),
  ADD COLUMN candidate_source_hash deedglass.digest NULL,
  ADD COLUMN evidence_digest deedglass.digest NOT NULL,
  ADD COLUMN window_from timestamptz NOT NULL,
  ADD COLUMN window_to timestamptz NOT NULL,
  -- ranking: the actions whose decision would change
  ADD COLUMN impact integer NOT NULL DEFAULT 0 CHECK (impact >= 0),
  -- the candidate's compile warnings (severity W), shown on the card
  ADD COLUMN warnings jsonb NOT NULL DEFAULT '[]' CHECK (pg_catalog.jsonb_typeof(warnings) = 'array' AND pg_catalog.octet_length(warnings::text) <= 16384),
  -- optional Claude wording (S28 §3.7); the template text in title/rationale always stands on its own
  ADD COLUMN phrased_title text NULL CHECK (pg_catalog.char_length(phrased_title) <= 90),
  ADD COLUMN phrased_rationale text NULL CHECK (pg_catalog.char_length(phrased_rationale) <= 400),
  ADD COLUMN phrasing jsonb NOT NULL DEFAULT '{"source":"template"}' CHECK (pg_catalog.jsonb_typeof(phrasing) = 'object' AND pg_catalog.octet_length(phrasing::text) <= 2048),
  ADD COLUMN stale_reason text NULL CHECK (stale_reason IN ('base_changed')),
  -- the accept saga: who started it (the sweeper finishes it as them) and what they asked for
  ADD COLUMN decided_by_type text NULL CHECK (decided_by_type IN ('user', 'api_key')),
  ADD COLUMN replace_shadow boolean NOT NULL DEFAULT false,
  ADD COLUMN decision_request_id text NULL CHECK (pg_catalog.length(decision_request_id) <= 64),
  ADD COLUMN decision_deed_id deedglass.deed_id NULL,
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  ADD CONSTRAINT suggestions_window CHECK (window_to > window_from),
  ADD CONSTRAINT suggestions_evidence_size CHECK (pg_catalog.octet_length(evidence::text) <= 65536),
  ADD CONSTRAINT suggestions_replay_size CHECK (replay_result IS NULL OR pg_catalog.octet_length(replay_result::text) <= 262144),
  ADD CONSTRAINT suggestions_candidate CHECK ((candidate_source IS NULL) = (candidate_source_hash IS NULL)),
  ADD CONSTRAINT suggestions_advisory CHECK ((kind = 'advisory') = (candidate_source IS NULL)),
  ADD CONSTRAINT suggestions_accepted CHECK (status <> 'accepted' OR (resulting_version_id IS NOT NULL AND decision_deed_id IS NOT NULL AND decided_at IS NOT NULL)),
  ADD CONSTRAINT suggestions_rejected CHECK (status <> 'rejected' OR (decision_deed_id IS NOT NULL AND decided_at IS NOT NULL AND decision_reason IS NOT NULL)),
  ADD CONSTRAINT suggestions_accepting CHECK (status <> 'accepting' OR (decided_by IS NOT NULL AND decided_by_type IS NOT NULL)),
  ADD CONSTRAINT suggestions_superseded CHECK (status <> 'superseded' OR stale_reason IS NOT NULL),
  ADD CONSTRAINT suggestions_policy_fk FOREIGN KEY (org_id, policy_id) REFERENCES deedglass.policies (org_id, id),
  ADD CONSTRAINT suggestions_base_fk FOREIGN KEY (org_id, base_version_id) REFERENCES deedglass.policy_versions (org_id, id);

-- status: open | accepting | accepted | rejected | superseded (adds S28's accepting).
ALTER TABLE deedglass.suggestions DROP CONSTRAINT suggestions_status_check;
ALTER TABLE deedglass.suggestions ADD CONSTRAINT suggestions_status_check
  CHECK (status IN ('open', 'accepting', 'accepted', 'rejected', 'superseded'));

CREATE TRIGGER suggestions_touch BEFORE UPDATE ON deedglass.suggestions FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();

DROP INDEX deedglass.suggestions_open;
CREATE UNIQUE INDEX suggestions_open_fp_uq ON deedglass.suggestions (org_id, fingerprint) WHERE status IN ('open', 'accepting');
CREATE INDEX suggestions_org_status_idx ON deedglass.suggestions (org_id, status, impact DESC, created_at DESC);
-- the dedupe read: a fingerprint's latest rejection
CREATE INDEX suggestions_org_fp_idx ON deedglass.suggestions (org_id, fingerprint, decided_at DESC);
