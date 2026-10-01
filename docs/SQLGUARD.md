# sqlguard: what Deedglass reads in your SQL

**Status:** describes `sqlguard.Version = "1"` (S11). Owner: S11. Code: `internal/sqlguard`. Decision: ADR-033 (PROPOSED).
**Audience:** anyone who writes a policy for a Postgres connector, and anyone who reviews one.

## 1. What it is

sqlguard is a small, pure-Go tokenizer and classifier. Before the Gate runs an agent's SQL, sqlguard reads the string and fills the `sql` facts that policies use (`sql.kind`, `sql.category`, `sql.multi`, `sql.unbounded_write`, `sql.ambiguous` and the rest).

It is a **conservative pre-execution classifier, not a Postgres parser** (ADR-033). It knows the Postgres lexical rules (comments, strings, dollar quotes, identifiers) and enough of the grammar to find the statements, their leading keywords, CTEs, `WHERE` clauses, tables and function calls. It does not build a parse tree and it does not know every statement Postgres accepts.

Its one rule: **when it isn't sure, it says `ambiguous`**, with a named reason. The starter policies deny ambiguous SQL, so anything sqlguard can't read with confidence does not run.

Properties the tests hold it to:

- **Pure.** It classifies and never executes. It imports no database driver (`internal/archtest`).
- **Lossless.** Concatenating the tokens gives back the input, byte for byte (`FuzzTokenize`).
- **Fail closed.** Anything it can't read is `ambiguous`, with category `unknown`.
- **Linear.** One pass over the input; inputs over 256 KiB are refused as `too_large`.
- **Never panics.** A panic inside `Analyze` becomes `ambiguous` with the reason `internal_error`.

## 2. How it runs

`policy.Prepare` calls `sqlguard.Analyze` only when the tool's `connector_type` is `postgres` and `args.sql` is a string. It passes the connector's default schema. For every other action, `sql.present` is false, `sql.kind` is `none` and `sql.category` is `none`.

The facts are derived, not stored. The Gate stores the raw input (the SQL string) with the action; a replay (S27) runs the classifier again, and every decision and trace records `sqlguard_version`.

## 3. The facts

Every field of `sqlguard.Facts`, as policies see it under `sql.` ([POLICY_LANGUAGE.md](POLICY_LANGUAGE.md) section 7.1). Lists are never null. **`kinds` is in statement order; every other list is sorted and has no duplicates.**

| Field | Type | Meaning |
|---|---|---|
| `present` | bool | the action carries SQL (always true from `Analyze`) |
| `kind` | string | the first statement's kind (section 4); `empty` when the string holds only whitespace or comments; `none` when the action has no SQL |
| `category` | string | the most severe category across all statements (section 5) |
| `kinds` | list | every non-empty statement's kind, in order |
| `statements` | int | the number of non-empty statements. `SELECT 1;` is 1; `-- only a comment` is 0 |
| `multi` | bool | `statements > 1` |
| `writes` | bool | anything in the string may change data or schema (section 6) |
| `write_kinds` | list | the row-level writes: `insert`, `update`, `delete`, `copy` only |
| `has_where` | bool | every top-level UPDATE or DELETE has its own `WHERE` at its own depth |
| `unbounded_write` | bool | any UPDATE or DELETE, **at any depth, including inside a CTE**, lacks a `WHERE` or has a trivial one |
| `where_trivial` | bool | any UPDATE or DELETE `WHERE` is trivially true (best effort, section 10) |
| `has_returning` | bool | a `RETURNING` clause anywhere |
| `cte_write` | bool | a data-modifying `WITH` query |
| `select_into` | bool | `SELECT … INTO`, which creates a table (category `ddl`) |
| `copy_program` | bool | `COPY … PROGRAM` (category `server_code`) |
| `explain_analyze` | bool | `EXPLAIN` with `ANALYZE` on, which runs the statement |
| `locks_rows` | bool | `FOR UPDATE`, `FOR SHARE`, or `LOCK` |
| `tables` | list | `schema.table` names from `FROM`, `JOIN`, `UPDATE`, `INTO`, `DELETE FROM`, `USING`, `TRUNCATE`, `MERGE INTO`, `TABLE`, `COPY`, `LOCK` and DDL targets, at every depth. Unquoted names are lowercased, quoted names are kept as written, an unqualified name gets the default schema, and CTE names are left out |
| `tables_complete` | bool | false when the extractor met something it can't follow (section 10) |
| `functions` | list | every function called, as written (lowercased unless quoted) |
| `dangerous_functions` | list | functions on the dangerous list (section 8) |
| `write_functions` | list | functions on the write list (section 8) |
| `unknown_functions` | list | functions on no list (section 8) |
| `ambiguous` | bool | sqlguard isn't sure; fail closed |
| `reasons` | list | why it is ambiguous (section 7) |
| `bytes` | int | the SQL's size in bytes |

