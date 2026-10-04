# The Stripe connector

Deedglass governs refunds, invoice items, customer metadata and subscription cancellations in a
**Stripe sandbox**. It reads the charge from Stripe before a refund is decided, so your policy
reasons about real money, never what the agent claims. Every write carries the action id as its
`Idempotency-Key`, and every write is read back from Stripe's own objects (L1).

Facts on this page were checked on 2026-10-04 against docs.stripe.com (rows C-074 to C-077 in
`CLAIMS_REGISTER.md`).

## Sandbox only, and why

Live money is where an agent's mistake hurts most, so Deedglass connects to Stripe sandboxes only.
A live key can't get in:

1. **When it is stored.** `PUT /v1/connectors/{id}/credentials` refuses any value that isn't
   `sk_test_…` or `rk_test_…` before it is encrypted, so a refused value is never stored, logged or
   echoed (messages show its first 8 characters at most). `deedglass connector credentials set`
   refuses it before anything leaves your machine.
   - `sk_live_…`, `rk_live_…` → `stripe_live_key_refused`
   - `sk_org_…` → organization keys act across accounts
   - `pk_…` → publishable keys can't read charges or refund
   - `whsec_…` → that is a webhook signing secret
2. **Before every call.** The opened key is checked again. A stored row that holds a live key
   (edited in the database, restored from an old backup) disables the connector, raises an alert,
   and nothing is sent.
3. **On every answer.** Any `livemode: true`, anywhere in Stripe's answer, trips the **breaker**:
   the connector goes to `status: disabled` with `health.reason = livemode_detected`, the admin deed
   `connector.disabled` is sealed with Stripe's request id, an alert goes to the log, and the action
   is recorded as unknown (UNVERIFIABLE), because something may have happened in live mode. No
   further request leaves.

There is no config key, environment variable or flag that allows live mode. A source test fails
the build if one appears. Live mode would need a founder-approved ADR and its own review.

```
$ deedglass connector credentials set stripe --from-env STRIPE_LIVE_KEY
✕ Refused · stripe · this is a live-mode key (sk_live_…)
  Deedglass connects to Stripe sandboxes only. Create a restricted key that starts with rk_test_ in your sandbox:
  https://dashboard.stripe.com/test/apikeys
```

## The restricted-key recipe

A restricted key limits Deedglass to the tools you use; a secret key (`sk_test_`) reaches every
Stripe API, so `deedglass connector test stripe` warns about one.

1. In the Stripe Dashboard, turn on your **sandbox**, then open Developers › API keys › **Create
   restricted key**. Name it `deedglass-sandbox`.
2. When Stripe asks what the key is for, **don't** tag it as an agent key (see "Stripe's approvals
   and Deedglass approvals" below).
3. Permissions: Charges Read, Refunds Write, Customers Write, Invoices Write (invoice items),
   Subscriptions Write, PaymentIntents Read (Write only for `deedglass dev stripe-fixtures`), Balance
   Read, Products and Prices Write (fixtures only). Stripe's names can differ;
   `deedglass connector test stripe` tells you what's missing.
4. Store it: `export STRIPE_TEST_KEY=…`, then
   `deedglass connector credentials set stripe --from-env STRIPE_TEST_KEY`.

## Config

```yaml
type: stripe
name: stripe
config:
  api_version: "2026-08-26.dahlia"   # sent as Stripe-Version on every request
  refund_limits:                     # the most one refund may be, in minor units; other currencies are refused
    usd: 100000                      # $1,000.00
    eur: 100000
    inr: 5000000                     # ₹50,000.00
    jpy: 150000                      # ¥150,000 (zero-decimal)
  refund_reasons: [duplicate, fraudulent, requested_by_customer]
```

`refund_limits` is defence in depth: your policy is stricter, but a mis-edited policy still can't
refund $10,000 through this connector. A refund in a currency the map doesn't list is refused.

The API version is pinned to `2026-08-26.dahlia`, the last dahlia release. Stripe released
`2026-09-30.endive` on 2026-09-30 with breaking changes to subscriptions and invoices; moving to it
is a deliberate upgrade.

## Catalog

The table below comes from `GET /v1/connector-types/stripe` (`deedglass connector types --type stripe`).

