# PHI mode

PHI mode is an org setting for teams whose agents handle health data. When it is on, Deedglass keeps
ids and digests of each action's request and result, not the clinical content, once the action is
sealed and its checks are done. Deeds already hold digests only; PHI mode applies the same rule to
the action history.

It doesn't make Deedglass, or how you use it, meet any law or standard. It reduces what the
action history holds. [What it doesn't cover](#what-it-doesnt-cover) is listed below.

## Turn it on

An owner sets it on the org (`PATCH /v1/org`, owner only):

```sh
curl -X PATCH "$DEEDGLASS_SERVER/v1/org" -H "Cookie: …" -H "Deedglass-CSRF: …" \
  -H "Content-Type: application/json" -d '{"settings":{"phi_mode":true}}'
```

The change is sealed in an `org.updated` deed (`fields: ["settings.phi_mode"]`). `GET /v1/org` shows
`"phi_mode": true` under `settings` while it is on. Turning it on also covers the org's earlier
actions that still hold their payloads. Turning it off (`"phi_mode": false`) stops it for new
actions; payloads already replaced don't come back.

## What it keeps

Once an action is sealed and no read-back or Proven check still needs it, the serve job (every 2 s)
replaces its stored payloads:

| Stored field | In PHI mode |
|---|---|
| Request (`args`) | Each argument's name, and a digest of its value. Arguments the connector names as identifiers keep their value: for the REST connector, the path arguments (`/fhir/Observation/{id}` keeps `id`); for the FHIR connector, `resource_type`, `id` and `version_id`. |
| Result | The paths the connector names as identifiers, everything else as one digest per subtree. For the REST connector: the HTTP status and the `etag`, `location`, `last-modified`, `content-type`, `request-id` and `x-request-id` headers. The body is a digest. For the FHIR connector: the status, the `etag`, `location` and `last-modified` headers, and the body's `resourceType`, `id` and `meta.versionId`; the resource itself is a digest. |
| Error message | Its digest. The error code stays. |
| Witness detail | Verdicts, levels, methods, reason codes and classes, the compared field paths, the read-back's digests and counts. Summaries, messages and compared values become digests. |
| Policy input | Removed (it holds the arguments). |

Each digest is `sha256:` and the hex SHA-256 of the value's canonical JSON, the same digest the
deeds use, so you can check a value you hold against it. The action's `request_digest`,
`result_digest` and `witness_detail_digest` keep the digests of what was sent and seen, so the deeds
verify as before.

An approval request in a PHI-mode org is written in this form from the start: the person deciding
sees the tool, the agent, the rule, the argument names with digests (identifiers as they are) and the
SQL facts, not the values. The connector's extra sections (an email preview, an undo summary) are
left out. Emails and Slack cards are built from that snapshot.

A connector that doesn't name its identifiers keeps none: argument names stay, every value and the
whole result become digests.

## While an action is in flight

PHI mode never changes what an agent sends or what the target receives. Until an action is sealed
and its checks are done, the action history holds the request and result as sent: an action that
waits for approval runs with its stored request, and a read-back reads with it. A Proven (L2) check
that waits for the change log keeps them until it finishes. Then they are replaced within a few
seconds.

## What it doesn't cover

- **Short or common values can be found from their digests.** A status like `final` or a small
  number can be guessed by trying values. Digests aren't salted per org yet.
- **Row images the decoder keeps** for the Postgres connector's Proven check and observe mode are not
  changed by PHI mode. Mark sensitive columns with `witness.sensitive_columns`.
- **The agent and the target see everything**: PHI mode only changes what Deedglass stores.
- **Logs and backups made before PHI mode was on**, and copies you exported, keep what they hold.
- **The deed's digests stay** (that is what makes a deed checkable); they are digests, not values.
- **Replays can't re-run a minimized action.** Its policy input is removed, so
  [`deedglass replay`](REPLAY.md) counts it as unknown (for now under "recorded before inputs were
  stored").
- **Turning it off** doesn't bring payloads back, and turning it on again covers only actions that
  still hold their payloads.
