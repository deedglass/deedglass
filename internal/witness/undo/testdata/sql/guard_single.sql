-- guard, one key column, preview (no lock)
SELECT "id", "note", "status" FROM "public"."orders" WHERE "id" = ANY($1::text[]::bigint[]) ORDER BY "id";
-- $1 = []string{"1042", "42", "9"}
