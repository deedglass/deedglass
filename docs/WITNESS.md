# The Witness

Other tools log what your agent asked for. The Witness shows what it actually did. After a tool
runs, it compares what the tool reported with what the target shows, and records a **verdict**
with a **level** and a **method** in the action's deed. This page covers the verdict engine, the
L0 checks and the L1 read-backs (S19). L2, the Postgres change stream, arrives in S21.

Every reason a verdict can carry, with what to do next, is in [witness/REASONS.md](witness/REASONS.md)
(generated from the reason catalog). The formal statements behind each verdict are in
[VERIFICATION_MODEL.md](VERIFICATION_MODEL.md).

## What the Witness does

- **It checks every executed action.** A tool that ran (or may have run) gets a verdict. A tool
  that failed gets none: the action goes `failed → sealed`, as before (S13).
- **It names how far it checked.** `L1 · VERIFIED · readback.rest.get` means exactly this: a
  read-back through `orders.get` confirmed the fields the tool was supposed to change. Nothing
  more.
- **One function decides.** Only `witness.Decide` (`internal/witness/precedence.go`) produces
  `VERIFIED`. A build test (`verdict_guard_test.go`) fails if any other file tries, and `Decide` is
  tested against an independent oracle over every combination of reason classes at every level
  and every required level: 1,536 cases.
- **A gap never becomes a MISMATCH, and a violation is never hidden.** Missing evidence gives
  `UNVERIFIABLE`. Contradicting evidence gives `MISMATCH`, at any level, always.
- **No database transaction is open while Deedglass talks to the target.** A read-back runs
  between two short transactions (B1 and B2, below). Tools that only get L0 stay on the fast path.
- **Every verdict explains itself in one sentence.** All of those sentences come from one reason
  catalog, so the CLI, the API, the Console and this documentation say the same words.

The Witness itself never does I/O. The Gate hands it plain data about the action and an observer
that reads through the connector; the Witness resolves templates, compares fields, evaluates
postconditions and decides.

## Levels

| Level | Name | What it means | Methods |
|---|---|---|---|
| `L0` | Reported | The tool's own answer, checked against its output schema when it has one. | `reported`, `edge_reported` (proxy mode), `response.schema` |
| `L1` | Confirmed | Deedglass read the result back from the target after the write. | `readback.<connector type>.<read tool>` (for example `readback.rest.get`), `readback.postgres.row`, `postcondition.<connector type>` |
| `L2` | Witnessed | Deedglass saw the change in the system of record: the Postgres change stream (S21). | `postgres.pgoutput` |

The deed carries the claimed level in its `witness` section (keys in RFC 8785 order). One example
per level:

```json
"witness": {"detail_digest": "sha256:0b5f…9e2f", "level": "L0", "method": "reported", "verdict": "VERIFIED"}
```

```json
"witness": {"detail_digest": "sha256:c3e1…9b20", "level": "L1", "method": "readback.rest.get", "verdict": "MISMATCH"}
```

```json
"witness": {"detail_digest": "sha256:7254304143e36a4e106c525e35b74e30961135caf01c192641c9e6322a741d37", "level": "L2", "method": "postgres.pgoutput", "verdict": "VERIFIED"}
```

The L2 line is the deed from [DEED_SPEC.md](DEED_SPEC.md)'s worked example; the L0 and L1 digests
are shortened here. `detail_digest` is `sha256:` of the RFC 8785 form of the action's full
`witness_detail` (below), so the detail can be checked against the sealed deed.

## Verdicts

| Verdict | Symbol | Says |
|---|---|---|
| `VERIFIED` | `✓` | At the claimed level, at least one check agreed with what the tool reported, no evidence was missing, and the level is at or above what the policy requires. |
| `MISMATCH` | `✕` | Some evidence contradicts what the tool reported. |
| `UNVERIFIABLE` | `○` | Deedglass needed evidence it couldn't get, or couldn't reach the level the policy requires. The tool may well have done what it said. |

