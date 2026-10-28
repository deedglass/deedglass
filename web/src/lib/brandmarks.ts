// brandmarks.ts: the real marks on the Connect screen's tiles (DL-143 Connect v1). Each is one
// inline SVG path from src/icons/brandmarks.gen.ts (simple-icons, CC0 1.0; the marks belong to
// their owners, web/BRAND_MARKS.md), filled with the brand's own colour. A black mark (OpenAI,
// Cursor, Copilot) takes the text colour instead, so it stays visible in dark mode. Pure DOM, no
// fetch: the app is loopback-only and its CSP allows no remote image.

import { BRAND_MARKS, type BrandMarkId } from '../icons/brandmarks.gen.ts';
import { s } from './dom.ts';

export type { BrandMarkId };

export const brandMarkIds = Object.keys(BRAND_MARKS) as BrandMarkId[];

/** brandMark renders one mark at size px. It is decorative: the tile's name carries the meaning. */
export function brandMark(id: BrandMarkId, size: 20 | 24 | 28 = 24): SVGSVGElement {
  const m = BRAND_MARKS[id];
  const svg = s('svg', { class: `brandmark${m.dark ? ' brandmark-ink' : ''}`, width: size, height: size, viewBox: '0 0 24 24', 'aria-hidden': 'true', focusable: 'false', 'data-mark': id });
  svg.append(s('path', { d: m.path, fill: m.dark ? 'currentColor' : m.hex }));
  return svg;
}

/** brandTitle is the owner's name for the mark, for alt text and the attribution test. */
export function brandTitle(id: BrandMarkId): string {
  return BRAND_MARKS[id].title;
}
