-- 01-trigger: an orders table with a legacy AFTER UPDATE trigger that ships the rest of a batch
-- when one order in it ships. 1,000 orders; order 42 is in batch 7 with 300 other open orders.
CREATE TABLE public.orders (
  id          integer PRIMARY KEY,
  batch       integer NOT NULL,
  status      text    NOT NULL,
  total_cents integer NOT NULL
);
ALTER TABLE public.orders REPLICA IDENTITY FULL;

INSERT INTO public.orders (id, batch, status, total_cents)
SELECT g, CASE WHEN g = 42 OR g BETWEEN 501 AND 800 THEN 7 ELSE 1 + g % 6 END, 'open', 1000 + g
FROM generate_series(1, 1000) AS g;

CREATE FUNCTION public.ship_open_batch() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF pg_trigger_depth() > 1 THEN
    RETURN NULL;
  END IF;
  IF NEW.status = 'shipped' AND OLD.status <> 'shipped' THEN
    UPDATE public.orders SET status = 'shipped'
    WHERE batch = NEW.batch AND status = 'open' AND id <> NEW.id;
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER orders_ship_open_batch
AFTER UPDATE OF status ON public.orders
FOR EACH ROW EXECUTE FUNCTION public.ship_open_batch();
