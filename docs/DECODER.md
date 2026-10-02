# The decoder

The decoder reads what each watched Postgres database actually committed. Every committed
transaction reaches Deedglass as exact before and after images of its rows, read from the
database's own change stream (logical decoding, pgoutput). A transaction that carries a Deedglass
action is **bound** to that action; everything else is classified and kept. The Witness (S21)
turns bound frames into L2 verdicts ("Witnessed by Postgres"). The decoder itself never decides a
verdict.

This page covers S20. Setting a database up is [POSTGRES.md](POSTGRES.md) and
[POSTGRES_DEPLOYMENT.md](POSTGRES_DEPLOYMENT.md); what is and isn't watched is
[OBSERVATION_BOUNDARY.md](OBSERVATION_BOUNDARY.md).

## What the decoder does

```
customer Postgres (wal_level=logical)                         Deedglass (one binary, many instances)
┌──────────────────────────────────────┐                     ┌───────────────────────────────────────────────┐
│ agent tx (S17): ledger row → SQL →   │                     │ Supervisor: a lease per connector (own DB)    │
│   pg_logical_emit_message(true,      │  WAL / pgoutput v1  │   └─ Runner(connector)                        │
│   'deedglass', act_…) → COMMIT       │ ─────────────────►  │        peek → SafeParse → assemble → classify  │
│ publication deedglass_pub            │  (SQL peek, as      │        → persist (decoded_txs, decoder_slots)  │
│ slot deedglass_<connector id>        │  deedglass_witness) │        → advance the slot                      │
│ role deedglass_witness (REPLICATION) │ ◄── advance ─────── │        → OnBound hook / NOTIFY → the Witness   │
└──────────────────────────────────────┘                     └───────────────────────────────────────────────┘
```

- **One runner per witnessed connector.** A Postgres connector opts in with `witness.enabled: true`
  in its config. Every `deedglass serve` runs a supervisor; leases in Deedglass's own database
  (`claim_decoder_leases`, renewed every 5 s) give each connector exactly one runner across all
  instances, and the runner also holds an advisory lock on the watched database. When an instance
  stops, another takes the lease within the lease time (15 s; measured 4.1 s in
  `TestTwoSupervisors`).
- **It reads with an ordinary connection** as `deedglass_witness`, using the `witness_dsn` stored
  with the connector's credential (opened through the Postgres connector, never by the decoder
  itself), dialling through Deedglass's one egress door.
- **It reads whole transactions.** Each poll peeks the slot
  (`pg_logical_slot_peek_binary_changes`, pgoutput protocol 1, messages on, origin any). Postgres
  returns whole committed transactions only, so a 12,000-row transaction arrives in one piece even
  when a poll asks for 5,000 changes (`TestLargeTransactionWhole`).
- **It parses nothing it hasn't checked.** Every message goes through `SafeParse`: the complete
  wire layout is checked first (fixed sizes, strings inside the message, declared lengths exact,
  nothing after the last field), then `pglogrepl.Parse` runs inside `recover`. On 120,631 strict
  prefixes of real messages from PG 16, 17 and 18, SafeParse gives 0 panics and 0 silent accepts;
  the raw parser panics on 119,576 of them (`TestSafeParsePrefixes`).
- **Values are exact.** The session's settings are pinned in the startup packet (`TimeZone UTC`,
  `DateStyle 'ISO, YMD'`, `IntervalStyle postgres`, `bytea_output hex`, `extra_float_digits 3`,
  which outrank `ALTER ROLE … SET`) and checked before every poll. `9007199254740993` is stored as
  that text, never through a float (`TestImagesExact`). Hostile role settings change nothing
  (`TestHostileSettings`, attack case E11).
- **It persists, then advances.** A poll's frames and the new checkpoint commit in one transaction
  in Deedglass's database; only then does the slot move. A crash anywhere loses nothing and stores
  nothing twice (below).
- **Sensitive columns are digested on arrival.** Columns listed in `witness.sensitive_columns`
  (`schema.table.column`) are stored as `sha256:` of their text, never the text.

## How a transaction is bound to an action

S17's connector runs every agent statement in a transaction Deedglass controls:

