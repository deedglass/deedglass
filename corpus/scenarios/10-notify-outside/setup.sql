-- 10-notify-outside: invoice batches and invoices. When a batch is approved, a trigger tells the
-- mailer (a process outside the database, listening on send_invoice) to send each invoice in it.
CREATE TABLE public.invoice_batches (
  id     integer PRIMARY KEY,
  status text    NOT NULL
);
CREATE TABLE public.invoices (
  id       integer PRIMARY KEY,
  batch_id integer NOT NULL REFERENCES public.invoice_batches (id),
  amount   integer NOT NULL
);
ALTER TABLE public.invoice_batches REPLICA IDENTITY FULL;
ALTER TABLE public.invoices REPLICA IDENTITY FULL;

INSERT INTO public.invoice_batches (id, status) SELECT g, 'draft' FROM generate_series(1, 5) AS g;
INSERT INTO public.invoices (id, batch_id, amount)
SELECT g, 1 + g % 5, 1000 + g FROM generate_series(1, 1200) AS g;

CREATE FUNCTION public.send_batch() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
BEGIN
  IF NEW.status = 'approved' AND OLD.status <> 'approved' THEN
    PERFORM pg_notify('send_invoice', i.id::text) FROM public.invoices i WHERE i.batch_id = NEW.id;
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER invoice_batches_send
AFTER UPDATE OF status ON public.invoice_batches
FOR EACH ROW EXECUTE FUNCTION public.send_batch();
