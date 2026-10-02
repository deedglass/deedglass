~ publication deedglass_pub · stored generated columns
plan sha256:e9fdc4c2835be0a6637392aad1753c937100bdaf90d62e7e64c830ad7a832ca1

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '5s';

-- ~ publication deedglass_pub · stored generated columns
ALTER PUBLICATION deedglass_pub SET (publish_generated_columns = stored);

COMMIT;
