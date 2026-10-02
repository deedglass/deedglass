-- guard, a composite key
SELECT "order_id", "line", "price", "qty", "tags" FROM "public"."line_items" WHERE ("order_id", "line") IN (SELECT u.c1::bigint, u.c2::integer FROM pg_catalog.unnest($1::text[], $2::text[]) AS u(c1, c2)) ORDER BY "order_id", "line" FOR UPDATE;
-- $1 = []string{"10", "7", "7"}
-- $2 = []string{"1", "1", "2"}
