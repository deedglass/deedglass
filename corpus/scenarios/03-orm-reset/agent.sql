-- What an ORM's reset does (prisma migrate reset, db push --force-reset, alembic downgrade base and
-- upgrade): drop the schema, apply the migrations again, run the seed. Written out as SQL.
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
CREATE TABLE public.customers (
  id    integer PRIMARY KEY,
  email text    NOT NULL
);
INSERT INTO public.customers (id, email) VALUES
  (1, 'alice@example.com'), (2, 'bob@example.com'), (3, 'carol@example.com');