<!-- deedglass:generated start -->
<!-- Generated from the connector catalog by go run ./internal/connector/cmd/gendocs. Don't edit by hand. -->

**Stripe** · beta · levels L0 L1 · sandbox only

Refunds, invoice items and subscriptions in a Stripe sandbox.

### Tools

| Tool | Side effect | Levels | What L1 checks |
|---|---|---|---|
| `stripe.get_customer` | read | L0 | Not read back |
| `stripe.list_charges` | read | L0 | Not read back |
| `stripe.get_charge` | read | L0 | Not read back |
| `stripe.get_payment_intent` | read | L0 | Not read back |
| `stripe.list_refunds` | read | L0 | Not read back |
| `stripe.get_subscription` | read | L0 | Not read back |
| `stripe.create_refund` | write | L0 L1 | Stripe shows the refund for the same amount and currency on the same charge, status succeeded or pending. |
| `stripe.create_invoice_item` | write | L0 L1 | Stripe shows the invoice item with the same amount, currency and customer. |
| `stripe.update_customer_metadata` | write | L0 L1 | Every key has the requested value, and deleted keys are gone. |
| `stripe.cancel_subscription` | destructive | L0 L1 | Stripe shows the subscription canceled, or set to cancel at the period end. |

| Tool | What it does |
|---|---|
| `stripe.get_customer` | Read a customer: id, name, email and metadata. |
| `stripe.list_charges` | List recent charges, optionally for one customer (at most 25). |
| `stripe.get_charge` | Read a charge: amount, currency, what was refunded and whether it is disputed. |
| `stripe.get_payment_intent` | Read a PaymentIntent: amount, currency, status and its latest charge. |
| `stripe.list_refunds` | List the refunds of one charge or PaymentIntent (at most 25). |
| `stripe.get_subscription` | Read a subscription: status, price per period and when the period ends. |
| `stripe.create_refund` | Refund part or all of a charge, in the charge's currency. Send charge or payment_intent, and amount in minor units. |
| `stripe.create_invoice_item` | Add an item to a customer's next invoice. |
| `stripe.update_customer_metadata` | Set metadata keys on a customer. An empty string removes a key. |
| `stripe.cancel_subscription` | Cancel a subscription at the end of its period (the default), or at once with at_period_end false. |

### Credentials

Kind `bearer`:

- `token` (secret): A restricted key from your Stripe sandbox. Starts with `rk_test_`.

### Setup

1. **Create a restricted key.** In your Stripe sandbox: Developers › API keys › Create restricted key. Don't tag it as an agent key: Deedglass is the approval surface. https://dashboard.stripe.com/test/apikeys
2. **Create the connector.**
   `deedglass connector create --config examples/connectors/stripe.yaml`
3. **Store the key.**
   `deedglass connector credentials set stripe --from-env STRIPE_TEST_KEY`
4. **Test the connection.**
   `deedglass connector test stripe`

### What it doesn't do

- Sandbox only. Live keys are refused, and a live-mode answer disables the connector.
- No payouts, transfers or payment creation.
- No Stripe Connect accounts.
- L1 shows Stripe's record, not whether the refund was right.
- A refund Stripe holds for its own approval is UNVERIFIABLE: Deedglass can't see Stripe's reviewers.

<!-- deedglass:generated end -->

## Money

