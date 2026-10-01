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

## What this page does not do

It does not list bugs or missing features on the schedule. It lists limits of the design that stay true after they ship.
