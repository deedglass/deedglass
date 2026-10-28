// firstrungate.ts: sends a new local-app install from /app to /app/start (DL-143). Only the local
// app answers GET /v1/local with first_run true; any other server answers 404 and nothing changes,
// so today's Console opens exactly as before. Kept tiny: it loads on the Overview.

import { getLocal } from '../lib/simple-api.ts';
import { hasStarted } from '../lib/theme.ts';

export function firstRunGate(): void {
  if (hasStarted()) return;
  void getLocal()
    .then((l) => {
      if (l?.local && l.first_run) location.replace('/app/start');
    })
    .catch(() => undefined);
}