Each piece of evidence is a **reason** with a class from the catalog:

| Class | Meaning | Effect |
|---|---|---|
| pass | Evidence that agrees with what the tool said happened. | Can make its level clean. |
| violation | Evidence that contradicts it. | `MISMATCH`, always. |
| gap | Evidence Deedglass needed and couldn't get. | Keeps its level from being clean. Never a `MISMATCH`. |
| note | Context, such as "no read-back for this statement". | Never affects a verdict. |

An executed action answers HTTP `200` whatever the verdict. The tool ran; the verdict says what
that means.

## How a verdict is decided

**Precedence**, applied to one set of reasons (contract §6): any violation gives `MISMATCH`;
otherwise any gap gives `UNVERIFIABLE`; otherwise at least one pass gives `VERIFIED`; otherwise
(no checks) `UNVERIFIABLE`. Notes are ignored.

A deed names one level, so Deedglass also has to say which level the verdict is about. That is the
**claim rule** ([ADR-042](adr/README.md)):

1. Any violation at any level gives **MISMATCH**, at the highest level that has one.
   Contradicting evidence is never outranked.
2. Otherwise **VERIFIED**, at the highest clean level (at least one pass and no gap) that is at or
   above the required level.
3. Otherwise **UNVERIFIABLE**:
   - at the highest clean level below the required one, with the reason `level_below_required`;
   - or else at the highest level that produced a pass, a gap or a violation;
   - or, when no level did, at L0 with the reason `no_checks`.

Gaps at other levels stay listed in the reasons; they are never turned into anything else. Notes
never count. The method is the claimed level's method.

