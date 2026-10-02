-- insert a removed row, types with modifiers and arrays
INSERT INTO "public"."line_items" ("code", "line", "order_id", "price", "qty", "seen_at", "tags") VALUES ($1::text::character varying(20), $2::text::integer, $3::text::bigint, $4::text::numeric(12,2), $5::text::integer, $6::text::timestamp with time zone, $7::text::integer[]);
-- $1 = "AB-1"
-- $2 = "2"
-- $3 = "7"
-- $4 = "12.50"
-- $5 = "3"
-- $6 = "2026-10-07 13:12:09.114+00"
-- $7 = "{1,2}"
