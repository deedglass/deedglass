# Verification model

What a Witness verdict asserts, at each level, as short statements; what each statement rests on;
and what a deed never asserts. The engine itself is described in [WITNESS.md](WITNESS.md), and every
reason code in [witness/REASONS.md](witness/REASONS.md).

## Terms

- **Action** `a`: one tool call through the Gate, with its arguments `args(a)`, its tool `t(a)` and
  its result `res(a)` as the connector returned it.
- **Reason**: one piece of evidence with a level and a class: pass (agrees with what the tool
  reported), violation (contradicts it), gap (needed and missing) or note (context only).
- **Clean level** `L`: the reasons at `L` hold at least one pass and no gap or violation.
- **Required level** `req(a)`: the policy decision's `verify:` for a tool whose side effect isn't
  `read` (default `L0`); `L0` for a read.
- **Claim**: the `(level, verdict, method)` the deed carries, with `detail_digest`, the SHA-256 of
  the RFC 8785 form of the action's `witness_detail`.

## What each verdict asserts

A deed's verdict is about its claimed level only, and never stronger than the evidence at that
level. Every statement below is a statement about what Deedglass observed, at the time it
observed it.

**At every level.**

- V. `VERIFIED` at `L` asserts: `L` is clean, `L ≥ req(a)`, and no level holds a violation.
- M. `MISMATCH` at `L` asserts: `L` holds at least one violation, and no higher level does.
- U. `UNVERIFIABLE` at `L` asserts: no level holds a violation, and no clean level is at or above
  `req(a)`. If some level is clean, `L` is the highest clean level and the reasons include
  `level_below_required`; otherwise `L` is the highest level that produced any pass, gap or
  violation, or `L0` with `no_checks` when none did.

**L0 Reported.**

- `L0 · VERIFIED · reported` asserts: the connector Deedglass called returned success for `t(a)`.
- `L0 · VERIFIED · response.schema` asserts, in addition: the output matched `t(a)`'s output
  schema.
- `L0 · VERIFIED · edge_reported` asserts: the edge that ran `t(a)` reported success. Deedglass did
  not run the call and can't tell a truthful edge from one that lies.
- `L0 · UNVERIFIABLE` with `outcome_unknown` asserts: Deedglass can't tell whether `t(a)` ran. It
  may have.

**L1 Confirmed.**

- `L1 · VERIFIED · readback.<type>.<tool>` asserts: after `t(a)` returned success, the read tool
  `<tool>` of the same connector, called with arguments derived from `args(a)` or `res(a)`,
  returned values equal (by the documented JSON equality) to every `expect` field, and the
  postcondition, if any, was true over that answer.
- `L1 · VERIFIED · postcondition.<type>` asserts: the same, with a postcondition and no `expect`.
- `L1 · VERIFIED · readback.postgres.row` asserts: after the statement committed, the row named by
  its primary key had `xmin` equal to the low 32 bits of the action's transaction id, and every
  compared `SET` column was not distinct from the value the statement set.
- `L1 · MISMATCH` asserts: the read-back answered and a compared field or the postcondition
  disagreed with what `t(a)` implied (`postcondition_failed`, `value_mismatch`), or the record was
  absent where the connector says absence is a contradiction (`readback_absent`).

**L2 Witnessed** (S21; the definitions here are fixed now so S21 fills them).

- `L2 · VERIFIED · postgres.pgoutput` asserts: the row changes Postgres committed in the action's
  transaction, read from its own change stream, equal the changes the action was expected to make.
- `L2 · MISMATCH` asserts: they differ, for example in the number of rows or in a column value.

## What these statements rest on

- **An honest target API for L1.** A read-back sees what the target chooses to answer. A target
  that answers consistently but falsely can't be caught by reading it back.
- **An honest Postgres engine for L2 and for the row read-back.** Deedglass trusts the Postgres
  server, its owners and its superusers, and an unmodified decoding path.
- **An uncompromised Deedglass host and seal key.** The Witness runs inside Deedglass, and a
  verdict is only as good as the process that decided it and the key that sealed it.
- **Sound primitives.** Ed25519 and SHA-256, as used for seals and digests ([DEED_SPEC.md](DEED_SPEC.md)).

## What a deed never asserts

- **The wisdom of the action.** A verdict says whether the tool did what it said, not whether the
  agent should have asked for it. An in-policy action by a hijacked agent can be `VERIFIED`.
- **Effects outside the watched system.** L1 sees only the fields you name on the target you read;
  L2 sees only the Postgres change stream of watched tables. Emails sent, files written and calls
  to other systems are outside both.
- **The state of the target now.** L1 records what the target reported at the time of the
  read-back. A later change doesn't alter the deed.
- **More than the claimed level.** `L1 · VERIFIED` says nothing about L2, and gaps at other levels
  are listed in the detail, not folded into the claim.

## Precedence, formally

For a set of reasons `R`: `Precedence(R)` is `MISMATCH` if `R` holds a violation; else
`UNVERIFIABLE` if it holds a gap; else `VERIFIED` if it holds a pass; else `UNVERIFIABLE`. Notes are
ignored. The claim rule ([ADR-042](adr/README.md)) applies `Precedence` to the reasons of the
claimed level, and treats violations as global: a violation at any level gives `MISMATCH`. Only
`witness.Decide` produces `VERIFIED`; it is tested against an independent statement of this rule
over 1,536 cases (`internal/witness/precedence_test.go`).

## V0 history

The V0 verifier (`docs/v0/VERIFICATION_MODEL.md`) issued `VALID`, `INVALID` or `INCONCLUSIVE` for
each committed Postgres transaction with a ledger entry, by comparing the row changes decoded from
the database's logical-replication stream with the changes computed from a signed intent, inside a
declared observation boundary. Two of its ideas carry into L2 (S21):

- the same precedence, in new words: a contradiction is a violation, and a gap is still a gap;
- the marker is authenticated only by the ledger row in the same transaction, and `marker_lsn` is
  evidence only, never compared with `commit_lsn`.

V0's assumptions A1 (an honest Postgres engine, owners and superusers), A2 (an honest verifier host
and key) and A5 (sound Ed25519 and SHA-256) are the assumptions above in Deedglass terms.
