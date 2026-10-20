-- The agent moves customer 7 to the free plan.
UPDATE public.customers SET plan = 'free' WHERE id = 7;
