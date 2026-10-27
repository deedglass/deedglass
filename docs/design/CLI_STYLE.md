# CLI style

How every `deedglass` command lays out text in a terminal. The helpers live in one package,
`internal/cli/ui`; commands and `deedglass play` build their output from it, so spacing, colour,
glyphs and alignment are the same everywhere. The voice (calm, exact, sentence case, a next step on
every error) is the brand doc's; this page is the layout.

The rules come from studying well-designed CLIs: GitHub's `gh` and its Primer CLI guidelines, Astral's
`uv` and `ruff`, Bun, Vercel, Fly, Railway, Tailscale, Stripe, Charm's lipgloss and glamour, and
[clig.dev](https://clig.dev/). The research notes, with a source for each pattern, are kept with the
before and after captures of this redesign.

## Principles

1. **Summary first.** The first line says what happened. Detail follows, and `-v` shows more.
2. **One line, one fact.** A row with a key beats a chain of `a · b · c`.
3. **Whitespace groups.** A blank line separates blocks, an indent shows what belongs to what.
4. **Colour repeats; it never carries.** Every glyph and word reads the same with colour off.
5. **80 columns.** Nothing a helper lays out is wider than the terminal or 80 columns, whichever is
   smaller. A wider terminal doesn't make lines longer: the eye shouldn't travel.

## Palette

Five roles, drawn only from the ANSI-16 base colours. The person's terminal theme decides the exact
shade, so the same codes read well on a light and a dark background. There are no bright colours
(90–97), no backgrounds, and no 256-colour or truecolor codes in body text. The terminal mark (the
banner) is the one exception: it has its own depth and theme detection (`internal/cli/mark`).

| Role    | SGR | Used for                                                      | `ui.Style`          |
|---------|-----|---------------------------------------------------------------|---------------------|
| accent  | 36  | the brand teal: ✓, and the one value a screen is about         | `Accent`, `Success` |
| success | 36  | the same as accent: verified is the brand colour              | `Success`, `Teal`   |
| warn    | 33  | ◐ waiting for a person, ○ can't tell or a notice               | `Warn`, `Amber`, `Violet` |
| error   | 31  | ✕ denied, mismatch, failed                                    | `Error`, `Red`      |
| dim     | 2   | keys, meta, provenance tags, hints, table headers, separators | `Dim`               |
| bold    | 1   | titles, section headings, the primary noun                    | `Bold`              |

Dim is SGR 2 (faint), not grey: faint follows the foreground colour, so it works on both
backgrounds, where a fixed grey disappears on one of them. Pending shares the warn colour (before
this redesign it was magenta): one palette, fewer meanings.

Colour is on only when stdout is a terminal, `NO_COLOR` is unset (any value turns it off),
`TERM` isn't `dumb` and `--no-color` isn't given (`ui.ColorAllowed`). `--json` never has colour.

## Glyphs

| Glyph | Means                          | Colour  | ASCII |
|-------|--------------------------------|---------|-------|
| ✓     | done, verified, allowed        | success | `+`   |
| ✕     | failed, denied, mismatch       | error   | `x`   |
| ◐     | waiting for a person, working  | warn    | `~`   |
| ○     | can't tell; a notice           | warn    | `o`   |
| •     | a neutral list item or note    | dim     | `-`   |
| →     | a change: `paid → shipped`     | none    | `->`  |
| …     | elided: an id, the rest of a list | none | `...` |
| ·     | joins two or three short facts in dim meta | dim | `-` |

These are the brand's marks. ✕ and ○ play the parts other CLIs give ✗ and `!`; they stay because
Slack cards, MCP results, the Console and the recorded demo share them. A glyph is always followed
by words that say the same thing (`✕ denied`), so the line reads with the glyph alone, the words
alone, or both.

ASCII stand-ins replace the glyphs on a terminal that can't draw them: when `DEEDGLASS_ASCII` is
set, `TERM` is `dumb`, or the Windows console has no virtual-terminal support (`ui.ASCIIOnly`,
`ui.ASCIIWriter`). A pipe or a file keeps UTF-8, and `--json` is never rewritten.

## Spacing

- A block's first line starts at column 0; its body is indented one level (`ui.Indent`, two spaces).
  Nested detail is indented one more level, or hangs under the value column.
- One blank line between blocks. Never two, never one at the start, never one at the end.
- No horizontal rules or boxes. A section is a bold heading over an indented block.
- Two spaces between columns, and between a key and its value.

## Header and sections

A command that needs a header prints one line (`Style.Header`): the title in bold, then up to
three items of dim meta joined by ` · `.

```
deedglass scan  agentproj · 0.1 s · read-only
```

On a terminal the inline mark can stand in for the title (`◖│◗ deedglass scan  agentproj · …`).
Commands whose first line is already the result (`verify`, `lockdown status`, `actions show`)
don't add one. A section is a bold heading (`Style.Section`) after a blank line, its rows indented.

## Key/value

`Style.KV` lays out rows: the key in dim, padded to the longest key in the block, two spaces, the
value. A long value wraps under itself, never under the key. A row can carry a status glyph
(drawn in its colour, with later lines hanging under the text, not the glyph) and a dim tag at the
right edge (`deedglass play`'s `live` and `replay`).

```
  org    Local
  agent  quickstart-agent
  steps  10 checked (-v lists them)
```

Blocks a command prints line by line (`actions show`) go through `ui.WrapWriter`, which wraps each
line under its value column (`ui.HangWrap`). A separator or an arrow stays at the end of its line,
never the start of the next.

## Tables

`Style.Table`: a dim UPPERCASE header row, two-space gutters, cells clipped with … to a column
limit, styled cells measured without their codes. The footer is one dim line that says what was
shown: `Showing 5 of 50 actions · next page: …`. Tables keep their shape when piped; scripts use
`--json`, whose structure never changes with the layout.

## Ids

Text output shortens ids to their prefix and the first eight characters, then … (`ui.ShortID`):
`act_01m4x8kq…`. The full id appears in `--json`, under `-v` where a command has more to show, when piped
where a script may read the column (`keys list`), and in every command a person is meant to copy
(`next  deedglass deeds show deed_01m4x8kq…full`).

## Next steps and errors

The last line of a result or an error is what to do now (`Style.Next`):

```
✕ Unknown command "actoins". Did you mean "actions"?
  next  Run deedglass help to see every command.
```

When a result offers several commands, they go under a bold `Next` heading, one per line, or as a
command list (`Style.Commands`): the command as typed, then what it does in dim.

Every first-touch command (`start`, `play`, `scan`, `protect`, `health pilot`, `init`) ends this
way, and the commands follow how the binary was run (`npx deedglass …` under npx). The first one
is the next thing most people want: after `scan`, `protect`; after the offline scene, the live
one; after the live scene, the undo it found.

## Summary first, and the line budget

The first line is the outcome; the rest is evidence. Detail a person rarely needs waits for `-v`.
A run that changed nothing says so in a few lines: `deedglass init` on a set-up directory prints
its summary and how many steps it checked, not every step (`-v` lists them; a failed step always
prints).

The budget at 80 columns, not counting the terminal mark:

| Command                     | Lines                                  |
|-----------------------------|----------------------------------------|
| an error                    | 2 to 3: the message, detail, next      |
| `version` (piped)           | 1                                      |
| `lockdown status`           | up to 8                                |
| `verify`                    | up to 6 (`-v` adds the detail)         |
| `keys list`, `actions list`, `approvals list` | one per row, plus header and footer (2) |
| `actions show`              | up to 16                               |
| bare `deedglass` (welcome)  | 5, plus the inline mark on a terminal  |
| `init`, first run           | up to 35, with the smoke test          |
| `init`, nothing to change   | up to 7                                |
| `scan`                      | up to 40                               |
| `help`                      | up to 72 (subcommands with `-v` or `help <command>`) |
| `play`                      | up to 70                               |

## deedglass play

`play` reads as a story: six scenes, each a short block with a before, a check and a result.

```
4/6  The trigger
     A legacy trigger on public.orders is on. The agent sends the same request.
     before   order 42 paid, 339 others open, trigger on                  replay
     request  UPDATE public.orders SET status = 'shipped' WHERE id = 42
     rule     ✓ allowed by allow-order-status-updates, checked at Proven    live
     check    ✕ Doesn't match: Postgres changed 301 rows in               replay
                public.orders. The agent reported 1.
              ✓ id 42   paid → shipped
              ✕ id 218  pending → shipped
              ✕ id 201  paid → shipped
              … 298 more rows
     result   ✕ Mismatch at Proven (L2), 301 rows · act_01m4x8kv…         replay
```

- The scene title is the step in dim and the name in bold; the body sits under the name.
- Rows, in order: `before` (the starting state), `request` (what the agent or person asked),
  `rule` (the live policy decision), `check` (what Postgres's change log said), `result`. The undo
  scene adds `plan` and `person`; the records scene has `records`, `verify` and `tamper`.
- Every row with a source says where it came from at its right edge: `live` (computed in this run
  by the product's own code) or `replay` (read from the recorded run, never invented). A legend
  at the top says what each means.
- A diff shows three rows and counts the rest.
- `-v` adds, under each replayed scene, the recorded lines exactly as they printed.

## Testing

- `internal/cli/ui`: the helpers at 0, 60, 80 and 200 columns; colour adds codes and nothing else;
  no escape codes under `NO_COLOR`; the ASCII stand-ins.
- `internal/play`: no line wider than 80 columns (or the terminal), colour changes nothing but codes,
  every row's `live` or `replay` tag, and `-v`'s lines equal to the recording.
- `cmd/deedglass`: the goldens (`go test ./cmd/deedglass -update`, then read the diff) and the
  integration goldens in `testdata/ledger`.
