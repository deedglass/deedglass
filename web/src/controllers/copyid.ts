// copyid.ts: any [data-copy] button copies its value and says so (IdChip, HashChip, request ids).
import { copy } from '../lib/copy.ts';
import { toast } from './toast.ts';

export function wireCopy(root: ParentNode = document): void {
  root.addEventListener('click', async (e) => {
    const btn = (e.target as HTMLElement).closest?.('[data-copy]') as HTMLElement | null;
    if (!btn) return;
    const value = btn.getAttribute('data-copy') ?? '';
    try {
      await navigator.clipboard.writeText(value);
      btn.classList.add('copied');
      setTimeout(() => btn.classList.remove('copied'), 1200);
      toast(copy.toast.copied, { kind: 'success' });
    } catch {
      toast("Deedglass couldn't copy to the clipboard. Select the value and copy it by hand.", { kind: 'error' });
    }
  });
}
