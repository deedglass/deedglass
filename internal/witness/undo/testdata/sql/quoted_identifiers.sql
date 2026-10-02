-- identifiers with double quotes, spaces, capitals and a NUL; a quoted type
UPDATE "My""Schema"."we""ird table" SET "col""x" = $1::text::"My Type" WHERE "Id" = $2::text::bigint;
-- $1 = "it's"
-- $2 = "1"
