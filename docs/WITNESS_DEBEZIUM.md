# Checking an action against a Debezium change feed

`deedglass witness debezium` compares what an agent's statement should have changed with what
another database committed, as a [Debezium](https://debezium.io) change feed shows it (TY-05,
ADR-114). It runs the same comparison as the Proven (L2) check on Postgres (the footprint, the row
counts, the keys, the columns and the diff of [`DECODER.md`](DECODER.md) and the L2 witness) and labels
the level by where the evidence came from:

```
L2 · Doesn't match · Proven · via Debezium (MySQL)
MySQL also changed 300 rows in shop.shipments, which the statement doesn't touch.

  expected   UPDATE 1 row in shop.orders where id = 42 · sets status
  observed   UPDATE 1 row in shop.orders · UPDATE 300 rows in shop.shipments
  …
Proven · via Debezium (MySQL) · transaction d37cd757-cd2b-11f1-927d-b22b7c5c4779:17 · mysql-bin.000003:1230 · 2026-10-21 08:46:11 UTC
```

The record's method says the same: `debezium.mysql` (or `debezium.postgresql`, `debezium.sqlserver`,
and so on, from the event's `source.connector`). A "Proven" from a foreign feed rests on that feed:
Deedglass reads what Debezium carried, not the database's own log.

It is offline. It reads events from a file or stdin and connects to nothing. The Gate doesn't call
it yet: there is no MySQL connector, so for now you run it beside an action, or over a feed you
recorded.

## What it reads

JSON lines, one event per line, in any of these shapes:

- the change event's value (`before`, `after`, `source`, `op`, `transaction`), as Debezium's JSON
  converter writes it with `schemas.enable=false`;
- the same wrapped with its schema (`{"schema": …, "payload": …}`);
- a `{"key": …, "value": …}` pair: the key's fields become the table's primary key;
- the transaction metadata events (`{"status": "BEGIN" | "END", "id": …, "event_count": …,
  "data_collections": […]}`);
- a JSON array of any of these (the HTTP sink's batch mode).

Schema change events, heartbeats, logical messages, tombstones and snapshot reads (`op: r`) are
skipped. `.jsonl.gz` files are read as gzip.

```sh
deedglass witness debezium --events feed.jsonl --schema shop \
  --pk shop.orders=id --pk shop.shipments=order_id \
  --sql "UPDATE orders SET status = 'shipped' WHERE id = 42" --reported 1 \
  --action act_01k82m4v9q5n3d7f8g2h6j0kxp
```

| Flag | |
|---|---|
| `--events <file\|->` | the events; `-` reads stdin |
| `--sql`, `--sql-file` | the statement the agent ran |
| `--reported <n>` | the row count the agent reported; leave it out when it reported none |
| `--schema <name>` | where unqualified table names live (MySQL: the database) |
| `--pk table=col[,col]` | a table's primary key, when the events carry no key (the HTTP sink sends values only) |
| `--action <act_…>` | the action the ledger row names |
| `--org`, `--agent`, `--sql-digest` | compared with that ledger row when given |
| `--ledger-table <name>` | default `deedglass_ledger` in any database |
| `--tx <id>` | compare this transaction (a MySQL GTID) when no ledger row names the action |
| `--max-rows table=n` | the most rows one action may change in a table |
| `--json` | the `foreign_witness` object, with the `witness_detail` and its digest inside |

Exit status: 0 Matches, 1 Doesn't match, 2 Can't check or a usage error.

## Which transaction is the action's

**The feed carries the action id** when the writer inserts one row into the ledger table in the
action's own transaction, the MySQL counterpart of `deedglass.begin_action`'s ledger row:

```sql
START TRANSACTION;
INSERT INTO deedglass_ledger (action_id, org_id, agent_id, sql_digest)
VALUES ('act_01k82m4v9q5n3d7f8g2h6j0kxp', 'org_…', 'agt_…', 'sha256:…');
UPDATE orders SET status = 'shipped' WHERE id = 42;
COMMIT;
```

The table needs those four columns (`intent_hash` is read when present); capture it in the
connector like any other table. A transaction whose ledger insert names the action is that action's
(attribution `action`); its ledger row is cross-checked against `--org`, `--agent` and `--sql-digest`
(`ledger_mismatch` names the field), and it never counts as a changed table. Two ledger rows in one
transaction is `marker_count`; two transactions naming the action is `multiple_transactions`.

The ledger row here is written by your code, not by a Deedglass function the decoder checks, so it
shows which transaction the writer said was the action's. It isn't the authentication the Postgres
connector's `begin_action` gives.

**Without a ledger row, any writer counts.** The transactions in the feed that touch the
statement's tables (and hold no other action's ledger row) are compared as the action's
(attribution `any_writer`), and the note `any_writer` says so in the record: "The Debezium feed from
MySQL doesn't carry this action's id, so changes from any writer to these tables are compared as
this action's." Two such transactions give `multiple_transactions`. Narrow the feed to the action's
time, or name the transaction with `--tx` (attribution `named`; the note still applies).

A feed with no transaction that could be the action's is the gap `feed_no_match`.

## A transaction the feed holds only part of is never compared

