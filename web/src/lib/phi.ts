// phi.ts: Health mode (console-v1). When the org's PHI mode is on (GET /v1/org settings.phi_mode,
// docs/PHI_MODE.md), the Console shows field names and never values: the DiffTable's cells, the
// request and the result stay hidden, and a badge says why. The org object is the one state.ts
// read at boot (dg:ready); before that, or without the field, Health mode counts as off.

import { get } from './state.ts';

export function phiOn(): boolean {
  const org = get()?.org as { settings?: { phi_mode?: boolean } } | undefined;
  return org?.settings?.phi_mode === true;
}