**The required level** is the policy decision's `verify:` (default `L0`), recorded on the action
as `witness_required` when the decision is made. It applies to tools whose side effect isn't
`read` (`write`, `destructive` and `unknown`); a read always requires L0. See
[POLICY_LANGUAGE.md](POLICY_LANGUAGE.md#66-verify-and-the-witness).

Worked cases (each one is a test, `TestDecideWorkedTable`):

| L0 | L1 | L2 | Required | Result |
|---|---|---|---|---|
| pass | none | none | L0 | `L0 · VERIFIED` |
| pass | violation | none | L0 | `L1 · MISMATCH` |
| pass | gap (`readback_failed`) | none | L0 | `L0 · VERIFIED`, the gap listed |
| pass | gap | none | L1 | `L0 · UNVERIFIABLE` + `level_below_required` |
| gap (`response_schema_invalid`) | pass | none | L0 | `L1 · VERIFIED`, the L0 gap listed |
| pass | pass | gap (an L2 gap, S21) | L2 | `L1 · UNVERIFIABLE` + `level_below_required` |
| pass | pass | violation | L0 | `L2 · MISMATCH` |
| none | none | none | L0 | `L0 · UNVERIFIABLE` + `no_checks` |

Why per level, and not "any gap anywhere gives UNVERIFIABLE": an L2 gap (say, a table without a
primary key) must not erase a clean L1 read-back. The deed then says `L1 · VERIFIED` and lists the
L2 gap: it claims exactly what was checked. The required level stops this from weakening a policy:
with `verify: L2`, the same L2 gap gives `UNVERIFIABLE`.

## L0: the tool's answer

L0 runs for every executed action, inline, with no I/O.

| Situation | Reasons |
|---|---|
| Succeeded, gateway mode | `tool_reported_success` (pass); method `reported` |
| Succeeded, proxy mode (the edge ran the tool and reported it) | `edge_reported_success` (pass); method `edge_reported` |
| The tool has an output schema and the output matches it | + `response_schema_valid` (pass); method `response.schema` |
| The output doesn't match the schema | + `response_schema_invalid` (gap, with the path of the first failure). No schema pass is added. |
| The output was over 256 KiB, so only its digest was kept | + `schema_skipped` (note) |
| Outcome unknown (timeout, crash, lost edge, client cancel) | `outcome_unknown` (gap, with the cause) |
| Failed (including an MCP result with `isError: true`) | No witness: the action is sealed as `failed`. |

An unknown outcome is never read back. A read-back can't contradict a tool that made no claim, so
the action stays `L0 · UNVERIFIABLE` and the deed says plainly that the tool may have run.

## L1: read-backs

After a write, Deedglass asks the target whether the write is really there.

**When L1 runs.** For a gateway call that succeeded, when the tool has a `readback` in its
connector config, or it is `postgres.execute` running a single-row update by primary key (below).
Proxy-mode calls stay at L0: the edge ran the tool, and Deedglass has no connector to read with.

### Config reference

A read-back is set per write tool in the connector config (admin only). The REST example from
`examples/connectors/orders.yaml`:

```yaml
    - name: update_status
      method: PATCH
      path: /v1/orders/{id}
      idempotency_header: Idempotency-Key
      args:
        id:     { in: path, type: string, pattern: "^[0-9]{1,12}$", required: true }
        status: { in: body, type: string, enum: [pending, shipped, cancelled], required: true }
      readback:
        tool: get
        args: { id: "{{args.id}}" }
        expect: { "body.status": "{{args.status}}" }
        consistency: strong
        attempts: 1
        interval_ms: 300
        on_not_found: gap
      postcondition: 'observation.body.status == args.status'
```

| Key | Default | Limit | What it does |
|---|---|---|---|
| `readback.tool` | required | a tool of the same connector with `side_effect: read` that is idempotent, and not the tool itself | The read tool the read-back calls. A tool that writes is refused when the config is saved. |
| `readback.args` | none | each key an argument of the read tool | The read tool's arguments: literals, or whole-value templates `{{args.<path>}}` (the write's arguments) or `{{result.body.<path>}}` (the write's response body). |
| `readback.expect` | none | at most 32 paths, each matching `^[a-z_][a-z0-9_]*(\.[a-z0-9_]+\|\[[0-9]+\])*$` | Path to expected value. Paths start at the observation: `body.<field>`, `status` (the HTTP status) or `meta.<key>`. Values are literals or whole-value templates. |
| `readback.consistency` | `strong` | `strong` or `eventual` | `strong` reads once. `eventual` retries a disagreeing or missing answer. |
| `readback.attempts` | 1 (`eventual`: 3) | 1 to 5; above 1 needs `eventual` | How many reads `eventual` may make. |
| `readback.interval_ms` | 300 | 100 to 2000 | The wait between `eventual` attempts. |
| `readback.on_not_found` | `gap` | `gap` or `violation` | What "nothing there" means. `gap` (`readback_not_found`) suits targets that may lag; `violation` (`readback_absent`) suits targets that are always current. |
| `postcondition` | none | CEL, at most 2,048 characters, estimated cost at most 100,000; needs a `readback` | A condition over the observation, for checks `expect` can't express. |

Server settings ([CONFIG.md](CONFIG.md)):

| Key | Default | Limit | What it does |
|---|---|---|---|
| `witness.inline_budget` | `1500ms` | 100 ms to 5 s | The longest a read-back may hold the request path. Past it, the worker finishes the read-back. |
| `witness.readback_timeout` | `10s` | 100 ms to 60 s | The worker's limit for one read-back job. Past it, the read-back is the gap `readback_timeout`. |
| `witness.max_detail_bytes` | `65536` | 4,096 to 262,144 | The largest `witness_detail` (RFC 8785 form). Beyond it, diff rows and evidence are cut and counted in `truncated`. |
| `gate.worker.max_attempts` | `8` | 1 to 100 | Attempts per witness job that can't run before the Witness decides without it. |

The method of an L1 claim is `readback.<connector type>.<read tool>` when `expect` is set, and
`postcondition.<connector type>` when only a postcondition is. With both, both run and both
reasons are listed. A read-back with neither passes when the read tool answers
(`readback_matched`: "the record is there").

### The REST read-back

The read tool runs through the same path as any call to it:

- its argument schema is checked again, and path values pass S16's path rules again, so a template
  that resolves to `../admin` is refused (`readback_failed`, `validation_failed`) and no request is
  sent;
- egress and the pinned address, as for every call ([NETWORK_EGRESS.md](NETWORK_EGRESS.md));
- the credential is added last; redaction and the size cap apply.

It is not an action: it gets no deed of its own and doesn't spend the agent's budgets. The detail
keeps only the values of the compared fields; the body itself is kept as a digest. What the
read-back did goes into `levels.L1.readback`: `tool`, `target`, `request_digest`, `status`,
`response_digest`, `attempts` and `latency_ms`.

| The read tool | Reason |
|---|---|
| answered, and every `expect` field and the postcondition agree | `readback_matched` / `postcondition_held` (pass) |
| answered, and a field or the postcondition disagrees | `postcondition_failed` (violation, with a diff) |
| found nothing (404) | `readback_not_found` (gap) or, with `on_not_found: violation`, `readback_absent` (violation) |
| failed (5xx, refused, blocked, invalid arguments) | `readback_failed` (gap, with the connector's error code) |
| ran out of time in the worker | `readback_timeout` (gap) |
| the postcondition errored or hit its cost limit | `postcondition_error` (gap) |

### Eventual consistency

With `consistency: eventual`, a disagreeing answer or a not-found is read again, up to `attempts`
times, `interval_ms` apart. Only the last attempt counts, and the attempt count is recorded. The
retries never run past the budget: when the next attempt wouldn't fit, the last answer stands. A
transport error isn't retried by the Witness (the connector's own retries for idempotent reads
still apply).

### The Postgres row read-back

For `postgres.execute`, Deedglass reads the row back when the statement is one simple `UPDATE` of
one row by its primary key:

- `sqlguard.ShapeOf` recognises every token: a single-table `UPDATE … SET … WHERE …` whose `WHERE`
  names exactly one row;
- the `WHERE` columns are exactly the table's primary key (`pg_index.indisprimary`, read as the
  agent role, so a table the agent can't see isn't read);