Debezium delivers at least once and can be cut or replayed. A transaction is compared only when
the feed holds its BEGIN and its END, exactly the events END counts, numbered 1 to N with no gap, and
the per-table counts END lists. A repeated event that is byte-for-byte the same is dropped; a
different event at the same position stops the read. On MySQL, the first change must also be row 0
of its binlog event.

That last rule comes from a real run. When Debezium Server 3.3.1 restarted inside a 301-row
transaction, it sent BEGIN again, skipped the 48 rows it had already delivered, numbered the other
252 from 1 and sent an END that counts 252. The metadata looked whole; the order row and the ledger
row were gone. Compared, it would have said "MySQL committed 0" rows in `shop.orders`. Instead it is
the gap `feed_incomplete` (`it resumes at row 48 of a binlog event`), and the verdict is Can't check
(`internal/witness/feed/debezium/testdata/mysql-resumed.jsonl.gz`, `TestResumedFeed`).

Anything less than a whole transaction, including a feed with no transaction metadata at all, is
`feed_incomplete`. Turn on `provide.transaction.metadata`.

## Setting up Debezium

What the comparison needs from the connector:

| Setting | Why |
|---|---|
| `provide.transaction.metadata=true` | BEGIN, END and each event's `transaction` block: without them no transaction is whole |
| MySQL `binlog_row_image=FULL` | old row images, so the comparison sees which columns changed (`old_image_missing` otherwise) |
| MySQL `gtid_mode=ON` | transaction ids that stay the same across binlog files |
| `decimal.handling.mode=string` | exact decimal text, not base64 bytes |
| `include.query=true` with MySQL `binlog_rows_query_log_events=ON` | optional: each event carries the statement that wrote it |

`deploy/debezium/` is a working pair: MySQL 8.4.6 and Debezium Server 3.3.1, pinned by digest.
Debezium Server has no file or stdout sink, so it posts each event to an HTTP receiver
(`debezium.sink.type=http`); write the bodies to a file, one per line, and that file is the input.
Debezium Server 3.3.1's HTTP sink sends each record's Connect headers and keeps adding them to every
later request, so a receiver with a header limit starts answering 431 after a few dozen events;
`application.properties` drops those headers with Kafka Connect's `DropHeaders` transform.

### Kafka

There is no Kafka source yet. Reading a Kafka-compatible topic directly needs a Kafka client, a new
dependency (and an ADR); it comes when a user's feed needs it. Until then, dump the topics to JSON
lines and pipe them in, for example with kcat:

```sh
kcat -C -b broker:9092 -t shop.shop.orders -t shop.shop.shipments -t shop.transaction -e -f '%s\n' \
  | deedglass witness debezium --events - --schema shop --pk shop.orders=id …
```

Keep every topic of the tables and the transaction topic in one stream, in commit order.

## The trigger scene on MySQL

`make debezium-it` runs the scene end to end in Docker (`TestMySQLTriggerLive`, about 30 s after the
images are pulled; never in required CI): `deploy/debezium/mysql/01-shop.sql` loads 1,000 orders and
a legacy `AFTER UPDATE` trigger, Debezium Server streams the binlog to a receiver the test runs on
127.0.0.1, and the events go through this command's code.

MySQL refuses a trigger that writes the table it fires on (error 1442), so the MySQL version keeps
shipping state in `shop.shipments`: shipping order 42 makes the trigger ship the other 300 open
orders of its batch there, in the same transaction. The client is told 1 row; the feed shows 301.

| Action | Statement | Feed | Verdict |
|---|---|---|---|
| A, with its ledger row | `UPDATE orders SET status = 'shipped' WHERE id = 42`, reported 1 | 1 row in `shop.orders`, 300 in `shop.shipments` | Doesn't match: `unexpected_table` (attribution `action`) |
| B, no ledger row | `… WHERE id = 43`, reported 1 | the same, batch 8 | Doesn't match: `unexpected_table`, note `any_writer` |
| C, with its ledger row | `UPDATE orders SET total_cents = 2000 WHERE id = 7`, reported 1 | 1 row | Matches: `footprint_matched` |

`make debezium-it RECORD=1` rewrites `internal/witness/feed/debezium/testdata/mysql-trigger.jsonl.gz`,
the recording the CI tests read (`TestRecordedScene`, `TestCutFeedNeverDecides` and the CLI goldens
in `cmd/deedglass/testdata/witness/`). `KEEP=1` keeps the containers. The images take about 3 GB.

## What the comparison can and can't read

- **Types come from the JSON.** Without a schema the feed says only string, number, boolean or
  null, so integers and strings are compared as Postgres's `int8` and `text` are; dates, decimals and
  JSON columns are shown but not compared.
- **Statements are read by sqlguard**, the Postgres-dialect reader. Plain MySQL `UPDATE`, `INSERT`
  and `DELETE` on one table read the same; backtick-quoted names, `INSERT … ON DUPLICATE KEY UPDATE`
  and multi-table statements are `footprint_unknown`.
- **Sentences say the source.** The L2 sentences name Postgres; this command says the feed's
  database instead ("MySQL also changed 300 rows …").
- **No live binding to the Gate yet.** The Gate doesn't wait for a feed; the record this command
  prints is not sealed into the Ledger.

Limits that stay true are in [`LIMITATIONS.md`](LIMITATIONS.md#foreign-change-feeds).
