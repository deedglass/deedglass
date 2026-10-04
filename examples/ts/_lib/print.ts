// CLI-style lines for the examples (brand §13.3): ✓ ✕ ◐ ○, · separators, ids shortened with …,
// colour only on a TTY and never with NO_COLOR.
import type { Action } from '@deedglass/sdk';

const color = process.stdout.isTTY && !process.env.NO_COLOR;
const paint = (code: string, s: string): string => (color ? `\x1b[${code}m${s}\x1b[0m` : s);
export const ok = (s: string): void => console.log(paint('36', '✓') + ' ' + s);
export const no = (s: string): void => console.log(paint('31', '✕') + ' ' + s);
export const wait = (s: string): void => console.log(paint('35', '◐') + ' ' + s);
export const note = (s: string): void => console.log(paint('33', '○') + ' ' + s);

/** "deed_01j9zw…": the prefix and 6 characters. */
export function short(id: string | null | undefined): string {
  if (!id) return '…';
  const i = id.indexOf('_');
  return id.slice(0, i + 7) + '…';
}

/** One line for a sealed action. */
export function line(a: Action): string {
  const w = a.witness;
  return [a.tool, a.status, w?.level ?? 'L0', w?.verdict ?? 'pending', short(a.deed)].join(' · ');
}

export const live = process.argv.includes('--live');
