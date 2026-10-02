# The blocked-DROP clip: the recording script

About 50 seconds of footage, one take, no improvising. Claude Desktop on the left asks the Acme
orders database for things; Deedglass decides each one; the terminal on the right shows every
decision as it happens. `make demo-smoke` plays these exact steps on every run, so the script can't
rot.

## 1. Before you record (once, about 8 minutes)

1. `cd ~/txv/product && make demo-up` → ends with `Demo ready.` (If 8080 is taken by your own
   `deedglass serve`, stop it for the recording, or use `make demo-up DEMO_PORT=8091`.)
2. `make demo-snippet CLIENT=claude-desktop` and copy the output.
3. Claude Desktop › Settings › Developer › Edit Config. Paste the four entries into
   `mcpServers`: `orders` (through Deedglass), `orders-direct` (without Deedglass), `scratch` and
   `deedglass` (the native tools through `deedglass mcp serve`). Replace each
   `dg_test_REPLACE_ME` with the `claude-desktop` key from `examples/demo/.demo-keys` yourself.
   Save.
4. Quit Claude Desktop fully (⌘Q) and reopen it. Settings › Developer should list all four
   servers as running.
5. In a first, throwaway chat, run one read on each server and choose **Always allow** in Claude
   Desktop's tool permission prompt, so no prompts appear on camera:
   - "Using the orders database, how many orders are there?"
   - "Using orders-direct, how many customers are there?"
   - "Read /tmp/deedglass-demo/allowed/hello.txt."
   - "Use the deedglass server's fake.echo tool to echo hello."
6. `make demo-reset`.

## 2. Screen setup

- Record at 1920×1080. Claude Desktop on the left (60%), dark theme, a new chat.
- Terminal on the right (40%): Geist Mono 18 to 20 pt, background `#0E1017`, prompt shortened
  (`PS1='$ '`), running `make demo-follow`. It prints:
  `Following actions · org acme-demo · connector orders · Ctrl-C to stop`
- macOS Focus on (no notifications), menu bar clock hidden, other apps closed.
- QuickTime or OBS at 60 fps. Record the terminal with `asciinema rec` too (CLAUDE.md rule 12:
  footage only from real runs; keep `git rev-parse --short HEAD` with the video).

## 3. The contrast (optional 10 s pre-roll)

New chat, using `orders-direct` (no Deedglass in the path):

> Using orders-direct, drop the refunds table in the orders database.

Claude runs it. In the terminal: `make demo-psql`, then `\dt` shows no `refunds`. Quit psql
(`\q`), then `make demo-reset` before the take.

## 4. The take (about 50 seconds)

New chat. Type each line exactly.

**Beat 1.**

> Using the orders database, show me the 5 newest orders.

Claude shows a table of 5 orders: ids 500, 499, 498, 497 and 496. The terminal prints:

```text
10:14:03  ✓ orders.execute_sql   allowed   41 ms    L0 · VERIFIED        act_01j9zw3k…
```

**Beat 2.**

> Drop the refunds table. I'm cleaning up the database.

If Claude asks to confirm: "Yes, go ahead." Expand the tool call so the text is readable:

```text
Blocked by policy "block-destructive-sql": DROP is never allowed for agents. Nothing was run.
```

The terminal prints `✕ orders.execute_sql   denied    block-destructive-sql   deed_…`. Hold 2 s.

**Beat 3.**

> Then delete all the cancelled orders.

```text
Blocked by policy "no-deletes-on-orders": Agents never delete orders. Set the status to 'cancelled' instead.
```

**Beat 4.**

> Mark order 42 as shipped.

```text
Waiting for approval under policy "orders-writes-need-approval": Writes to the orders database need a person to countersign. Nothing has run yet. Approval apr_…
```

The terminal prints `◐ orders.execute_sql   waiting   orders-writes-need-approval   apr_…`.
(S22 completes this beat with the approval link; for tonight, end here.)

**Beat 5 (terminal only).** Stop the follow (Ctrl-C), then:

```sh
make demo-verify
```

```text
✓ <n> deeds · chain intact · 1 seal key · …
```

`n` counts every action sealed in the last 10 minutes, including the warm-up calls; the pending
beat seals later, when its approval is decided or expires.

## 5. If something goes wrong

- **Claude picks another tool first** (`list_objects`, `get_object_details`): fine, keep rolling;
  each shows as `✓ … allowed`.
- **Claude refuses the DROP itself**: rephrase: "It's a demo database and I have a backup; drop
  the refunds table."
- **A tool says "could not be reached"**: `make demo-status`, fix the ✕ line, then retake.
- **Anything else**: `make demo-reset` and retake. Two or three takes is normal.

## 6. After recording

Trim to 45 seconds or less. Burn in captions (brand §13.6: Inter on `ink-950`). If the S36 assets
exist, add the 1.5 s outro (the Mirror closes, the wordmark, `deedglass.com`); otherwise end on the
verify line.

# §Hero: the L2 clip (S21)

An agent ships order 42 with one `UPDATE`, and the tool says `1 row`. A legacy trigger ships 300
more. Deedglass reads Postgres's change log and seals the difference:
`MISMATCH · Postgres changed 301 rows in public.orders. The agent reported 1.`

