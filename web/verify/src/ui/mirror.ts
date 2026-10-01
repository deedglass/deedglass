// The Mirror mark (brand §5, flat rendition) with the "Sealed" moment (brand §10): two halves of
// one orb, aurora on the left (what the agent claimed), frosted glass on the right (what happened),
// a pane of light between them. Valid: the halves slide together (240 ms) and one teal pulse
// ripples out (400 ms, 20% opacity). Invalid: the halves stop short with a 1-unit red hairline in
// the gap; no shake. Built with createElementNS only: no markup from strings.

export type MirrorState = 'idle' | 'checking' | 'valid' | 'invalid' | 'cannot' | 'unsupported';

const NS = 'http://www.w3.org/2000/svg';
const R = 44; // radius in a 120-unit box
const APART = 4; // each half 4 units from the seam: the 8-unit glass gap
const SHORT = 2.5; // invalid: the halves stop short
const CLOSED = 1.5; // valid: the halves close on the 3-unit pane

function el<K extends keyof SVGElementTagNameMap>(name: K, attrs: Record<string, string | number>): SVGElementTagNameMap[K] {
  const e = document.createElementNS(NS, name);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v));
  return e;
}

/** The left half (x ≤ 60) or right half of the orb as a path with a flat inner edge. */
function half(side: 'left' | 'right'): string {
  const top = 60 - R;
  const bottom = 60 + R;
  return side === 'left' ? `M60 ${top} A${R} ${R} 0 0 0 60 ${bottom} Z` : `M60 ${top} A${R} ${R} 0 0 1 60 ${bottom} Z`;
}

export class Mirror {
  readonly svg: SVGSVGElement;
  private left: SVGGElement;
  private right: SVGGElement;
  private pulse: SVGCircleElement;
  private state: MirrorState = 'idle';

  constructor(title: string) {
    const uid = Math.random().toString(36).slice(2, 8);
    const svg = el('svg', { viewBox: '0 0 120 120', class: 'mirror', role: 'img', 'data-state': 'idle' });
    const t = el('title', {});
    t.textContent = title;
    svg.append(t);
    const defs = el('defs', {});
    const grad = el('linearGradient', { id: `aurora-${uid}`, x1: '0', y1: '0', x2: '1', y2: '1' }); // 135°
    for (const [off, color] of [['0', '#7C6CFF'], ['0.5', '#4F8CFF'], ['1', '#2FE6D1']] as const) {
      grad.append(el('stop', { offset: off, 'stop-color': color }));
    }
    defs.append(grad);
    svg.append(defs);

    this.pulse = el('circle', { cx: 60, cy: 60, r: R, class: 'pulse' });
    svg.append(this.pulse);

    this.left = el('g', { class: 'half left' });
    this.left.append(el('path', { d: half('left'), fill: `url(#aurora-${uid})` }));
    this.left.append(el('path', { d: half('left'), class: 'outline' }));
    this.right = el('g', { class: 'half right' });
    this.right.append(el('path', { d: half('right'), class: 'frost' }));
    this.right.append(el('path', { d: half('right'), class: 'outline' }));
    svg.append(this.left, this.right);

    // The pane of light (3 units) and the red hairline (1 unit) share the seam.
    svg.append(el('rect', { x: 58.5, y: 60 - R + 6, width: 3, height: 2 * R - 12, rx: 1.5, class: 'pane' }));
    svg.append(el('rect', { x: 59.5, y: 60 - R + 4, width: 1, height: 2 * R - 8, class: 'hairline' }));
    this.svg = svg;
    this.place(APART);
  }

  private place(offset: number): void {
    this.left.style.transform = `translateX(${-offset}px)`;
    this.right.style.transform = `translateX(${offset}px)`;
  }

  set(state: MirrorState): void {
    const was = this.state;
    this.state = state;
    this.svg.setAttribute('data-state', state);
    this.pulse.classList.remove('go');
    switch (state) {
      case 'valid':
        this.place(CLOSED);
        if (was !== 'valid') {
          // Restart the one pulse after the halves meet.
          void this.pulse.getBoundingClientRect();
          this.pulse.classList.add('go');
        }
        break;
      case 'invalid':
        this.place(SHORT);
        break;
      default:
        this.place(APART);
    }
  }
}