## 4. Statement kinds

sqlguard takes the first keyword of each statement, after any leading `(` and any `WITH` list.

| Leading keyword(s) | `kind` | Category |
|---|---|---|
| `select`, `values`, `table` | `select` | read (write if anything in it writes; ddl with `INTO`) |
| `insert`, `update`, `delete`, `merge` | the same | write |
| `copy … to` / `copy … from` / `copy … program` | `copy` | read / write / server_code |
| `create`, `alter`, `drop`, `truncate`, `comment`, `security label`, `import foreign schema` | the same (`security_label`, `import`) | ddl |
| `grant`, `revoke`, `reassign owned`, `drop owned`, `alter default privileges`, `create`/`alter`/`drop` `role`/`user`/`group`, `alter system`, `set role`, `set session authorization`, `reset role` | the leading word | privilege |
| `do`, `call`, `load` | the same | server_code |
| `set`, `reset`, `discard`, `listen`, `unlisten`, `notify`, `prepare`, `deallocate`, `declare`, `fetch`, `move`, `close` | the same | session |
| `show` | `show` | read |
| `begin`, `start`, `commit`, `end`, `rollback`, `abort`, `savepoint`, `release`, `prepare transaction`, `commit prepared`, `rollback prepared` | the leading word | transaction |
| `vacuum`, `analyze`, `cluster`, `reindex`, `checkpoint`, `refresh materialized view`, `lock` | the same | maintenance (`lock` also sets `locks_rows`) |
| `explain` | `explain` | read; with `ANALYZE` on, the inner statement's category, and `explain_analyze` |
| `execute` | `execute` | unknown, ambiguous (`execute_prepared`) |
| anything else | `other` | unknown, ambiguous (`unknown_statement`) |

## 5. Categories and their order

From most to least severe:

`unknown` > `server_code` > `privilege` > `ddl` > `write` > `transaction` > `session` > `maintenance` > `read` > `none`

`category` is the most severe category across all statements. Two rules differ from a naive reading, and both are on purpose:

1. **When `ambiguous` is true, `category` is always `unknown` and `writes` is true**, whatever the statements looked like. A policy that checks `sql.category == "read"` can never let an ambiguous string through.
2. **A string with several statements is at least `transaction`, so it is never `read`.** Postgres runs a multi-statement simple query as one implicit transaction block. `SELECT 1; SELECT 2` has category `transaction`; `SELECT 1; DROP TABLE orders` has category `ddl`.

## 6. When `writes` is true

`writes` is true when any of these holds:

- a statement's category is `write`, `ddl`, `privilege`, `server_code` or `unknown`;
- a CTE writes (`WITH d AS (DELETE …) SELECT …`);
- `SELECT … INTO` (it creates a table);
- `EXPLAIN ANALYZE` of a write (it runs the write);
- a function on the write list is called (`SELECT nextval('orders_id_seq')`);
- the string is ambiguous.

`write_kinds` is narrower. It lists only row-level writes: `insert`, `update`, `delete` and `copy` (for `COPY … FROM`). A `MERGE` adds the kinds of its `WHEN` actions, and `INSERT … ON CONFLICT DO UPDATE` adds both `insert` and `update`. DDL, privilege changes and write functions set `writes` but add nothing to `write_kinds`.

## 7. Ambiguity reasons

Each reason in `sql.reasons`, in plain English. The Console shows the same sentences (S25).

| Reason | What it means |
|---|---|
| `unterminated_string` | A quoted string `'…'` never closes. Close the quote and send the statement again. |
| `unterminated_comment` | A `/* … */` comment never closes. Comments nest in Postgres, so every `/*` needs its own `*/`. |
| `unterminated_dollar_quote` | A dollar-quoted string (`$tag$ … $tag$`) never closes. Check that the closing tag matches the opening one. |
| `unterminated_identifier` | A quoted identifier `"…"` never closes. Close the quote and send the statement again. |
| `non_ascii_outside_literal` | The SQL contains characters outside quotes that Postgres would not read as keywords (for example a Cyrillic "о" in "DRОP"). Deedglass blocks what it can't read with confidence. |
| `nul_byte` | The SQL contains a NUL byte, which Postgres refuses. Remove it and send the statement again. |
| `backslash_command` | The SQL contains a psql backslash command (`\copy`, `\!`). Those run in psql, not in Postgres; send plain SQL. |
| `backslash_in_string` | A backslash inside '…' changes meaning when standard_conforming_strings is off. Use E'…' for escapes. |
| `execute_prepared` | EXECUTE runs a statement Deedglass can't see. Send the statement itself. |
| `unknown_statement` | sqlguard doesn't recognise this statement, or its shape (an unbalanced parenthesis, a lone `$`, a `BEGIN ATOMIC` body). Send one plain statement, or ask an admin to run it as a reviewed migration. |
| `too_large` | The SQL is over 256 KiB. Send a smaller statement. |
| `too_many_statements` | The string holds more than 1,000 statements. Send one statement per action. |
| `too_deep` | The SQL nests parentheses more than 256 deep. Simplify the statement. |
| `internal_error` | sqlguard failed while reading this SQL, so Deedglass treats it as unreadable. Report it with the statement; the action did not run. |

