~ role deedglass_agent · attributes nosuperuser · leaves pg_write_all_data
- grants · write public.products
~ publication deedglass_pub · set 4 tables
plan sha256:32c4783eab139853c00bad042fb1a4e27c414fdc1ebd8c9ce98dd1df7ce11243

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '5s';

-- ~ role deedglass_agent · attributes nosuperuser · leaves pg_write_all_data
ALTER ROLE deedglass_agent NOSUPERUSER;
REVOKE "pg_write_all_data" FROM deedglass_agent;

-- - grants · write public.products
REVOKE UPDATE, TRUNCATE ON TABLE "public"."products" FROM deedglass_agent;

-- ~ publication deedglass_pub · set 4 tables
ALTER PUBLICATION deedglass_pub SET TABLE deedglass.ledger, "public"."customers", "public"."orders", "public"."refunds";

COMMIT;
