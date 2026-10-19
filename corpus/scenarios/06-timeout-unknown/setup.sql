-- 06-timeout-unknown: one account, and a function that makes a statement slow (a lock wait or a
-- busy server, made reproducible).
CREATE TABLE public.accounts (
  id      integer PRIMARY KEY,
  balance integer NOT NULL
);
ALTER TABLE public.accounts REPLICA IDENTITY FULL;
INSERT INTO public.accounts (id, balance) VALUES (1, 500);

CREATE FUNCTION public.corpus_slow(ms integer) RETURNS boolean
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
  PERFORM pg_sleep(ms / 1000.0);
  RETURN true;
END;
$$;
