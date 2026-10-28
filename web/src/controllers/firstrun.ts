// firstrun.ts: the first run (DL-143). Welcome → who it's for → Connect. The choice only orders the
// Connect tiles and picks the default view (Simple for clinics and hospital IT, Detailed for
// builders); it is saved in this browser (theme.ts) and, when the local app has the route, on the
// server (PATCH /v1/local). Keyboard: every control is a button or a radio; focus moves to each
// panel's heading so a screen reader hears the new step.

import { simpleCopy as c } from '../lib/copy-simple.ts';
import { byId } from '../lib/dom.ts';
import { saveLocal } from '../lib/simple-api.ts';
import { choosePath, getPath, isPath, type Path } from '../lib/theme.ts';
import { announce } from './shell.ts';

function showStep(n: 1 | 2): void {
  byId('fs-1').hidden = n !== 1;
  byId('fs-2').hidden = n !== 2;
  for (const li of byId('fs-steps').querySelectorAll<HTMLElement>('li')) {
    const step = Number(li.dataset.step);
    if (step === n) li.setAttribute('aria-current', 'step');
    else li.removeAttribute('aria-current');
    li.classList.toggle('done', step < n);
    li.classList.toggle('current', step === n);
  }
  const head = byId(n === 1 ? 'fs-h1' : 'fs-h2');
  requestAnimationFrame(() => head.focus());
  announce(c.firstRun.stepOf.replace('{n}', String(n)));
}

export function mountFirstRun(): void {
  const next = byId<HTMLButtonElement>('fs-next');
  const hint = byId('fs-hint');
  const radios = [...document.querySelectorAll<HTMLInputElement>('input[name="path"]')];
  const chosen = (): Path | null => {
    const v = radios.find((r) => r.checked)?.value;
    return isPath(v) ? v : null;
  };
  const sync = () => {
    const p = chosen();
    next.setAttribute('aria-disabled', p ? 'false' : 'true');
    hint.hidden = !!p;
  };
  const saved = getPath();
  if (saved) radios.forEach((r) => (r.checked = r.value === saved));
  radios.forEach((r) => r.addEventListener('change', sync));
  sync();

  byId('fs-start').addEventListener('click', () => showStep(2));
  byId('fs-back').addEventListener('click', () => showStep(1));
  next.addEventListener('click', async () => {
    const p = chosen();
    if (!p) {
      radios[0]?.focus();
      return;
    }
    next.setAttribute('aria-busy', 'true');
    choosePath(p);
    try {
      await saveLocal({ path: p, first_run: false });
    } catch {
      /* the server keeps nothing: this browser's choice still holds */
    }
    location.assign('/app/connect?from=start');
  });
}