## 8. Functions

A function is an identifier followed by `(` that isn't a keyword or a type name (`exists (`, `cast(`, `varchar(10)`, `over (` are not functions). Each function is looked up by its last name part, so `pg_catalog.pg_read_file` is `pg_read_file`. The lists live in `internal/sqlguard/functions.go`.

**Dangerous functions** (`dangerous_functions`). The starter policies deny any of these.

| Function | Why |
|---|---|
| `pg_read_file` | reads files on the database server |
| `pg_read_binary_file` | reads files on the database server |
| `pg_ls_dir` | lists directories on the database server |
| `pg_stat_file` | reads file metadata on the database server |
| `lo_import` | reads a server file into a large object |
| `lo_export` | writes a large object to a server file |
| `dblink*` | opens connections to other databases and runs SQL there |
| `query_to_xml*` | runs the SQL text it is given (dynamic SQL) |
| `cursor_to_xml` | reads from a cursor that can hide any query |
| `pg_terminate_backend` | ends other sessions |
| `pg_cancel_backend` | cancels other sessions' queries |
| `pg_reload_conf` | reloads the server configuration |
| `pg_rotate_logfile` | rotates the server log |
| `pg_promote` | promotes a standby to primary |
| `pg_switch_wal` | forces a WAL switch |
| `set_config` | changes settings such as search_path or role for the session |
| `pg_sleep*` | holds a connection open (denial of service) |
| `pg_advisory_*` containing `lock` | takes or releases advisory locks (denial of service) |
| `pg_logical_emit_message` | writes logical messages, which can forge Deedglass's own markers |
| `pg_*` containing `replication_slot` | creates, advances or drops replication slots |
| `pg_replication_origin_*` | changes replication origins |
| `pg_file_write` | writes server files (adminpack) |
| `pg_file_rename` | renames server files (adminpack) |
| `pg_file_unlink` | deletes server files (adminpack) |

**Write functions** (`write_functions`). They change data even inside a `SELECT`, so they set `writes`.

| Function | Why |
|---|---|
| `nextval` | advances a sequence |
| `setval` | sets a sequence |
| `lo_create` | creates a large object |
| `lo_unlink` | deletes a large object |
| `lo_put` | writes into a large object |
| `lo_truncate` | truncates a large object |
| `pg_notify` | sends a notification to listeners |

**The safe list** holds 238 common built-ins that only read: aggregates, window functions, string, math, date and time, JSON and JSONB, array, `coalesce`/`nullif`/`greatest`/`least`, `now`, `gen_random_uuid`, `merge_action` and the type-name functions (`int4(…)`, `text(…)`). `sqlguard.SafeFunctionCount()` returns the exact number, and a test keeps it at 150 or more.

**How to read `unknown_functions`.** Any function on none of the three lists is unknown. That includes every function in your own schemas (`app.cleanup_everything`) and every built-in sqlguard doesn't list. sqlguard can't see a function's body, so **a policy should treat an unknown function as possibly writing**. The starter `allow-reads` rule requires `size(sql.unknown_functions) == 0`; a read that calls an unknown function falls through to the next rule. To allow a specific function of your own, name it in a rule: `sql.unknown_functions.all(f, f in ["app.order_total"])`.

## 9. Assumptions

- **`standard_conforming_strings = on`.** It is the Postgres default since 9.1, and S17's Postgres connector pins it on every session. sqlguard reads `'…'` strings with that setting. A backslash inside a plain string would read differently with it off, so it is `ambiguous` (`backslash_in_string`). Use `E'…'` for escapes.
- **The default schema** is the connector's configured schema, `public` unless you set another (`tool.default_schema` in the input). An unqualified table name gets it. S17's connector pins `search_path` to match.
- **One SQL string per action, sent with the simple query protocol.** That is how the Postgres connector runs it (S17), and it is what the differential test uses.