Amounts are always integer **minor units** plus a lowercase ISO 4217 currency: `2000` with `usd`
is $20.00, `5000` with `jpy` is ¥5,000. A float amount (`20.5`) is refused with
"Send amount as an integer in minor units, like 2000 for $20.00." and never reaches Stripe.
People see amounts the way they read them: `$120.00`, `¥5,000`, `€9.50`, `₹1,499.00`, `KWD 1.250`
(Stripe's zero-decimal and three-decimal currencies included).

## Policy facts: `request.stripe`

Before a refund is decided, the connector reads the charge (or the PaymentIntent's latest charge)
from Stripe, cached for 30 seconds:

| Fact | Meaning |
|---|---|
| `charge`, `payment_intent`, `customer` | ids from Stripe |
| `currency` | the charge's currency, lowercase |
| `charge_amount`, `amount_refunded`, `refundable` | minor units; `refundable = charge_amount − amount_refunded` |
| `disputed` | the charge is disputed |
| `charge_age_days` | whole days since the charge |
| `amount_display` | `args.amount` for people, like `$120.00` |
| `subscription`, `status`, `current_period_end`, `amount_per_period`, `interval` | for `cancel_subscription` |
| `enriched` | false when Stripe couldn't be read |

When `enriched` is false, the starter policy sends the refund to a person: an enrichment outage
never loosens a decision.

## Idempotency and metadata

Every POST carries `Idempotency-Key: act_…` (the action id) and `metadata[deedglass_action_id]`.
Stripe treats a POST with a key as idempotent, so Deedglass retries once on a connect error, a 5xx,
a `409 idempotency_key_in_use` or `Stripe-Should-Retry: true`, and never when Stripe says
`Stripe-Should-Retry: false`. Two attempts with the same key create one refund. Metadata keys
starting with `deedglass_` are reserved, so an agent can't forge the correlation.

## Cost and budgets

For a refund, the deed's `cost` is the amount refunded (`cost $20.00`). The starter's
`refunds-per-day` budget reserves the amount before the call (USD only) and releases it on a
deny, a failure or an expiry; other currencies go to a person.

## Stripe's approvals and Deedglass approvals

Stripe can hold actions from **agent-tagged** restricted keys for its own approval: its default
rules cover refund creation and subscription cancellation. Stripe answers `approval_required` and
its reviewers have 14 days before the request expires. If one approves, Stripe completes the
action by itself.

Deedglass maps that honestly: the action is recorded as unknown, the deed is UNVERIFIABLE, and the
admin detail says "Stripe is holding this refund for its own approval (apreq_…). Deedglass can't
see whether a Stripe reviewer approves it." (`upstream_approval_pending`).

**Choose one approval surface.** Use a restricted key **without** the agent tag when Deedglass is
the gateway: Deedglass shows, approves and seals everything. Or keep Stripe's rules and expect
UNVERIFIABLE deeds for held actions.

## Read-back (L1)

| Write | Stripe shows | Verdict |
|---|---|---|
| refund `succeeded` or `pending`, same amount, currency, charge and action id | | VERIFIED |
| refund `requires_action` | Stripe needs the customer to act before the refund completes | UNVERIFIABLE (a gap) |
| refund `failed` or `canceled` | No money was returned | MISMATCH |
| a different amount, or another action's id | | MISMATCH |

## Connector test

```
$ deedglass connector test stripe
✓ stripe · 6 checks passed · 1 skipped · 521 ms
  ✓ config        valid · refund limits eur, inr, jpy, usd
  ✓ credentials   restricted key · rk_test_…9f2c
  ✓ destination   api.stripe.com → public
  ✓ auth          sandbox · livemode false
  ✓ version       2026-08-26.dahlia
  ✓ permissions   charges, customers, subscriptions readable
  · writes        not probed · Deedglass never creates objects to test a connection
```

## Demo fixtures

`deedglass dev stripe-fixtures --from-env STRIPE_TEST_KEY` creates the customer "Acme Demo
Customer" (`demo@example.test`), confirmed payments of $149.00, $20.00 and ¥5,000, and a
$29.00/month subscription, through the connector's own rails (sandbox keys only, the breaker,
the pinned version). Each POST uses the fixed key `fixtures-<date>-<n>`, so a second run the same
day creates nothing new. The ids go to `examples/demo/.stripe-fixtures.json` (mode 0600,
git-ignored).

## Errors

`stripe_live_key_refused`, `stripe_livemode_detected` and `upstream_approval_pending` are in
[the error reference](../api/errors.md); Stripe's own refusals map to `upstream_auth_failed` (401,
403), `upstream_not_found` (404), `upstream_rate_limited` (429) and `upstream_error`.

## What it doesn't do

- Live mode. Sandboxes only, with no switch.
- Payouts, transfers or creating payments (outside the dev fixtures command).
- Stripe Connect accounts.
- Judge whether a refund was right: L1 shows Stripe's record.
- See Stripe's own approvals: held actions are UNVERIFIABLE.
