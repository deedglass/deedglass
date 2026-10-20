// A Medplum Bot that writes through the Deedglass gate instead of calling medplum.createResource.
// The write is decided by your Deedglass policy, read back at the version Medplum reported, and
// sealed as a deed, the same as the agent in ../agent.ts. Synthetic data only while you try it.
//
// Bot secrets (Medplum: Project › Secrets): DEEDGLASS_SERVER (an https URL the bot runtime can
// reach) and DEEDGLASS_API_KEY (an agent key: one agent per bot is easiest to read in the Ledger).
// Input: a FHIR resource of a type the connector allows, for example an Observation the bot built
// from a QuestionnaireResponse. Deedglass doesn't look at or change clinical content; it records
// what was sent and compares it with what Medplum stored.
//
// Deploy it like any other bot (a source and dist entry in medplum.config.json). Bundle
// @deedglass/sdk into the dist file: Medplum's bot layer supplies @medplum/core, not this SDK. The
// SDK has no runtime dependency and needs Node 22.12 or newer. ./run-local.ts calls this handler on
// your machine against the example's Deedglass server, without deploying it.
import type { BotEvent, MedplumClient } from '@medplum/core';
import { Deedglass } from '@deedglass/sdk';

/** The connector's name in Deedglass (the fhir connector in front of this Medplum project). */
const CONNECTOR = 'medplum';

export interface GateOutcome {
  action: string;
  status: string;
  check: string; // VERIFIED, MISMATCH, UNVERIFIABLE, or none when nothing ran
  summary: string;
  id?: string;
  deed?: string | null;
}

export async function handler(_medplum: MedplumClient, event: BotEvent): Promise<GateOutcome> {
  const resource = event.input as { resourceType?: string } | undefined;
  if (!resource?.resourceType) throw new Error('The bot expects a FHIR resource as input.');

  const dg = new Deedglass({
    baseUrl: event.secrets['DEEDGLASS_SERVER']?.valueString,
    apiKey: event.secrets['DEEDGLASS_API_KEY']?.valueString,
    appInfo: { name: 'medplum-bot' },
  });

  // One gate call per write. A rule that needs a person returns pending_approval: the bot stops
  // here and the write runs when someone countersigns in Console › Approvals.
  let a = await dg.act(`${CONNECTOR}.create`, { resource_type: resource.resourceType, resource: resource as Record<string, unknown> });
  const deadline = Date.now() + 25_000;
  while (a.status === 'executed' && a.state !== 'sealed' && Date.now() < deadline) a = await dg.actions.get(a.id, { waitMs: 5_000 });

  const outcome: GateOutcome = {
    action: a.id,
    status: a.status,
    check: a.witness?.verdict ?? 'none',
    summary: a.witness?.summary ?? a.decision?.message ?? '',
    id: (a.execution?.result as { body?: { id?: string } } | undefined)?.body?.id,
    deed: a.deed,
  };
  // Make a stored-but-different write visible in the bot's own log, not only in Deedglass.
  if (outcome.check === 'MISMATCH') console.log(`Deedglass: Medplum stored something different (${a.id}). ${outcome.summary}`);
  return outcome;
}
