-- Deedglass undo SQL for watched session obs_01m3x71p6wesj7xewec6bwbwxm (connector pg-demo).
-- Written 2026-10-17 14:05 UTC from the row images Postgres committed. Deedglass doesn't run it; you do.
-- It puts back 3 rows: 1 update, 1 insert, 1 delete, newest change first, in one transaction.
-- Each step checks the row still holds what the session left. If any row changed since, the
-- step stops the script and Postgres rolls back everything: nothing is half undone.
-- Covered: the tables below, by primary key. Not covered: sequences (they don't move back),
-- unwatched tables, and work your triggers do when these statements run.
--   public.order_items
--   public.orders
-- Left out by a person (not undone): dtx_01m3x71p6wesj7xewec6bwbw03
-- Check: Triggers: public.orders has a trigger that fires again.
-- Run it with: psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f undo.sql
-- Plan sha256:ae91ffa80fe324fd94990e5204f172271716793361f8b405efa626f6ffd9c724
BEGIN;
SET LOCAL TimeZone = 'UTC';
SET LOCAL DateStyle = 'ISO, YMD';
SET LOCAL IntervalStyle = 'postgres';
SET LOCAL bytea_output = 'hex';
SET LOCAL extra_float_digits = 3;
SET LOCAL standard_conforming_strings = on;
-- put back id=2 in public.orders (it was deleted)
INSERT INTO "public"."orders" ("id", "note", "status") VALUES ('2'::bigint, 'it''s a ''quoted'' note\n'::text, 'paid'::text);
-- remove id=9 in public.order_items
DO $dg$ BEGIN
  DELETE FROM "public"."order_items" WHERE "id" = '9'::bigint AND "order_id" IS NOT NULL AND pg_catalog.format('%s', "order_id") = '1' AND "sku" IS NOT NULL AND pg_catalog.format('%s', "sku") = 'sku-9';
  IF NOT FOUND THEN RAISE EXCEPTION USING MESSAGE = 'Deedglass undo stopped: id=9 in public.order_items changed after the session. Nothing was undone.'; END IF;
END $dg$;
-- restore id=1 in public.orders
DO $dg$ BEGIN
  UPDATE "public"."orders" SET "status" = 'paid'::text WHERE "id" = '1'::bigint AND "note" IS NOT NULL AND pg_catalog.format('%s', "note") = 'n-1' AND "status" IS NOT NULL AND pg_catalog.format('%s', "status") = 'shipped';
  IF NOT FOUND THEN RAISE EXCEPTION USING MESSAGE = 'Deedglass undo stopped: id=1 in public.orders changed after the session. Nothing was undone.'; END IF;
END $dg$;
COMMIT;
