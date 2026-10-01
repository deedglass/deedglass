-- Declared fixtures for H08–H11 (S18). They need S21's L2 witness: the cases call
-- RequireWitness("S21") first and report "waiting on S21" until it lands. S21 loads this file
-- into the case's database before each of those cases and removes the cases' `needs`.

-- H08: trigger fan-out. The agent updates 1 order; the trigger touches 300 order_events rows, so
-- Postgres commits 301 changed rows while the agent reports 1.
CREATE TABLE public.order_events (
  id       integer PRIMARY KEY,
  order_id integer NOT NULL,
  touched  integer NOT NULL DEFAULT 0
);
INSERT INTO public.order_events (id, order_id) SELECT n, 1 + (n % 50) FROM pg_catalog.generate_series(1, 300) AS n;
CREATE FUNCTION public.fan_out() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  UPDATE public.order_events SET touched = touched + 1;
  RETURN NEW;
END
$fn$;
CREATE TRIGGER orders_fan_out AFTER UPDATE OF status ON public.orders FOR EACH ROW EXECUTE FUNCTION public.fan_out();

-- H09: a trigger changes a column outside the statement's SET list (internal_note).
ALTER TABLE public.orders ADD COLUMN internal_note text NOT NULL DEFAULT '';
CREATE FUNCTION public.set_note() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  NEW.internal_note := 'changed by a trigger';
  RETURN NEW;
END
$fn$;
CREATE TRIGGER orders_set_note BEFORE UPDATE OF total_cents ON public.orders FOR EACH ROW EXECUTE FUNCTION public.set_note();

-- H10: a second session forges the marker with no ledger row. The harness runs, as a separate
-- role, in its own transaction:
--   SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', 'act_<a real action id>');

-- H11: a watched table back on the default replica identity.
--   ALTER TABLE public.customers REPLICA IDENTITY DEFAULT;
