# Limitations

What Deedglass does not do, stated plainly. **Owner: S37**, which finalizes this page before launch. Until then, each session adds the limits it decides. This list already holds the ones the contract fixes.

## Known limits in v1

- **L2 covers Postgres only.** Other connectors reach L0 (Reported) or L1 (Confirmed).
- **L2 needs database settings.** `wal_level=logical`, a replication slot, and `REPLICA IDENTITY FULL` on watched tables. Some managed services need a flag or a restart (`docs/CLOUD_COMPATIBILITY.md`).
- **L2 sees only the WAL.** Effects outside it (pg_net, foreign data wrappers, NOTIFY), unlogged and temp tables, sequences and other databases are not witnessed. DDL can be blocked by policy beforehand, but it is not witnessed or undone.
- **L1 is the target's own view.** "Stripe says the refund exists" is not "the refund was a good idea".
- **Seal keys are per deployment.** One set of seal keys signs every org's deeds. Per-org seal keys are v2.
- **No test or live mode for connectors.** `dg_test_` and `dg_live_` API keys are recorded on each action (`livemode`) but not enforced yet (S08 section below).
- **COSE is an encoding, not a transparency log.** The optional COSE_Sign1 output is a signed statement from your Deedglass instance. It is not an RFC 9942 transparency-log entry, and deeds are not anchored in any external log in v1.
- **Deeds hold digests, not payloads.** A deed shows that a request with a given digest was handled. You need the original request to match it.
- **Unknown stays unknown.** When Deedglass cannot tell whether a tool ran, the verdict is UNVERIFIABLE.
- **Authorized is not the same as wise.** A hijacked agent with a valid key and an in-policy action is allowed. Policies, budgets and approvals narrow this; they do not close it.
- **Trust assumptions.** Deedglass trusts the Postgres server and its superusers, the host it runs on, and its seal key. A REPLICATION role can decode every row change in its database.

## Database limits (S02)

- **A Postgres superuser or the schema owner can change anything.** They can disable triggers, turn off row-level security and rewrite deeds. The database can't stop its own superuser; offline deed verification (S05) detects a rewritten or broken chain, it doesn't prevent one.
- **Row-level security stops a forgotten filter, not an attacker on the server.** Anyone who can run SQL as the runtime role can set the org setting too. Injection and a compromised server are handled by parameterized SQL, no user SQL on the control plane, the two-role split and offline verification.
- **The database checks a seal's shape, not its signature.** Ed25519 seals are checked by `deedglass verify`, not by Postgres.
- **Forward-only migrations.** There is no down migration; a mistake is fixed by the next one. `CREATE INDEX CONCURRENTLY` isn't available through the runner in v1.
- **Direct Postgres only for session settings.** Session settings travel in the connection's startup packet; a transaction-mode pooler may drop them.
- **The control plane and a watched database are separate databases.** Deedglass refuses to put its own schema into a database it watches.

## Deed limits (S04)

- **Removing the newest deeds isn't visible in the chain alone.** A chain cut at seq 800 looks complete. Keep the head of each export you receive; S05's sealed manifest records it.
- **The seal key holder can write any deed.** Including any `issued_at`. A deed shows what this Deedglass deployment recorded, signed by that deployment, not by an independent party. Revoking a key makes every deed it sealed fail.
- **One signer per deed.** A deed is not countersigned by a transparency service or a second party in v1.
- **Small payloads can be guessed from their digests.** A digest of a yes/no answer or a short amount can be found by trying values.
- **New seal keys reach `keys.json` within 60 seconds,** not at once, and clients may cache it for 300 seconds.
- **No `keys revoke` command yet.** Revocation is a manual database change until it ships (`docs/SEAL_KEYS.md`).

## Verifier limits (S05)

- **A bare `.jsonl` can lose its newest deeds unnoticed.** Only a sealed bundle manifest, or a head you kept from an earlier export, shows that deeds were cut from the end. `deedglass verify` warns when a bundle's manifest is not sealed.
- **Keys inside a bundle show integrity, not authenticity.** Whoever made the bundle chose those keys. Pin the issuer's published keys (`--keys`, `--pin`) to know who sealed the deeds; `--strict` refuses to call an unpinned check valid.
- **`verify` checks seals and the chain, not the truth of a verdict.** A `VERIFIED` written in a deed is what the Witness recorded then; `verify` does not re-run it.
- **Browsers verify Ed25519 with their own engines.** The page checks key safety and `s < L` itself first and agrees with Go on every published vector and the 500-case parity corpus, but a browser engine could still differ on an input nobody has tried. The CLI is the reference verifier. Browsers without WebCrypto Ed25519 (before Safari 17, Firefox 129, Chrome 137) get "use the CLI", never a verdict.
- **The browser verifier reads `.json` and `.jsonl`, not bundles, in v1.**

## Ledger limits (S06)

- **The chain check runs inside the database it checks.** It finds a changed, removed or reordered
  deed anywhere before the head, but not deeds deleted from the end of the chain. Keep the head
  (`deedglass ledger check` prints it) somewhere outside Deedglass and compare.
- **Someone with database superuser rights can change history.** The append-only triggers stop
  every Deedglass role; a superuser can disable them. The chain check and every external
  `deedglass verify` then name the first changed deed; they can't undo the change or say who made it.
- **Pruned payloads are gone.** Deeds keep their digests, so a copy you kept elsewhere can still be
  matched, but Deedglass can no longer show the original arguments or results.
- **No search inside payloads.** `q` matches the tool name and the error code only.
- **Exports stop at 1,000,000 deeds and checks at 2,000,000 deeds or 30 s per request**; larger
  ranges are split (`next_from_seq`).

## Identity limits (S07)

- **Sign-in needs the email inbox, or an operator.** There are no passwords, no SSO or SCIM yet, and
  no self-serve signup. Without a mail provider, operators issue links with `deedglass org login-link`.
- **Rate limits live in each server process.** Behind N instances a client gets up to N times the IP
  and email budgets; the per-user cap on new sign-in tokens is counted in the database.
- **A newer sign-in email voids older links in its own org only**; a link into another org stays
  usable until it expires (15 minutes).
- **Unknown addresses and wrong codes take similar, not identical, time** (a dummy hash is computed
  when there is nothing to compare; the number of database reads differs).

## Agents, keys and Stop limits (S08)

- **Test keys aren't enforced against connectors.** A `dg_test_` key carries `livemode: false` and
  is recorded on each action from S13, but connectors have no test mode in v1, so a test key can
  reach a live system. Enforcement waits for connector modes (v0.2).
- **The failed-auth limiter lives in each server process.** Behind N instances, a client gets up to
  N times the per-address and per-prefix budgets.
- **While an org is stopped, its admin keys can only read** (`GET`). Every request from them that
  changes state gets `org_stopped`. Changing agents during a Stop all needs a Console session or the
  operator CLI.
- **Stop can't un-run an action already executing at a tool.** It refuses new calls and, from S13
  and S22, queued and approved work; a call a tool already received happens.
- **The 2-second Stop bound assumes every instance can reach Postgres.** If Postgres is down, no
  agent can act at all: Deedglass fails closed.
- **Agent keys' deed reads filter on `body->'agent'->>'id'` without an index.** Fine at v1 volumes;
  in a large org, an agent key listing its deeds reads more rows than it returns.
- **Key lists show at most 100 keys per agent** (and 100 org admin keys), newest first, revoked ones
  included. There is no pagination yet.

## What this page does not do

It does not list bugs or missing features on the schedule. It lists limits of the design that stay true after they ship.
