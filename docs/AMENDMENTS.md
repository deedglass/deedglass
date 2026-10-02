# Amendment deeds

A deed is sealed once and never changed. When evidence about an action arrives after its deed was
sealed, Deedglass seals a second, smaller deed that says so: an **amendment deed**. The chain stays
append-only, and anyone checking it offline sees both.

## When an amendment is sealed

Today the one source of late evidence is L2 ([WITNESS.md](WITNESS.md#amendment-deeds)):

1. An action waits for Postgres's change log. When `witness.l2_timeout` passes first, its deed is
   sealed with the gap `l2_timeout` (for example `L1 · UNVERIFIABLE` under `verify: L2`).
2. The decoder catches up and binds the action's transaction. The Witness decides again with the
   new L2 reasons in place of the gap.
3. If the claim (level and verdict) is different, an amendment deed is sealed. If it is the same,
   the action gets a note (`l2_late_confirmed`) and no deed.

A second transaction claiming an action that was already sealed (`multiple_transactions`) or a
ledger row that doesn't match what was authorized (`ledger_mismatch`) amends the same way.

## What an amendment deed holds

```json
{
  "v": 1,
  "kind": "amendment",
  "deed_id": "deed_01m3xa8q…",
  "amends": "deed_01m3xa8f…",
  "org": "org_…",
  "seq": 14,
  "prev_hash": "sha256:…",
  "agent": {"id": "agt_…", "name": "orders-bot"},
  "action": {"id": "act_…", "tool": "postgres.execute", "connector": "con_…", "mode": "gateway", "request_digest": "sha256:…"},
  "witness": {"level": "L2", "verdict": "VERIFIED", "method": "postgres.pgoutput", "detail_digest": "sha256:…"},
  "seal_key_id": "sk_…"
}
```

- `amends` always names the **action deed**, never an earlier amendment.
- `action` is copied exactly from the action deed; `witness` is the new claim, and
  `detail_digest` is the digest of the new `witness_detail`.
- It takes the next `seq` in the org's chain, like any deed.

The action itself (`GET /v1/actions/{id}`) shows the latest claim, and `witness.history` lists
every one, oldest first; the first entry names the reason it was replaced (`l2_timeout`).
`deedglass actions show` prints the same as a `history` block.

## How `deedglass verify` treats amendments

An amendment deed is a deed: it is sealed, hashed and linked like the others, so `deedglass
verify` checks it the same way and counts it in the chain (`✓ 14 deeds · chain intact`). The
action deed it amends keeps its own seal and still verifies. Read them together: the action deed
is what Deedglass knew when it sealed; the amendment is what it learned later.

## The limit

At most three amendments per action (`actions.witness_amendments`, a CHECK in the database). A
fourth attempt changes nothing and raises the event `decoder.alert` with `kind:
amendment_limit`, because evidence that keeps changing its mind about one action is itself worth a
look. Amendments are possible while the decoder keeps the action's frames (`decoder.retention`, 7
days).

## What amendments don't do

- They never change the action deed, its `prev_hash` or its seal.
- They don't re-run the tool, and they don't change the policy decision.
- A late gap (a lost slot, a missing frame) never amends a sealed deed; only new evidence does.
