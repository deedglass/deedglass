-- 05-partial-failure: two accounts and a transfers table whose next id is already taken.
CREATE TABLE public.accounts (
  id      integer PRIMARY KEY,
  balance integer NOT NULL CHECK (balance >= 0)
);
CREATE TABLE public.transfers (
  id      integer PRIMARY KEY,
  from_id integer NOT NULL REFERENCES public.accounts (id),
  to_id   integer NOT NULL REFERENCES public.accounts (id),
  amount  integer NOT NULL
);
ALTER TABLE public.accounts REPLICA IDENTITY FULL;
ALTER TABLE public.transfers REPLICA IDENTITY FULL;
INSERT INTO public.accounts (id, balance) VALUES (1, 500), (2, 0);
INSERT INTO public.transfers (id, from_id, to_id, amount) VALUES (1001, 2, 1, 50);
