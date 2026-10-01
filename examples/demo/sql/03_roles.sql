-- Acme Store roles (S15). acme_owner owns the demo tables. Both demo MCP entries (orders through
-- Deedglass and orders-direct without it) connect as acme_owner on purpose: the only difference on
-- screen is Deedglass in the path. Agents should never get owner credentials in real life; S17's
-- native Postgres connector uses a least-privilege role with its password in the vault.
--
-- The password is local-only, generated into examples/demo/.env by make demo-up, and read here
-- from the container's ACME_OWNER_PASSWORD (psql's \getenv).
\set ON_ERROR_STOP on
\getenv acme_owner_password ACME_OWNER_PASSWORD
SELECT :'acme_owner_password' <> '' AS has_password \gset
\if :has_password
\else
  \echo 'ACME_OWNER_PASSWORD is empty. Run make demo-up, which writes examples/demo/.env.'
  \quit 3
\endif
BEGIN;
SELECT NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'acme_owner') AS need_role \gset
\if :need_role
  CREATE ROLE acme_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD :'acme_owner_password';
\endif
ALTER DATABASE acme OWNER TO acme_owner;
ALTER SCHEMA public OWNER TO acme_owner;
ALTER TABLE public.customers OWNER TO acme_owner;
ALTER TABLE public.products OWNER TO acme_owner;
ALTER TABLE public.orders OWNER TO acme_owner;
ALTER TABLE public.order_items OWNER TO acme_owner;
ALTER TABLE public.refunds OWNER TO acme_owner;
COMMIT;
