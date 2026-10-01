-- Acme Store controlled functions (S17): what postgres.call may run. SECURITY DEFINER, owned by the
-- non-superuser acme_owner, search_path pinned, EXECUTE revoked from PUBLIC (deedglass connector
-- postgres init grants it to deedglass_agent, because the connector's config allow-lists it).
-- Loaded after 03_roles.sql, which creates acme_owner.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
CREATE FUNCTION public.ship_order(p_order_id bigint) RETURNS TABLE (id integer, status text)
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $fn$
  UPDATE public.orders AS o SET status = 'shipped', updated_at = pg_catalog.now()
   WHERE o.id = p_order_id AND o.status = 'paid'
  RETURNING o.id, o.status
$fn$;
ALTER FUNCTION public.ship_order(bigint) OWNER TO acme_owner;
REVOKE ALL ON FUNCTION public.ship_order(bigint) FROM PUBLIC;
COMMIT;