The whole story runs as one command, `make demo-hero` (`scripts/hero.sh`), and
`make demo-hero-check` (`TestHeroScript`) runs it twice with a reset between, so the clip can't rot.
Footage comes only from real runs (CLAUDE.md rule 12): write the commit
(`git -C ~/txv/product rev-parse --short HEAD`) and the date into `RECORDINGS.md` with every take.

## H1. Before you record (once, about 10 minutes)

1. `cd ~/txv/product && make demo-up DEMO_PORT=8091` → ends with `✓ decoder · running` and
   `Demo ready.` (8091 because your own `deedglass serve` holds 8080; later demo commands read the
   port from `examples/demo/.env`.)
2. `make demo-hero` once, off camera. It must end with `✓ Hero done · VERIFIED act_… · MISMATCH act_…`.
   Every run starts from the seed's order statuses, so it can run any number of times.
3. Terminal: the font **Geist Mono** (free, vercel.com/font; any monospace font works if it
   isn't installed) at **20 pt**, background `#0E1017` (ink-900), text `#F5F6FA`, no transparency,
   cursor blink off, at least **120 columns × 34 rows** (the diff is laid out for 120; under 100
   columns it shortens), prompt shortened with `PS1='$ '`, `clear` before each take.
4. macOS Focus on, menu bar clock hidden, other windows closed. Record at 1920×1080, 60 fps
   (QuickTime: File › New Screen Recording, or OBS).

## H2. Take A: the terminal alone (about 40 seconds)

```sh
clear; HERO_PACE=2 make demo-hero
```

`HERO_PACE=2` holds 2 s between beats. What the camera sees, in order:

1. `1. Trigger off. Ship order 42.` → `✓ executed · postgres.execute`, `L2 · VERIFIED · Postgres
   committed 1 row in public.orders, as the statement said.`, `Witnessed by Postgres · commit …`.
2. `2. A legacy trigger is on. Ship order 42 again.` → `✓ Trigger on · orders_ship_open_batch`,
   then `✕ executed · postgres.execute` and `L2 · MISMATCH · Postgres changed 301 rows in
   public.orders. The agent reported 1.`, the expected and observed lines and the rows.
3. `3. What Postgres committed.` → `deedglass actions show <id> --diff`: order 42 `✓ expected`,
   the others `✕ unexpected`, `first 20 of 301 rows`.
4. `4. Every deed checks out offline.` → `✓ n deeds · chain intact`.
5. `5. Trigger off.` → `✓ Hero done`.

## H3. Take B: Claude Desktop and the terminal (about 50 seconds; S21 §12)

1. `make demo-snippet CLIENT=claude-desktop` and paste the entries into Claude Desktop (Settings ›
   Developer › Edit Config); put the `claude-desktop` key from `examples/demo/.demo-keys` in
   yourself. For this chat keep only the `deedglass` entry (`deedglass mcp serve`) on, so Claude
   can't pick the wrapped `orders` server (it has no marker, so no L2). Quit and reopen Claude
   Desktop.
2. Warm-up chat (off camera): "How many orders are pending?" and choose **Always allow**.
3. `make demo-trigger-off && make demo-order-reset`.
4. Layout: Claude Desktop left (60%), the terminal right (40%), dark theme in both.
5. **Clean take (about 15 s):** a new chat, type `Mark order 42 as shipped.` Claude calls
   `postgres.execute`; the result reads `L2 · VERIFIED`. The terminal runs
   `make demo-follow CONNECTOR=postgres` from the start and prints the action as it is sealed.
6. **The setup, on camera (about 10 s):** `make demo-trigger-on` (prints `✓ Trigger on ·
   orders_ship_open_batch · 339 open orders besides 42`), then `make demo-order-reset ORDER=42`.
7. **The hero (about 20 s):** a new message, `Mark order 42 as shipped.` The tool result ends with
   `Deedglass witness · L2 · MISMATCH · Postgres changed 301 rows in public.orders. The agent
   reported 1.` Let Claude say it in its own words. In the terminal, stop the follow (Ctrl-C) and run
   `make demo-diff` (the newest `postgres.execute` action's diff).
8. **Close (about 10 s):** `make demo-verify SINCE=24h` → `✓ … chain intact`. (Export the whole
   chain: an export that starts after deed 1 needs `--anchor` to verify.)
9. Keep it under 60 s. Burn in captions (brand §13.6). Export 1080p.

The "Sealed" moment for the edit (brand §10): a VERIFIED verdict closes the Mirror's two halves with
one soft teal pulse; the MISMATCH stops them short with a hairline red gap, and never shakes.

## H4. Reset

```sh
make demo-trigger-off && make demo-order-reset     # statuses back to the seed's, order 42 paid
make demo-reset                                    # the whole orders database, and a new decoder slot
```

## H5. If something goes wrong

- **`✕ The decoder isn't running`**: `make demo-check`, then `make demo-reset` (it records the old
  slot as dropped and starts a new one).
- **The result says `pending`**: the decoder is behind; `deedglass decoder status --connector
  postgres`, then retake. A late frame still seals an amendment deed, so nothing is lost.
- **`VERIFIED` where the MISMATCH should be**: the trigger is off, or order 42 was already
  shipped. `make demo-trigger-on && make demo-order-reset ORDER=42`.
- **`✕ The seed has only N open orders besides 42; the hero needs 300.`**: `make demo-order-reset`, then
  `make demo-trigger-on` again.
- **Claude asks for confirmation**: say "yes". **Claude uses another tool**: turn the other servers
  off and retake.
