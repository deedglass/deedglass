// insightscharts.ts: Insights' Trends (S28 §3.8). Inline SVG, no chart dependency: rects with
// attributes only (the CSP refuses inline styles), fills from state tokens through classes
// (VERIFIED teal, MISMATCH red, UNVERIFIABLE amber, not witnessed neutral; denials and waits are
// neutral), no aurora, no entrance animation. Each chart is a figure: role="img" with a summary
// aria-label, a legend in icon and text, and a <details> "Show as table" with every number.

import { fill } from '../lib/copy.ts';
import { insightsCopy } from '../lib/copy-insights.ts';
import { h, icon, s, type Child } from '../lib/dom.ts';
import { number } from '../lib/format.ts';
import { dayLabel, denialsLabel, fillDays, mixLabel, waitLabel, waitText, type Day, type Stats } from '../lib/models/insights.ts';

const c = insightsCopy.trends;
const H = 96;
const STEP = 10;
const BAR = 7;

type Series = 'verified' | 'unverifiable' | 'mismatch' | 'none';
const STACK: { key: Series; label: string; icon: string }[] = [
  { key: 'verified', label: c.verified, icon: 'check' },
  { key: 'mismatch', label: c.mismatch, icon: 'x' },
  { key: 'unverifiable', label: c.unverifiable, icon: 'circle-dashed' },
  { key: 'none', label: c.none, icon: 'circle' },
];

function frame(title: string, label: string, svg: SVGSVGElement, legend: Child, axis: Child, table: HTMLTableElement, cls = ''): HTMLElement {
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', label);
  return h('figure', { class: `ins-chart card card-pad ${cls}`.trim() },
    h('figcaption', { class: 'ins-chart-head' }, h('h3', { class: 't-footnote ins-chart-title' }, title), legend),
    h('div', { class: 'ins-chart-plot' }, svg, axis),
    h('details', { class: 'ins-chart-table' }, h('summary', { class: 't-footnote t-2' }, c.showTable),
      h('div', { class: 'ins-table-frame', role: 'region', tabindex: '0', 'aria-label': fill('{t}: {s}', { t: title, s: c.showTable }) }, table)));
}

function axisRow(days: Day[], max: number): HTMLElement {
  return h('div', { class: 'ins-axis num', 'aria-hidden': 'true' },
    h('span', null, days.length ? dayLabel(days[0]!.date) : ''),
    h('span', null, fill(c.max, { n: number(max) })),
    h('span', null, days.length ? dayLabel(days[days.length - 1]!.date) : ''));
}

function svgFor(days: number): SVGSVGElement {
  return s('svg', { class: 'ins-svg', viewBox: `0 0 ${Math.max(1, days) * STEP} ${H}`, preserveAspectRatio: 'none', focusable: 'false' });
}

/** mixChart: verification mix per day, stacked, from the bottom: VERIFIED, UNVERIFIABLE, MISMATCH, not witnessed. */
export function mixChart(st: Stats): HTMLElement {
  const days = fillDays(st);
  const max = Math.max(1, ...days.map((d) => d.actions));
  const svg = svgFor(days.length);
  svg.append(s('line', { class: 'ins-base', x1: 0, x2: days.length * STEP, y1: H - 0.5, y2: H - 0.5 }));
  days.forEach((d, i) => {
    let y = H;
    const order: Series[] = ['verified', 'unverifiable', 'mismatch', 'none'];
    for (const k of order) {
      const v = d[k];
      if (!v) continue;
      // A small non-zero count stays visible (2 units); the table carries the exact numbers.
      const hgt = Math.max((v / max) * H, k === 'mismatch' || k === 'unverifiable' ? 2 : 0.5);
      y -= hgt;
      svg.append(s('rect', { class: `bar-${k}`, x: i * STEP + (STEP - BAR) / 2, y: Math.max(0, y), width: BAR, height: hgt }));
    }
  });
  const t = st.totals.verdicts;
  const totals: Record<Series, number> = { verified: t.VERIFIED, mismatch: t.MISMATCH, unverifiable: t.UNVERIFIABLE, none: t.none };
  const legend = h('ul', { class: 'ins-legend', role: 'list' }, ...STACK.map((x) =>
    h('li', { class: `lg-${x.key}` }, icon(x.icon, 16), h('span', null, x.label), h('span', { class: 'num t-2' }, number(totals[x.key])))));
  const table = h('table', { class: 'ins-table' },
    h('thead', null, h('tr', null, h('th', { scope: 'col' }, c.day), h('th', { scope: 'col', class: 'num' }, c.actions), ...STACK.map((x) => h('th', { scope: 'col', class: 'num' }, x.label)))),
    h('tbody', null, ...days.filter((d) => d.actions > 0).map((d) => h('tr', null, h('th', { scope: 'row' }, dayLabel(d.date)), h('td', { class: 'num' }, number(d.actions)),
      ...STACK.map((x) => h('td', { class: 'num' }, number(d[x.key])))))));
  return frame(c.mix, mixLabel(st.totals), svg, legend, axisRow(days, max), table, 'ins-chart-wide');
}

