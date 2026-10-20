-- The agent adds the supplier's 12 new items. Five of the SKUs are already in the catalogue, and
-- the upsert overwrites their names and prices.
INSERT INTO public.products (sku, name, price_cents) VALUES
  ('P096', 'Supplier item 96', 596),
  ('P097', 'Supplier item 97', 597),
  ('P098', 'Supplier item 98', 598),
  ('P099', 'Supplier item 99', 599),
  ('P100', 'Supplier item 100', 600),
  ('P101', 'Supplier item 101', 601),
  ('P102', 'Supplier item 102', 602),
  ('P103', 'Supplier item 103', 603),
  ('P104', 'Supplier item 104', 604),
  ('P105', 'Supplier item 105', 605),
  ('P106', 'Supplier item 106', 606),
  ('P107', 'Supplier item 107', 607)
ON CONFLICT (sku) DO UPDATE SET name = EXCLUDED.name, price_cents = EXCLUDED.price_cents;
