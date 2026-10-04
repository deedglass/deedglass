// reltime.ts: <time class="reltime"> elements re-render their relative text every 30 s.
import { relative } from '../lib/format.ts';

export function tickTimes(root: ParentNode = document): () => void {
  // Only touch what changed, 200 elements a frame: a 500-row feed holds 1,000 of these, and one
  // long single pass would be a long task.
  const tick = () => {
    const now = new Date();
    const all = [...root.querySelectorAll<HTMLTimeElement>('time.reltime[datetime]')];
    const step = (i: number) => {
      for (const t of all.slice(i, i + 200)) {
        const v = relative(t.dateTime, now, t.getAttribute('data-tz') ?? 'UTC');
        if (t.textContent !== v) t.textContent = v;
      }
      if (i + 200 < all.length) requestAnimationFrame(() => step(i + 200));
    };
    step(0);
  };
  tick();
  const id = window.setInterval(tick, 30_000);
  return () => clearInterval(id);
}
