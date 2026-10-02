-- The hero's planted bug (S21): a believable legacy trigger. "When one order ships, mark the rest
-- of the open batch shipped too." It ships the next 300 orders that aren't shipped yet, so an
-- agent's one-row UPDATE of order 42 makes Postgres commit 301 changed rows while the tool reports 1.
--
-- Demo only: it lives in examples/demo/sql, and make demo-trigger-on (examples/demo/scripts/demo.sh)
-- applies it only to the demo's orders-db container on 127.0.0.1:55434, as the bootstrap
-- superuser. It follows the SQL rules even though it is a planted bug: schema-qualified names,
-- search_path pinned, REVOKE in the same transaction as CREATE.
--
-- S15's seed has only 282 orders besides 42 in pending or paid (60 + 222), so the predicate is
-- "not shipped yet" (339 besides 42); S21's report records the widening.
--
-- The image also runs this file on a fresh volume, right before 11_trigger_off.sql: a new stack
-- starts with the trigger off.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
CREATE SCHEMA IF NOT EXISTS acme_legacy AUTHORIZATION acme_owner;
CREATE OR REPLACE FUNCTION acme_legacy.ship_open_batch() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF pg_catalog.pg_trigger_depth() = 1 THEN
    UPDATE public.orders o SET status = 'shipped'
     WHERE o.id IN (SELECT i.id FROM public.orders i
                     WHERE i.id <> NEW.id AND i.status <> 'shipped'
                     ORDER BY i.id LIMIT 300);
  END IF;
  RETURN NULL;
END $fn$;
REVOKE ALL ON FUNCTION acme_legacy.ship_open_batch() FROM PUBLIC;
DROP TRIGGER IF EXISTS orders_ship_open_batch ON public.orders;
CREATE TRIGGER orders_ship_open_batch AFTER UPDATE OF status ON public.orders
  FOR EACH ROW WHEN (NEW.status = 'shipped' AND OLD.status IS DISTINCT FROM 'shipped')
  EXECUTE FUNCTION acme_legacy.ship_open_batch();
COMMIT;
