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

// ---------------------------------------------------------------------------------------------
// The Console's Mirror (S24 §3.4): one implementation, two consumers. Four sizes, five states, and
// the Sealed and Mismatch moments with the Web Animations API (no inline styles, so the Console's
// CSP holds). Colours come from CSS classes (`dg-mirror` rules in each consumer's stylesheet), never
// from hex in code. The mark is decorative: aria-hidden, and the chip beside it carries the meaning.
//
//   Sealed     0–240 ms   halves move from ±4 to ±1.5 and meet on the pane (enter easing)
//              240–640    one ring r 44→58, aurora-teal stroke, opacity .20→0
//   Mismatch   0–240 ms   halves move to ±2.5 and stop short
//              240–420    a 1-unit hairline in the gap fades in. No shake.
//   Pending    static, apart; the right half's rim in the pending colour
//   Unverifiable  static, apart; the right half's rim dashed, in the unverifiable colour
//   Reduced motion: the final geometry at once, then a 180 ms colour fade only.

export type ConsoleMirrorState = 'open' | 'sealed' | 'mismatch' | 'pending' | 'unverifiable';
export type MirrorSize = 16 | 20 | 32 | 48;

export interface MirrorHandle {
  set(s: ConsoleMirrorState, o?: { animate?: boolean }): Promise<void>;
  state(): ConsoleMirrorState;
  destroy(): void;
}

const EASE_ENTER = 'cubic-bezier(.2,.8,.2,1)';
let mirrorSeq = 0;

function offsetFor(s: ConsoleMirrorState): number {
  return s === 'sealed' ? CLOSED : s === 'mismatch' ? SHORT : APART;
}

function reducedMotion(): boolean {
  return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
}

/** mountMirror draws the mark into host and returns its handle. */
export function mountMirror(host: HTMLElement, o: { size: MirrorSize; state: ConsoleMirrorState }): MirrorHandle {
  const uid = `dgm${++mirrorSeq}`;
  const flat = o.size < 24;
  const svg = el('svg', { viewBox: '0 0 120 120', width: o.size, height: o.size, class: `dg-mirror${flat ? ' flat' : ''}`, 'aria-hidden': 'true', focusable: 'false', 'data-state': o.state });
  const defs = el('defs', {});
  const grad = el('linearGradient', { id: `${uid}-a`, x1: '0', y1: '0', x2: '1', y2: '1' }); // 135°
  grad.append(el('stop', { offset: '0', class: 'a0' }), el('stop', { offset: '0.5', class: 'a1' }), el('stop', { offset: '1', class: 'a2' }));
  defs.append(grad);
  svg.append(defs);
  const ring = el('circle', { cx: 60, cy: 60, r: R, class: 'ring' });
  const left = el('g', { class: 'half left' });
  left.append(el('path', { d: half('left'), fill: `url(#${uid}-a)` }));
  const right = el('g', { class: 'half right' });
  right.append(el('path', { d: half('right'), class: 'frost' }), el('path', { d: half('right'), class: 'rim' }));
  const pane = el('rect', { x: 58.5, y: 60 - R + 6, width: 3, height: 2 * R - 12, rx: 1.5, class: 'pane' });
  const hair = el('rect', { x: 59.5, y: 60 - R + 4, width: 1, height: 2 * R - 8, class: 'hairline' });
  svg.append(ring, left, right, pane, hair);
  host.replaceChildren(svg);

  let current = o.state;
  let running: Animation[] = [];
  const place = (off: number) => {
    left.setAttribute('transform', `translate(${-off} 0)`);
    right.setAttribute('transform', `translate(${off} 0)`);
  };
  const stopAll = () => {
    for (const a of running) a.cancel();
    running = [];
  };
  place(offsetFor(current));
  if (current === 'mismatch') hair.classList.add('on');

  const finish = (s: ConsoleMirrorState) => {
    stopAll();
    place(offsetFor(s));
    hair.classList.toggle('on', s === 'mismatch');
    svg.setAttribute('data-state', s);
  };

  return {
    state: () => current,
    destroy: () => {
      stopAll();
      svg.remove();
    },
    async set(s, opts = {}) {
      const from = offsetFor(current);
      current = s;
      const animate = opts.animate === true && (s === 'sealed' || s === 'mismatch') && typeof svg.animate === 'function';
      if (!animate) {
        finish(s);
        return;
      }
      stopAll();
      svg.setAttribute('data-state', s);
      if (reducedMotion()) {
        finish(s);
        const fade = svg.animate([{ opacity: 0.6 }, { opacity: 1 }], { duration: 180, easing: 'linear' });
        running = [fade];
        await fade.finished.catch(() => undefined);
        return;
      }
      const to = offsetFor(s);
      // Animate from the current geometry; the attribute holds the end state for when it finishes.
      left.removeAttribute('transform');
      right.removeAttribute('transform');
      const opt: KeyframeAnimationOptions = { duration: 240, easing: EASE_ENTER, fill: 'forwards' };
      const l = left.animate([{ transform: `translateX(${-from}px)` }, { transform: `translateX(${-to}px)` }], opt);
      const r = right.animate([{ transform: `translateX(${from}px)` }, { transform: `translateX(${to}px)` }], opt);
      running = [l, r];
      if (s === 'sealed') {
        // The ring grows r 44 → 58 as a scale from the centre (transform-box: fill-box in CSS).
        const g = ring.animate(
          [{ transform: 'scale(1)', opacity: 0.2 }, { transform: `scale(${58 / R})`, opacity: 0 }],
          { duration: 400, delay: 240, easing: EASE_ENTER, fill: 'forwards' },
        );
        running.push(g);
      } else {
        hair.classList.add('on');
        const hf = hair.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 180, delay: 240, easing: 'linear', fill: 'both' });
        running.push(hf);
      }
      await Promise.all(running.map((a) => a.finished.catch(() => undefined)));
      if (current === s) {
        for (const a of running) a.cancel();
        running = [];
        place(to);
      }
    },
  };
}
