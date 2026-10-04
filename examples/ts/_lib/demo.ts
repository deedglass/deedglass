// The three demo calls every adapter example makes, and the closing offline check.
import type { Action, Deedglass } from '@deedglass/sdk';
import { fetchKeys, verifyDeed } from '@deedglass/sdk/verify';
import { no, ok, short, wait } from './print.ts';

export const CALLS = [
  { name: 'refund_customer', args: { customer: 'cus_123', amount: 12000 } },
  { name: 'refund_customer', args: { customer: 'cus_123', amount: 2000 } },
  { name: 'delete_customer', args: { customer: 'cus_123' } },
] as const;

/** Prints one line per decided action, as it is decided; returns the deeds seen. */
export function printer(): { onAction: (a: Action) => void; deeds: string[] } {
  const deeds: string[] = [];
  return {
    deeds,
    onAction(a: Action): void {
      if (a.deed) deeds.push(a.deed);
      if (a.status === 'pending_approval') wait(`${a.tool} · pending approval · ${short(a.approval?.id)} · a person decides in Console › Approvals`);
      else if (a.status === 'denied') no(`${a.tool} · denied · ${(a.decision?.message ?? 'Deedglass refused this action.').replace(/\.?$/, '.')} Nothing was run.`);
      else ok(`${a.tool} · ${a.status} · ${a.witness?.level ?? 'L0'} · ${a.witness?.verdict ?? 'pending'} · ${short(a.deed)}`);
    },
  };
}

/** Checks the deeds offline against the server's published seal keys. */
export async function checkOffline(dg: Deedglass, deeds: string[]): Promise<void> {
  const { keys } = await fetchKeys(dg.baseUrl + '/.well-known/deedglass/keys.json');
  const kids = new Set<string>();
  for (const id of deeds) {
    const d = await dg.deeds.get(id);
    try {
      await verifyDeed(JSON.stringify(d), keys);
      kids.add(d.seal_key_id);
    } catch (e) {
      no(`${id} · ${(e as Error).message}`);
      process.exitCode = 1;
      return;
    }
  }
  ok(`${deeds.length} deeds · seals intact · chain not checked · ${kids.size} seal key${kids.size === 1 ? '' : 's'}   (checked offline with @deedglass/sdk/verify)`);
}
