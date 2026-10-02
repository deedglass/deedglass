-- guard on the quoted table
SELECT "Id", "col""x" FROM "My""Schema"."we""ird table" WHERE "Id" = ANY($1::text[]::bigint[]) ORDER BY "Id" FOR UPDATE;
-- $1 = []string{"1"}
