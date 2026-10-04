-- 0023_overview.sql: what S25's Overview (GET /v1/overview) needs.
--
-- The Overview counts an org's actions in a 24 h or 7 d window by bucket, decision, witness verdict
-- and witness level. actions_org_time (0005) finds the window but leaves every count to the wide
-- action rows. This covering index holds the four columns the counts read, so the scan reads the
-- index alone where the rows are all-visible (make bench-overview: 100,000 actions in 24 h on
-- PostgreSQL 18, raw numbers in bench/results/). Every transition already changes an indexed
-- column (state), so action updates were never HOT; this adds one small entry per row version.
CREATE INDEX actions_overview ON deedglass.actions (org_id, created_at) INCLUDE (decision, witness_verdict, witness_level);