- the key values are literals or `$n` parameters, and the statement's transaction id was recorded.

The read runs in a `READ ONLY` transaction with `statement_timeout` of 2 s, as the agent role,
through the connector's pinned pool, and is always rolled back. Table and column names come only
from the catalog; every value is a parameter.

**How values are compared.** Each `SET` literal or `$n` parameter is sent as text together with the
column's type, so Postgres parses `'t'` as a boolean and `10.50` as a numeric exactly as it did for
the write. Each column is compared with `IS NOT DISTINCT FROM`, so `NULL` compares as a value. A
`SET` value that is an expression (`total + 1`, `now()`) can't be predicted, so it isn't compared;
the evidence says how many columns were (`2 of 3 columns`).

**The xmin rule, in plain words.** Every row version in Postgres records the transaction that
wrote it (`xmin`, 32 bits). Deedglass knows this action's transaction id (`xid8`), so:

- If the row's `xmin` equals the low 32 bits of this action's transaction id, the row Deedglass
  reads is the one this action wrote. Then the values decide: all equal is `row_written` (pass);
  a column that differs is `value_mismatch` (violation), for example when a trigger rewrote it.
- If `xmin` is different, another transaction has written the row since. A read-back can't tell
  what this action wrote, so it is `row_superseded` (gap), never a violation.
- If the row isn't there, it is `row_missing` (gap).

Anything else (a `WHERE` that isn't the primary key, a join, a CTE, several rows, an `INSERT` or a
`DELETE`) gets the note `readback_skipped` and stays at L0 until L2 witnesses the connector. The
method is `readback.postgres.row`.

### Timing: B1 and B2

Most tools get only L0: the verdict and the deed are written in the same transaction as the
execution, as before S19. A planned read-back splits that transaction in two:

