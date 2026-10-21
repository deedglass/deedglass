# FHIR write-integrity scenarios

Seventeen small, reproducible cases in which an AI agent's FHIR write is answered with success (or a
clear refusal) and what the agent reports about it may or may not match what the server stored.
Each one is a few FHIR R4 REST calls on synthetic data against a real server, with the server
behaviour it rests on cited from the FHIR specification, an RFC or the vendor's documentation. The
benchmark that scores tools against them is [`BENCHMARK.md`](BENCHMARK.md); the latest results are
in [`results/latest.md`](results/latest.md).

The scenarios are about what was stored versus what the agent reported. None of them judges
clinical content.

| Scenario | The agent reports | Medplum 5.2.6 stores (no gateway) |
|---|---|---|
| [`01-conditional-create-match`](scenarios/01-conditional-create-match/scenario.json) | Charted the heart rate (74 beats/minute) as a new Observation. | nothing |
| [`02-conditional-update-creates`](scenarios/02-conditional-update-creates/scenario.json) | Updated the phone number on the patient's existing record. | create 1 Patient |
| [`03-transaction-as-batch`](scenarios/03-transaction-as-batch/scenario.json) | Stored the three heart-rate readings in one transaction. | create 2 Observation |
| [`04-transaction-atomic`](scenarios/04-transaction-atomic/scenario.json) | The transaction was refused (one reading had no status), so nothing was stored. | nothing |
| [`05-readonly-field-dropped`](scenarios/05-readonly-field-dropped/scenario.json) | Charted the heart rate with the note about the short walk. | create 1 Observation; Observation.note not as the agent believes |
| [`06-client-id-replaced`](scenarios/06-client-id-replaced/scenario.json) | Charted the heart rate as Observation/<the id it chose>, to link from the care plan. | create 1 Observation; Observation.id not as the agent believes |
| [`07-author-replaced`](scenarios/07-author-replaced/scenario.json) | Charted the heart rate with the clinician as its author. | create 1 Observation; Observation.meta.author not as the agent believes |
| [`08-server-meta-added`](scenarios/08-server-meta-added/scenario.json) | Charted the heart rate as a new Observation. | create 1 Observation |
| [`09-lost-update`](scenarios/09-lost-update/scenario.json) | Marked the heart-rate reading final; nothing else changed. | update 1 Observation; overwrote 1 Observation version the agent hadn't read |
| [`10-stale-if-match`](scenarios/10-stale-if-match/scenario.json) | The update was refused because the reading had changed since it was read; nothing was stored. | nothing |
| [`11-patch-add-replaces-array`](scenarios/11-patch-add-replaces-array/scenario.json) | Added the device identifier; the Observation keeps its own identifier too. | update 1 Observation; Observation.identifier not as the agent believes |
| [`12-patch-missing-path`](scenarios/12-patch-missing-path/scenario.json) | The patch was refused because the Observation has no note; nothing was stored. | nothing |
| [`13-dangling-reference`](scenarios/13-dangling-reference/scenario.json) | Charted the heart rate for the patient. | create 1 Observation; 1 reference in Observation.subject to a resource that doesn't exist |
| [`14-reference-checked`](scenarios/14-reference-checked/scenario.json) | The server refused the reading because the patient wasn't found; nothing was stored. | nothing |
| [`15-delete-history-kept`](scenarios/15-delete-history-kept/scenario.json) | Removed the reading entered on the wrong patient; it is gone. | delete 1 Observation; 1 deleted Observation still readable in history |
| [`16-retry-duplicate`](scenarios/16-retry-duplicate/scenario.json) | Charted the heart rate (the first attempt timed out, so it retried once). | create 2 Observation |
| [`17-other-writer-status`](scenarios/17-other-writer-status/scenario.json) | Put the medication order on hold. | update 1 MedicationRequest |

Six are controls, where the agent's report is right (04, 08, 10, 12, 14, 17).

## What is in a scenario

| File | What it is |
|---|---|
| `scenario.json` | `title`, `story`, `behaviour` and its `sources`; `servers`; `setup` (project settings, read-only elements for the agent's client, resources another client seeds); `steps` (each call, by the agent or another writer, with what the agent believes it stored when that isn't what it sent); `agent_reports` (the agent's own words and, as an effect, what it says it stored) |
| `expected.json` | per server, what it stored with no gateway in front, as `make fhir-bench WRITE_EXPECTED=1` read it from the server's database (reviewed by hand) |

Strings in a scenario may hold `${nonce}` (unique per run), `${id:<label>}` and
`${version:<label>}` (the id and version of the resource a labelled request wrote).

## Add a scenario

Copy a directory, change the calls, cite where the behaviour is documented, run
`make fhir-bench WRITE_EXPECTED=1 RUN=<id>` and review the `expected.json` it writes. A behaviour
that contradicts the server's own documentation isn't a scenario: report it to the vendor first.
Published under CC-BY-4.0 ([`../LICENSE`](../LICENSE)).
