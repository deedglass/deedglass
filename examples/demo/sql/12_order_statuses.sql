-- Puts every order's status back to the seed's (02_seed.sql's hash order: 37 cancelled,
-- 20 refunded, 60 pending, 160 shipped, the rest paid; order 42 paid), for a new hero take (S21).
-- make demo-order-reset runs it as acme_owner; the image runs it once on a fresh volume, where it
-- changes nothing. Statuses only: totals, items and refunds are untouched.
\set ON_ERROR_STOP on
BEGIN;
WITH ranked AS (
  SELECT id, row_number() OVER (ORDER BY pg_catalog.md5('acme-' || id)) AS r
  FROM public.orders WHERE id <> 42
)
UPDATE public.orders AS o
SET status = CASE WHEN r.r <= 37 THEN 'cancelled' WHEN r.r <= 57 THEN 'refunded' WHEN r.r <= 117 THEN 'pending'
                  WHEN r.r <= 277 THEN 'shipped' ELSE 'paid' END
FROM ranked AS r WHERE o.id = r.id AND o.status IS DISTINCT FROM (CASE WHEN r.r <= 37 THEN 'cancelled' WHEN r.r <= 57 THEN 'refunded'
                  WHEN r.r <= 117 THEN 'pending' WHEN r.r <= 277 THEN 'shipped' ELSE 'paid' END);
UPDATE public.orders SET status = 'paid' WHERE id = 42 AND status IS DISTINCT FROM 'paid';
COMMIT;
