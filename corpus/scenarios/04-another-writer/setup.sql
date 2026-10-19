-- 04-another-writer: ten orders, and a nightly job that archives orders 40 to 44.
CREATE TABLE public.orders (
  id     integer PRIMARY KEY,
  status text    NOT NULL
);
ALTER TABLE public.orders REPLICA IDENTITY FULL;
INSERT INTO public.orders (id, status) SELECT g, 'open' FROM generate_series(40, 49) AS g;