## 10. Best effort fields

- **`where_trivial`** recognises `WHERE true`, `WHERE 1`, `WHERE <n> = <same n>`, `WHERE '<s>' = '<same s>'`, `WHERE <col> = <same col>` and `WHERE NOT false`. A trivially true condition written any other way (`WHERE id > -1`, `WHERE id IS NOT NULL`) is not detected. The L2 witness still sees how many rows changed.
- **`tables`** is best effort. `tables_complete` is false when the extractor meets a function in `FROM`, `LATERAL`, `ROWS FROM`, a three-part name, server code (`DO`, `CALL`, `LOAD`), or a DDL target that isn't a relation. A policy that allow-lists tables should also require `sql.tables_complete`, as the starter `allow-listed-writes` rule does.

## 11. Limits

| Limit | Value | Over it |
|---|---|---|
| Input size | 256 KiB (262,144 bytes) | `too_large` (the string is not tokenized at all) |
| Statements | 1,000 | `too_many_statements` (`statements` stops at 1,000) |
| Parenthesis depth | 256 | `too_deep` |

## 12. What sqlguard can't see

sqlguard sees the statement. It cannot see what Postgres does because of it:

- **triggers** on the tables a statement touches;
- **rules** (`CREATE RULE`) that rewrite a statement;
- **function bodies**, including your own functions and any built-in on no list;
- **`DO` bodies** and procedures (they are `server_code`, and the starter policies deny them);
- **settings changed earlier in a session**, such as `search_path` or `standard_conforming_strings` (S17's connector pins both per session).

That gap is what the L2 witness covers (S20 and S21). The Witness reads what Postgres actually committed from its change stream and compares it with what the action said it would do: "the agent reported 1 row; Postgres changed 301". sqlguard decides before execution; the Witness checks after it.

## 13. The corpus and the differential test

`internal/sqlguard/testdata/corpus.yaml` is the executable description of sqlguard: 206 named cases, each with the facts that matter for it. `TestCorpus` checks every case and prints a diff per field.

| Family | Cases | Run against Postgres |
|---|---|---|
| read | 28 | 26 |
| write | 24 | 22 |
| cte | 8 | 8 |
| ddl | 13 | 11 |
| privilege | 12 | 4 |
| server_code | 10 | 0 |
| functions | 21 | 9 |
| multi | 16 | 16 |
| obfuscation | 40 | 35 |
| session | 11 | 10 |
| transaction | 11 | 9 |
| maintenance | 7 | 5 |
| limits | 5 | 2 |
| **total** | **206** | **157** |

**The differential test** (`corpus_integration_test.go`, run by `make integration` on Postgres 18 and by `make matrix` on 16, 17 and 18) uses real Postgres as the oracle:

1. It resets schema `public` on a throwaway cluster and runs the corpus `setup`.
2. For every case marked `differential: true`, it runs the SQL inside `BEGIN … ROLLBACK` with the simple query protocol.
3. It checks that Postgres's command tags (`SELECT 5`, `DELETE 3`, `MERGE 2` …) agree with `kinds`, and that the number of results equals `statements`.
4. A case Postgres rejects as a syntax error must be `ambiguous` or kind `other`.

It never runs `server_code` cases, dangerous functions, `COPY … PROGRAM`, role changes or `ALTER SYSTEM`; the test refuses a case that would. Cases that need a newer server carry `min_pg` (`RETURNING old/new` needs 18; `MERGE … RETURNING` needs 17). The per-version results are in each session report.

## 14. Versioning

`sqlguard.Version` is `"1"`. It changes whenever the classification output changes for any input: a new kind, a new reason, a function moved between lists, a fix that changes a fact. Every decision records it as `sqlguard_version`, and the explain trace prints it (`sqlguard 1`), so a replay says which classifier it used.

## 15. Performance

`Analyze` is one pass over the input and allocates in proportion to the number of tokens. Throughput at 200 B, 4 KiB and 256 KiB is measured in `bench/results/` by `make bench-eval`.

## 16. What it doesn't do

- It doesn't parse SQL into a tree, and it doesn't accept or reject SQL the way Postgres does. Valid SQL can be ambiguous; SQL Postgres would refuse can still classify (Postgres then refuses it).
- It doesn't run anything, connect to a database or read your schema. It can't tell a table from a view, or know which columns exist.
- It doesn't see triggers, rules, function bodies or session state (section 12).
- It doesn't decide. It reports facts; your policy decides what to do with them.
- It doesn't know dialects other than Postgres.
