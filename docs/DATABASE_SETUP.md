# Database setup

Deedglass needs one Postgres database of its own, the **control plane**. PostgreSQL 16, 17 or 18. No superuser is needed at runtime.

## Two databases

| Database | Holds | Created by |
|---|---|---|
| Control plane (`deedglass`) | Deedglass's own state: orgs, agents, actions, deeds | You, with `deploy/postgres/control-plane-roles.sql`, then `deedglass migrate up` |
| A watched customer database | Your data, plus the `deedglass.ledger` table the Postgres connector uses | `deedglass connector postgres init` (S17) |

Both use a schema named `deedglass`, so they must never be the same database. `deedglass migrate up` refuses a database that has `deedglass.ledger` or a `deedglass_pub` publication, and `connector postgres init` (S17) will refuse one that has `deedglass.schema_migrations`.

## Three roles

| Role | Used by | Can |
|---|---|---|
| `deedglass_migrator` | `deedglass migrate` only | Owns the schema and every object in it |
| `deedglass_app` | `deedglass serve` | Read and write rows under row-level security. It owns nothing, so it can't ALTER tables, disable triggers or disable RLS, and it has no UPDATE or DELETE on history |
| `deedglass_backup` | `pg_dump` (S35) | Read-only, with BYPASSRLS |

Neither of the first two is a superuser or has BYPASSRLS, and `deedglass_app` is not a member of the migrator. Role names are cluster-wide; they don't collide with the customer-side roles (`deedglass_owner`, `deedglass_agent`, `deedglass_witness`).

**Why two roles:** if the server owned the tables, an SQL injection could run `ALTER TABLE … DISABLE TRIGGER`. With two roles, that needs the migrator's credentials, and the server never holds them.

## Create the roles and the database

Run once per cluster, as an admin that can create roles and databases:

```sh
psql -v ON_ERROR_STOP=1 -v migrator_password='…' -v app_password='…' \
  -f deploy/postgres/control-plane-roles.sql
```

The file:

```sql
CREATE ROLE deedglass_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'migrator_password';
CREATE ROLE deedglass_app      LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'app_password';
CREATE DATABASE deedglass OWNER deedglass_migrator;
REVOKE ALL ON DATABASE deedglass FROM PUBLIC;
GRANT CONNECT ON DATABASE deedglass TO deedglass_app;
```

Generate each password with `openssl rand -base64 24`, and keep them in your secret store.

## The two URLs

| Setting | Role | Read by |
|---|---|---|
| `DEEDGLASS_MIGRATE_DATABASE_URL` | `deedglass_migrator` | `deedglass migrate` |
| `DEEDGLASS_DATABASE_URL` | `deedglass_app` | `deedglass serve`, `org`, `dev` |

```sh
DEEDGLASS_MIGRATE_DATABASE_URL=postgres://deedglass_migrator:…@db.internal:5432/deedglass?sslmode=verify-full
DEEDGLASS_DATABASE_URL=postgres://deedglass_app:…@db.internal:5432/deedglass?sslmode=verify-full
```

- In dev, `migrate` falls back to `DEEDGLASS_DATABASE_URL` and says so: "Using DEEDGLASS_DATABASE_URL for migrations (dev only)." Prod requires the migrate URL or `--database-url`.
- Prod requires `sslmode=require` or `verify-full` on both, unless the host is loopback or a Unix socket.
- Secrets go in the environment, never in `deedglass.yaml` in prod.

Then:

```sh
deedglass migrate up
deedglass migrate status
```

## The startup role check

`deedglass serve` checks the role it connects as (`store.CheckRuntimeRole`). If that role is a superuser, has BYPASSRLS, or owns schema `deedglass` (or is a member of its owner), row-level security doesn't protect you. Prod refuses to start; dev prints a warning (S03 wires this).

## Managed Postgres

The bootstrap needs an admin role that can create roles and databases. A superuser isn't needed at any point.

| Provider | Run the bootstrap as | Status |
|---|---|---|
| Amazon RDS / Aurora | The master user, a member of `rds_superuser` (C-014) | Expected to work as written; not yet tested |
| Neon | A role created in the console, a member of `neon_superuser`, which has CREATEROLE and CREATEDB (C-015) | Expected to work as written; not yet tested |
| Supabase | The `postgres` role (C-016) | Not confirmed: Supabase's docs don't state whether it can create roles and databases. Test before relying on it, and use a database separate from your app's |

`docs/CLOUD_COMPATIBILITY.md` tracks what has been tested. The C-ids are rows in the claims register; C-014 and C-015 are approved, C-016 isn't confirmed.

## Session settings

Every pooled connection of `deedglass_app` starts with `application_name = deedglass`, `search_path = pg_catalog, pg_temp`, `TimeZone = UTC`, `statement_timeout = 15s`, `idle_in_transaction_session_timeout = 30s` and `lock_timeout = 5s`. They are sent in the startup packet, so a connection pooler in transaction mode (for example PgBouncer) may drop them; point Deedglass at Postgres directly, or at a session-mode pool.
