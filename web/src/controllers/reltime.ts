// reltime.ts: <time class="reltime"> elements re-render their relative text every 30 s.
import { relative } from '../lib/format.ts';

export function tickTimes(root: ParentNode = document): () => void {
  const tick = () => {
    const now = new Date();
    for (const t of root.querySelectorAll<HTMLTimeElement>('time.reltime[datetime]')) {
      t.textContent = relative(t.dateTime, now, t.getAttribute('data-tz') ?? 'UTC');
    }
  };
  tick();
  const id = window.setInterval(tick, 30_000);
  return () => clearInterval(id);
}
