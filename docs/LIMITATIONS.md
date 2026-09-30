# Limitations

What Deedglass does not do, stated plainly. **Owner: S37**, which finalizes this page before launch. Until then, each session adds the limits it decides. This list already holds the ones the contract fixes.

## Known limits in v1

- **L2 covers Postgres only.** Other connectors reach L0 (Reported) or L1 (Confirmed).
- **L2 needs database settings.** `wal_level=logical`, a replication slot, and `REPLICA IDENTITY FULL` on watched tables. Some managed services need a flag or a restart (`docs/CLOUD_COMPATIBILITY.md`).
- **L2 sees only the WAL.** Effects outside it (pg_net, foreign data wrappers, NOTIFY), unlogged and temp tables, sequences and other databases are not witnessed. DDL can be blocked by policy beforehand, but it is not witnessed or undone.
- **L1 is the target's own view.** "Stripe says the refund exists" is not "the refund was a good idea".
- **Seal keys are per deployment.** One set of seal keys signs every org's deeds. Per-org seal keys are v2.
- **No test or live mode for connectors.** `dg_test_` and `dg_live_` API keys are recorded on each action (`livemode`) but not enforced yet.
- **COSE is an encoding, not a transparency log.** The optional COSE_Sign1 output is a signed statement from your Deedglass instance. It is not an RFC 9942 transparency-log entry, and deeds are not anchored in any external log in v1.
- **Deeds hold digests, not payloads.** A deed shows that a request with a given digest was handled. You need the original request to match it.
- **Unknown stays unknown.** When Deedglass cannot tell whether a tool ran, the verdict is UNVERIFIABLE.
- **Authorized is not the same as wise.** A hijacked agent with a valid key and an in-policy action is allowed. Policies, budgets and approvals narrow this; they do not close it.
- **Trust assumptions.** Deedglass trusts the Postgres server and its superusers, the host it runs on, and its seal key. A REPLICATION role can decode every row change in its database.

## What this page does not do

It does not list bugs or missing features on the schedule. It lists limits of the design that stay true after they ship.
