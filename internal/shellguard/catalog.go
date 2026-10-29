package shellguard

// Entry is one row of the catalog: a command shape that destroys database data, why, and the page
// that says so. Every entry was checked against its source on the date in Checked.
type Entry struct {
	ID      string `json:"id"`
	Shape   string `json:"shape"`  // what it matches, for people: "prisma migrate reset"
	Why     string `json:"why"`    // completes "Deedglass asks first: <command> …"
	Source  string `json:"source"` // the primary page that describes the behaviour
	Checked string `json:"checked"`

	progs []string   // program names after the path and any @version
	words []string   // non-flag words in order ("*" is any word); with task, any one of them
	task  bool       // words are task names (rails db:drop), matched anywhere, with sub-tasks
	flags [][]string // each group needs one of its flags (--x or --x=value)
	not   []string   // none of these flags
	arg   string     // an argument or VAR=value that must also be present
	first bool       // words start at the first non-flag word (railway delete, not railway volume delete)
	then  []string   // after words, a later non-flag word names one of these (kubectl delete -n prod pvc x)
	// custom matches instead of words, flags and not; it returns the label after the program.
	custom func(args []string) (string, bool)
}

const checked = "2026-10-18"

// checkedCloud is the date the cloud, infrastructure and hosted-database rows were checked.
const checkedCloud = "2026-10-26"

// checkedQA is the date the Compose, Helm and MongoDB rows were checked (QA v2).
const checkedQA = "2026-10-27"

// rakeInfo are rake's options that list or describe tasks, or do a dry run, instead of running one.
var rakeInfo = []string{"-T", "--tasks", "-D", "--describe", "-W", "--where", "-P", "--prereqs", "-n", "--dry-run"}

