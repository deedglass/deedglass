-- Acme Store seed (S15): deterministic (setseed(0.42) and fixed dates), fictional people only,
-- emails at example.test. 120 customers, 40 products, 500 orders (ids 1 to 500, 37 cancelled,
-- order 42 paid), about 1,250 order items, 60 refunds. Two loads give identical rows.
\set ON_ERROR_STOP on
BEGIN;
SELECT pg_catalog.setseed(0.42) AS seeded \gset

INSERT INTO public.customers (id, name, email, city, created_at)
SELECT n,
       f.first || ' ' || l.last,
       lower(f.first) || '.' || lower(l.last) || n || '@example.test',
       (ARRAY['Austin', 'Pune', 'Leeds', 'Lyon', 'Osaka', 'Denver', 'Kochi', 'Porto', 'Tartu', 'Quito'])[1 + (n % 10)],
       timestamptz '2026-01-05 09:00:00+00' + (n * interval '1 day 3 hours')
FROM pg_catalog.generate_series(1, 120) AS n
CROSS JOIN LATERAL (SELECT (ARRAY['Asha', 'Ben', 'Chen', 'Dara', 'Elif', 'Femi', 'Gita', 'Hugo', 'Ines', 'Jun',
                                  'Kai', 'Lena', 'Milo', 'Nora', 'Omar', 'Pia'])[1 + (n % 16)] AS first) AS f
CROSS JOIN LATERAL (SELECT (ARRAY['Rao', 'Silva', 'Novak', 'Okafor', 'Lind', 'Moreau', 'Tanaka', 'Kowal', 'Reyes',
                                  'Haddad', 'Berg', 'Iyer', 'Costa', 'Fischer', 'Yilmaz'])[1 + ((n * 7) % 15)] AS last) AS l;

INSERT INTO public.products (id, name, price_cents)
SELECT n,
       (ARRAY['Desk lamp', 'Notebook', 'Water bottle', 'Backpack', 'Desk mat', 'Headphones', 'Mug', 'Plant pot',
              'Phone stand', 'Cable kit'])[1 + ((n - 1) % 10)] || ' ' || (ARRAY['Mini', 'Classic', 'Pro', 'Max'])[1 + ((n - 1) / 10)],
       500 + (pg_catalog.random() * 9500)::integer
FROM pg_catalog.generate_series(1, 40) AS n;

-- Orders over the 90 days before 2026-10-05, oldest first.
INSERT INTO public.orders (id, customer_id, status, created_at, updated_at)
SELECT n,
       1 + (pg_catalog.random() * 119)::integer,
       'paid',
       timestamptz '2026-07-07 08:00:00+00' + (n * interval '4 hours 19 minutes'),
       timestamptz '2026-07-07 08:00:00+00' + (n * interval '4 hours 19 minutes')
FROM pg_catalog.generate_series(1, 500) AS n;

-- Statuses, fixed by a hash order: 37 cancelled, 20 refunded, 60 pending, 160 shipped, the rest paid.
-- Order 42 stays paid (the demo marks it shipped).
WITH ranked AS (
  SELECT id, row_number() OVER (ORDER BY pg_catalog.md5('acme-' || id)) AS r
  FROM public.orders WHERE id <> 42
)
UPDATE public.orders AS o
SET status = CASE WHEN r.r <= 37 THEN 'cancelled' WHEN r.r <= 57 THEN 'refunded' WHEN r.r <= 117 THEN 'pending'
                  WHEN r.r <= 277 THEN 'shipped' ELSE 'paid' END
FROM ranked AS r WHERE o.id = r.id;

-- One to four items per order.
INSERT INTO public.order_items (id, order_id, product_id, quantity, unit_price_cents)
SELECT row_number() OVER (ORDER BY o.id, k), o.id, p.id, 1 + (pg_catalog.random() * 2)::integer, p.price_cents
FROM public.orders AS o
CROSS JOIN LATERAL pg_catalog.generate_series(1, 1 + (o.id % 4)) AS k
CROSS JOIN LATERAL (SELECT id, price_cents FROM public.products WHERE id = 1 + ((o.id * 13 + k * 7) % 40)) AS p;

UPDATE public.orders AS o
SET total_cents = s.total
FROM (SELECT order_id, sum(quantity * unit_price_cents)::integer AS total FROM public.order_items GROUP BY order_id) AS s
WHERE s.order_id = o.id;

-- 60 refunds: every refunded order in full, and 40 partial refunds on shipped orders.
INSERT INTO public.refunds (id, order_id, amount_cents, reason, created_at)
SELECT row_number() OVER (ORDER BY x.order_id), x.order_id, x.amount, x.reason, x.at
FROM (
  (SELECT id AS order_id, total_cents AS amount, 'Returned' AS reason, created_at + interval '6 days' AS at
   FROM public.orders WHERE status = 'refunded' ORDER BY id)
  UNION ALL
  (SELECT id, greatest(100, total_cents / 4), 'Arrived damaged', created_at + interval '3 days'
   FROM public.orders WHERE status = 'shipped' ORDER BY pg_catalog.md5('refund-' || id) LIMIT 40)
) AS x;

COMMIT;
ANALYZE;
