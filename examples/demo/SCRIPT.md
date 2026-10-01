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
