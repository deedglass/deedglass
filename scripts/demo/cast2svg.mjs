#!/usr/bin/env node
// cast2svg.mjs: renders a captured terminal run (a deedglass_cast JSON from
// ~/txv/growth/deploy/content/scripts/short/capture.mjs, which runs the real `make demo-hero` and keeps
// every output line with the millisecond it appeared) as a looping, self-contained SVG for the README.
// Nothing is typed or edited: each line appears at its recorded time. The only change to timing is
// that the idle start-up before the first line is skipped (the title bar says so), and the window
// scrolls at reading pace instead of jumping when many lines print at once. The still frame
// (no animation, or prefers-reduced-motion) shows the mismatch.
//
//   node scripts/demo/cast2svg.mjs docs/assets/demo-hero.cast.json docs/assets/demo-hero.svg
import { readFileSync, writeFileSync } from 'node:fs';

const [src, dst] = process.argv.slice(2);
if (!src || !dst) {
  console.error('usage: node scripts/demo/cast2svg.mjs <cast.json> <out.svg>');
  process.exit(2);
}
const cast = JSON.parse(readFileSync(src, 'utf8'));
if (cast.object !== 'deedglass_cast' || !Array.isArray(cast.lines) || !cast.commit) {
  console.error('✕ Not a deedglass_cast file (object, lines and commit are required).');
  process.exit(1);
}

const ROWS = 24; // visible terminal rows
const COLS = 104; // visible columns (longer lines are clipped by the window)
const FS = 13; // font size, px
const CW = 7.83; // monospace advance at 13 px (Menlo/SF Mono); only the window width depends on it
const LH = 18; // line height, px
const PAD = 16;
const BAR = 34; // title bar
const HOLD = 5000; // the last frame stays this long before the loop restarts

const first = cast.lines[0].t;
const skipped = first;
const lines = cast.lines.map((l) => ({ t: l.t - first + 400, text: l.text }));
const total = lines.at(-1).t + HOLD;
const W = Math.round(PAD * 2 + COLS * CW);
const H = BAR + PAD * 2 + ROWS * LH;

const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const pct = (ms) => ((ms / total) * 100).toFixed(3) + '%';

function cls(text) {
  const s = text.trimStart();
  if (/^check\s+Proven · Doesn't match/.test(s)) return 'bad strong';
  if (s.startsWith('$ ')) return 'cmd';
  if (/^\d\. /.test(text)) return 'step';
  if (s.startsWith('✕')) return 'bad';
  if (s.startsWith('✓')) return 'ok';
  if (s.startsWith('◐')) return 'wait';
  if (s.startsWith('…') || /^Proven by|^record|^Next:/.test(s)) return 'dim';
  return '';
}

// The still frame: scrolled so the second step's check line and the first rows of its diff show.
const anchor = Math.max(0, lines.findIndex((l) => /Doesn't match/.test(l.text)));
const posterTop = Math.max(0, Math.min(anchor - 6, lines.length - ROWS));

let css = `
text{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace;font-size:${FS}px;white-space:pre;fill:#C9CCD6}
.ok{fill:#34E3C9}.bad{fill:#FF7A70}.wait{fill:#A99CFF}.cmd{fill:#F5F6FA}.step{fill:#F5F6FA;font-weight:600}.dim{fill:#8B90A0}.strong{font-weight:700}
.bar{font-family:ui-sans-serif,-apple-system,"Segoe UI",Inter,sans-serif;font-size:12px;fill:#8B90A0}
#scroll{transform:translateY(${-posterTop * LH}px)}
.l{opacity:1}
@media (prefers-reduced-motion:no-preference){
#scroll{animation:scroll ${total}ms steps(1,end) infinite}
.l{animation-duration:${total}ms;animation-iteration-count:infinite;animation-timing-function:steps(1,end)}
`;
// Scroll keyframes: like a terminal, the window follows the newest line, but at reading pace (one
// row per TICK ms) so a block that printed at once (the diff) scrolls past instead of jumping to its end.
const TICK = 70;
const frames = ['0%{transform:translateY(0px)}'];
let pos = 0;
let shown = 0;
let lastEnd = 0;
for (let t = 0; t <= total; t += TICK) {
  while (shown < lines.length && lines[shown].t <= t) shown++;
  const target = Math.max(0, shown - ROWS);
  if (target > pos) {
    pos += 1;
    frames.push(`${pct(t)}{transform:translateY(${-pos * LH}px)}`);
    lastEnd = t;
  }
}
frames.push(`100%{transform:translateY(${-pos * LH}px)}`);
if (lastEnd + 1500 > total) console.error('○ The scroll ends less than 1.5 s before the loop restarts; raise HOLD.');
css += `@keyframes scroll{${frames.join('')}}\n`;
lines.forEach((l, i) => {
  css += `.l${i}{animation-name:a${i}}@keyframes a${i}{0%{opacity:0}${pct(l.t)}{opacity:1}100%{opacity:1}}\n`;
});
css += '}\n';

const body = lines
  .map((l, i) => `<text class="l l${i} ${cls(l.text)}" x="${PAD}" y="${i * LH + FS}" xml:space="preserve">${esc(l.text)}</text>`)
  .join('\n');
const date = cast.recorded_at.slice(0, 10);
const title = `make demo-hero · a real run · product ${cast.commit} · ${date} · lines at their recorded times · ${(skipped / 1000).toFixed(1)} s of start-up skipped`;
const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" role="img" aria-labelledby="t d">
<title id="t">Recording of make demo-hero (product ${cast.commit}, ${date})</title>
<desc id="d">A real run, replayed line by line at the times it printed. An agent ships order 42: Postgres committed 1 row, Proven, Matches. A legacy trigger is turned on and the same UPDATE runs again: Proven, Doesn't match, Postgres changed 301 rows in public.orders, the agent reported 1. The diff lists the rows, then every record verifies offline. Log sha256 ${cast.log_sha256.slice(0, 12)}.</desc>
<style>${css}</style>
<rect width="${W}" height="${H}" rx="10" fill="#07080C"/>
<rect x="0.5" y="0.5" width="${W - 1}" height="${H - 1}" rx="10" fill="none" stroke="#1C1F2A"/>
<circle cx="18" cy="17" r="5" fill="#2A2E3B"/><circle cx="34" cy="17" r="5" fill="#2A2E3B"/><circle cx="50" cy="17" r="5" fill="#2A2E3B"/>
<text class="bar" x="66" y="21">${esc(title)}</text>
<line x1="0" y1="${BAR}" x2="${W}" y2="${BAR}" stroke="#1C1F2A"/>
<svg x="0" y="${BAR + PAD}" width="${W}" height="${ROWS * LH}" overflow="hidden">
<g id="scroll">
${body}
</g>
</svg>
</svg>
`;
writeFileSync(dst, svg);
console.log(`✓ ${dst} · ${lines.length} lines · ${(total / 1000).toFixed(1)} s loop · ${(svg.length / 1024).toFixed(1)} KiB · product ${cast.commit}`);
