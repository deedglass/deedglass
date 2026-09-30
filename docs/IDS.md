# Ids

Every object Deedglass stores has an id like `agt_01j9zr3m5k8q2w7x4t6v0b1c9d`: a short prefix
that says what it is, an underscore, and a ULID in lowercase (contract §3, ADR-014). `internal/ids`
is the only place ids are made or parsed.

## Format

```
agt_01j9zr3m5k8q2w7x4t6v0b1c9d
└┬┘ └───┬────┘└──────┬───────┘
prefix  time      randomness
        48 bits   80 bits
```

- The ULID part is 26 characters of Crockford base32 in lowercase: `0-9` and `a-z` without
  `i`, `l`, `o` and `u`. The first character is `0`–`7`.
- The first 10 characters are the creation time in milliseconds since 1970 (UTC); the last 16 are
  random, from `crypto/rand`.
- The one regular expression, which S02 copies into SQL domains:

  ```
  ^<prefix>_[0-7][0-9a-hjkmnp-tv-z]{25}$
  ```

  `ids.Pattern("agt")` returns it for a prefix, and a test checks that it agrees with the parser on
  random input for every prefix.

## Parsing is strict

The API never normalizes an id. Each of these is refused, with a message that names the expected
kind:

| Input | Message |
|---|---|
| `agt_01J9ZR3M5K8Q2W7X4T6V0B1C9D` | `invalid agent id "agt_01J9…": ids are lowercase` |
| `org_01j9zr3m5k8q2w7x4t6v0b1c9d` where an agent id is expected | `expected an agent id (agt_…)` |
| `agt01j9…`, `agt_`, 25 or 27 characters, a leading `8`, any of `i l o u`, spaces | refused |

The CLI trims and lowercases what you type (`ids.Normalize`) before parsing; nothing else does.

## Prefixes

<!-- prefixes:begin (generated from internal/ids; go test ./internal/ids -run TestDocsTable -update) -->
| Prefix | Go type | What it names | In API responses | Source |
|---|---|---|---|---|
| `org_` | `ids.OrgID` | An org: the company account that owns agents, policies and deeds. | yes | contract §3 |
| `usr_` | `ids.UserID` | A person. Global: access comes only from memberships. | yes | contract §3 |
| `mem_` | `ids.MembershipID` | A user's role (owner, admin, viewer) in one org. | yes | contract §3 |
| `ses_` | `ids.SessionID` | A Console sign-in session. The cookie holds a secret, never this id alone. | yes | contract §3 |
| `agt_` | `ids.AgentID` | A registered AI agent. | yes | contract §3 |
| `key_` | `ids.APIKeyID` | The record id of an API key (the secret is dg_live_… / dg_test_…). | yes | contract §3 |
| `pol_` | `ids.PolicyID` | A named policy. | yes | contract §3 |
| `pv_` | `ids.PolicyVersionID` | One immutable version of a policy. | yes | contract §3 |
| `con_` | `ids.ConnectorID` | A configured tool integration. | yes | contract §3 |
| `act_` | `ids.ActionID` | One tool call that passed through the Gate. | yes | contract §3 |
| `evt_` | `ids.ActionEventID` | An action event row, and the event object sent over SSE and webhooks. | yes | contract §3 |
| `apr_` | `ids.ApprovalID` | A request for a human decision. | yes | contract §3 |
| `deed_` | `ids.DeedID` | A sealed deed. | yes | contract §3 |
| `sk_` | `ids.SealKeyID` | A deployment-wide seal key. | yes | contract §3 |
| `sug_` | `ids.SuggestionID` | An Insights suggestion that a person accepts or rejects. | yes | contract §3 |
| `anm_` | `ids.AnomalyID` | An Insights anomaly. | yes | contract §3 |
| `rpl_` | `ids.ReplayID` | A policy replay job. | yes | contract §3 |
| `whk_` | `ids.WebhookID` | A webhook endpoint. | yes | contract §3 |
| `req_` | `ids.RequestID` | One API request (Deedglass-Request-Id, the error envelope). | yes | ADR-014 |
| `ltk_` | `ids.LoginTokenID` | A magic-link login token record. The link holds a secret, never this id alone. | no | ADR-014 |
| `pa_` | `ids.PolicyActivationID` | One activation of a policy version. | yes | ADR-014 |
| `cred_` | `ids.CredentialID` | An encrypted connector credential record. | yes | ADR-014 |
| `atk_` | `ids.ApprovalTokenID` | An approval-link token record. | no | ADR-014 |
| `dtx_` | `ids.DecodedTxID` | A transaction decoded from a Postgres change stream. | no | ADR-014 |
| `whd_` | `ids.WebhookDeliveryID` | One attempt to deliver an event to a webhook. | yes | ADR-014 |
| `adm_` | `ids.AdminEventID` | An administrative change in an org. | yes | ADR-014 |
| `wl_` | `ids.WaitlistID` | A waitlist entry (shared shape with the site waitlist). | no | ADR-014 |
| `idk_` | `ids.IdempotencyID` | A stored Idempotency-Key response. | no | ADR-014 |
<!-- prefixes:end -->

Not in the table on purpose: `witness_jobs` rows use a bigint identity id (S13). The site
waitlist (S36) reuses the `wl_` shape, so an entry can move between the two without a new id.

## Ordering and its limits

- One process hands out strictly increasing ids: within a millisecond it adds one to the random
  part; if that ever overflows it waits for the next millisecond; if the clock steps back it keeps
  the last timestamp.
- Sorting ids as strings sorts them by creation time, to the millisecond.
- Ids from different processes or machines are only roughly time-ordered: two servers can mint ids
  in the same millisecond in either order, and their clocks can differ. Don't use id order to
  decide what happened first across processes; the deed chain's `seq` is the order that counts.

## Ids are not secrets

- An id never authorizes anything. Every request is checked against its API key or session and
  the org's row-level security, whatever ids it names.
- An id reveals when it was created. Don't use ids where that would leak something.
- Secrets have their own formats (`dg_live_…` API keys, one-time link tokens) and are stored
  hashed. A record's id (`key_…`, `ltk_…`) is not its secret.

## Using ids in Go

```go
id := ids.New[ids.AgentKind]()           // ids.AgentID
a, err := ids.Parse[ids.AgentKind](s)    // strict; errors wrap ids.ErrInvalid
t := a.Time()                            // creation time, UTC, to the millisecond
```

`ids.ID` implements text and JSON marshalling, `sql.Scanner`, `driver.Valuer` (the zero id is
NULL) and `slog.LogValuer`. `deedglass dev id <prefix>` prints a fresh id.
