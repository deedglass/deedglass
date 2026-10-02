-- Turns the hero's planted trigger off (S21): drops the trigger and its function. The empty schema
-- acme_legacy stays (dropping it would need CASCADE). make demo-trigger-off runs it; the image runs
-- it once on a fresh volume, after 10_trigger_on.sql.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
DROP TRIGGER IF EXISTS orders_ship_open_batch ON public.orders;
DROP FUNCTION IF EXISTS acme_legacy.ship_open_batch();
COMMIT;
