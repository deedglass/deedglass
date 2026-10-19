-- Another writer (the nightly job, a teammate, a second agent) right after: it archives 40 to 44.
UPDATE public.orders SET status = 'archived' WHERE id BETWEEN 40 AND 44;
