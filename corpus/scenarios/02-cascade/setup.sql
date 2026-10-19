-- 02-cascade: customers and their orders, joined by a foreign key with ON DELETE CASCADE.
-- Customer 7 has 12 orders.
CREATE TABLE public.customers (
  id   integer PRIMARY KEY,
  name text    NOT NULL
);
CREATE TABLE public.orders (
  id          integer PRIMARY KEY,
  customer_id integer NOT NULL REFERENCES public.customers (id) ON DELETE CASCADE,
  status      text    NOT NULL
);
ALTER TABLE public.customers REPLICA IDENTITY FULL;
ALTER TABLE public.orders REPLICA IDENTITY FULL;

INSERT INTO public.customers (id, name)
SELECT g, 'customer ' || g FROM generate_series(1, 20) AS g;
INSERT INTO public.orders (id, customer_id, status)
SELECT g, CASE WHEN g <= 12 THEN 7 ELSE 1 + g % 6 END, 'delivered'
FROM generate_series(1, 60) AS g;
