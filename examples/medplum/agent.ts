// An AI-agent-style client writing to Medplum through the Deedglass gate (examples/medplum/README.md).
// It makes the calls an agent's tools would make, scripted so no model key is needed. Synthetic data only.
//
//   (a) a normal create: Medplum stores what the agent sent        → Matches
//   (b) a create Medplum answers 201 but stores without the note    → Doesn't match, the field named
//   (c) an update: a rule says a person decides first               → pending, then approved, run, checked
//
//   DEEDGLASS_SERVER=… DEEDGLASS_API_KEY=<agent key> MEDPLUM_PATIENT_ID=… node examples/medplum/agent.ts
import { Deedglass } from '@deedglass/sdk';
import type { Action } from '@deedglass/sdk';

const color = process.stdout.isTTY && !process.env.NO_COLOR;
const paint = (code: string, s: string): string => (color ? `\x1b[${code}m${s}\x1b[0m` : s);
const ok = (s: string): void => console.log(paint('36', '✓') + ' ' + s);
const no = (s: string): void => console.log(paint('31', '✕') + ' ' + s);
const wait = (s: string): void => console.log(paint('35', '◐') + ' ' + s);
const indent = (s: string): void => console.log('  ' + s);

const patient = process.env.MEDPLUM_PATIENT_ID;
if (!patient) {
  console.error('✕ MEDPLUM_PATIENT_ID isn\'t set. Run examples/medplum/run.sh, or set it to a synthetic Patient id.');
  process.exit(2);
}
const dg = new Deedglass(); // DEEDGLASS_API_KEY (an agent key) and DEEDGLASS_SERVER

/** A synthetic heart-rate Observation. */
function heartRate(value: number, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    resourceType: 'Observation',
    status: 'preliminary',
    category: [{ coding: [{ system: 'http://terminology.hl7.org/CodeSystem/observation-category', code: 'vital-signs' }] }],
    code: { coding: [{ system: 'http://loinc.org', code: '8867-4', display: 'Heart rate' }] },
    subject: { reference: `Patient/${patient}` },
    valueQuantity: { value, unit: 'beats/minute', system: 'http://unitsofmeasure.org', code: '/min' },
    ...extra,
  };
}

/** Waits until the action's deed is sealed: the check (and Proven, where it runs) is in by then. */
async function sealed(a: Action): Promise<Action> {
  const deadline = Date.now() + 60_000;
  while (a.state !== 'sealed' && Date.now() < deadline) a = await dg.actions.get(a.id, { waitMs: 5_000 });
  return a;
}

const verdictWords: Record<string, string> = { VERIFIED: 'Matches', MISMATCH: "Doesn't match", UNVERIFIABLE: "Can't check" };
const levelWords: Record<string, string> = { L0: 'Reported', L1: 'Checked', L2: 'Proven' };

function report(label: string, a: Action): string {
  const w = a.witness;
  const verdict = verdictWords[w?.verdict ?? ''] ?? 'no check';
  const level = levelWords[w?.level ?? ''] ?? '';
  const line = `${label} · ${a.tool} · ${a.status} · ${level} · ${verdict}`;
  if (w?.verdict === 'VERIFIED') ok(line);
  else no(line);
  if (w?.summary) indent(w.summary);
  for (const r of w?.reasons ?? []) if (r.class !== 'note' || w?.verdict !== 'VERIFIED') indent(`${r.level} ${r.code}: ${r.message}`);
  indent(`action ${a.id} · deed ${a.deed ?? '…'}`);
  return w?.verdict ?? '';
}

let failed = 0;

// (a) A normal create.
let a = await sealed(await dg.act('medplum.create', { resource_type: 'Observation', resource: heartRate(72) }));
const created = (a.execution?.result as { body?: { id?: string; meta?: { versionId?: string } } } | undefined)?.body;
if (report('(a)', a) !== 'VERIFIED') failed++;

// (b) The same kind of create with a note. The project's AccessPolicy marks Observation.note
// read-only, so Medplum answers 201 and stores the Observation without the note. The agent's own
// result looks like success; the read-back of the version Medplum reported says otherwise.
a = await sealed(await dg.act('medplum.create', { resource_type: 'Observation', resource: heartRate(74, { note: [{ text: 'Synthetic note: measured after a short walk.' }] }) }));
if (report('(b)', a) !== 'MISMATCH') failed++;

// (c) An update of (a)'s Observation. The policy says a person countersigns first; nothing reaches
// Medplum until then. The agent waits (a person approves in Console › Approvals, or on a dev server
// with: deedglass approve <apr_…> --as <email>).
if (!created?.id) {
  no('(c) skipped: (a) returned no Observation id.');
  failed++;
} else {
  const update = { resource_type: 'Observation', id: created.id, if_match: created.meta?.versionId ?? '', resource: { ...heartRate(72), id: created.id, status: 'final' } };
  a = await dg.act('medplum.update', update);
  if (a.status === 'pending_approval') {
    wait(`(c) · medplum.update · pending approval · ${a.approval?.id ?? ''} · a person decides in Console › Approvals`);
    a = await dg.actions.waitUntilSettled(a.id, { timeoutMs: 10 * 60_000 });
  }
  if (a.status !== 'executed') {
    no(`(c) · medplum.update · ${a.status}${a.decision?.message ? ' · ' + a.decision.message : ''}`);
    failed++;
  } else if (report('(c)', await sealed(a)) !== 'VERIFIED') failed++;
}

if (failed) {
  console.error(`✕ ${failed} of 3 scenarios didn't end as the README says.`);
  process.exitCode = 1;
}
