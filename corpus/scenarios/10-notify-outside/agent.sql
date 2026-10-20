-- The agent approves batch 3 so a person can review it before anything is sent.
UPDATE public.invoice_batches SET status = 'approved' WHERE id = 3;