```sql
BEGIN;
SELECT xid8, marker_lsn FROM deedglass.begin_action($1, $2, $3, $4, $5);
  -- inserts one deedglass.ledger row (with xid8 = pg_current_xact_id()) and emits the
  -- transactional message pg_logical_emit_message(true, 'deedglass', '<action id>')
<the one authorized statement>;
COMMIT;
```

A frame is **bound** to an action only when the same transaction holds **exactly one ledger
insert and exactly one transactional `deedglass` marker, naming the same action**, the ledger row
belongs to the connector's org, and the low 32 bits of the row's `xid8` equal the transaction's
xid. A marker on its own says nothing about who sent it: `pg_logical_emit_message` is open to
every role by default. The ledger row is the authentication; only `begin_action`, whose body's
sha256 the runner checks at start, writes it.

Classification looks only at what is inside the transaction and each message's transactional
flag. It never uses the peek's xid column or the order of peek LSNs, and the marker's LSN is
stored as evidence and never compared with the commit LSN (a build test checks every Go file).

## Frames and classes

A **frame** is one committed transaction (or one stray message), stored in `decoded_txs` with its
system identifier, timeline and commit LSN as its identity, the tables it touched (relation id,
primary key, replica identity, columns, type oids), exact counts per table, the first 1,000 changes
with their images, a digest over every change, the ledger rows, the markers and flags.

