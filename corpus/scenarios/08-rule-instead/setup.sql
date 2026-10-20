-- 08-rule-instead: 50 customers and an old rule that turns every DELETE into a soft delete.
CREATE TABLE public.customers (
  id         integer     PRIMARY KEY,
  email      text        NOT NULL,
  deleted_at timestamptz
);
ALTER TABLE public.customers REPLICA IDENTITY FULL;

INSERT INTO public.customers (id, email)
SELECT g, 'customer' || g || '@example.com'
FROM generate_series(1, 50) AS g;

CREATE RULE customers_soft_delete AS ON DELETE TO public.customers
DO INSTEAD UPDATE public.customers SET deleted_at = now() WHERE id = OLD.id;
