-- The agent marks order 42 as shipped.
UPDATE public.orders SET status = 'shipped' WHERE id = 42;