| Class | What happened | Kept | Bound to an action | Signal |
|---|---|---|---|---|
| (not kept) | Nothing on a watched table, no ledger row, no marker | no | no | none |
| `unattributed` | Rows changed on a watched table, with no ledger row and no marker | yes | no | counted; input to Insights (S27) |
| `forged_marker` | A transactional `deedglass` marker with no ledger row | yes | no | `decoder.alert` |
| `publication_gap` | A marker with no ledger row while `deedglass_pub` changed (Postgres 18 skips rows it can't publish) | yes | yes, as an evidence gap | `decoder.degraded` |
| `forged_marker_nontx` | A non-transactional `deedglass` message that isn't this runner's heartbeat | yes | no | `decoder.alert` |
| `marker_count` | A ledger row, and more than one row or not exactly one marker | yes | the action, when every row names one | the Witness: a violation |
| `marker_mismatch` | One ledger row and one marker naming different actions, a ledger row that doesn't parse (`ledger_invalid`), or a different xid (`ledger_xid_mismatch`) | yes | the ledger row's action | the Witness: a violation |
| `foreign_ledger` | A ledger row of another org | yes | no | `decoder.alert` |
| `bound` | Exactly one ledger row and one marker naming the same action, same org, same xid | yes | yes | NOTIFY + the Witness |
| `missing` | Reconcile found a ledger row with no frame (no commit LSN) | yes | yes, as an evidence gap | the Witness: `no_evidence` |

Flags add detail: `truncate:<table>`, `relation_changed:<table>` (the only trace DDL leaves in the
stream), `old_image_missing:<table>` (replica identity isn't full), `no_primary_key:<table>`,
`images_truncated` (more than 1,000 changes), `origin:<name>` (changes applied under a replication
origin), `other_messages:<n>`, `ledger_modified`, `publication_changed`.

A change looks like this (`decoded_txs.changes`; S23 plans undo from it):

```json
{"seq": 1, "op": "U", "table": "public.orders", "key": {"id": "42"},
 "old": {"id": {"t": "42"}, "status": {"t": "paid"}, "note": {"n": true}},
 "new": {"id": {"t": "42"}, "status": {"t": "shipped"}, "note": {"n": true}},
 "changed": ["status"]}
```

A value is `{"t": text}`, `{"n": true}` (NULL), `{"u": true}` (an unchanged TOAST value Postgres
didn't send), `{"d": "sha256:…"}` (a sensitive column) or `{"x": hex}` (text that isn't UTF-8).
The frame's `changes_digest` is `sha256` over `JCS({new, old, op, seq, table}) + "\n"` for every
change in order, including those past the image cap.

### Persist, then advance

```
1. assert the settings · read the flush LSN · read the publication       (one round trip)
2. peek (whole transactions) → SafeParse → assemble
3. read the publication again · collect notices
4. classify (pure) · look up primary keys of new relations
5. ONE transaction in Deedglass's DB: insert frames (duplicates skipped), the checkpoint,
   counters, NOTIFY deedglass_jobs for frames that name an action, decoder.alert events · COMMIT
6. pg_replication_slot_advance(slot, checkpoint)
7. OnBound hooks · metrics
```

| Crash point | On restart | Outcome |
|---|---|---|
| During steps 1 to 4 | Nothing persisted; the slot hasn't moved | The same frames are read again |
| After 5, before 6 | The slot hasn't moved; the same frames return | Inserts skip duplicates: nothing stored twice (E01, `TestCrashBetweenPersistAndAdvance`) |
| After 6, then Postgres crashes before its checkpoint | The slot's position rewinds (it is written out at the next checkpoint) | Re-delivered frames are skipped (E02, `TestRewindAfterCrash`: the position went from 0/1C6E920 back to 0/1C6E5A0, and the transaction was stored once) |
| Deedglass's DB is down at 5 | No advance | Retried with backoff; the lag grows; the alert fires at the threshold |

A frame's identity is (connector, system identifier, timeline, commit LSN, and whether it is a
stray message): delivery is at least once, storage exactly once. 500 actions across a supervisor
restart give exactly 500 bound frames (`TestBurstAcrossRestart`).

**The checkpoint** is the largest commit end LSN or message LSN the poll returned, and, when the
peek drained everything, the flush LSN read before it (ADR-045). So when the watched tables are idle
and the rest of the database is busy, the slot still follows the database: after 50 MB of WAL on an
unwatched table, the slot held 624 bytes (`TestIdleAdvance`).

### Reconcile

Every `decoder.reconcile_interval` (60 s) the runner reads the ledger rows created since its
watermark (minus 5 minutes of overlap), **then** reads the current WAL position `q`. Once the
checkpoint reaches `q`, every ledger row of the connector's org without a frame is recorded as
`missing`. The order matters: a row visible in the first read committed before it, so its commit
record is below `q`. (V0's spec read `q` first, which misses a transaction committing between the
two reads.) The watermark survives a new slot, so actions committed while there was no slot (an
invalidated or dropped one) are found (`TestDropAndReslot`, `TestSlotInvalidated`).

### The heartbeat

Every 10 s the runner writes a non-transactional `deedglass` message to the watched database:
`hb1:<connector>:<unix ms>:<hmac>`, keyed by 32 random bytes held only in the runner's memory.
Reading it back shows the slot is moving. Anything else under `hb1:` is a forged message. A
heartbeat dated before the runner started is ignored, and so is one for another connector watching
the same database.

## Health and alerts

`deedglass decoder status` shows every witnessed connector:

```
$ deedglass decoder status
✓ postgres  · healthy · lag 3.9 kB · last commit 4s ago
◐ billing   · connecting · attempt 3 · next try in 8s
  Last error: dial tcp 10.0.0.5:5432: connect: connection refused
✕ analytics · halted · slot invalidated at 2026-10-07 07:40 UTC (wal_status lost)
  Actions waiting on L2 for analytics won't get that evidence (slot_invalidated).
  Next: deedglass decoder drop --connector analytics, then check max_slot_wal_keep_size (docs.deedglass.com/decoder#sizing)
```

`--connector <name> --recent N` shows one in detail (the slot, positions, watched tables, counters
and the newest frames), and `--json` gives the decoder object of
`GET /v1/connectors/{id}/decoder`. A halted decoder exits 1.

| Condition | Status | Reason | What it means |
|---|---|---|---|
| Lag above min(`DEEDGLASS_SLOT_LAG_ALERT_MB`, a quarter of a finite `max_slot_wal_keep_size`) | degraded | `lag_high` | Still reading; behind |
| The slot keeps more than 4 × that threshold | degraded | `restart_retained` | A long transaction elsewhere holds the restart point |
| No heartbeat read back within 60 s | degraded | `stalled` | The runner isn't reading |
| The publication changed during a poll (or PG 18 skipped it) | degraded | `publication_changed` | Frames from that poll are flagged; marker-only ones become `publication_gap` |
| One read takes more than 2 s | degraded | `peek_slow` | Usually a long transaction |
| A new timeline | degraded | `timeline_changed` | Failover or restore; frames are keyed by the new timeline |
| `wal_status lost`, an invalidation reason, or `conflicting` | halted | `slot_invalidated` | Evidence was lost |
| The slot is gone (dropped outside Deedglass) | halted | `slot_missing` | Evidence was lost |
| Peek error "publication … does not exist" (16, 17) | halted | `publication_missing` | Postgres can't read past that point |
| A message that isn't valid pgoutput | halted | `malformed_evidence` | Nothing after it is used; retried every 5 minutes |
| Session settings changed | halted | `session_settings_changed` | Values could differ |
| `begin_action`'s body isn't S17's | halted | `ledger_function_changed` | Ledger rows can't be trusted |
| A different cluster (system identifier) | halted | `system_changed` | A restore or a new server |
| `wal_level`, REPLICATION, a slot of another plugin, a publication without a watched table | halted | `wal_level`, `not_replication`, `slot_conflict`, `publication_invalid` | Setup |

Each move into degraded or halted publishes `decoder.degraded` (`state`, `reason`, `lsn`); the way
back publishes `decoder.recovered`. Security signals publish `decoder.alert` with the `kind`
(`forged_marker`, `forged_marker_nontx`, `foreign_ledger`), the frame's commit LSN and the action
it names. Events are ids and states only. Halts that lose evidence tell the Witness (`SlotLost`), so
actions waiting for L2 become evidence gaps (S21).

Metrics (`DEEDGLASS_METRICS_LISTEN`): `deedglass_decoder_frames_total{connector,class}`,
`deedglass_decoder_halts_total{connector,reason}`, `deedglass_decoder_peek_seconds` (histogram),
`deedglass_decoder_lag_bytes`, `deedglass_decoder_retained_bytes`, `deedglass_decoder_running`.
`/v1/health` reports `checks.decoder` as `ok` or `degraded` for the runners on that instance.

### Settings

| Setting | Default | What it does |
|---|---|---|
| `decoder.enabled` | `true` | Run the decoder on this server |
| `decoder.transport` | `peek` | SQL peek, then advance (streaming is planned) |
| `decoder.poll_active` / `decoder.poll_idle` | `100ms` / `1s` | Poll interval while an action waits for L2 / otherwise; doubles (to 5 s) while a read takes over a quarter of it |
| `decoder.batch_changes` | `5000` | Rows one read asks for; transactions always come whole |
| `decoder.max_images_per_tx` | `1000` | Changes per transaction that keep their images (S23's undo limit) |
| `decoder.heartbeat_interval` / `decoder.heartbeat_deadline` | `10s` / `60s` | Heartbeat cadence, and when its absence degrades |
| `decoder.lease` | `15s` | How long an instance holds a slot without renewing |
| `decoder.reconcile_interval` | `60s` | How often ledger rows are checked for frames |
| `decoder.retention` | `168h` | How long frames (with row images) are kept; unattributed and forged-marker frames stay 30 days |
| `DEEDGLASS_SLOT_LAG_ALERT_MB` | `512` | The lag alert |
| `DEEDGLASS_DECODER_PAUSE` | unset | Dev only (refused in prod): runners stop reading, so tests can see late evidence |

## Sizing and slot safety

A slot keeps WAL until Deedglass has read it. Three things keep that bounded:

- **Set `max_slot_wal_keep_size`.** With it, a slot that falls too far behind is invalidated
  instead of filling the disk; Deedglass halts (`slot_invalidated`) and reconcile records the actions
  it missed. Size it from your WAL rate: at 5 MB/s of WAL, `4GB` covers about 13 minutes of the
  decoder being down; `10GB` about 34 minutes. [POSTGRES_DEPLOYMENT.md](POSTGRES_DEPLOYMENT.md#sizing)
  has the worked example.
- **The checkpoint follows idle WAL** (ADR-045), so idle watched tables don't hold the database's
  WAL.
- **The lag alert** fires at min(512 MB, a quarter of `max_slot_wal_keep_size`).

Each peek starts from the slot's restart point. A long-running transaction anywhere in the database
holds that point back, so every poll reads again from it; the runner backs off (`peek_slow`) and
reports `restart_retained`. Streaming replication removes that cost and is the next transport.

## Postgres 16, 17 and 18

| Behaviour | 16.15 | 17.11 | 18.6 | What the decoder does |
|---|---|---|---|---|
| `pg_logical_emit_message` | 3 arguments only | 3 and 4 | 3 and 4 | Only the 3-argument form (a build test checks every call) |
| `pg_replication_slots` | `wal_status`, `safe_wal_size`, `conflicting` | adds `invalidation_reason`, `inactive_since`, `failover`, `synced` | as 17 | The column list follows the server version; on 16, `wal_status lost` alone means invalidated |
| The publication missing at a WAL position | error, even after it is re-created | same | a warning; row changes skipped, messages kept | 16/17 halt `publication_missing`; 18 gives `publication_gap`, never `forged_marker` (`TestPublicationDropped`) |
| `idle_replication_slot_timeout` | n/a | n/a | new (default 0) | Polling keeps the slot active; an invalidation is handled like any other |
| `pg_control_system()` | PUBLIC | PUBLIC | PUBLIC | The system identifier; the timeline comes from the current WAL file name |

Evidence: `ops/spikes/evidence/version_probes_pg{16,17,18}.txt`, `s1_s2_s3_pg{16,17,18}.txt`,
`lsngap_pg18.txt`; the integration suite runs on all three (`make matrix`).

## Dropping and recovering a slot

```
$ deedglass decoder drop --connector postgres
This drops slot deedglass_01j9zq7c4m8x2v6t0r3n5k1h7d on "postgres".
Actions still waiting for L2 evidence on this connector won't get it (slot_dropped).
A new slot starts a new baseline. Type the connector name to confirm: postgres
✓ Slot dropped · deedglass_01j9zq7c… · admin deed deed_01j9zv8k… · a new slot starts on the next sync
```

Dropping stops the runner, drops the slot as `deedglass_witness`, seals the admin deed
`decoder.slot_dropped` and marks the decoder `dropped`. The next sync creates a new slot (sealed as
`decoder.slot_created`): a new baseline. Reconcile then records every action committed while there
was no slot as `missing`. `DELETE /v1/connectors/{id}/decoder` with `{"confirm": "<name>"}` does the
same over the API.

Runbook:

1. **Halted, `slot_invalidated`:** drop, then raise `max_slot_wal_keep_size` or fix what kept the
   decoder down.
2. **Halted, `publication_missing` (16, 17):** re-create the publication with
   `deedglass connector postgres init`, then drop the slot. Postgres can't read past the gap.
3. **Halted, `malformed_evidence`:** send `deedglass decoder status --connector <name> --json` to
   support; dropping the slot skips the bad message (reconcile records the actions it held).
4. **Halted, `system_changed`:** the connector points at another cluster; drop to start there.
5. **Deleting a witnessed connector** drops its slot first and seals `decoder.slot_dropped`
   (`connector_deleted`). When the database can't be reached, the decoder shows `disabled ·
   slot_orphaned` with the slot's name and the SQL to drop it by hand.

## What the decoder doesn't do

- **It doesn't decide verdicts.** It produces frames; the Witness (S21) compares them with each
  action. Until S21, frames are stored and counted, and nothing is amended.
- **It sees only the publication's tables.** Unpublished, unlogged and temporary tables, sequences
  and large objects are invisible. DDL shows only as a relation shape change. See
  [OBSERVATION_BOUNDARY.md](OBSERVATION_BOUNDARY.md).
- **`deedglass_witness` could decode every table in its database.** REPLICATION isn't scoped to
  tables (spike S1(h): it decoded rows it couldn't `SELECT`). Deedglass stores only the
  publication's tables, and the credential lives in the vault.
- **It doesn't stream.** It peeks over SQL, which re-reads WAL from the slot's restart point on every
  poll. Streaming (`START_REPLICATION`) is the next transport.
- **Failover slots and point-in-time recovery aren't tested.** A new timeline degrades the decoder
  and frames are keyed by it; a different cluster halts it.
- **Stored images are customer data.** They are kept for `decoder.retention` (7 days), under
  row-level security, with sensitive columns digested. A frame keeps at most 1,000 images; counts
  and the digest cover the rest.
- **A huge single message is held in memory.** Postgres returns a logical message's content in one
  piece; a role that writes a 500 MB message makes the decoder hold it once.
- **A heartbeat for another connector can't be checked**, so one claiming to be another
  connector's is ignored rather than alerted.
