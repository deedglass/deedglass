-- 12-upsert-overwrite: a catalogue of 100 products, P001 to P100.
CREATE TABLE public.products (
  sku         text    PRIMARY KEY,
  name        text    NOT NULL,
  price_cents integer NOT NULL
);
ALTER TABLE public.products REPLICA IDENTITY FULL;

INSERT INTO public.products (sku, name, price_cents)
SELECT 'P' || lpad(g::text, 3, '0'), 'Product ' || g, 1000 + g
FROM generate_series(1, 100) AS g;
