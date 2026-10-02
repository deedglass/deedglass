-- insert a removed row with a GENERATED ALWAYS identity, non-UTF-8 text from hex
INSERT INTO "public"."orders" ("id", "note", "status") OVERRIDING SYSTEM VALUE VALUES ($1::text::bigint, $2::text::text, $3::text::text);
-- $1 = "9"
-- $2 = "\xffA"
-- $3 = "paid"
