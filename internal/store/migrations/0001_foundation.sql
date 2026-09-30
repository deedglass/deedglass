-- 0001_foundation.sql: id domains, digest and currency domains, shared trigger functions, schema grants.
--
-- Every id column uses a domain that mirrors ids.Pattern(prefix) (contract §3). The domains are
-- written out, one per ids.Registry() prefix; TestDomains checks the set and every regex.

CREATE SCHEMA IF NOT EXISTS deedglass;

CREATE DOMAIN deedglass.org_id  AS text CHECK (VALUE ~ '^org_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.usr_id  AS text CHECK (VALUE ~ '^usr_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.mem_id  AS text CHECK (VALUE ~ '^mem_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.ses_id  AS text CHECK (VALUE ~ '^ses_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.agt_id  AS text CHECK (VALUE ~ '^agt_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.key_id  AS text CHECK (VALUE ~ '^key_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.pol_id  AS text CHECK (VALUE ~ '^pol_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.pv_id   AS text CHECK (VALUE ~ '^pv_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.con_id  AS text CHECK (VALUE ~ '^con_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.act_id  AS text CHECK (VALUE ~ '^act_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.evt_id  AS text CHECK (VALUE ~ '^evt_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.apr_id  AS text CHECK (VALUE ~ '^apr_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.deed_id AS text CHECK (VALUE ~ '^deed_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.sk_id   AS text CHECK (VALUE ~ '^sk_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.sug_id  AS text CHECK (VALUE ~ '^sug_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.anm_id  AS text CHECK (VALUE ~ '^anm_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.rpl_id  AS text CHECK (VALUE ~ '^rpl_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.whk_id  AS text CHECK (VALUE ~ '^whk_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.req_id  AS text CHECK (VALUE ~ '^req_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.ltk_id  AS text CHECK (VALUE ~ '^ltk_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.pa_id   AS text CHECK (VALUE ~ '^pa_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.cred_id AS text CHECK (VALUE ~ '^cred_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.atk_id  AS text CHECK (VALUE ~ '^atk_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.dtx_id  AS text CHECK (VALUE ~ '^dtx_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.whd_id  AS text CHECK (VALUE ~ '^whd_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.adm_id  AS text CHECK (VALUE ~ '^adm_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.wl_id   AS text CHECK (VALUE ~ '^wl_[0-7][0-9a-hjkmnp-tv-z]{25}$');
CREATE DOMAIN deedglass.idk_id  AS text CHECK (VALUE ~ '^idk_[0-7][0-9a-hjkmnp-tv-z]{25}$');

CREATE DOMAIN deedglass.digest   AS text CHECK (VALUE ~ '^sha256:[0-9a-f]{64}$');
CREATE DOMAIN deedglass.currency AS text CHECK (VALUE ~ '^[a-z]{3}$');

-- Append-only history (deeds, action_events, admin_events, policy_versions): UPDATE, DELETE and
-- TRUNCATE raise DG001, from a row trigger and a statement trigger (so an UPDATE that matches no row
-- is refused too). The runtime role has no such privilege anyway; this is the second layer, and it
-- also stops the owner.
CREATE FUNCTION deedglass.forbid_mutation() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  RAISE EXCEPTION USING ERRCODE = 'DG001',
    MESSAGE = pg_catalog.format('deedglass: %I.%I is append-only (%s refused)', TG_TABLE_SCHEMA, TG_TABLE_NAME, TG_OP),
    HINT = 'Write a new row. Deeds are corrected by amendment deeds, never edited.';
END $fn$;

CREATE FUNCTION deedglass.touch_updated_at() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN NEW.updated_at := pg_catalog.clock_timestamp(); RETURN NEW; END $fn$;

REVOKE ALL ON FUNCTION deedglass.forbid_mutation(), deedglass.touch_updated_at() FROM PUBLIC;
REVOKE ALL ON SCHEMA deedglass FROM PUBLIC;
GRANT USAGE ON SCHEMA deedglass TO deedglass_app;
