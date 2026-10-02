-- update a moved row back, its key included
UPDATE "public"."orders" SET "id" = $1::text::bigint, "note" = $2::text::text, "status" = $3::text::text WHERE "id" = $4::text::bigint;
-- $1 = "42"
-- $2 = NULL
-- $3 = "paid"
-- $4 = "1042"
