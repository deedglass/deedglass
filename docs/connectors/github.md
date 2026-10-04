# GitHub connector

The `github` connector gives your agents fourteen tools over GitHub's REST API: read repositories,
issues, files and pull requests; open issues, comment, create branches, commit a file, open a pull
request; and, when policy allows, merge a pull request, delete a branch or close an issue. Every
call passes through the Gate, so policy decides it and it is sealed as a deed. Every write is read
back from GitHub at **L1**: the same title and body, the same blob SHA, the branch really gone.

Two ways to sign in:

| `auth` | Credential | Use it when |
|---|---|---|
| `pat` (default) | A fine-grained personal access token (kind `bearer`, field `token`, starts with `github_pat_`) | One person or a sandbox account owns the access. Quickest to set up. |
| `app` | A GitHub App's private key (kind `fields`, field `private_key`) | An org wants a bot identity, per-repository installs and tokens that last one hour. |

The configured `repos` are the hard boundary. A call naming any other repository is refused before
anything is sent, whatever the token itself can reach.

## Set up with a fine-grained token

1. On GitHub, open **Settings › Developer settings › Personal access tokens › Fine-grained tokens**
   and choose **Generate new token**.
2. **Expiration:** 30 days. Deedglass's connector test warns when fewer than 7 days are left.
3. **Repository access:** **Only select repositories**, and pick exactly the repositories you list
   in `repos` (for the sandbox, `arjun/deedglass-sandbox`).
4. **Repository permissions:**

   | Permission | Access | Why |
   |---|---|---|
   | Contents | Read and write | `get_file`, `create_branch`, `create_or_update_file`, `merge_pr`, `delete_branch` |
   | Issues | Read and write | `list_issues`, `get_issue`, `create_issue`, `comment`, `close_issue` |
   | Pull requests | Read and write | `list_prs`, `get_pr`, `create_pr`, `merge_pr` |
   | Metadata | Read (GitHub adds it) | `get_repo` and every other call |

   Leave every other permission at **No access**. Deedglass never needs Actions, Workflows,
   Administration or any organization permission.
5. Generate the token and copy it into an environment variable, then store it:

```sh
deedglass connector create --config examples/connectors/github.yaml
export GITHUB_TOKEN=…                         # paste it; never put it on the command line itself
deedglass connector credentials set github --from-env GITHUB_TOKEN
unset GITHUB_TOKEN
deedglass connector test github
```

```
$ deedglass connector test github
✓ github · 7 checks passed · 412 ms
  ✓ config        valid · 1 repo · protected: main, master, release/*
  ✓ credentials   set · fine-grained token · …9f2c
  ✓ destination   api.github.com → 140.82.112.6 · public
  ✓ auth          signed in as arjun-sandbox-bot
  ✓ token         expires 2026-11-09 · 30 days left
  ✓ repos         arjun/deedglass-sandbox · push allowed
  ✓ rate          4,987 of 5,000 left · resets 11:02 UTC
```

A classic token (`ghp_…`) still works, but the `token` check warns: a classic token reaches every
repository its user can see. OAuth, user-to-server, refresh and installation tokens (`gho_`, `ghu_`,
`ghr_`, `ghs_`) are refused: they expire or belong to another flow, and nothing is sent with them.

## Set up with a GitHub App

