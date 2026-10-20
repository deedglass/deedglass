-- What the orders API runs for POST /v1/orders/42/ship. The agent calls the API; it never sees
-- this statement.
UPDATE public.orders SET status = 'shipped' WHERE id = 42;