1. **B1** commits `executed → witnessing` and an `l1_readback` job due at now + `witness.inline_budget`
   + 2 s. The job carries the evidence a read-back needs after a restart (at most 16 KiB: ids,
   digests and small numbers).
2. **The read-back** runs with no transaction open, within `witness.inline_budget` (1,500 ms by
   default).
3. **B2** commits the verdict, seals the deed and deletes the job, in one transaction. Every step is
   a compare-and-set on the action's state.

If the budget runs out first, the agent gets `202` with `status: "processing"`, and the worker
finishes the read-back when the job falls due, within `witness.readback_timeout` (10 s). If the
server stops between B1 and B2, the job falls due the same way, and the action is sealed exactly
once. A job that can't run (the connector can't be resolved, the database fails) is retried with
backoff (2^n s, at most 5 min). After `gate.worker.max_attempts` the Witness decides without L1 and
lists `witness_gave_up` as an L1 gap: with `verify: L0` and a clean L0, the deed then says
`L0 · VERIFIED` and lists the gap.

## Writing expect and postconditions

**Prefer `expect`.** It is exact, per field, and its diff names every field that disagrees. Use it
whenever the check is "this field now has this value".

**Use a postcondition** for what `expect` can't say: a comparison other than equality, a condition
across several fields, or a field that must merely exist.

```yaml
expect: { "body.status": "{{args.status}}", "body.id": "{{args.id}}" }
postcondition: 'observation.body.updated_at >= result.output.body.updated_at && has(observation.body.shipped_at)'
```

### Templates

Templates are whole values only: `"{{args.<path>}}"` or `"{{result.body.<path>}}"`. There is no
string interpolation, so a template can't build a path or a query. A template must name an
argument the write tool declares; anything else is refused when the config is saved.

### How `expect` compares

JSON equality after decoding numbers exactly: numbers by value (`1.0` equals `1`), strings exactly,
objects and arrays member by member. One convenience: a string of decimal digits equals a number
with the same digits, so `"42"` equals `42`, and `"042"` doesn't.

### The CEL environment

Postconditions use the same `cel.dev/cel-go` v0.32.0 as policies (ADR-017), with four variables,
all maps:

