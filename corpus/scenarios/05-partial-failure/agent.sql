-- The agent moves 100 from account 1 to account 2 with three statements and no transaction, and
-- stops at the first error (psql -v ON_ERROR_STOP=1). Each statement commits on its own.
UPDATE public.accounts SET balance = balance - 100 WHERE id = 1;
INSERT INTO public.transfers (id, from_id, to_id, amount) VALUES (1001, 1, 2, 100);
UPDATE public.accounts SET balance = balance + 100 WHERE id = 2;
