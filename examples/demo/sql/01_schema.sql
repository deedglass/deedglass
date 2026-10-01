-- Acme Store: the demo's orders database (S15). Fictional data only. This database stands in for a
-- customer's system of record; it is never Deedglass's own database.
--
-- Loaded once by the postgres image on an empty volume (docker-entrypoint-initdb.d), and again by
-- make demo-reset, as the bootstrap superuser, into the database acme.
\set ON_ERROR_STOP on
BEGIN;

CREATE TABLE public.customers (
  id         integer PRIMARY KEY,
  name       text NOT NULL,
  email      text NOT NULL UNIQUE,
  city       text NOT NULL,
  created_at timestamptz NOT NULL
);

CREATE TABLE public.products (
  id          integer PRIMARY KEY,
  name        text NOT NULL UNIQUE,
  price_cents integer NOT NULL CHECK (price_cents > 0),
  currency    text NOT NULL DEFAULT 'usd' CHECK (currency = 'usd')
);

CREATE TABLE public.orders (
  id          integer PRIMARY KEY,
  customer_id integer NOT NULL REFERENCES public.customers (id),
  status      text NOT NULL CHECK (status IN ('pending', 'paid', 'shipped', 'cancelled', 'refunded')),
  total_cents integer NOT NULL DEFAULT 0,
  currency    text NOT NULL DEFAULT 'usd',
  created_at  timestamptz NOT NULL,
  updated_at  timestamptz NOT NULL
);
CREATE INDEX orders_created_at ON public.orders (created_at DESC);
CREATE INDEX orders_status ON public.orders (status);

CREATE TABLE public.order_items (
  id               integer PRIMARY KEY,
  order_id         integer NOT NULL REFERENCES public.orders (id),
  product_id       integer NOT NULL REFERENCES public.products (id),
  quantity         integer NOT NULL CHECK (quantity > 0),
  unit_price_cents integer NOT NULL
);
CREATE INDEX order_items_order ON public.order_items (order_id);

CREATE TABLE public.refunds (
  id           integer PRIMARY KEY,
  order_id     integer NOT NULL REFERENCES public.orders (id),
  amount_cents integer NOT NULL CHECK (amount_cents > 0),
  reason       text NOT NULL,
  created_at   timestamptz NOT NULL
);

COMMIT;
