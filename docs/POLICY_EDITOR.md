# The policy editor

The Console's policy editor (Console › Policies › a policy › Editor) is a plain text field with a
diagnostics layer behind it. It behaves like a developer tool, not a textarea: line numbers, the
server's lint at the exact line and column, quick fixes where the lint names the replacement, and
a dry run and tests against the unsaved buffer. Source: `web/src/controllers/policyeditor.ts`,
`web/src/lib/{editormath,lintloop,policyfix,linediff}.ts` (S26).

## How it is built

- **One editable surface.** The text is a native `<textarea>`, so the browser's undo, IME input,
  spellcheck-off, selection and screen readers all work as usual.
- **A mirror behind it** with identical metrics (Geist Mono 13/20, tab size 2, `white-space: pre`,
  the same padding) draws the diagnostic underlines (errors in the mismatch colour, warnings in the
  unverifiable colour; underlines, never shaded blocks) and the highlighted line. Scrolling is
  synced on each animation frame; only lines with a diagnostic are rebuilt.
- **A gutter** with line numbers and a marker per line with a problem (✕ error, ○ warning), whose
  tooltip carries the message.
- **Columns count characters the way the server does** (Unicode code points: a tab is one, an emoji
  is one), so `Line 9, col 5` in the list is the place the underline marks.
- No dependency: about 500 lines of TypeScript. A CodeMirror-class library would need an ADR.

## Keys

| Key | What it does |
|---|---|
| Tab | inserts two spaces, or indents every selected line |
| Shift Tab | removes up to two leading spaces from each selected line |
| Enter | a new line with the current indent (after `- id:` it lines up with the item's keys) |
| ⌘ S / Ctrl S | `Save as vN` (admins; when the buffer differs from the version you opened) |
| Esc, then Tab | leaves the editor (Tab is the editor's own key until you press Esc; the field's description says so) |
| ⌘ Z / Ctrl Z | the browser's own undo, including undoing an applied fix |

## Lint

- 400 ms after you stop typing, the buffer goes to `POST /v1/policies/lint`. A newer change aborts
  the call before it, so there is never more than one in flight.
- Results appear three ways: the gutter marker, the underline, and the list under the editor:
  `Line 40, col 5 · Unknown key "desicion" in rule "block-destructive-sql". Did you mean "decision"?`
  with `Apply fix` and `Docs`. Clicking a row reveals its line.
- **Quick fixes:** S10's diagnostics carry a `hint`, not a structured fix. When the hint is exactly
  `Did you mean "x"?` (today the `unknown_key` and `cel_no_field` codes), the Console replaces the
  diagnostic's range with `x`. After a fix it lints again at once and only says
  `✓ No problems · 16 rules · 1 budget` when the server does.
- **Limits:** a policy is at most 256 KiB (UTF-8); a bigger buffer shows
  `Policies are limited to 256 KiB. Split this one.` and nothing is sent. Lint, dry run and tests
  share S12's 60 calls a minute per person; a 429 shows `Lint paused for 20 seconds (rate limit).`
  and lint resumes on its own.

## Saving and versions

- `Save as v5` posts the buffer as a new version. Versions are immutable and hashed; saving content
  whose normalized form is unchanged (a comment, whitespace, key order) answers
  `No changes: this is already v4.`
- Leaving with unsaved changes (a link in the Console, or closing the tab) asks first.
- A viewer gets the same editor, read-only: `Viewers can read policies. Editing needs the Admin
  role.` Dry run and tests still work against the buffer.

## Accessibility

- The textarea has a label (the policy and version) and a description that includes the Esc, then
  Tab rule and the diagnostics list.
- Diagnostics are text with a word (`Error`, `Warning`) and a glyph; colour never carries meaning
  alone. The status line under the editor is a polite live region with the count of problems; it
  never announces on every keystroke.
- The focus ring is drawn around the whole editor.

## Performance

| Budget | Measured (S26) |
|---|---|
| Typing in a 2,000-line policy: < 16 ms per keystroke | 0.32 ms per keystroke with the mirror render (happy-dom); the mirror updates once per frame |
| Lint round trip < 150 ms locally, never more than one in flight | one in flight by construction (AbortController) |
| Diff of two 2,000-line versions < 50 ms | 1.3 ms for a 5 % edit, 0.9 ms for a moved 200-line block, 48 ms for a full shuffle |

## What it doesn't do

- No syntax colouring or completion.
- No collaborative editing: two people saving produce two versions; the last is the newest.
- No fix where the lint doesn't name the replacement.
