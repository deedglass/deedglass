-- guard, one key column, inside the undo (FOR UPDATE)
SELECT "id", "note", "status" FROM "public"."orders" WHERE "id" = ANY($1::text[]::bigint[]) ORDER BY "id" FOR UPDATE;
-- $1 = []string{"1042", "42", "9"}