/** denialsChart: deny decisions per day, neutral bars (a denial is a policy doing its job). */
export function denialsChart(st: Stats): HTMLElement {
  const days = fillDays(st);
  const max = Math.max(1, ...days.map((d) => d.denied));
  const svg = svgFor(days.length);
  svg.append(s('line', { class: 'ins-base', x1: 0, x2: days.length * STEP, y1: H - 0.5, y2: H - 0.5 }));
  days.forEach((d, i) => {
    if (!d.denied) return;
    const hgt = Math.max((d.denied / max) * H, 1);
    svg.append(s('rect', { class: 'bar-neutral', x: i * STEP + (STEP - BAR) / 2, y: H - hgt, width: BAR, height: hgt }));
  });
  const total = days.reduce((a, d) => a + d.denied, 0);
  const legend = h('ul', { class: 'ins-legend', role: 'list' }, h('li', { class: 'lg-neutral' }, icon('ban', 16), h('span', null, c.denied), h('span', { class: 'num t-2' }, number(total))));
  const table = h('table', { class: 'ins-table' },
    h('thead', null, h('tr', null, h('th', { scope: 'col' }, c.day), h('th', { scope: 'col', class: 'num' }, c.denied), h('th', { scope: 'col', class: 'num' }, c.actions))),
    h('tbody', null, ...days.filter((d) => d.actions > 0).map((d) => h('tr', null, h('th', { scope: 'row' }, dayLabel(d.date)), h('td', { class: 'num' }, number(d.denied)), h('td', { class: 'num' }, number(d.actions))))));
  return frame(c.denials, denialsLabel(days), svg, legend, axisRow(days, max), table);
}

/** waitChart: the median and the 95th percentile of decided approvals, as two neutral bars. */
export function waitChart(st: Stats): HTMLElement {
  const a = st.totals.approval;
  const med = a.median_seconds ?? 0;
  const p95 = a.p95_seconds ?? 0;
  const top = Math.max(1, p95, med);
  const W = 300;
  const svg = s('svg', { class: 'ins-svg ins-svg-wait', viewBox: `0 0 ${W} 40`, preserveAspectRatio: 'none', focusable: 'false' });
  svg.append(s('rect', { class: 'bar-neutral', x: 0, y: 4, width: (med / top) * W, height: 12 }), s('rect', { class: 'bar-neutral-2', x: 0, y: 24, width: (p95 / top) * W, height: 12 }));
  const legend = h('ul', { class: 'ins-legend', role: 'list' },
    h('li', { class: 'lg-neutral' }, icon('clock', 16), h('span', null, c.median), h('span', { class: 'num' }, waitText(a.median_seconds))),
    h('li', { class: 'lg-neutral-2' }, icon('clock', 16), h('span', null, c.p95), h('span', { class: 'num' }, waitText(a.p95_seconds))));
  const rows: [string, string][] = [[c.median, waitText(a.median_seconds)], [c.p95, waitText(a.p95_seconds)], [c.requested, number(a.requested)],
    [insightsCopy.evidence.approved!, number(a.approved)], [c.expired, number(a.expired)]];
  const table = h('table', { class: 'ins-table' },
    h('thead', null, h('tr', null, h('th', { scope: 'col' }, c.measure), h('th', { scope: 'col', class: 'num' }, c.value))),
    h('tbody', null, ...rows.map(([k, v]) => h('tr', null, h('th', { scope: 'row' }, k), h('td', { class: 'num' }, v)))));
  const note = a.median_seconds === null ? h('p', { class: 't-footnote t-2 m-0' }, c.waitNone) : null;
  return frame(c.wait, waitLabel(a), svg, legend, note, table);
}

export function trendsView(st: Stats): HTMLElement {
  return h('div', { class: 'ins-trends' }, mixChart(st), denialsChart(st), waitChart(st));
}
