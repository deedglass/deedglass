- grants · write public.refunds
~ publication deedglass_pub · set 3 tables
○ public.refunds leaves the publication and keeps replica identity full. Set it back with ALTER TABLE "public"."refunds" REPLICA IDENTITY DEFAULT if nothing else needs it.
plan sha256:f1c301bd8a595656fea264c257bcddd9c86fbc95e0b664d0f5280451bba2b8fc

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '5s';

-- - grants · write public.refunds
REVOKE INSERT, UPDATE, DELETE ON TABLE "public"."refunds" FROM deedglass_agent;

-- ~ publication deedglass_pub · set 3 tables
ALTER PUBLICATION deedglass_pub SET TABLE deedglass.ledger, "public"."customers", "public"."orders";

COMMIT;
