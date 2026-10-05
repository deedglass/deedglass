# The launch demo: script, recording plan and takes

Every frame the public sees comes from a real run (CLAUDE.md rule 12). The script is
`examples/demo/launch.yaml`; `deedglass dev agent-script` runs it through the real Gate and checks every
outcome, so a take that drifts from the product fails before it is recorded. Runway makes abstract
b-roll only (`~/txv/ops/launch/runway_shotlist.md`).

## The seven scenes

| # | Scene | What runs | Offline (`make demo`) | Live (`make demo-live`) |
|---|---|---|---|---|
| 1 | A safe read | `postgres.query` for order 42 | allowed · L0 · VERIFIED | the same |
| 2 | A destructive request | `DROP TABLE public.refunds` | denied by `block-destructive-sql`, nothing ran | the same |
| 3 | A normal write | trigger off, ship order 17 | L2 · VERIFIED, 1 row | the same |
| 4 | The trigger | trigger on, ship order 42 | L2 · MISMATCH, 301 rows vs 1, the diff | the same |
| 5 | Put it back | undo preview, undo run, a countersign by `ops@example.test` | L2 · VERIFIED, 301 rows restored | countersign on the phone (`make demo-lan`) or in Slack |
| 6 | A refund over $50 | `stripe.create_refund` of $120.00 | skipped: needs Stripe test mode | a Slack approval card, then L1 · VERIFIED from Stripe |
| 7 | Check every deed | export, verify, change one byte, verify | `✓ N deeds · chain intact · 1 seal key`, then `✕ Deed K was changed after it was sealed.` | the same |

`make demo` starts from a fresh stack (`demo-reset HARD=1`) so the deed numbers are the same on every
run; `make demo-launch-check` runs it twice and compares the normalized output with
`examples/demo/testdata/launch.golden`. Scene 7 changes the first agent action's deed (the read), the
same deed role the site's "Change one byte" changes in its 17-deed sample.

### Commands

```sh
make demo                         # offline, about 45 s including the fresh stack
make demo-live                    # preflight, then --live --step (Enter between scenes)
bin/deedglass dev agent-script examples/demo/launch.yaml --only verify    # one scene (after make demo)
```

`make demo-live` refuses to start without a Stripe **test** key (`STRIPE_TEST_KEY=sk_test_…` in the
repo's `.env`, which you put there yourself), a running `cloudflared` tunnel for Slack, the Stripe
fixtures (`bin/deedglass dev stripe-fixtures --from-env STRIPE_TEST_KEY`) and passing
`connector test slack` and `connector test stripe`. Each refusal names the next step.

## Recording setup

- **Capture:** 1920×1080 at 60 fps (the Sealed moment needs it). Do Not Disturb on, the Dock hidden,
  a plain `#07080C` desktop. One app full-frame at a time, joined by hard cuts: Claude Desktop for
  chat scenes, the terminal for CLI scenes, the Console for the Sealed moment.
- **Terminal:** Geist Mono 20 px, prompt `$ `, background `#0E1017`, foreground `#F5F6FA`; ANSI green
  `#34E3C9`, red `#FF7A70`, magenta `#A99CFF`, yellow `#F7B955`, blue `#6F8DFF`, bright black
  `#8B90A0` (the same theme as `examples/demo/tapes/_brand.tape`).
- **Console:** dark mode at 100% zoom.
- **Chat scenes:** Claude Desktop through the demo's MCP entries (`make demo-snippet
  CLIENT=claude-desktop`), starting from the state the script sets up.
- **Audio:** narration recorded separately in a quiet room, levelled to about −14 LUFS; captions from
  the narration below (burned in for X and Reels, an `.srt` for YouTube).

### Narration (from `launch.yaml`)

1. First, a plain read. The Gate allows it and seals a deed.
2. Now something destructive. The policy says no, before anything touches the database.
3. A normal write. Postgres committed one row, and the agent said one row.
4. Same request, but a trigger touches 300 more orders. The agent says one row. Postgres says 301.
5. Undo is built from what Postgres committed. A person countersigns, and Postgres confirms the fix.
6. Refunds over $50 wait for a person. Stripe confirms the refund exists.
7. Every step is a signed deed. Change one byte, and anyone can see it.

## The 60-second cut

| Time | Shot |
|---|---|
| 0–3 s | Title card: "Your agent said it updated 1 row." |
| 3–12 s | Scene 4 in Claude Desktop; hold the MISMATCH line for 3 s |
| 12–20 s | The diff in the terminal (`make demo-diff`, or `mismatch.tape`) |
| 20–32 s | Scene 5: countersign on the phone, then `301 rows restored` |
| 32–40 s | Scene 2: the blocked DROP (`drop.tape`) |
| 40–50 s | Scene 7: verify, then the changed byte (`verify.tape`) |
| 50–55 s | One line: "Deedglass. See what your agents actually did." |
| 55–60 s | The site's outro frames: the Split Orb closes, the wordmark, deedglass.com |

Dry run: the offline demo's scenes 2, 4, 5 and 7 take under 5 s of terminal time together, so every
beat fits its slot with the pauses above.

## The 8–10 minute walkthrough (YouTube chapters)

00:00 What Deedglass is · 00:40 Install with `deedglass init` · 01:50 Connect Claude Code · 02:40 A
blocked DROP · 03:30 One row reported, 301 committed · 05:00 Undo with a countersign · 06:10 A Stripe
refund approved in Slack · 07:10 Deeds you can check offline · 08:00 What it doesn't do · 08:40 Where
to go next.

## Terminal-only shots (VHS)

`examples/demo/tapes/{drop,mismatch,undo,verify}.tape` and `verify-vertical.tape` source
`_brand.tape` (or `_brand-vertical.tape`, 1080×1920, typed lines at most 42 columns) and print the
commit hash in their first frame. Run them from the repo root after the state each one names
(`vhs examples/demo/tapes/verify.tape`). Installing VHS (v0.12.1, MIT) is your choice; the tapes are
ready either way.

## Takes and resets

Between takes: `make demo-reset && make demo-trigger-off` (or `make demo` for the whole script).
Log every kept take in `examples/demo/RECORDINGS.md` with the commit and the command.

### If something goes wrong

- **The decoder isn't running** (scene 3 or 4 is UNVERIFIABLE): `make demo-check`, then `make demo-reset`.
- **Scene 4 shows 1 row, not 301:** the trigger is off, or the orders were shipped by an earlier take:
  `make demo-order-reset && make demo-trigger-on`.
- **The undo is refused:** the trigger is still on; `make demo-trigger-off` and preview again.
- **Port 8080 is taken:** the demo uses `DEMO_PORT=8091` (in `examples/demo/.env`).
- **Anything else:** `make demo-status`, then `make demo` from scratch.

## What it doesn't do

The offline demo never calls Stripe or Slack, and it countersigns through the dev-only
`deedglass approve --as`. Live takes need your Stripe sandbox and Slack app; nothing in the demo
touches a live key (`make demo-live` refuses one).
