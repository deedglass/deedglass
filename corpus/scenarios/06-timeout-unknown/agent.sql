-- The agent credits 100 to account 1. The statement takes 1.5 s.
UPDATE public.accounts SET balance = balance + 100 WHERE id = 1 AND public.corpus_slow(1500);
