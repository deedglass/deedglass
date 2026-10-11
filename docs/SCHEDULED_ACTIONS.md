# Scheduled actions (S40)

Approve now, run later. `POST /v1/actions` accepts an optional window:

```json
{ "tool": "mail.send", "args": { "…": "…" },
  "schedule": { "not_before": "2026-10-21T15:30:00Z", "not_after": "2026-10-21T17:30:00Z" } }
```

The schedule is stored once in `actions.schedule` and never changes (the runtime role has no
UPDATE grant on it, and a trigger refuses any role with `DG004`). The action object shows it as
`schedule`, and once approved `next_action` is `{"type": "scheduled", "not_before", "not_after"}`.

## Rules

| Decision | What happens |
|---|---|
| `require_approval` | The approval's context gains `schedule` with a sentence in the org's time zone, so `context_digest` binds it. The countersign schedules the run at `greatest(now, not_before)`. |
| `allow`, `not_before` at most 60 s away | It runs now; the schedule is recorded. |
| `allow`, a later `not_before` | Denied and sealed: 403 `schedule_needs_approval`. An allowed action runs at once, so it would run early. |
| `deny` | As always. |

Shape rules, each 400 `validation_failed` with `param: schedule`: gateway mode only (in proxy mode
the edge decides when to run), both times set, at least 5 minutes apart, `not_after` within 72
hours and still ahead.

## After the countersign

The `approval_resume` job runs at `not_before`. Resume checks Stop first (a stopped agent or org
is denied, nothing runs), then the window:

- before `not_before`: it waits;
- after `not_after` (the worker or the Mac was asleep): `pending_approval → cancelled → sealed`,
  reason `schedule_missed`, actor `{system, deedglass}`. Nothing ran, and the approval stays
  `approved`, so the deed shows both. A missed window is never burst-run later.

## Cancelling

`POST /v1/actions/{id}/cancel` works on an approved scheduled action while its `approval_resume`
job is still waiting: one transaction locks the action, deletes the unclaimed job and seals the
action cancelled with the caller as actor. Once a worker holds the job, the answer is 409
`action_executing`. Unscheduled approved actions keep S22's refusal. The cancel and the worker
can't both win: 200 racing runs end in exactly one outcome each (`TestCancelRacesWorker`).

## What it doesn't do

- No recurring schedules, and no schedule in proxy mode.
- No run before `not_before` or after `not_after`, and nothing catches up a missed window.
- The deed doesn't carry the schedule itself; the approval context (bound by `context_digest`) and
  the event's `schedule_missed` reason do.
