// Runs the bot's handler on this machine, as Medplum would call it, against the example's
// Deedglass server: no deploy, no Medplum bot runtime. run.sh calls it after agent.ts.
//
//   DEEDGLASS_SERVER=… DEEDGLASS_API_KEY=<agent key> MEDPLUM_PATIENT_ID=… node examples/medplum/bot/run-local.ts
import type { BotEvent, MedplumClient } from '@medplum/core';
import { handler } from './deedglass-gate-bot.ts';

const secret = (name: string) => ({ name, valueString: process.env[name] ?? '' });
const event = {
  bot: { reference: 'Bot/example' },
  contentType: 'application/fhir+json',
  secrets: { DEEDGLASS_SERVER: secret('DEEDGLASS_SERVER'), DEEDGLASS_API_KEY: secret('DEEDGLASS_API_KEY') },
  input: {
    resourceType: 'Observation',
    status: 'preliminary',
    code: { coding: [{ system: 'http://loinc.org', code: '9279-1', display: 'Respiratory rate' }] },
    subject: { reference: `Patient/${process.env.MEDPLUM_PATIENT_ID ?? ''}` },
    valueQuantity: { value: 16, unit: 'breaths/minute', system: 'http://unitsofmeasure.org', code: '/min' },
  },
} as unknown as BotEvent;

const out = await handler(undefined as unknown as MedplumClient, event);
const words: Record<string, string> = { VERIFIED: 'Matches', MISMATCH: "Doesn't match", UNVERIFIABLE: "Can't check" };
console.log(`${out.check === 'VERIFIED' ? '✓' : '✕'} (bot) · medplum.create · ${out.status} · ${words[out.check] ?? out.check}`);
if (out.summary) console.log(`  ${out.summary}`);
console.log(`  action ${out.action} · deed ${out.deed ?? '…'}`);
if (out.check !== 'VERIFIED') process.exitCode = 1;
