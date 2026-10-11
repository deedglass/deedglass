// The kit's S25 section (S25 step 5): MetricTile, VerificationBar, UsageMeter, AttentionList, the
// FilterBar controls, SnippetTabs, DiffTable, JsonView and DeedPanel, from sample data in the Acme
// story. Dev builds only, like the rest of /app/_kit.
import { copy } from '../lib/copy.ts';
import { diffModel } from '../lib/diff.ts';
import { h, icon, maybe } from '../lib/dom.ts';
import { attentionLine, segments } from '../lib/overview.ts';
import type { Overview, WitnessDetail } from '../lib/types.ts';
import { relTime } from '../lib/chips.ts';
import { time } from '../lib/format.ts';
import { attentionList, filterBar, metricTile, segmented, select, snippetTabs, toggles, usageMeter, verificationBar } from './components.ts';
import { deedPanel } from './deedpanel.ts';
import { diffTable } from './difftable.ts';
import { jsonView } from './jsonview.ts';

const now = new Date();
const ago = (m: number) => new Date(now.getTime() - m * 60_000).toISOString();

const sampleL2 = {
  verdict: 'MISMATCH', method: 'postgres.pgoutput', redacted: ['public.customers.email'], rows_shown: 4, rows_more: 297, columns_more: 0, frames: 1,
  expected: { derivation: 'exact_keys', tables: [{ table: 'public.orders', ops: ['update'], rows: 1, keys: [{ id: '42' }], columns: ['status'] }] },
  observed: { total: 301, tables: [{ table: 'public.orders', insert: 0, update: 301, delete: 0 }] },
  rows: [
    { table: 'public.orders', op: 'update', key: { id: '42' }, expected: true, cells: [{ column: 'status', before: 'paid', after: 'shipped' }] },
    { table: 'public.orders', op: 'update', key: { id: '43' }, expected: false, cells: [{ column: 'status', before: 'paid', after: 'shipped' }] },
    { table: 'public.orders', op: 'update', key: { id: '44' }, expected: false, cells: [{ column: 'status', before: 'pending', after: 'shipped' }] },
    { table: 'public.customers', op: 'update', key: { id: '7' }, expected: false, cells: [{ column: 'email', before: '•••', after: '•••' }] },
  ],
};

