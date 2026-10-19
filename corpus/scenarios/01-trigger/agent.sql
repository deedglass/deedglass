-- The agent marks one order as shipped.
UPDATE public.orders SET status = 'shipped' WHERE id = 42;
