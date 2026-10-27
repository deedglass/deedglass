# The Notion connector

Deedglass lets agents search Notion and write pages and plain paragraphs **under the parent pages
you list**, and nowhere else. Every write is read back from Notion and compared at L1 before the
deed is sealed.

Facts on this page were checked on 2026-10-04 (row C-080 in `CLAIMS_REGISTER.md`).

## The boundary

- `parents` in the config is the hard boundary. `notion.create_page` creates a page only directly
  under one of them; any other `parent_page_id` is refused with `validation_failed` before anything
  is sent.
- `notion.append_block` reads the target page first (one `GET /v1/pages/{id}`) and appends only
  when the page is a parent or sits directly under one, and isn't in the trash. Otherwise it is
  refused with `validation_failed` and nothing is written.
- There is no tool that edits, moves, archives or deletes a page. The starter policy
  `notion-pages` denies any destructive tool as well.
- Notion has no idempotency key, so a create is sent once. If its answer is lost, the action is
  UNVERIFIABLE ("it may have run"); Deedglass never sends it again.

## Set up Notion (once, about 5 minutes, free)

1. At notion.so/profile/integrations, create an **internal integration** for your workspace with
   **Read content** and **Insert content**. Copy its token (`ntn_…`) into your shell yourself:
   `export NOTION_TOKEN=…`.
2. Open each parent page in Notion › **•••** › **Connections**, and add the integration.
3. Put the parent page ids in `examples/connectors/notion.yaml`, then:

```
$ deedglass connector create --config examples/connectors/notion.yaml
$ deedglass connector credentials set notion --from-env NOTION_TOKEN
$ deedglass connector test notion
✓ config        valid · 1 parent pages · max_blocks 50
✓ credentials   internal integration token · ntn_…
✓ destination   api.notion.com → 203.0.113.10 · public
✓ auth          bot "Deedglass" · workspace "Acme"
✓ parents       1 of 1 readable
· writes        not probed · Deedglass never creates pages to test a connection
```

## What L1 checks

| Tool | Read back | Compared |
|---|---|---|
| `notion.create_page` | `GET /v1/pages/{id}` and `GET /v1/blocks/{id}/children` | the title, the parent, `in_trash: false`, and a digest of the paragraphs in order |
| `notion.append_block` | `GET /v1/blocks/{id}` for each appended block | the number of blocks, each block's text, `in_trash: false` |

Text is compared as plain text: CRLF reads as LF and trailing spaces are ignored. Rich formatting
(bold, links, colours) isn't compared. A page in the trash at read-back time is a MISMATCH; a 404
or a rate limit is a gap (UNVERIFIABLE), never a mismatch.

## Errors

| Notion answers | Deedglass records |
|---|---|
| 401 | `upstream_auth_failed` (failed: nothing applied) |
| 403 | `upstream_auth_failed`: give the integration Insert content, or share the page |
| 404 | `upstream_not_found`: the page doesn't exist or isn't shared with the integration |
| 429 | `upstream_rate_limited` with `Retry-After` (failed: nothing applied) |
| 400, 409 | `upstream_error` (failed: Notion refused it, nothing applied) |
| 5xx, timeout | `upstream_error` (unknown: it may have run) |

Tokens are held in the vault and scrubbed from every log line and message by shape (`ntn_…` and the
legacy 43-character `secret_…`).

## What it doesn't do

- No databases or data source rows (`notion.create_row` under the 2026-03-11 data source model is
  a later connector pass), no files, comments or mentions.
- No edits, moves or deletes.
- The read-back shows Notion's record, not whether the page was a good idea.

<!-- deedglass:generated start -->
<!-- Generated from the connector catalog by go run ./internal/connector/cmd/gendocs. Don't edit by hand. -->

**Notion** · beta · levels L0 L1

Pages and paragraphs under the Notion pages you list.

### Tools

| Tool | Side effect | Levels | What L1 checks |
|---|---|---|---|
| `notion.search` | read | L0 | Not read back |
| `notion.create_page` | write | L0 L1 | Notion shows the page under the same parent with the same title, not in the trash, and its paragraphs in order. |
| `notion.append_block` | write | L0 L1 | Notion shows each appended block on the page with the same text, not in the trash. |

| Tool | What it does |
|---|---|
| `notion.search` | Search the pages shared with the integration by title (at most 20 results). |
| `notion.create_page` | Create a page under one of the connector's parent pages, with a title and plain paragraphs. |
| `notion.append_block` | Append plain paragraphs to the end of a page that is a parent page or sits directly under one. |

### Credentials

Kind `bearer`:

- `token` (secret): An internal integration token, shared with the parent pages. Starts with `ntn_`.

### Setup

1. **Create an internal integration.** Give it Read content and Insert content. Then open each parent page › ••• › Connections and add the integration. https://app.notion.com/profile/integrations
2. **Create the connector.**
   `deedglass connector create --config examples/connectors/notion.yaml`
3. **Store the token.**
   `deedglass connector credentials set notion --from-env NOTION_TOKEN`
4. **Test the connection.**
   `deedglass connector test notion`

### What it doesn't do

- Only under the parent pages in the config. Nothing is moved, edited or deleted.
- Plain paragraphs only: no databases, data source rows, files or comments.
- The read-back compares plain text, not rich formatting.
- Notion has no idempotency key: a create whose answer was lost is UNVERIFIABLE, never sent twice.

<!-- deedglass:generated end -->