export function mountKit25(): void {
  const host = maybe('kit-s25');
  if (!host) return;
  const o = { window: '24h', from: ago(1440), verification: { verified: 1021, mismatch: 3, unverifiable: 58, not_witnessed: 122, by_level: { L0: 400, L1: 262, L2: 420 } } } as unknown as Overview;
  const tiles = h('div', { class: 'tiles' },
    metricTile({ id: 'k-t1', label: 'Actions', value: '1,204', sub: '+8% vs previous 24 hours', spark: { values: [31, 28, 44, 52, 38, 61, 47, 40, 35, 30, 28, 33], label: 'Actions per hour, last 12 hours: min 28, max 61' }, href: '/app/actions' }),
    metricTile({ id: 'k-t2', label: 'Blocked', value: '37', sub: '3.1% of actions · policy, budget and Stop', spark: { values: [2, 1, 4, 3, 2, 5, 3, 2, 4, 3, 2, 6], label: 'Blocked actions per hour: min 1, max 6' } }),
    metricTile({ id: 'k-t3', label: 'Waiting for approval', value: '2', sub: '1 expires in 6 min' }),
    h('section', { class: 'tile tile-wide', 'aria-labelledby': 'k-t4-l' }, h('h2', { class: 'tile-label', id: 'k-t4-l' }, 'Verification'), verificationBar(segments(o), 'Verification')));
  const attn = attentionList([
    { ...attentionLine({ object: 'attention_item', kind: 'mismatch', severity: 'high', at: ago(13), ref: null, data: { tool: 'postgres.execute', agent: 'support-bot', summary: 'Postgres changed 301 rows in public.orders. The agent reported 1.' } }, 'Acme', now, 'UTC', time), at: relTime(ago(13)) },
    { ...attentionLine({ object: 'attention_item', kind: 'approval_expiring', severity: 'medium', at: ago(4), ref: null, data: { tool: 'stripe.create_refund', agent: 'billing-bot', expires_at: new Date(now.getTime() + 6 * 60_000).toISOString() } }, 'Acme', now, 'UTC', time), at: relTime(ago(4)) },
    { ...attentionLine({ object: 'attention_item', kind: 'budget_near', severity: 'low', at: ago(2), ref: null, data: { agent: 'support-bot', budget: 'calls_per_day', used: 4210, limit: 5000 } }, 'Acme', now, 'UTC', time), at: relTime(ago(2)) },
  ], copy.overview.attentionNone);
  const meters = h('div', { class: 'card card-pad grid gap-4' },
    usageMeter(212, 5000, 'support-bot, calls today'), usageMeter(4210, 5000, 'billing-bot, calls today'), usageMeter(5000, 5000, 'ops-bot, calls today', '5,000 of 5,000 calls today · new calls are refused until 00:00 Asia/Kolkata'), usageMeter(18, null, 'research-bot, calls today'));
  const bar = filterBar({
    search: h('div', { class: 'filter-search' }, icon('search', 16), h('input', { class: 'input', type: 'search', name: 'kit-q', autocomplete: 'off', 'aria-label': copy.feed.searchLabel, placeholder: `${copy.feed.search}…` })),
    controls: [select('Agent', [{ value: '', label: copy.feed.agentAny }, { value: 'a', label: 'support-bot' }], '', () => {}, 'kit-agent')],
    segments: [
      segmented('Decision', [{ value: '', label: 'All' }, { value: 'allow', label: 'Allowed' }, { value: 'deny', label: 'Denied' }], '', () => {}, 'kit-decision'),
      toggles('Verdict', [{ value: 'VERIFIED', label: copy.chips.verified }, { value: 'MISMATCH', label: copy.chips.mismatch }], ['MISMATCH'], () => {}, 'kit-verdict'),
    ],
    end: [h('button', { type: 'button', class: 'btn btn-secondary btn-sm' }, icon('sliders-horizontal', 16), copy.feed.more)],
  });
  const detail = { levels: { L0: null, L1: null, L2: sampleL2 } } as unknown as WitnessDetail;
  const snip = snippetTabs(() => ({ server: location.origin, apiKey: 'dg_test_REPLACE_ME' }), 'kit-snip');
  const panel = deedPanel({ deed: null, mirror: 'sealed', pending: true });
  host.replaceChildren(
    h('p', { class: 'kit-label' }, 'MetricTile · VerificationBar'), tiles,
    h('div', { class: 'grid gap-4 lg:grid-cols-2' },
      h('div', null, h('p', { class: 'kit-label' }, 'AttentionList'), h('div', { class: 'card' }, attn)),
      h('div', null, h('p', { class: 'kit-label' }, 'UsageMeter: ok, near (80 %), at the limit, no limit'), meters)),
    h('p', { class: 'kit-label' }, 'FilterBar'), bar,
    h('p', { class: 'kit-label' }, 'DiffTable (a sample: 4 of 301 rows, a masked column)'), h('div', { class: 'card card-pad' }, diffTable(diffModel(detail)!)),
    h('div', { class: 'grid gap-4 lg:grid-cols-2' },
      h('div', null, h('p', { class: 'kit-label' }, 'JsonView'), h('div', { class: 'card card-pad' }, jsonView({ sql: 'UPDATE public.orders SET status = $1 WHERE id = $2', params: ['shipped', 42], customer: { email: '•••', tier: 'gold' } }))),
      h('div', null, h('p', { class: 'kit-label' }, 'DeedPanel (no deed yet)'), panel.el)),
    h('p', { class: 'kit-label' }, 'SnippetTabs'), snip.el);
}
