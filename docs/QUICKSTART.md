# Quickstart

Five minutes from nothing to a blocked `DROP TABLE` and an `UPDATE` that Postgres's own change log
confirms.

**Prerequisites:** Docker running, about 2 GiB of free disk, and the `deedglass` CLI.

## 1. Install

```sh
npx -y deedglass version            # the npm wrapper (no install script; one platform binary)
# or
go install github.com/deedglass/deedglass/cmd/deedglass@latest
```

## 2. deedglass init

In a new directory:

```sh
deedglass init
```

```
deedglass init · setting up Deedglass in ~/code/acme-agents

  Will create  .deedglass/ (keys, local secrets) · Postgres 18.6 in Docker (127.0.0.1:55432)
               org "Local" · agent quickstart-agent · policy postgres-safe (enforced)
               demo database
Continue? [Y/n]

✓ Docker                  Docker 29.8.1 · darwin/arm64 · ports 8080, 55432 free
✓ Local state             .deedglass/ (0700)
✓ Keys                    vault key · pepper · database passwords · seal key sk_01m449… · .deedglass/ (0600)
✓ Postgres                127.0.0.1:55432 · wal_level logical · demo database on 127.0.0.1:55442
✓ Deedglass               http://127.0.0.1:8080 · schema at 0026
✓ Org                     Local · owner you@localhost.test · admin key in .deedglass/admin.key
✓ Demo database           500 orders · connector postgres ready for L2
✓ Agent                   quickstart-agent · key saved to .deedglass/quickstart.key (shown once below)
    dg_test_••••••••   copy it now; it isn't shown again
✓ Policies                postgres-safe v1 · enforce (replaces default-deny, the onboarding policy)
✓ Client                  none chosen · the Claude Code line is under Next
✓ Smoke · postgres.query  L0 · VERIFIED · deed_01m4…
✕ Smoke · DROP TABLE      Blocked by policy "block-destructive-sql": DROP and TRUNCATE are never allowed for agents.
  That ✕ is the point: Deedglass stopped it before Postgres saw it.
◐ Smoke · UPDATE 1 row    waiting for approval · countersigning as you@localhost.test (dev only)
✓ Smoke · UPDATE 1 row    L2 · VERIFIED · Postgres committed the row in public.orders · deed_01m4…

✓ Sealed. Your first deed is in the ledger.
Ready in 8 s.
```

With the Postgres image already pulled this takes 6 to 8 seconds on an M-series Mac
(`bench/results/2026-10-05-darwin-arm64-quickstart.json`); the first pull adds its download. Run
`deedglass init` again and it says `Nothing to change.`; run `deedglass doctor` to check
everything (16 checks). Port 8080 or 55432 taken? `deedglass init --listen 127.0.0.1:8090
--pg-port 55440`. Every step is in [INIT_AND_DOCTOR.md](INIT_AND_DOCTOR.md).

## 3. Connect your agent

**Claude Code**

```sh
claude mcp add --transport http deedglass http://127.0.0.1:8080/mcp/native --header "Authorization: Bearer $(cat .deedglass/quickstart.key)"
```

**Claude Desktop, Cursor, VS Code, Windsurf:** `deedglass init --client cursor` (or the others)
prints the config snippet to paste.

**Python**

```sh
pip install "deedglass[langchain]"
export DEEDGLASS_API_KEY="$(cat .deedglass/quickstart.key)"
python examples/python/langchain_orders.py --scripted
```

**TypeScript**

```sh
npm install @deedglass/sdk
export DEEDGLASS_API_KEY="$(cat .deedglass/quickstart.key)"
node examples/ts/quickstart/index.ts
```

## 4. Watch a blocked action and a verified one

```sh
export DEEDGLASS_API_KEY="$(cat .deedglass/admin.key)"
deedglass actions list
```

The `DROP TABLE` is sealed as denied: the deed records the policy's rule and that nothing ran. The
`UPDATE` waited for a person (postgres-safe sends writes to approval), ran once approved, and was
witnessed at L2: Postgres's change log shows exactly one row of `public.orders` changed.

## 5. Look in the Console

Open http://127.0.0.1:8080/app and sign in as `you@localhost.test`. Dev mode writes mail to the log,
so the sign-in link is in `.deedglass/serve.log`.

## What just happened

1. The agent asked the Gate to run a tool; the Gate authenticated its key.
2. The enforced policy decided: allow, deny or require approval.
3. A denied call was sealed as a deed and never reached Postgres.
4. An allowed call ran through the Postgres connector as the least-privilege role `deedglass_agent`.
5. The Witness compared what the tool reported with Postgres's change log (L2).
6. The result was sealed into the org's deed chain, which anyone can check offline.

## What it doesn't do

- The local stack is for a laptop: 127.0.0.1 only, dev mode, throwaway data.
- `deedglass init` doesn't write your MCP client's config file; it prints what to paste.
- It doesn't use an existing database yet, and doesn't run on Windows (use WSL 2).

## Next steps

- [Policies](POLICIES_GUIDE.md): edit `postgres-safe` for your tables.
- [deedglass scan](SCAN.md): see what your own agent setup can change, graded A to F.
- [The Python SDK](SDK_PYTHON.md) and [the TypeScript SDK](sdk/typescript.md).
