# Starter policies

Nine complete policies to start from. Each one is a single readable file with a plain-English header, a cases file that covers every rule, and reasons that tell the agent what to do next. They lint with zero warnings under `--voice`.

| File | What it is for | Rules | Cases |
|---|---|---|---|
| [`default-deny.yaml`](default-deny.yaml) | Nothing runs. Every new org starts here, and each agent is held to 60 calls a minute. | 0 (and 1 budget) | 13 |
| [`postgres-safe.yaml`](postgres-safe.yaml) | Reads run freely. Writes wait for a human, except on the tables you allow-list. Schema changes, server-side code, several statements at once and SQL Deedglass can't read with confidence never run. | 12 (and 2 budgets) | 28 |
| [`stripe-refunds.yaml`](stripe-refunds.yaml) | Refunds up to $50.00 run; larger ones wait for a human. All agents together refund at most $500.00 a day. Cancellations and invoice items wait for a human; deletes never run. | 8 (and 1 budget) | 21 |
| [`github-read-mostly.yaml`](github-read-mostly.yaml) | Reads, issues, comments and feature branches run. Commits and pull requests to main, master or a release branch wait for a human. Merges and branch deletes never run. | 10 | 22 |
| [`gmail-drafts-only.yaml`](gmail-drafts-only.yaml) | Agents read mail and write drafts. A person sends every email. | 4 | 14 |
| [`notion-pages.yaml`](notion-pages.yaml) | Agents search Notion and write pages and paragraphs under the listed parent pages, read back at L1. Deleting, moving and archiving stay with people. | 3 | 7 |
| [`sdk-demo.yaml`](sdk-demo.yaml) | The `@deedglass/sdk` examples: refunds up to $50.00 run, larger ones wait for a human, and agents never delete customers. | 5 | 8 |
| [`claude-agent-local.yaml`](claude-agent-local.yaml) | The Claude Agent SDK hooks: reads run; `rm -rf /`, curl into a shell, force pushes and `.env` writes never run; writes outside `/workspace/` and MCP tools wait for a human. | 7 | 15 |
| [`slack-post-with-approval.yaml`](slack-post-with-approval.yaml) | Agents post on their own in allow-listed channels. Everywhere else waits for a human, and so does any message that notifies a whole channel. | 5 | 16 |

Each `<name>.yaml` has its cases in `<name>.cases.yaml` next to it. The GitHub, Slack, Stripe, Gmail and Notion tool names are the real connectors' (S29–S31).

## Test them

```
deedglass policy test --dir examples/policies --require-coverage --voice
```

This runs every case of every starter, fails if any rule decides no case, and fails on any reason that doesn't follow the Deedglass voice. The output ends with one summary line:

```
✓ 114 cases passed · 6 policies · every rule covered · 0.0 s
```

To test one starter, name its file; the cases file is found automatically:

```
deedglass policy test examples/policies/postgres-safe.yaml
```

## Push and activate one

With an org admin key in `DEEDGLASS_API_KEY`, push a starter, watch it in shadow, then enforce it:

```
deedglass policy push examples/policies/stripe-refunds.yaml --notes "Refund rules, first cut"
deedglass policy activate stripe-refunds --version 1 --mode shadow --reason "Watch refunds for a day before enforcing"
deedglass policy activate stripe-refunds --version 1 --mode enforce --reason "Cap agent refunds at 50 dollars each"
```

Every activation is sealed as a deed, and enforcing asks you to type the policy name back (or `--yes` in scripts). The whole workflow is in [POLICIES_GUIDE.md](../../docs/POLICIES_GUIDE.md). On a dev server, `deedglass dev seed` saves the five starters other than default-deny as inactive policies, ready to activate.

## Pinned hashes

`hashes.json` pins each starter's `version_hash`, the hash of what its rules mean ([POLICY_LANGUAGE.md](../../docs/POLICY_LANGUAGE.md) section 9). Any change to a starter's rules, reasons, numbers or order changes its hash, so a change to a starter is a visible diff in review. Comments and formatting don't change the hash.

The starters are embedded in the binary through `embed.go`, so onboarding (which enforces default-deny for every new org), `deedglass dev seed` and the tests read the same bytes as the files here.

## Adapt, don't fork

A starter is a starting point. To make one yours:

1. Copy the starter and its cases file into your own repository, for example into `policies/`.
2. Edit the allow-lists: the tables in postgres-safe's `allow-listed-writes`, the channels in slack-post-with-approval's `allow-listed-channels`, the protected branches in github-read-mostly, the $50.00 line and the daily cap in stripe-refunds. Each header comment says where.
3. Keep the starter's cases, and add your own for every value you changed.
4. Run `deedglass policy test --dir policies --require-coverage --voice` until it passes, then push.

Copying keeps your policy readable in one file and lets you review every change. When a starter improves here, compare it with your copy and take the rules you want. To cover more than one system, combine starters into one policy: copy the rules, keep ids unique, and put denies first ([POLICIES_GUIDE.md](../../docs/POLICIES_GUIDE.md) section 9). More rules to copy are in [POLICY_COOKBOOK.md](../../docs/POLICY_COOKBOOK.md).