| Variable | Holds |
|---|---|
| `args` | The write's arguments. |
| `request` | `tool`, `args` and `mode` (`gateway`). `request.status` isn't a field; the config check suggests `args.status`. |
| `result` | `output` (the write's output) and `meta` (`http_status`, `rows_affected`, …). |
| `observation` | `body`, `status` and `meta` of the read-back. |

Limits: at most 2,048 characters; the result must be exactly `true` or `false`; the estimated cost
must be at most 100,000 when the config is saved (strings are assumed up to 4 KiB, lists and maps
up to 256 entries); at run time `CostLimit` is 100,000 and an interrupt check runs every 64
iterations. Compiled programs are cached per connector config and tool.

Postconditions never use `ext.NativeTypes`: every variable is a plain map. The advisory
[GO-2026-6094](https://pkg.go.dev/vuln/GO-2026-6094) reports private struct fields exposed through
`NativeTypes` and `ParseStructTag` in cel-go before v0.30.0 (checked 2026-10-02). Deedglass pins
v0.32.0.

**A false postcondition explains itself.** Deedglass splits the condition at its top-level `&&`
and re-checks each part. For each comparison that is false (`==`, `!=`, `<`, `<=`, `>`, `>=`), the
diff shows the left operand as the path, its value as observed and the right value as expected.
For `observation.body.a == 1 && observation.body.b == "x"` where only `b` is wrong, the diff has
one row, for `b`. An error at run time (a missing key, a division by zero, the cost limit) is the
gap `postcondition_error`; guard optional fields with `has()`.

### Config errors

The connector's config check refuses a bad read-back with `validation_failed` and the field's
`param` ([CONNECTORS.md](CONNECTORS.md#read-backs) lists them):

```text
config.tools[1].readback.tool: "update_status" writes, so it can't be a read-back. Name a read tool such as "get".
config.tools[1].postcondition: 1:28: request.status isn't a field. Did you mean args.status?
```

## Reading a MISMATCH

With `examples/mock-orders` in lying mode (`MOCK_ORDERS_MODE=lying`), the API answers `200` and
saves nothing:

```text
$ deedglass act orders.update_status --args '{"id":"42","status":"shipped"}'
✕ executed · orders.update_status · 41 ms
  witness  L1 · MISMATCH · orders.update_status reported success, but orders.get says status is "pending", not "shipped".
           field          expected    observed
           body.status    shipped     pending
  deed     deed_01j9zx4k… · seq 311 · sealed
  Next: deedglass actions show act_01j9zx4k1b8q2w7x4t6v0b1c9d · https://docs.deedglass.com/witness/mismatch
```

In honest mode the same command prints:

```text
✓ executed · orders.update_status · 38 ms
  action   act_01j9zx6p2n4r8s1t3v5w7x9y0z
  witness  L1 · VERIFIED · Read back orders.get: status is "shipped".
  deed     deed_01j9zx6p… · seq 312 · sealed
```

With the read API down and a policy that says `verify: L1`:

```text
○ executed · orders.update_status · 38 ms
  witness  L0 · UNVERIFIABLE · The policy asks for L1. The strongest clean evidence was L0.
           ○ L1  The read-back orders.get failed (upstream_error), so the result couldn't be confirmed.
  deed     deed_01j9zx7r… · seq 313 · sealed
  Next: check the orders connector (deedglass connector test orders), then deedglass actions show act_01j9zx7r2k5m8p1q4s7u0w3y6a
```

`deedglass actions show` lists every reason, passes included:

```text
$ deedglass actions show act_01j9zx4k1b8q2w7x4t6v0b1c9d
orders.update_status · support-bot · 2026-10-07 04:41:22 UTC
  decision   allow · rule allow-order-updates · pv_01j9zr2a…
  execution  executed · 41 ms
  witness    L1 · MISMATCH · readback.rest.get
             ✓ L0  The tool reported success.
             ✕ L1  orders.update_status reported success, but orders.get says status is "pending", not "shipped".
                   field          expected    observed
                   body.status    shipped     pending
  deed       seq 311 · deed_01j9zx4k… · sealed with sk_01j9zr2a…
  events     received → authenticated → evaluated → executing → executed → witnessing → mismatch → sealed
  next       deedglass deeds show deed_01j9zx4k2m6p9r3t5v8x1z4b7c
```

`--json` prints the API's action object unchanged. `deedglass act` exits 1 on a `MISMATCH` and 0 on
`VERIFIED` or `UNVERIFIABLE`, so a script can stop on a contradiction.

**How to read it.**

1. The claim line: the level, the verdict, and the one-sentence summary (or the method).
2. The diff: each field the read-back compared, what the write implied, what the target shows.
3. The other reasons: gaps at other levels are listed, never hidden.
4. Then check the target. A `MISMATCH` means the write isn't there as the tool said; the reason's
   "what to do next" in [witness/REASONS.md](witness/REASONS.md) says where to look.

### In the API

`GET /v1/actions/{id}` and the Gate's own responses carry the `witness` object:

```json
"witness": {
  "object": "witness",
  "level": "L1",
  "verdict": "MISMATCH",
  "method": "readback.rest.get",
  "required_level": "L1",
  "summary": "orders.update_status reported success, but orders.get says status is \"pending\", not \"shipped\".",
  "reasons": [
    { "code": "postcondition_failed", "class": "violation", "level": "L1", "check": "readback.expect",
      "message": "orders.update_status reported success, but orders.get says status is \"pending\", not \"shipped\".",
      "diff": [ { "path": "body.status", "expected": "shipped", "observed": "pending" } ] }
  ],
  "witnessed_at": "2026-10-07T04:41:22.418Z",
  "detail_digest": "sha256:c3e1…9b20"
}
```

- Lists carry `object`, `level`, `verdict`, `method`, `required_level` and `detail_digest`. Single
  reads and the Gate's responses add `summary`, `reasons` (violations and gaps) and `witnessed_at`.
- `?expand[]=witness.detail` adds `detail`: the full `witness_detail`, with passes and notes.
- `GET /v1/actions?verdict=MISMATCH&level=L1` filters by verdict and level;
  `deedglass actions list --verdict MISMATCH` does the same from the CLI.
- The `action.witnessed` event carries `level`, `verdict`, `required_level` and `flags` (for
  example `["level_below_required"]`).

### `witness_detail` v1

```json
{
  "object": "witness_detail",
  "version": 1,
  "claim": { "level": "L1", "verdict": "MISMATCH", "method": "readback.rest.get", "required_level": "L1" },
  "summary": "orders.update_status reported success, but orders.get says status is \"pending\", not \"shipped\".",
  "levels": {
    "L0": { "verdict": "VERIFIED", "method": "reported" },
    "L1": { "verdict": "MISMATCH", "method": "readback.rest.get",
            "readback": { "tool": "orders.get", "target": "id \"42\"", "request_digest": "sha256:5d1c…",
                          "status": 200, "response_digest": "sha256:a07e…", "attempts": 1, "latency_ms": 12 } },
    "L2": null
  },
  "reasons": [
    { "code": "tool_reported_success", "class": "pass", "level": "L0", "check": "status",
      "message": "The tool reported success." },
    { "code": "postcondition_failed", "class": "violation", "level": "L1", "check": "readback.expect",
      "message": "orders.update_status reported success, but orders.get says status is \"pending\", not \"shipped\".",
      "diff": [ { "path": "body.status", "expected": "shipped", "observed": "pending" } ] }
  ],
  "witnessed_at": "2026-10-07T04:41:22.418Z"
}
```

A level that ran no check is `null`. Reasons are sorted by level, then class (violation, gap,
pass, note), then code. `truncated` appears when diff rows or evidence were cut to stay under
`witness.max_detail_bytes`. `detail_digest` is `sha256:` of this object's RFC 8785 form. From
version 1 on, changes to this shape are additive only.

### Over MCP

Results of `tools/call` through the gateway and `/mcp/native` carry
`_meta["com.deedglass/witness"] = {"level": "L1", "verdict": "MISMATCH"}`. When the verdict isn't
`VERIFIED`, one text block is appended:

```text
Deedglass witness · L1 · MISMATCH · orders.update_status reported success, but orders.get says status is "pending", not "shipped".
```

`isError` stays `false`, because the tool did run. Proxy mode (`deedglass mcp wrap`) returns before
the witness and is unchanged.

## What the Witness doesn't do

- **L1 confirms what the target reports now, not what it reported at commit time.** A concurrent
  change can make a REST read-back disagree, and the diff says so. For Postgres, the xmin rule
  turns that case into a gap, never a `MISMATCH`.
- **L1 checks only the fields you name.** A tool that also changed something else is invisible to
  L1. L2 (S21) sees every row change for Postgres.
- **A target that lies consistently can't be caught by reading it back.** L1 trusts the read tool
  as much as the system it reads. L2 reads Postgres's own change stream instead.
- **No read-back in proxy mode, and none for an unknown outcome.** Both stay at L0.
- **No read-back for most Postgres statements.** Only a single-row `UPDATE` by primary key is read
  back; everything else waits for L2.
- **The L0 schema check of a 64 KiB output takes about 1 ms** (0.99 to 1.00 ms per check on an
  Apple M1, `bench/results/2026-10-02-darwin-arm64-witness.json`), above the 200 µs target. The
  cost is in S16's schema validator (`internal/connector/schema`), and it applies only to tools
  with an output schema.
- **A verdict is about the action, not about whether it was a good idea.** `VERIFIED` means the
  write is there as the tool said; it says nothing about whether the agent should have made it.
