# The shell guard

`deedglass guard shell` is a Claude Code `PreToolUse` hook. Before Claude Code runs a Bash command, it
reads the command and, when part of it would destroy database data or infrastructure (`prisma migrate reset`,
`alembic downgrade base`, `dropdb`, a `psql -c` with `DROP TABLE`, `railway volume delete`,
`terraform destroy`), it asks you first, with one sentence that names Deedglass and the reason:

```text
Deedglass asks first: prisma db push --force-reset resets the database before it pushes the schema, which deletes every row.
```

It is a second check, not a sandbox. It reads the command as written, compares it with a catalog of
commands, and never runs or expands anything. It needs no server, no account and no network. In the
Deedglass plugin for Claude Code it is on (`ask`) by default; everywhere else it is off until you
turn it on. `npx deedglass protect` turns it on in every coding agent it finds
([protect.md](protect.md)).

## Why the shell

Public Claude Code issues report agents destroying databases from the shell, not through a database
tool. Each command below is in `internal/shellguard/testdata/incidents.txt`, and each one gets an ask:

| Issue | What ran |
|---|---|
| [claude-code#80868](https://github.com/anthropics/claude-code/issues/80868) | `prisma migrate diff --shadow-database-url "$(grep DATABASE_URL .env …)"` inside a compound command; the database was restored from a backup |
| [claude-code#36183](https://github.com/anthropics/claude-code/issues/36183) | `npx prisma db push --force-reset` |
| [claude-code#33183](https://github.com/anthropics/claude-code/issues/33183) | a destructive `prisma db push` |
| [claude-code#26913](https://github.com/anthropics/claude-code/issues/26913) | `alembic downgrade base` |
| [claude-code#46684](https://github.com/anthropics/claude-code/issues/46684) | `ALTER TABLE … DROP COLUMN` on a production database over SSH (the issue names the statement, not the client) |

## Turn it on

You need a `deedglass` binary on your `PATH` that has the command (`deedglass help guard shell`
prints its help; see [INSTALL.md](INSTALL.md)). The guard runs it locally for each Bash command.

### With the Deedglass plugin

The plugin (`/plugin install deedglass@deedglass`) carries the hook, on (`ask`) by default. Change it
with one setting in `.claude/settings.json` (this project) or `~/.claude/settings.json` (every project):

```json
{
  "env": { "DEEDGLASS_SHELL_GUARD": "off" }
}
```

- unset, `ask`, or any value but the ones below: a matching command waits for your yes in the
  permission prompt.
- `deny`: a matching command doesn't run, and Claude reads the reason, which ends "if it is
  intended, ask the user to run it themselves."
- `off` (or `0`, `false`, `no`): the hook reads its input and answers nothing. It doesn't run
  `deedglass` at all.

Restart Claude Code after changing it.

If the guard can't check, because `deedglass` isn't on your `PATH` or is an older one without
`guard shell`, it never blocks the command. Left unset, it then answers nothing, so installing the
plugin without the binary changes nothing. Set to `ask`, it exits 0 with no decision and a note to
you that it checked nothing; with `deny` it prints why on stderr and exits 1, which Claude Code
shows as a non-blocking error. The plugin's hook also sets `"onFailure": "continue"` (Claude
Code v2.1.295 or later), so a hook that can't start or times out lets the command go ahead.

### On Windows

The plugin's hook is `sh "${CLAUDE_PLUGIN_ROOT}/hooks/shell-guard.sh"`, which Claude Code runs with
Git Bash on Windows; Git for Windows provides `sh`. Without Git Bash, Claude Code registers no Bash
tool, so a hook on `Bash` never runs and the guard checks nothing. If it did run there, in
PowerShell, it would fail to find `sh`, and `"onFailure": "continue"` lets the command go ahead
instead of blocking it. To use the guard on Windows, install Git for Windows and put a `deedglass`
binary on your `PATH`.

### Without the plugin

`deedglass protect --client claude-code` adds the hook to `~/.claude/settings.json` for you, with a
backup and `--undo` ([protect.md](protect.md)). Or add it to a settings file yourself:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{ "type": "command", "command": "deedglass guard shell", "timeout": 10 }]
      }
    ]
  }
}
```

Add `--deny` to the command to deny instead of ask, or `--no-files` to read only the command.
If `deedglass` isn't on your `PATH`, the shell can't start it, and Claude Code shows a non-blocking
error before each Bash command. Add `"onFailure": "continue"` to the hook if it should never block
a command when it fails, including on Windows without Git Bash.

### In Codex, Gemini CLI, GitHub Copilot and Cursor

The same guard can be installed as each of these agents' before-a-command hook: `deedglass guard shell
--client codex` (or `gemini`, `copilot`, `cursor`) reads that agent's input and answers in its
format. `deedglass guard shell --print-config --client <name>` prints the hook config for it.
Codex and Gemini CLI can't ask, so there it denies; VS Code has no snippet yet. Each client's file,
format and limits: [INSTALL_CLIENTS.md](INSTALL_CLIENTS.md#the-shell-guard-in-each-coding-agent).

### Try it first

```sh
deedglass guard shell --command "cd api && npx prisma migrate reset --force"
deedglass guard shell --list
```

`--command` prints the sentence the hook would show and each part it found, and exits 1 when it
would ask (0 when it wouldn't). `--json` prints `decision`, `reason` and `findings`.

## What it answers

The hook follows the contract on Claude Code's [hooks page](https://code.claude.com/docs/en/hooks)
(checked 2026-10-18); the other clients' contracts are in
[INSTALL_CLIENTS.md](INSTALL_CLIENTS.md#the-shell-guard-in-each-coding-agent). It reads one JSON object on stdin and uses `tool_name`, `tool_input.command`
and `cwd`.

- **Something matches:** it writes one object and exits 0:
  `{"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "ask", "permissionDecisionReason": "Deedglass asks first: …"}}`.
  For `ask` Claude Code shows the reason to you in the permission prompt; for `deny` it shows it to
  Claude. With more than one match the sentence names the first and counts the rest.
- **Nothing matches, another tool, or another event:** it writes nothing and exits 0. That is not an
  approval: Claude Code's own permission rules decide as if the hook weren't there.
- **It never answers `allow`.** Your deny and ask rules still apply whatever a hook returns, and when
  several hooks answer, Claude Code takes the strictest.
- **It can't read its input:** it prints why on stderr and exits 1, which Claude Code shows as a
  non-blocking error and goes on. It never exits 2, which Claude Code reads as a block.

## How it reads a command

A small reader written for this (no new dependency) splits the command line the way `sh`
and `bash` would, far enough to find every command in it:

- `&&`, `||`, `;`, `&`, pipes, newlines, `( … )` subshells and `{ …; }` groups;
- quotes and backslashes (`p''risma`, `"npx" "prisma"`, `\prisma` all read as `prisma`), `$'…'`
  strings and line continuations;
- command substitutions (`$(…)`, backticks, `<(…)`) as commands of their own, since the shell runs
  them first;
- `VAR=value` prefixes (`DATABASE_URL=… npx prisma …`);
- `sh -c`, `bash -lc`, `zsh -c`, `eval`, `ssh host '…'`, `watch '…'` and `find … -exec` strings,
  read again as command lines;
- heredocs, here-strings (`<<< "…"`) and `echo`, `printf` or `cat` piped into `psql`, `mysql`,
  `sqlite3` or a shell;
- wrappers before the real program: `npx`, `pnpm exec`, `yarn`, `bunx`, `uv run`, `poetry run`,
  `bundle exec`, `python -m`, `docker compose exec`, `kubectl exec`, `sudo`, `env`, `timeout`,
  `dotenv` and others; a path or `@version` on the program (`./node_modules/.bin/prisma`,
  `prisma@5.22.0`) is dropped.

SQL that `psql` (`-c`, `-f`, stdin), `mysql` or `mariadb` (`-e`, stdin), `sqlite3` or
`prisma db execute` would run goes through Deedglass's SQL reader (`internal/sqlguard`), the same
one the policy engine uses, so `DELETE FROM users` asks and `DELETE FROM users WHERE id = 42` doesn't.

From the hook's working directory it also reads, at most once each and only regular files up to
1 MiB: the `package.json` script that `npm run`, `pnpm`, `yarn` or `bun` would run, a shell script
run by path or by `sh`/`bash`, and the SQL files `psql -f`, `psql < file` or `cat file | psql`
would read. `--no-files` turns this off.

## What it asks about

Every row was checked against its source on 2026-10-18, and the cloud, infrastructure and hosted
database rows (from `railway-volume-delete` on) on 2026-10-26; `deedglass guard shell --list` prints
the same table with the reason and date for each.

| Id | Shape | Source |
|---|---|---|
| `prisma-migrate-reset` | prisma migrate reset | [source](https://www.prisma.io/docs/orm/reference/prisma-cli-reference#migrate-reset) |
| `prisma-db-push-force-reset` | prisma db push --force-reset | [source](https://www.prisma.io/docs/orm/reference/prisma-cli-reference#db-push) |
| `prisma-db-push-accept-data-loss` | prisma db push --accept-data-loss | [source](https://www.prisma.io/docs/orm/reference/prisma-cli-reference#db-push) |
| `prisma-shadow-database-url` | prisma migrate diff --shadow-database-url (Prisma 6 and earlier) | [source](https://www.prisma.io/docs/orm/prisma-migrate/understanding-prisma-migrate/shadow-database) |
| `drizzle-kit-push-force` | drizzle-kit push --force | [source](https://orm.drizzle.team/docs/drizzle-kit-push) |
| `alembic-downgrade-base` | alembic downgrade base | [source](https://alembic.sqlalchemy.org/en/latest/tutorial.html#downgrading) |
| `flask-db-downgrade-base` | flask db downgrade base | [source](https://flask-migrate.readthedocs.io/en/latest/#command-reference) |
| `rails-db-drop` | rails db:drop | [source](https://github.com/rails/rails/blob/main/activerecord/lib/active_record/railties/databases.rake) |
| `rails-db-reset` | rails db:reset, db:migrate:reset | [source](https://guides.rubyonrails.org/active_record_migrations.html#resetting-the-database) |
| `rails-db-schema-load` | rails db:schema:load, db:setup | [source](https://api.rubyonrails.org/classes/ActiveRecord/ConnectionAdapters/SchemaStatements.html#method-i-create_table) |
| `rails-db-truncate` | rails db:truncate_all, db:purge, db:seed:replant | [source](https://github.com/rails/rails/blob/main/activerecord/lib/active_record/railties/databases.rake) |
| `rails-migrate-version-0` | rails db:migrate VERSION=0 | [source](https://guides.rubyonrails.org/active_record_migrations.html#running-migrations) |
| `knex-rollback-all` | knex migrate:rollback --all | [source](https://knexjs.org/guide/migrations.html#migration-cli) |
| `typeorm-schema-drop` | typeorm schema:drop | [source](https://github.com/typeorm/typeorm/blob/master/docs/docs/using-cli.md) |
| `sequelize-db-drop` | sequelize db:drop | [source](https://github.com/sequelize/cli) |
| `sequelize-undo-all` | sequelize db:migrate:undo:all | [source](https://sequelize.org/docs/v6/other-topics/migrations/#undoing-migrations) |
| `django-flush` | manage.py flush | [source](https://docs.djangoproject.com/en/5.2/ref/django-admin/#flush) |
| `django-migrate-zero` | manage.py migrate &lt;app&gt; zero | [source](https://docs.djangoproject.com/en/5.2/ref/django-admin/#migrate) |
| `django-reset-db` | manage.py reset_db | [source](https://django-extensions.readthedocs.io/en/latest/command_extensions.html) |
| `laravel-migrate-fresh` | artisan migrate:fresh | [source](https://laravel.com/docs/13.x/migrations) |
| `laravel-db-wipe` | artisan db:wipe | [source](https://github.com/laravel/framework/blob/13.x/src/Illuminate/Database/Console/WipeCommand.php) |
| `laravel-rollback-all` | artisan migrate:reset, migrate:refresh | [source](https://laravel.com/docs/13.x/migrations) |
| `ecto-drop` | mix ecto.drop, ecto.reset (the Phoenix alias that runs ecto.drop) | [source](https://hexdocs.pm/ecto/Mix.Tasks.Ecto.Drop.html) |
| `ecto-rollback-all` | mix ecto.rollback --all | [source](https://hexdocs.pm/ecto_sql/Mix.Tasks.Ecto.Rollback.html) |
| `supabase-db-reset-remote` | supabase db reset --linked \| --db-url | [source](https://supabase.com/docs/reference/cli/supabase-db-reset) |
| `heroku-pg-reset` | heroku pg:reset | [source](https://devcenter.heroku.com/articles/managing-heroku-postgres-using-cli#pg-reset) |
| `dropdb` | dropdb | [source](https://www.postgresql.org/docs/current/app-dropdb.html) |
| `pg-restore-clean` | pg_restore --clean --dbname | [source](https://www.postgresql.org/docs/current/app-pgrestore.html) |
| `pg-dump-clean-pipe` | pg_dump --clean \| psql | [source](https://www.postgresql.org/docs/current/app-pgdump.html) |
| `mysqladmin-drop` | mysqladmin drop | [source](https://dev.mysql.com/doc/refman/8.4/en/mysqladmin.html) |
| `railway-volume-delete` | railway volume delete | [source](https://docs.railway.com/reference/cli-api) |
| `railway-delete-project` | railway delete | [source](https://docs.railway.com/reference/cli-api) |
| `railway-api-delete` | curl to Railway's GraphQL API with volumeDelete or projectDelete | [source](https://docs.railway.com/guides/manage-volumes) |
| `supabase-projects-delete` | supabase projects delete | [source](https://supabase.com/docs/reference/cli/supabase-projects-delete) |
| `neon-branches-delete` | neonctl branches delete | [source](https://neon.com/docs/cli/branches#delete) |
| `neon-projects-delete` | neonctl projects delete | [source](https://neon.com/docs/cli/projects#delete) |
| `terraform-destroy` | terraform destroy, terraform apply -destroy | [source](https://developer.hashicorp.com/terraform/cli/commands/destroy) |
| `pulumi-destroy` | pulumi destroy | [source](https://www.pulumi.com/docs/iac/cli/commands/pulumi_destroy/) |
| `aws-rds-delete` | aws rds delete-db-instance, delete-db-cluster | [source](https://docs.aws.amazon.com/cli/latest/reference/rds/delete-db-instance.html) |
| `aws-s3-rb-force` | aws s3 rb --force | [source](https://docs.aws.amazon.com/cli/latest/reference/s3/rb.html) |
| `aws-s3-rm-recursive` | aws s3 rm --recursive | [source](https://docs.aws.amazon.com/cli/latest/reference/s3/rm.html) |
| `gcloud-sql-instances-delete` | gcloud sql instances delete | [source](https://docs.cloud.google.com/sdk/gcloud/reference/sql/instances/delete) |
| `kubectl-delete-storage` | kubectl delete namespace \| pvc \| pv | [source](https://kubernetes.io/docs/concepts/storage/persistent-volumes/) |
| `docker-volume-rm` | docker volume rm \| remove \| prune | [source](https://docs.docker.com/reference/cli/docker/volume/rm/) |
| `fly-volumes-destroy` | fly volumes destroy | [source](https://docs.fly.io/flyctl/volumes-destroy) |
| `pscale-database-delete` | pscale database delete | [source](https://planetscale.com/docs/reference/database) |
| `turso-db-destroy` | turso db destroy | [source](https://docs.turso.tech/cli/db/destroy) |
| `sql-drop` | DROP TABLE, SCHEMA, DATABASE, OWNED or MATERIALIZED VIEW; DROP TYPE, DOMAIN or EXTENSION … CASCADE | [source](https://www.postgresql.org/docs/current/sql-droptable.html) |
| `sql-truncate` | TRUNCATE … | [source](https://www.postgresql.org/docs/current/sql-truncate.html) |
| `sql-delete-all` | DELETE with no WHERE, or WHERE true | [source](https://www.postgresql.org/docs/current/sql-delete.html) |
| `sql-update-all` | UPDATE with no WHERE, or WHERE true | [source](https://www.postgresql.org/docs/current/sql-update.html) |
| `sql-drop-column` | ALTER TABLE … DROP [COLUMN] | [source](https://www.postgresql.org/docs/current/sql-altertable.html) |
| `sql-do-drop` | DO block containing DROP or TRUNCATE | [source](https://www.postgresql.org/docs/current/sql-do.html) |
| `too-long` | a command over 1 MiB | [source](https://github.com/deedglass/deedglass/blob/main/docs/SHELL_GUARD.md) |
| `too-nested` | commands nested more than 12 deep (substitutions, subshells, sh -c) | [source](https://github.com/deedglass/deedglass/blob/main/docs/SHELL_GUARD.md) |

It asks about data, not every change: `DROP INDEX`, `DROP VIEW`, `DROP FUNCTION` and
`ALTER TABLE … DROP CONSTRAINT` pass, and so do migrations that go forward (`prisma migrate deploy`,
`alembic upgrade head`, `rails db:migrate`). `--help`, `--sql` (alembic's offline mode), `--plan`
and rake's listing flags pass too.

The cloud rows pass their safe neighbours: `terraform plan` and `terraform plan -destroy`,
`pulumi destroy --preview-only`, `aws s3 rm --recursive --dryrun`, `aws s3 rb` without `--force`
(it only removes an empty bucket), `kubectl get ns`, `kubectl delete pod`, any `kubectl delete`
with `--dry-run`, and `supabase db reset` on the local stack.

Not in the catalog: `railway down`. Railway's CLI reference says it removes the latest deployment,
not a volume or a project, so it passes. Nor is `drizzle-kit drop`: current drizzle-kit has no `drop` command
([drizzle-kit overview](https://orm.drizzle.team/docs/kit-overview), checked 2026-10-18), so there
is nothing to match.

## False positives

The test corpus `internal/shellguard/testdata/ordinary.txt` holds over 200 ordinary development
commands (git, npm and pnpm, go, cargo, docker and kubectl, `psql` with `SELECT`, forward
migrations, and near misses such as `git commit -m "Stop running prisma migrate reset"` or
`grep -rn "DROP TABLE" migrations/`). The test fails if any of them asks; the budget is zero.

## What it doesn't see

It reads text, so anything decided only when the command runs passes untouched. Each case below is
pinned in `internal/shellguard/testdata/limits.txt`; a change that starts catching one moves it to
the asks.

- A value known only at run time: `CMD="prisma migrate reset"; npx $CMD`, or `npx prisma migrate "$ACTION"`.
- SQL produced by another command: `psql -c "$(printf 'DROP TABLE %s' users)"`.
- Encoded text: `echo … | base64 -d | sh`.
- An interpreter running its own code: `python -c "import os; os.system('dropdb app')"`,
  `node -e "…sequelize.drop()"`.
- A Makefile target (`make db-reset`), an alias or shell function defined earlier in the session.
- A command substitution inside an unquoted heredoc body (bash runs it; the guard reads the body as text).
- A database reached over HTTP (`curl -X DELETE …`), or through an MCP tool rather than the shell.
  The one HTTP call it reads is a `curl` (or `wget`, `http`, `xh`) to Railway's GraphQL API whose
  arguments hold `volumeDelete` or `projectDelete`; a body read from a file (`-d @body.json`) passes.
- `npx @railway/cli volume delete` is not read as `railway`; the installed `railway` command is.
- Files: only those named above, only from the hook's working directory, and never through a
  variable or a glob.

It also doesn't decide which database a command points at: `dropdb app_test` asks like
`dropdb app_production`. A command over 1 MiB, or nested more than 12 levels deep, asks because
it can't be read in full.

For changes that do reach Postgres, the Gate and Observe mode show what Postgres committed
([OBSERVE.md](OBSERVE.md)).

## Tests

`go test ./internal/shellguard ./cmd/deedglass -run 'Incidents|Asks|Ordinary|Limits|GuardShell'`
runs the incident commands, the asks corpus (every catalog row through wrappers and compound forms),
the ordinary corpus, the limits and the hook contract. `-run Client` runs every client's snippet
and answer goldens (`internal/shellguard/testdata/clients`). `FuzzCheck` runs in `make fuzz-regress`.
