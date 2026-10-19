-- 03-orm-reset: a database with 500 real customers, which the agent takes for a development copy.
CREATE TABLE public.customers (
  id    integer PRIMARY KEY,
  email text    NOT NULL
);
ALTER TABLE public.customers REPLICA IDENTITY FULL;
INSERT INTO public.customers (id, email)
SELECT g, 'person' || g || '@example.com' FROM generate_series(1, 500) AS g;