// Catalog is every shape the shell guard asks about, in the order --list prints them.
var Catalog = []Entry{
	{ID: "prisma-migrate-reset", Shape: "prisma migrate reset",
		Why:    "drops the database, or every table in it, and applies your migrations again, so its data is gone",
		Source: "https://www.prisma.io/docs/orm/reference/prisma-cli-reference#migrate-reset",
		progs:  []string{"prisma"}, words: []string{"migrate", "reset"}},
	{ID: "prisma-db-push-force-reset", Shape: "prisma db push --force-reset",
		Why:    "resets the database before it pushes the schema, which deletes every row",
		Source: "https://www.prisma.io/docs/orm/reference/prisma-cli-reference#db-push",
		progs:  []string{"prisma"}, words: []string{"db", "push"}, flags: [][]string{{"--force-reset"}}},
	{ID: "prisma-db-push-accept-data-loss", Shape: "prisma db push --accept-data-loss",
		Why:    "applies schema changes that Prisma warns will delete data",
		Source: "https://www.prisma.io/docs/orm/reference/prisma-cli-reference#db-push",
		progs:  []string{"prisma"}, words: []string{"db", "push"}, flags: [][]string{{"--accept-data-loss"}}},
	{ID: "prisma-shadow-database-url", Shape: "prisma migrate diff --shadow-database-url (Prisma 6 and earlier)",
		Why:    "resets the database that URL names before it replays your migrations there, so a real database loses its data",
		Source: "https://www.prisma.io/docs/orm/prisma-migrate/understanding-prisma-migrate/shadow-database",
		progs:  []string{"prisma"}, words: []string{"migrate", "diff"}, flags: [][]string{{"--shadow-database-url"}}},
	{ID: "drizzle-kit-push-force", Shape: "drizzle-kit push --force",
		Why:    "applies every schema change without asking, including the ones Drizzle warns delete data",
		Source: "https://orm.drizzle.team/docs/drizzle-kit-push",
		progs:  []string{"drizzle-kit"}, words: []string{"push"}, flags: [][]string{{"--force"}}},
	{ID: "alembic-downgrade-base", Shape: "alembic downgrade base",
		Why:    "runs every downgrade back to an empty schema, which drops the tables your migrations created",
		Source: "https://alembic.sqlalchemy.org/en/latest/tutorial.html#downgrading",
		progs:  []string{"alembic"}, words: []string{"downgrade", "base"}, not: []string{"--sql"}},
	{ID: "flask-db-downgrade-base", Shape: "flask db downgrade base",
		Why:    "runs every Alembic downgrade back to an empty schema, which drops the tables your migrations created",
		Source: "https://flask-migrate.readthedocs.io/en/latest/#command-reference",
		progs:  []string{"flask"}, words: []string{"db", "downgrade", "base"}, not: []string{"--sql"}},
	{ID: "rails-db-drop", Shape: "rails db:drop",
		Why:    "drops the database",
		Source: "https://github.com/rails/rails/blob/main/activerecord/lib/active_record/railties/databases.rake",
		progs:  []string{"rails", "rake"}, task: true, words: []string{"db:drop"}, not: rakeInfo},
	{ID: "rails-db-reset", Shape: "rails db:reset, db:migrate:reset",
		Why:    "drops the database and builds it again, so its data is gone",
		Source: "https://guides.rubyonrails.org/active_record_migrations.html#resetting-the-database",
		progs:  []string{"rails", "rake"}, task: true, words: []string{"db:reset", "db:migrate:reset"}, not: rakeInfo},
	{ID: "rails-db-schema-load", Shape: "rails db:schema:load, db:setup",
		Why:    "loads the schema file into the database, and schema.rb recreates each table with force: :cascade, so their rows are gone",
		Source: "https://api.rubyonrails.org/classes/ActiveRecord/ConnectionAdapters/SchemaStatements.html#method-i-create_table",
		progs:  []string{"rails", "rake"}, task: true, words: []string{"db:schema:load", "db:setup"}, not: rakeInfo},
	{ID: "rails-db-truncate", Shape: "rails db:truncate_all, db:purge, db:seed:replant",
		Why:    "empties every table in the database",
		Source: "https://github.com/rails/rails/blob/main/activerecord/lib/active_record/railties/databases.rake",
		progs:  []string{"rails", "rake"}, task: true, words: []string{"db:truncate_all", "db:purge", "db:seed:replant"}, not: rakeInfo},
	{ID: "rails-migrate-version-0", Shape: "rails db:migrate VERSION=0",
		Why:    "reverts every migration, which drops the tables they created",
		Source: "https://guides.rubyonrails.org/active_record_migrations.html#running-migrations",
		progs:  []string{"rails", "rake"}, task: true, words: []string{"db:migrate"}, arg: "VERSION=0", not: rakeInfo},
	{ID: "knex-rollback-all", Shape: "knex migrate:rollback --all",
		Why:    "rolls back every migration, which drops the tables they created",
		Source: "https://knexjs.org/guide/migrations.html#migration-cli",
		progs:  []string{"knex"}, words: []string{"migrate:rollback"}, flags: [][]string{{"--all"}}},
	{ID: "typeorm-schema-drop", Shape: "typeorm schema:drop",
		Why:    "drops the whole database schema its data source points to, and the data in it",
		Source: "https://github.com/typeorm/typeorm/blob/master/docs/docs/using-cli.md",
		progs:  []string{"typeorm", "typeorm-ts-node-commonjs", "typeorm-ts-node-esm"}, words: []string{"schema:drop"}},
	{ID: "sequelize-db-drop", Shape: "sequelize db:drop",
		Why:    "drops the database",
		Source: "https://github.com/sequelize/cli",
		progs:  []string{"sequelize", "sequelize-cli"}, words: []string{"db:drop"}},
	{ID: "sequelize-undo-all", Shape: "sequelize db:migrate:undo:all",
		Why:    "reverts every migration, which drops the tables they created",
		Source: "https://sequelize.org/docs/v6/other-topics/migrations/#undoing-migrations",
		progs:  []string{"sequelize", "sequelize-cli"}, words: []string{"db:migrate:undo:all"}},
	{ID: "django-flush", Shape: "manage.py flush",
		Why:    "deletes every row from every table",
		Source: "https://docs.djangoproject.com/en/5.2/ref/django-admin/#flush",
		progs:  []string{"manage.py", "django-admin"}, words: []string{"flush"}},
	{ID: "django-migrate-zero", Shape: "manage.py migrate <app> zero",
		Why:    "unapplies every migration of that app, which drops its tables",
		Source: "https://docs.djangoproject.com/en/5.2/ref/django-admin/#migrate",
		progs:  []string{"manage.py", "django-admin"}, words: []string{"migrate", "*", "zero"}, not: []string{"--plan"}},
	{ID: "django-reset-db", Shape: "manage.py reset_db",
		Why:    "drops the database and creates it again, empty",
		Source: "https://django-extensions.readthedocs.io/en/latest/command_extensions.html",
		progs:  []string{"manage.py", "django-admin"}, words: []string{"reset_db"}},
	{ID: "laravel-migrate-fresh", Shape: "artisan migrate:fresh",
		Why:    "drops every table in the database, then migrates, so their rows are gone",
		Source: "https://laravel.com/framework/docs/13.x/migrations",
		progs:  []string{"artisan"}, task: true, words: []string{"migrate:fresh"}},
	{ID: "laravel-db-wipe", Shape: "artisan db:wipe",
		Why:    "drops every table, view and type in the database",
		Source: "https://github.com/laravel/framework/blob/13.x/src/Illuminate/Database/Console/WipeCommand.php",
		progs:  []string{"artisan"}, task: true, words: []string{"db:wipe"}},
	{ID: "laravel-rollback-all", Shape: "artisan migrate:reset, migrate:refresh",
		Why:    "rolls back every migration, which drops the tables they created",
		Source: "https://laravel.com/framework/docs/13.x/migrations",
		progs:  []string{"artisan"}, task: true, words: []string{"migrate:reset", "migrate:refresh"}, not: []string{"--step"}},
	{ID: "ecto-drop", Shape: "mix ecto.drop, ecto.reset (the Phoenix alias that runs ecto.drop)",
		Why:    "drops the database, so its data is gone",
		Source: "https://ecto.hexdocs.pm/Mix.Tasks.Ecto.Drop.html",
		progs:  []string{"mix"}, task: true, words: []string{"ecto.drop", "ecto.reset"}},
	{ID: "ecto-rollback-all", Shape: "mix ecto.rollback --all",
		Why:    "reverts every migration, which drops the tables they created",
		Source: "https://ecto-sql.hexdocs.pm/Mix.Tasks.Ecto.Rollback.html",
		progs:  []string{"mix"}, words: []string{"ecto.rollback"}, flags: [][]string{{"--all"}}},
	{ID: "supabase-db-reset-remote", Shape: "supabase db reset --linked | --db-url",
		Why:    "resets a remote database, the linked project or the one --db-url names, which deletes its data",
		Source: "https://supabase.com/docs/reference/cli/supabase-db-reset",
		progs:  []string{"supabase"}, words: []string{"db", "reset"}, flags: [][]string{{"--linked", "--db-url"}}},
	{ID: "heroku-pg-reset", Shape: "heroku pg:reset",
		Why:    "deletes all the content of the Heroku Postgres database",
		Source: "https://devcenter.heroku.com/articles/managing-heroku-postgres-using-cli#pg-reset",
		progs:  []string{"heroku"}, words: []string{"pg:reset"}},
	{ID: "dropdb", Shape: "dropdb",
		Why:    "drops a whole Postgres database",
		Source: "https://www.postgresql.org/docs/current/app-dropdb.html",
		progs:  []string{"dropdb"}, not: []string{"--version", "-V", "-?"}},
	{ID: "pg-restore-clean", Shape: "pg_restore --clean --dbname",
		Why:    "drops each object in the target database before it restores it",
		Source: "https://www.postgresql.org/docs/current/app-pgrestore.html",
		progs:  []string{"pg_restore"}, flags: [][]string{{"--clean", "-c"}, {"--dbname", "-d"}}},
	{ID: "pg-dump-clean-pipe", Shape: "pg_dump --clean | psql",
		Why:    "writes DROP statements for every object it dumps, and psql runs them on the target database",
		Source: "https://www.postgresql.org/docs/current/app-pgdump.html"},
	{ID: "mysqladmin-drop", Shape: "mysqladmin drop",
		Why:    "drops a whole MySQL database and all its tables",
		Source: "https://dev.mysql.com/doc/refman/8.4/en/mysqladmin.html",
		progs:  []string{"mysqladmin"}, words: []string{"drop"}},

	// Cloud, infrastructure and hosted-database CLIs: each one deletes a database, a volume, a
	// bucket's objects or the infrastructure itself, as its own reference page says.
	{ID: "railway-volume-delete", Shape: "railway volume delete",
		Why:    "deletes a Railway volume and the data on it",
		Source: "https://docs.railway.com/cli", Checked: checkedCloud,
		progs: []string{"railway"}, words: []string{"volume", "delete"}},
	{ID: "railway-delete-project", Shape: "railway delete",
		Why:    "deletes the Railway project",
		Source: "https://docs.railway.com/cli", Checked: checkedCloud,
		progs: []string{"railway"}, words: []string{"delete"}, first: true},
	{ID: "railway-api-delete", Shape: "curl to Railway's GraphQL API with volumeDelete or projectDelete",
		Why:    "calls Railway's API to delete a volume or a project",
		Source: "https://docs.railway.com/integrations/api/manage-volumes", Checked: checkedCloud,
		progs: []string{"curl", "wget", "http", "https", "xh"}, custom: railwayAPIDelete},
	{ID: "supabase-projects-delete", Shape: "supabase projects delete",
		Why:    "deletes the Supabase project",
		Source: "https://supabase.com/docs/reference/cli/supabase-projects-delete", Checked: checkedCloud,
		progs: []string{"supabase"}, words: []string{"projects", "delete"}},
	{ID: "neon-branches-delete", Shape: "neonctl branches delete",
		Why:    "deletes a Neon branch",
		Source: "https://neon.com/docs/cli/branches#delete", Checked: checkedCloud,
		progs: []string{"neonctl", "neon"}, words: []string{"branches", "delete"}},
	{ID: "neon-projects-delete", Shape: "neonctl projects delete",
		Why:    "deletes the Neon project",
		Source: "https://neon.com/docs/cli/projects#delete", Checked: checkedCloud,
		progs: []string{"neonctl", "neon"}, words: []string{"projects", "delete"}},
	{ID: "terraform-destroy", Shape: "terraform destroy, terraform apply -destroy",
		Why:    "destroys every resource the configuration manages",
		Source: "https://developer.hashicorp.com/terraform/cli/commands/destroy", Checked: checkedCloud,
		progs: []string{"terraform"}, custom: terraformDestroy},
	{ID: "pulumi-destroy", Shape: "pulumi destroy",
		Why:    "destroys every resource in the stack",
		Source: "https://www.pulumi.com/docs/iac/cli/commands/pulumi_destroy/", Checked: checkedCloud,
		progs: []string{"pulumi"}, words: []string{"destroy"}, not: []string{"--preview-only"}},
	{ID: "aws-rds-delete", Shape: "aws rds delete-db-instance, delete-db-cluster",
		Why:    "deletes the RDS database and its automated backups",
		Source: "https://docs.aws.amazon.com/cli/latest/reference/rds/delete-db-instance.html", Checked: checkedCloud,
		progs: []string{"aws"}, words: []string{"rds", "delete-db-instance|delete-db-cluster"}},
	{ID: "aws-s3-rb-force", Shape: "aws s3 rb --force",
		Why:    "deletes every object in the bucket, then the bucket",
		Source: "https://docs.aws.amazon.com/cli/latest/reference/s3/rb.html", Checked: checkedCloud,
		progs: []string{"aws"}, words: []string{"s3", "rb"}, flags: [][]string{{"--force"}}},
	{ID: "aws-s3-rm-recursive", Shape: "aws s3 rm --recursive",
		Why:    "deletes every object under the bucket or prefix",
		Source: "https://docs.aws.amazon.com/cli/latest/reference/s3/rm.html", Checked: checkedCloud,
		progs: []string{"aws"}, words: []string{"s3", "rm"}, flags: [][]string{{"--recursive"}}, not: []string{"--dryrun"}},
	{ID: "gcloud-sql-instances-delete", Shape: "gcloud sql instances delete",
		Why:    "deletes the Cloud SQL instance",
		Source: "https://docs.cloud.google.com/sdk/gcloud/reference/sql/instances/delete", Checked: checkedCloud,
		progs: []string{"gcloud"}, words: []string{"sql", "instances", "delete"}},
	{ID: "kubectl-delete-storage", Shape: "kubectl delete namespace | pvc | pv",
		Why:    "deletes a namespace and everything in it, or a volume claim or volume whose storage the Delete reclaim policy deletes too",
		Source: "https://kubernetes.io/docs/concepts/storage/persistent-volumes/", Checked: checkedCloud,
		progs: []string{"kubectl", "oc"}, words: []string{"delete"}, not: []string{"--dry-run"},
		then: []string{"namespace", "namespaces", "ns", "persistentvolumeclaim", "persistentvolumeclaims", "pvc", "persistentvolume", "persistentvolumes", "pv"}},
	{ID: "docker-volume-rm", Shape: "docker volume rm | remove | prune",
		Why:    "removes Docker volumes and the data in them",
		Source: "https://docs.docker.com/reference/cli/docker/volume/rm/", Checked: checkedCloud,
		progs: []string{"docker", "podman"}, words: []string{"volume", "rm|remove|prune"}},
	{ID: "compose-down-volumes", Shape: "docker compose down -v | --volumes",
		Why:    "removes the Compose project's named volumes and its containers' anonymous volumes, and the data in them",
		Source: "https://docs.docker.com/reference/cli/docker/compose/down/", Checked: checkedQA,
		progs: []string{"docker", "podman", "nerdctl", "docker-compose", "podman-compose"}, custom: composeDownVolumes},
	{ID: "helm-uninstall", Shape: "helm uninstall | delete | del | un",
		Why:    "removes every resource of the release, and the release history",
		Source: "https://helm.sh/docs/helm/helm_uninstall/", Checked: checkedQA,
		progs: []string{"helm"}, custom: helmUninstall},
	{ID: "fly-volumes-destroy", Shape: "fly volumes destroy",
		Why:    "destroys the Fly volume and permanently deletes its data",
		Source: "https://docs.fly.io/flyctl/cmd/fly_volumes_destroy", Checked: checkedCloud,
		progs: []string{"fly", "flyctl"}, words: []string{"volumes", "destroy"}},
	{ID: "pscale-database-delete", Shape: "pscale database delete",
		Why:    "deletes the PlanetScale database",
		Source: "https://planetscale.com/docs/cli/database", Checked: checkedCloud,
		progs: []string{"pscale"}, words: []string{"database", "delete"}},
	{ID: "turso-db-destroy", Shape: "turso db destroy",
		Why:    "destroys the Turso database",
		Source: "https://docs.turso.tech/cli/db/destroy", Checked: checkedCloud,
		progs: []string{"turso"}, words: []string{"db", "destroy"}},
	{ID: "redis-cli-flush", Shape: "redis-cli flushall, flushdb",
		Why:    "deletes every key in Redis, so its data and cached state are gone",
		Source: "https://redis.io/docs/latest/commands/flushall/", Checked: checkedQA,
		progs: []string{"redis-cli"}, words: []string{"flushall|flushdb"}},

	// MongoDB's shell with a drop in --eval.
	{ID: "mongo-drop", Shape: "mongosh or mongo --eval with dropDatabase() or a collection's drop()",
		Why:    "removes the database and its data files, or the collection and its indexes",
		Source: "https://www.mongodb.com/docs/manual/reference/method/db.dropDatabase/", Checked: checkedQA,
		progs: []string{"mongosh", "mongo"}, custom: mongoDrop},

	// SQL that psql (-c, a heredoc, a here-string, or echo, printf or cat piped in), mysql or
	// mariadb (-e or stdin), sqlite3 or prisma db execute --stdin would run, read by sqlguard.
	{ID: "sql-drop", Shape: "DROP TABLE, SCHEMA, DATABASE, OWNED or MATERIALIZED VIEW; DROP TYPE, DOMAIN or EXTENSION … CASCADE",
		Why:    "deletes the object and the rows in it",
		Source: "https://www.postgresql.org/docs/current/sql-droptable.html"},
	{ID: "sql-truncate", Shape: "TRUNCATE …",
		Why:    "deletes every row in the table",
		Source: "https://www.postgresql.org/docs/current/sql-truncate.html"},
	{ID: "sql-delete-all", Shape: "DELETE with no WHERE, or WHERE true",
		Why:    "deletes every row in the table",
		Source: "https://www.postgresql.org/docs/current/sql-delete.html"},
	{ID: "sql-update-all", Shape: "UPDATE with no WHERE, or WHERE true",
		Why:    "changes every row in the table",
		Source: "https://www.postgresql.org/docs/current/sql-update.html"},
	{ID: "sql-drop-column", Shape: "ALTER TABLE … DROP [COLUMN]",
		Why:    "deletes the column and the data in it",
		Source: "https://www.postgresql.org/docs/current/sql-altertable.html"},
	{ID: "sql-do-drop", Shape: "DO block containing DROP or TRUNCATE",
		Why:    "runs a code block that contains DROP or TRUNCATE",
		Source: "https://www.postgresql.org/docs/current/sql-do.html"},
	{ID: "too-long", Shape: "a command over 1 MiB",
		Why:    "is too long to read, so Deedglass can't say what it runs",
		Source: "https://github.com/deedglass/deedglass/blob/main/docs/SHELL_GUARD.md"},
	{ID: "too-nested", Shape: "commands nested more than 12 deep (substitutions, subshells, sh -c)",
		Why:    "nests commands too deeply to read, so Deedglass can't say what all of it runs",
		Source: "https://github.com/deedglass/deedglass/blob/main/docs/SHELL_GUARD.md"},
}

func init() {
	for i := range Catalog {
		if Catalog[i].Checked == "" {
			Catalog[i].Checked = checked
		}
	}
}

// entry returns the catalog row with this id; ids are compile-time constants, so a miss is a bug.
func entry(id string) *Entry {
	for i := range Catalog {
		if Catalog[i].ID == id {
			return &Catalog[i]
		}
	}
	panic("shellguard: no catalog entry " + id)
}

// progEntries maps a program name to its catalog rows.
var progEntries = func() map[string][]*Entry {
	m := map[string][]*Entry{}
	for i := range Catalog {
		for _, p := range Catalog[i].progs {
			m[p] = append(m[p], &Catalog[i])
		}
	}
	return m
}()
