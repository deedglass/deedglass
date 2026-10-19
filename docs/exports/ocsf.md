# Export: OCSF API Activity events

`deedglass export --format ocsf` writes each sealed record as an Open Cybersecurity Schema Framework
(OCSF) event, so a SIEM or security data lake that reads OCSF can index what agents did and what
Deedglass decided and checked. The output is NDJSON, one event per line. Overview and the other
formats: [EXPORTS.md](../EXPORTS.md).

## Pinned version

| What | Version | Date |
|---|---|---|
| OCSF schema | 1.9.0 | 2026-08-03 |
| Class | API Activity, `class_uid` 6003, category Application Activity (6) | |
| Profiles | `ai_operation` (adds `ai_agent`, `delegation`), `security_control` (adds `policy`, `action_id`, `disposition_id`, `is_alert`) | |

OCSF 1.9.0 added the `ai_agent` object (an autonomous AI agent, separate from the `agent` object
for security sensors) and the `delegation` object. It has no event class for an AI agent's tool
call, so the export uses API Activity: an agent calling a tool through Deedglass is an API call.

## Mapping

| OCSF attribute | From the record | Notes |
|---|---|---|
| `class_uid`, `category_uid`, names | fixed | 6003 API Activity, 6 Application Activity |
| `activity_id`, `activity_name` | the tool's operation (the part after the last dot) | `create`/`insert` → 1 Create; `read`, `get`, `list`, `search`, `query`… → 2 Read; `update`/`patch` → 3 Update; `delete`/`remove` → 4 Delete; anything else → 99 Other, named by the operation (`execute`) |
| `type_uid`, `type_name` | computed | `600300 + activity_id`; `API Activity: <activity_name>` |
| `time` | `issued_at` | epoch milliseconds: when the record was sealed |
| `severity_id` | the outcome | 1 Informational; 3 Medium when the call didn't run cleanly; 4 High when the check says `MISMATCH` |
| `status_id`, `status`, `status_detail`, `status_code` | `execution` | 1 Success, 2 Failure (also when it never ran), 0 Unknown when Deedglass can't know whether it ran; `status_code` is the record's `error_code` |
| `message` | a sentence built from the record | raw values: tool, agent, decision, rule, execution, verdict and level |
| `metadata.version` | fixed | `1.9.0` |
| `metadata.product` | fixed | `Deedglass`, with the version that wrote the file |
| `metadata.profiles` | fixed | `ai_operation`, `security_control` |
| `metadata.uid` | `deed_id` | |
| `metadata.correlation_uid` | `action.id` | the action and its later checks share it |
| `metadata.tenant_uid` | `org` | |
| `metadata.sequence` | `seq` | |
| `metadata.logged_time` | `issued_at` | |
| `metadata.event_code` | `kind` | `action` or `amendment` |
| `actor.user` | `on_behalf_of`, else the agent | the person the agent acted for (`type_id` 1 User); with no such reference, the agent as a service account (`type_id` 4 Service) |
| `ai_agent` | `agent.id`, `agent.name` | `type_id` 0 Unknown: the record doesn't say which framework the agent uses |
| `delegation` | `on_behalf_of` and `agent.id` | when the record names a person: `issuer_uid` is the person; `uid` is `<person>/<agent id>` |
| `src_endpoint` | `agent.id`, `agent.name` | |
| `api.operation` | `action.tool` | |
| `api.service` | the tool's connector part and `action.connector` | `name` `postgres`, `uid` `con_…` |
| `api.request.uid`, `api.request.data` | `action.id`; `request_digest`, `intent_hash`, `intent_signed` | |
| `api.response` | `execution` | `message` the status, `error` the error code, `data.result_digest` |
| `policy` | `policy` | `name` the rule (or `org default`), `uid` the policy id, `version` the policy version id, `is_applied` true in enforce mode |
| `action_id`, `disposition_id` | `policy.decision`, `approval` | allow → 1 Allowed / 1 Allowed; approved after `require_approval` → 1 Allowed / 8 Approved; deny or an approval not given → 2 Denied / 2 Blocked; shadow mode → 3 Observed / 17 Logged |
| `is_alert` | `witness.verdict` | true for `MISMATCH` |
| `unmapped.deedglass` | everything else | the record's `deed_id`, `seq`, `prev_hash`, `body_hash`, seal and key, policy mode and hash, approval, execution times, the check (`level`, `verdict`, `method`, `detail_digest`), cost, session |

CI validates every event of a real release's bundle against a transcription of the OCSF 1.9.0
schema for this class and the objects it uses (attribute names, types, requirements, enums and
`at_least_one` constraints; no attribute outside the schema), and against the class rules: the
computed `type_uid` and `type_name`, enum captions in the string siblings, the profiles declared for
the attributes used, and no `start_time`, `end_time` or `duration` without `count`.

## What is lost or approximated

- **The verdict has no OCSF attribute.** OCSF has no field for "a second source checked the effect".
  The check is in `unmapped.deedglass.check`; `MISMATCH` also raises `severity_id` to High and sets
  `is_alert`.
- **The activity comes from the tool name.** A tool whose name doesn't say what it does
  (`postgres.execute`) is Other; the SQL isn't in the record, so the export can't tell a read from a
  write.
- **Execution times.** OCSF allows `start_time` and `end_time` only for aggregated events, so the
  call's times and latency are in `unmapped`; `time` is when the record was sealed.
- **The actor.** OCSF's actor needs a user, process, session or application. An AI agent is none of
  those, so the actor is the person it acted for when the record names one, otherwise the agent as
  a service account; the agent is always in `ai_agent`.
- **Delegation is derived.** Deedglass records an opaque reference to the person, not a durable grant
  with its own id. `delegation.uid` is built from the person and the agent; there is no
  `created_time` or `parent_uid`.
- **No model.** `ai_model` (recommended by the `ai_operation` profile) is left out: Deedglass sees
  the tool call, not the model.
- **A later check is a second event** with the same `metadata.correlation_uid`, status Unknown and no
  policy fields: OCSF events are never updated.
- **Admin records** are skipped; the count is printed.
- **No payloads.** Request and result are digests.
- **The seal can't be checked from the event.** Check the record with `deedglass verify`.