1. On GitHub, open **Settings › Developer settings › GitHub Apps › New GitHub App** (under your
   organization's settings for an org-owned App). Turn the webhook off.
2. **Repository permissions:** Contents, Issues and Pull requests: **Read and write**; Metadata:
   **Read**. Nothing else.
3. Create the App, note its **Client ID** (`Iv23li…`) and **App ID**, and generate a **private key**.
   GitHub downloads a `.pem` file.
4. **Install** the App on the account and choose **Only select repositories**. The number at the
   end of the installation's settings URL is the `installation_id`.
5. Put the three values in the config (`auth: app`), then store the key from its file:

```sh
deedglass connector create --config examples/connectors/github.yaml
deedglass connector credentials set github --field private_key=file:./app.pem
# Deedglass keeps the key encrypted in the vault; delete the local copy when you no longer need it
deedglass connector test github
```

How the App signs in: Deedglass signs a JWT with the private key (`{iat: now − 60 s, exp: now + 540 s,
iss: client_id}`, RS256; GitHub accepts at most 10 minutes), then asks
`POST /app/installations/{installation_id}/access_tokens` for an installation token **downscoped**
to the repositories in `repos` (by name) and to `contents`, `issues` and `pull_requests: write`
plus `metadata: read`. When `repos` holds an `owner/*` entry, the token isn't narrowed by name and
covers what the installation covers. Installation tokens last one hour; Deedglass keeps each one in
memory only and asks for a new one 5 minutes before it expires, at most once at a time per
connector. The key needs RSA with at least 2048 bits (GitHub's keys are).

The App's identity, which the issue author check uses, is `<app-slug>[bot]`.

## Config reference

```yaml
type: github
name: github
config:
  auth: pat                        # pat | app
  # app: { client_id: Iv23liAbC123, app_id: 1234567, installation_id: 76543210 }   # when auth: app
  repos: [arjun/deedglass-sandbox] # 1 to 50 entries, "owner/name" or "owner/*"; the hard boundary
  protected_branches: [main, master, "release/*"]
  issue_marker: true               # append <!-- deedglass:act_… --> to bodies Deedglass creates
  api_base_url: https://api.github.com
  api_version: "2026-03-10"        # X-GitHub-Api-Version
  max_file_bytes: 1048576          # at most 1 MiB
```

| Key | Default | Rules |
|---|---|---|
| `auth` | `pat` | `pat` or `app`. |
| `app` | none | Required with `auth: app`, refused otherwise: `client_id` (`[A-Za-z0-9._-]`, up to 64), `app_id` and `installation_id` (positive integers). |
| `repos` | required | 1 to 50 entries, `owner/name` or `owner/*`. Names compare without case, as GitHub does. Duplicates are dropped. |
| `protected_branches` | `main`, `master`, `release/*` | Up to 50 names or globs (`*` and `?` as in Go's `path.Match`, so `release/*` matches `release/1.2` but not `release/1/2`). They feed `request.github.protected_branch`. |
| `issue_marker` | `true` | Append the marker to issue, comment and pull request bodies. `false` also turns off reconciliation. |
| `api_base_url` | `https://api.github.com` | https outside dev. No query, fragment or user name. |
| `api_version` | `2026-03-10` | Sent as `X-GitHub-Api-Version`. |
| `max_file_bytes` | 1048576 | 1 to 1048576. The largest `content` `create_or_update_file` sends. |

An unknown key is refused with its path (`config.extra`, `config.app.extra`). Every request carries
`Accept: application/vnd.github+json`, the API version, Deedglass's `User-Agent`, and the credential,
added last.

## Catalog

The table below comes from `GET /v1/connector-types/github` (`deedglass connector types --type github`).

<!-- deedglass:generated start -->
<!-- Generated from the connector catalog by go run ./internal/connector/cmd/gendocs. Don't edit by hand. -->

**GitHub** · beta · levels L0 L1

Issues, branches, files and pull requests in the repositories you list.

### Tools

| Tool | Side effect | Levels | What L1 checks |
|---|---|---|---|
| `github.get_repo` | read | L0 | Not read back |
| `github.list_issues` | read | L0 | Not read back |
| `github.get_issue` | read | L0 | Not read back |
| `github.get_file` | read | L0 | Not read back |
| `github.list_prs` | read | L0 | Not read back |
| `github.get_pr` | read | L0 | Not read back |
| `github.create_issue` | write | L0 L1 | GitHub shows the issue with the same title, body and marker, opened by this connector's identity. |
| `github.comment` | write | L0 L1 | The comment exists on issue n with the same body and marker. |
| `github.create_branch` | write | L0 L1 | The branch points at the requested commit. |
| `github.create_or_update_file` | write | L0 L1 | The file's blob SHA on that branch equals the git blob SHA of the content Deedglass sent. |
| `github.create_pr` | write | L0 L1 | The pull request is open from head into base with the same title and body. |
| `github.merge_pr` | destructive | L0 L1 | GitHub reports it merged, and the merge commit is contained in base. |
| `github.delete_branch` | destructive | L0 L1 | The branch no longer resolves (404). |
| `github.close_issue` | destructive | L0 L1 | The issue is closed with that reason. |

| Tool | What it does |
|---|---|
| `github.get_repo` | Read a repository's settings: visibility, default branch, description. |
| `github.list_issues` | List issues (and pull requests, flagged) in a repository, newest first, up to 50 a page. |
| `github.get_issue` | Read one issue: title, body, state and labels. |
| `github.get_file` | Read a file at a branch, tag or commit. Text up to 256 KiB comes back as is; anything larger or binary as a digest. |
| `github.list_prs` | List pull requests in a repository, newest first, up to 50 a page. |
| `github.get_pr` | Read one pull request: title, body, head, base and merge state. |
| `github.create_issue` | Open an issue. Deedglass appends a hidden marker with the action id to the body. |
| `github.comment` | Comment on an issue or a pull request. Deedglass appends a hidden marker with the action id. |
| `github.create_branch` | Create a branch from another branch or a commit (default: the default branch). |
| `github.create_or_update_file` | Create or replace one text file on a branch in one commit. Send expected_sha to replace an existing file. |
| `github.create_pr` | Open a pull request from head into base. Deedglass appends a hidden marker with the action id to the body. |
| `github.merge_pr` | Merge a pull request. expected_head_sha is required: GitHub refuses the merge if the head moved. |
| `github.delete_branch` | Delete a branch. The repository's default branch is never deleted. |
| `github.close_issue` | Close an issue as completed or not planned. |

### Credentials

Kind `bearer`:

- `token` (secret): A fine-grained personal access token for the listed repositories. Starts with `github_pat_`.

Kind `fields`:

- `private_key` (secret): A GitHub App's private key (PEM).

### Setup

1. **Create a fine-grained token.** Only the repositories you list, with Contents, Issues and Pull requests read and write. Or install a GitHub App. https://github.com/settings/personal-access-tokens/new
2. **Create the connector.**
   `deedglass connector create --config examples/connectors/github.yaml`
3. **Store the token.**
   `deedglass connector credentials set github --from-env GITHUB_SANDBOX_TOKEN`
4. **Test the connection.**
   `deedglass connector test github`

### What it doesn't do

- Only the repositories in the config. Nothing outside them, including forks.
- No repository settings, secrets, Actions runs or force pushes.
- L1 shows GitHub's state, not whether the change was right.

<!-- deedglass:generated end -->

## Tools

| Tool | Side effect | GitHub call | L1 check |
|---|---|---|---|
| `github.get_repo` | read | `GET /repos/{o}/{r}` | none |
| `github.list_issues` | read | `GET …/issues` (`state`, `labels`, `per_page` up to 50, `page`) | none |
| `github.get_issue` | read | `GET …/issues/{n}` | none |
| `github.get_file` | read | `GET …/contents/{path}?ref=` (text up to 256 KiB, else a digest) | none |
| `github.list_prs` | read | `GET …/pulls` | none |
| `github.get_pr` | read | `GET …/pulls/{n}` | none |
| `github.create_issue` | write | `POST …/issues` | GitHub shows the issue with the same title, body and marker, opened by this connector's identity. |
| `github.comment` | write | `POST …/issues/{n}/comments` | The comment exists on issue n with the same body and marker. |
| `github.create_branch` | write | `POST …/git/refs` (sha from `from`, the default branch if omitted) | The branch points at the requested commit. |
| `github.create_or_update_file` | write | `PUT …/contents/{path}` (`branch`, `message`, `content`, `expected_sha` for updates) | The file's blob SHA on that branch equals the git blob SHA of the content Deedglass sent. |
| `github.create_pr` | write | `POST …/pulls` | The pull request is open from head into base with the same title and body. |
| `github.merge_pr` | destructive | `PUT …/pulls/{n}/merge` with the required `expected_head_sha` | GitHub reports it merged, and the merge commit is contained in base. |
| `github.delete_branch` | destructive | `DELETE …/git/refs/heads/{branch}` | The branch no longer resolves (404). |
| `github.close_issue` | destructive | `PATCH …/issues/{n}` `{state: closed, state_reason}` | The issue is closed with that reason. |

Every tool's arguments are a closed JSON Schema (`additionalProperties: false`), and `repo` must
match `^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$` and be in `repos`:

```
✕ failed · github.create_issue
  arjun/payments isn't in this connector's repos. Add it in Console › Connectors › github, or use an allowed repo.
```

**Outputs are small projections**, never GitHub's raw payloads: `create_issue` returns
`{"number":42,"html_url":"…","state":"open","title":"…"}`. User emails, node ids and API URLs are
never echoed. Each tool declares its output schema, which the Witness checks at L0.

**Refused in the connector, with nothing sent** (policy is the first line; these hold even with a
permissive policy):

- `delete_branch` on the repository's default branch: "Deedglass never deletes a repository's
  default branch." The default branch comes from the 5-minute cache the policy facts filled, or one
  `GET /repos/{o}/{r}` first; no `DELETE` is ever sent for it.
- `merge_pr` without `expected_head_sha`: "Send expected_head_sha so the merge can't include commits
  pushed after approval." GitHub refuses the merge when the head moved, so an approval covers exactly
  the commits it saw.
- File paths over 1,024 bytes, absolute, not valid UTF-8, holding a backslash or a control character,
  with an empty, `.`, `..` or `.git` segment (any case), or not in Unicode NFC form.
- Branch names outside git's rules (`..`, `//`, a segment starting with `.`, `.lock`, `HEAD`).

**NFC, conservatively.** Deedglass checks NFC without a Unicode normalization library: ASCII paths
always pass, and a non-ASCII character passes only from blocks that NFC never changes (Latin-1
Supplement and Latin Extended-A and -B, basic Greek and Cyrillic letters, Hiragana, Katakana, CJK
ideographs, Hangul syllables). Combining marks such as U+0301 and everything else are refused. A
path that is NFC but uses another script is refused too; rename it, or ask us to widen the list.

## Policy facts: `request.github`

Before the decision, the connector adds what it knows about the call:

| Fact | Meaning |
|---|---|
| `repo`, `owner`, `name` | The repository the call names. |
| `default_branch` | From `GET /repos/{o}/{r}`, cached per repository for 5 minutes. |
| `branch` | The branch written. A commit with no `branch` writes to the default branch. |
| `base`, `head` | A pull request's branches (`merge_pr` reads the pull request for them). |
| `path` | A file's path. |
| `protected_branch` | `branch` (or `base` for pull requests) matches `protected_branches`. |
| `is_default_branch` | That branch is the default branch. |
| `touches_workflows` | `path` is under `.github/workflows/` or `.github/actions/`. |
| `enriched` | `false` when a lookup failed. A branch that couldn't be learned then counts as protected, and the starter rules treat missing facts as protected. A failed lookup never blocks the decision itself. |

Arguments the connector would refuse produce no facts.

## The marker

When `issue_marker` is `true`, the bodies of `create_issue`, `comment` and `create_pr` end with a
blank line and an HTML comment carrying the action id:

```
Fails 1 in 20 runs on CI.

<!-- deedglass:act_01j9zq3m8x2k7w4c5v6b7n8p9r -->
```

GitHub doesn't render it, but anyone reading the raw body (the API, an email notification, the edit
box) sees it. The L1 read-back strips it before comparing the body, then checks that it names this
action. Turn it off with `issue_marker: false`; that also turns off reconciliation.

## Reconciliation, never a blind retry

A write is never sent twice. When a create (`create_issue`, `comment`, `create_pr`) times out or the
connection breaks after the request left, GitHub may have created it. Instead of guessing or
retrying, the connector makes **one** bounded lookup within the call's own deadline (it keeps a
slice of the deadline for this): issues and pull requests this connector's identity opened since
the call started (`GET …/issues?creator={login}&since=…&state=all&sort=created&direction=desc&per_page=30`),
or the issue's comments since then, looking for this call's marker.

- **Found:** the call succeeded. The result is the normal projection with `meta.reconciled = true`.
- **Not found:** the original error stands. The Gate records the outcome as unknown, the verdict is
  UNVERIFIABLE, and the message says "It may have run."

## Read-back (L1)

The Witness runs the connector's own read-back after each write; one read per attempt, and the
Witness tries again within its budget. Comparisons are exact; long values show as `sha256:…`
digests in the detail.

- A **404 right after a create** is a gap (`readback_not_found`), retried 3 times over about 2 s, and
  never a mismatch. For `delete_branch` a 404 is the pass, and a branch that still resolves is the
  mismatch "still resolves".
- The **blob check** computes `sha1("blob " + length + "\0" + content)` locally, the value
  `git hash-object` prints (`"hello\n"` is `ce013625030ba8dba906f756967f9e9ca394464a`), and compares it
  with the blob SHA GitHub shows on that branch.
- The **merge check** reads the pull request (merged, the head SHA you approved, the merge commit),
  then `GET …/compare/{base}...{merge_commit_sha}`: it passes when the status is `identical` or
  `behind`.
- A **rate limit** during a read-back is a gap (UNVERIFIABLE), never a mismatch.

## Rate limits

GitHub limits how many requests a token makes each hour, and adds secondary limits on bursts and
writes. The `rate` check shows what is left.

- A 403 or 429 with `x-ratelimit-remaining: 0` is `upstream_rate_limited` with `Retry-After` set to
  the reset time (at most 1 hour). A `retry-after` header, or a secondary-limit answer, maps the same
  way (60 s when GitHub names no wait).
- **Writes are never retried automatically.** A read retries once, only when the wait is 5 s or less.
- `meta.ratelimit_remaining` on each result feeds the Console; the `rate` check shows the budget.

## Errors

| GitHub answers | Code | What the admin sees |
|---|---|---|
| 401 | `upstream_auth_failed` | The credential is expired, revoked or wrong. Store a new one. |
| 403 with `x-accepted-github-permissions` | `upstream_auth_failed` | "GitHub refused this token for POST /repos/arjun/deedglass-sandbox/issues. It needs Issues: write. Update the fine-grained token, then run deedglass connector test github." |
| 403 or 429, rate limited | `upstream_rate_limited` | The wait, and the budget left. |
| 404 | `upstream_not_found` | GitHub answers 404 when a token can't see a private repository, so check the token's repository access too. |
| 422 `Reference already exists` on `create_branch` | none, or `conflict` | At the same commit the call succeeds (`existed: true`); at another commit it is `conflict` and nothing changes. |
| 405 or 409 on `merge_pr` | `upstream_error` | GitHub's own reason ("Pull Request is not mergeable", "Head branch was modified"), redacted. |
| Wrong kind of token | `validation_failed` | Nothing was sent. Store a fine-grained token, or the App's key. |

## Connector test

`deedglass connector test github` runs seven checks, in order: `config`, `credentials` (the kind and
the last 4 characters), `destination`, `auth` (`GET /user`, or for an App the token mint plus
`GET /installation/repositories`; it records the login the author check uses), `token` (warns for a
classic token or an expiry within 7 days, fails when expired), `repos` (up to 10 probed; a
read-only repository warns "read-only for this token; write tools will fail") and `rate`. It changes
nothing on GitHub.

## What it doesn't do

- **No GraphQL.** REST only, one version (`api_version`).
- **No Actions runs.** It can't start, rerun or cancel workflows, and it never asks for the
  Workflows permission. A change under `.github/workflows/` is a plain file write that policy sees
  as `touches_workflows`.
- **No org administration.** No members, teams, settings, webhooks, secrets or repository creation.
- **No cross-fork pull requests.** `head` is a branch of the same repository.
- **Enterprise Server is untested.** `api_base_url` can point at one, but no fixture covers it yet.
- **No L2.** GitHub has no change log Deedglass can witness; L1 is the highest level.
- **Text files only** for `create_or_update_file`, up to `max_file_bytes`.
- L1 shows GitHub's state after the call, not whether the change was wise.
