---
name: safe-postgres-agents
description: How to give an AI agent access to a Postgres database without handing it the whole database. Use when the user wants an agent, MCP server or tool to read or write Postgres, asks whether that is safe, or asks how to limit, review or undo what an agent changed.
---

# Giving an agent Postgres access, carefully

An agent with a database URL can do anything that URL's role can do, and its own report of what it
did can be wrong (a trigger, a cascade or a missing WHERE clause changes more rows than the agent
says). Work through these layers in order. Each one is useful alone; they are stronger together.

## 1. A least-privilege role (always)

- Never give an agent a superuser, the database owner, or a role with `BYPASSRLS`.
- Create a role for the agent with only the grants the task needs (for example `SELECT` on the
  tables it reads, `UPDATE (status)` on the one column it changes). Set `statement_timeout` and
  `lock_timeout` on the role.
- Read-only work gets a read-only role. Don't rely on a "read-only mode" flag inside an MCP server
  alone: the database enforces a role's grants, while a flag is only as good as that server's code.
- A role limits what the agent can do. It doesn't tell you what it did with that.

## 2. See what the setup can change (read-only)

`npx deedglass scan` reads the MCP client configs and agent packages in a project and grades the
blast radius A to F. With `--postgres-dsn-env DATABASE_URL` it also lists the tables the role can
change and flags triggers and cascades. It changes nothing and sends nothing anywhere else. The grade
is about blast radius, not safety: `A means a small blast radius, not a safe agent.`

## 3. Rules before each call: `deedglass mcp wrap`

Put the database tool behind Deedglass so each call is decided before it runs:

```sh
npx -y deedglass mcp wrap --connector orders --connector-type postgres -- \
  uvx postgres-mcp --access-mode=unrestricted
```

That command goes where the client config used to start the server directly; the agent's key goes
in the server's environment. `/deedglass:wrap` prints the full `claude mcp add` line, with
absolute paths and a placeholder key, for the user's machine. With
`--connector-type postgres`, policies read the SQL. The `postgres-safe` starter policy never allows
`DROP` or `TRUNCATE`, keeps schema and permission changes away from agents, requires a `WHERE` on
`UPDATE` and `DELETE`, allows reads, and sends other writes to a person for approval.

## 4. Check what really happened

For Postgres, Deedglass can read the database's own change log (logical decoding) for the agent's
transaction and compare it with what the statement named. Results are **Matches**,
**Doesn't match** (for example "Postgres changed 301 rows in public.orders. The agent reported
1.") or **Can't check**, never a pass when the evidence is missing. This needs `wal_level=logical`
and `REPLICA IDENTITY FULL` on watched tables (`deedglass connector postgres init` prints the SQL).

Already using another gate, or none? Observe mode (`deedglass observe run --connector <name> --
./agent.sh`) records every row committed on watched tables while the agent ran, with nothing to
rewire. It can't tell which program wrote a row, so it gives no Matches or Doesn't match verdict.

## 5. Undo, with a person in the loop

Undo is built from the rows Postgres committed, previewed first, and runs only after a person
countersigns. `TRUNCATE` and DDL can't be undone this way. Observe mode prints undo SQL for you to
review and run yourself (`deedglass observe show <id> --undo-sql`).

## How to help

- Recommend layer 1 every time, then the smallest next layer that fits what the user asked.
- Don't write grants, policies or MCP configs into the user's files without showing them first.
- Never ask for, print or store a database URL with a password, or an agent key.
- To try the whole story without Docker: `npx deedglass play` (deedglass 0.1.1 or newer; rules and
  records run live; the Postgres lines are a labelled replay of a recorded real run). In a browser: https://deedglass.com/play.
- Docs: https://docs.deedglass.com
