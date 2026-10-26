# The 15-minute hospital pilot

`deedglass health pilot` is a self-guided demo for a hospital's IT, security and quality teams. One
command runs four short scenes on synthetic FHIR R4 data, on your own machine, with no Docker, no
server and no account. It ends with a report you can forward and the hospital IT review pack.

```sh
npx deedglass health pilot
```

It takes about 15 minutes at its own pace. Add `--fast` to print everything at once.

Deedglass is administrative and data-integrity software. It decides whether an agent's request runs,
waits for a person or is refused, records each request in a sealed record, and compares what the
FHIR server stored with what the agent sent. It does not diagnose, treat or recommend, and the pilot
uses synthetic records only.

## What it sets up

- **Rules:** the `healthcare-fhir` pack ([guide](../guides/healthcare-fhir-pack.md)), or
  `india-health` with `--region india` for ABDM and NHCX ([India](INDIA.md)). The pack is the one in
  the binary, checked against its seal, and the decisions come from the same policy engine
  `deedglass serve` runs.
- **PHI mode and lockdown on** ([PHI mode](../PHI_MODE.md), [lockdown](../LOCKDOWN.md)): every write
  waits for a person.
- **A FHIR store:** an in-memory FHIR R4 store inside the binary, holding synthetic records only.
  Nothing leaves your machine. Writes go through the FHIR connector's own code
  ([FHIR connector](../connectors/fhir.md)) and each one is read back.

## The four scenes

1. **A write whose read-back matches.** An agent creates a synthetic heart-rate Observation. The
   write waits for a person (lockdown), a stand-in admin countersigns it, the server answers
   `201 Created`, and the read-back shows **Checked · Matches**.
2. **"201 Created", but the stored value differs.** The synthetic store is set to change one value as
   it saves it, as a faulty interface mapping might. The server still answers `201`; the read-back
   shows **Doesn't match** and names the field, what was sent and what was stored.
3. **A high-risk write waits for a person.** A synthetic MedicationRequest matches the rule that
   orders and medication changes wait for a clinician. The pilot prints the Console link a real
   server would send to the named clinicians. Nothing reaches the record system.
4. **Seal and check offline.** The records are sealed with a key made for this run and checked with
   the code behind `deedglass verify`. Then one byte is changed, and the check names the changed
   record.

The pilot has no Console and no database, so the approvals that lockdown needs are countersigned by
a stand-in admin it names (`pilot-owner@example.test`). On your own server a named person does this
in Console › Approvals.

## What it writes

In `./deedglass-pilot` (change it with `--out`, or write nothing with `--no-files`):

| File | What it is |
|---|---|
| `REPORT.md` | What the pilot did, in words an IT lead can forward. Every number in it comes from the run. |
| `IT_REVIEW_PACK.md` | The [hospital IT review pack](IT_REVIEW_PACK.md): architecture, where data lives, threat model, security answers, the clinical-safety boundary. |
| `deeds.jsonl`, `keys.json` | The sealed records and the run's public key. Check them yourself: `deedglass verify deeds.jsonl --keys keys.json` |

## Options

| Flag | What it does |
|---|---|
| `--region india` | Use the `india-health` pack (ABDM and NHCX) instead of `healthcare-fhir`. |
| `--fhir-url <url>` | Send scene 1 to your own FHIR R4 sandbox (no auth, synthetic data only). The pilot first creates a synthetic Patient there. Scene 2 always runs on the in-memory store. |
| `--console-url <url>` | The Console base URL the approval link in scene 3 names (default `http://127.0.0.1:8080`). |
| `--out <dir>` | Where to write the files (default `deedglass-pilot`). |
| `--no-files` | Write no files. |
| `--fast` | No pauses. |
| `--print-review-pack` | Print the IT review pack as Markdown and exit. |
| `--json` | Print the run's result as JSON instead of the scenes. |

Only point `--fhir-url` at a test server that holds no patient data.

## After the pilot

- Put your own FHIR server behind Deedglass: [Use a FHIR server today](../guides/fhir-server.md).
- Read the review pack with your security team, or print it:
  `deedglass health pilot --print-review-pack > IT_REVIEW_PACK.md`.
- Healthcare overview: [deedglass.com/healthcare](https://deedglass.com/healthcare).
