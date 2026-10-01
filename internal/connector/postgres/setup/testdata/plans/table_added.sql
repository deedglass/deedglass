+ grants · read public (5 tables) · write public.order_items
+ replica identity full · 1 watched table · brief lock on each
~ publication deedglass_pub · set 5 tables
plan sha256:0d8564957e27f257308c8eb0eafa8ca3c2a52d870b90178434b59bb212ad6b58

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '5s';

-- + grants · read public (5 tables) · write public.order_items
GRANT INSERT, UPDATE, DELETE ON TABLE "public"."order_items" TO deedglass_agent;
GRANT SELECT ON TABLE "public"."order_items" TO deedglass_witness;

-- + replica identity full · 1 watched table · brief lock on each
ALTER TABLE "public"."order_items" REPLICA IDENTITY FULL;

-- ~ publication deedglass_pub · set 5 tables
ALTER PUBLICATION deedglass_pub SET TABLE deedglass.ledger, "public"."customers", "public"."order_items", "public"."orders", "public"."refunds";

COMMIT;
