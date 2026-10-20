-- 07-missing-where: 200 customers on the basic plan.
CREATE TABLE public.customers (
  id    integer PRIMARY KEY,
  email text    NOT NULL,
  plan  text    NOT NULL
);
ALTER TABLE public.customers REPLICA IDENTITY FULL;

INSERT INTO public.customers (id, email, plan)
SELECT g, 'customer' || g || '@example.com', 'basic'
FROM generate_series(1, 200) AS g;
