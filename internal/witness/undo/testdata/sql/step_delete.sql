-- delete a created row
DELETE FROM "public"."line_items" WHERE "order_id" = $1::text::bigint AND "line" = $2::text::integer;
-- $1 = "7"
-- $2 = "2"
