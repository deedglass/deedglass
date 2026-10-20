-- 09-unwatched-table: customers and their API keys. A legacy trigger revokes (deletes) a
-- customer's keys when the customer moves to the free plan. It runs as its owner.
CREATE TABLE public.customers (
  id   integer PRIMARY KEY,
  plan text    NOT NULL
);
CREATE TABLE public.api_keys (
  id          integer PRIMARY KEY,
  customer_id integer NOT NULL REFERENCES public.customers (id),
  label       text    NOT NULL
);
ALTER TABLE public.customers REPLICA IDENTITY FULL;
ALTER TABLE public.api_keys REPLICA IDENTITY FULL;

INSERT INTO public.customers (id, plan) SELECT g, 'team' FROM generate_series(1, 100) AS g;
INSERT INTO public.api_keys (id, customer_id, label)
SELECT g, CASE WHEN g <= 5 THEN 7 ELSE 10 + g % 90 END, 'key ' || g
FROM generate_series(1, 300) AS g;

CREATE FUNCTION public.revoke_keys_on_free() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
BEGIN
  IF NEW.plan = 'free' AND OLD.plan <> 'free' THEN
    DELETE FROM public.api_keys WHERE customer_id = NEW.id;
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER customers_revoke_keys_on_free
AFTER UPDATE OF plan ON public.customers
FOR EACH ROW EXECUTE FUNCTION public.revoke_keys_on_free();
