# Threat model

Who Deedglass trusts for what, and what happens when each of them is compromised. **Owner: S34**, which writes the full model and the static guards G6–G10. This page is a stub.

## What S34 will cover

- **Actors:** the agent and its process, the person it acts for, approvers, org admins, the Deedglass operator and host, the seal-key holder, the Postgres server and its superusers, upstream tools and MCP servers, and the network.
- **Assets:** connector credentials in the vault, seal keys, API keys and the pepper, the deed chain, and the Ledger.
- **For each actor and asset:** what an attacker gains, and whether Deedglass blocks it, detects it, reports it as UNVERIFIABLE, or leaves it outside scope.

## Already decided (contract)

- Agents never receive connector credentials in gateway mode.
- API key secrets are stored as `sha256(pepper ‖ secret)` and shown once.
- Ids are not secrets and never authorize anything.
- All outbound traffic goes through one egress path that refuses private and metadata addresses in prod.
- Deeds and action events are append-only, and the database checks the deed chain on insert.

## Deeds (S04)

| Threat | What Deedglass does |
|---|---|
| A stored deed is edited (database superuser, backup restore, a bad migration) | Normal paths can't: the append-only trigger and the app role's grants refuse it. If they are bypassed, any verifier sees `body_hash_mismatch` or `chain_prev_hash_mismatch`; S06's `ledger check` names the first break. |
| A deed is removed from the middle of a chain | `chain_seq_gap` names the missing seq. |
| Deeds are removed from the end of a chain | Not visible in the chain alone. Exports record the head (S05, S06); keep the heads you receive. |
| Two deeds claim the same parent (a fork) | The chain's advisory lock and `UNIQUE (org_id, prev_hash)`: the second insert aborts. |
| A signature made for something else is presented as a seal | Domain tags (`deedglass/deed/v1\n`, `deedglass/bundle/v1\n`, `deedglass/intent/v1\n`); seal keys are never intent keys. |
| A small-order or malformed public key enters a key set | Refused at key-file load, at registration and when a verifier parses `keys.json` (A13). |
| The seal key is stolen | Out of scope to prevent. The thief can seal false deeds with any `issued_at`. Mitigations: 0600 key files, the seed is never logged, prod refuses test keys, rotation, and revocation (a revoked key fails every deed). KMS or HSM signing is planned. |
| A payload leaks through a deed | The schema is closed and holds digests only; free text is capped (agent name, tool, references, reason). |
| Canonicalization ambiguity (duplicate keys, 2^53+1, `1.0`, lone surrogates) | The strict profile refuses it before JCS; nothing is repaired. |
| Clocks disagree between instances | `issued_at` comes from the database clock under the chain lock and never decreases. |
| An instance keeps sealing after a rotation | It refuses a key that retires within 30 s; a restart with a retired key is refused. |
| `keys.json` served partially during an outage | 503 `service_unavailable`, never a partial list. |

## Signed intents, on-behalf-of and MCP (S09)

Detail: [INTENTS.md](INTENTS.md) §13, [IDENTITY.md](IDENTITY.md) (threat notes) and
[MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md).

| Threat | What Deedglass does |
|---|---|
| An agent's API key leaks (`.env`, CI logs, an MCP config) | For an agent in `required` mode, every call without a valid signed intent is refused (`intent_required`). Otherwise the holder acts as the agent until the key is revoked. |
| A proxy or middlebox changes an argument | `args_digest` covers the exact arguments: `args_mismatch`. |
| A captured request is replayed | The nonce's primary key: `intent_replay`. Nonces are pruned only after the intent can no longer be accepted (`expired` first). |
| Algorithm confusion | No `alg` field; Ed25519 only; the signed input is domain-separated (`deedglass/intent/v1\n`). |
| Weak or malicious intent keys | Small-order, non-canonical and off-curve keys are refused at registration (422), and stored keys are parsed again on every read. |
| JSON ambiguity (duplicates, big integers, non-canonical bytes) | The strict parser runs before JCS; the intent's bytes must equal their JCS form: `intent_invalid`. |
| Clock abuse | 60 seconds of skew; a lifetime of at most 5 minutes. |
| On-behalf-of spoofing | A signed `on_behalf_of` is bound to the agent's key and a differing header is refused; an unsigned one is recorded as the agent's claim. v2 verifies people against the org's identity provider. |
| Header smuggling | One value per OBO header, a strict grammar (422), and Go refuses CR and LF. |
| A client's token passed to an upstream MCP server | `StripInboundCredentials`, which S15 must call on every forwarded request. |
| DNS rebinding against `/mcp/*` | A browser `Origin` other than the base URL's is refused with 403. |
| Enumerating connectors through the metadata | The same document for every well-formed id, with no database read. |
| An intent error starts an OAuth flow in a client | Intent errors are 403 `permission_error`, never 401. |
| **The agent's runtime is compromised** | **Not defended by intents:** the attacker holds both keys. Policies, budgets, approvals and Stop still apply. |
| A prompt-injected agent signs a harmful, well-formed action | Not an intent problem: the signature records what was meant. Policy decides (S10 to S12). |

## Attacker model for the attack suite (S18)

The attack suite ([ATTACK_SUITE.md](ATTACK_SUITE.md)) plays four attackers. S34 writes the full
STRIDE model.

| Attacker | What they hold | What the suite checks |
|---|---|---|
| **A manipulated model** | An agent's valid key and the tools the policy lets it see; it has read poisoned data (a customer note telling it to drop a table) | Destructive, smuggled and hidden SQL is denied by policy with a sealed deed, and refused by the Postgres connector even when policy allows it (H01 to H07); a burst stops at the agent's budget (J04) |
| **A stolen or misused agent key** | The key, possibly after an admin suspended the agent or revoked the key; a captured signed intent | Suspension and revocation take effect within 2 s; a signed intent is used exactly once, also under 16 concurrent replays (J01 to J03) |
| **A hostile or careless upstream** | The upstream server an org connected: it can redirect, echo credentials in errors, and rename its tools | No request reaches cloud metadata or another host; no credential appears in any response, log line, event, deed or result; look-alike tool names are hidden, logged and refused (K01 to K04) |
| **A curious org neighbour** | A valid admin key, agent key and owner session in another org on the same instance | Every route answers another org's ids exactly as it answers ids that exist nowhere; lists and exports show none of them; nothing changes (`TestOrgIsolation`) |

## What this page does not do

It is not a security review or a penetration-test report, and it makes no claim until S34 writes one with its evidence. The V0 trust model lives in `~/txv/ops/plan/20_ARCHITECTURE_SPEC_V0.md` §2.2.
