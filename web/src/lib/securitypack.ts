// securitypack.ts: S51's readiness view (GET /v1/security_pack/readiness) as the Console shows it.
// Pure: the controller renders it, the tests read it. Status words carry the meaning; the chip's
// colour only repeats it.

export type PackStatus = 'in_place' | 'partly' | 'not_set_up' | 'not_applicable' | 'cant_tell';

export interface ReadinessRow {
  id: string;
  group: string;
  group_title: string;
  question: string;
  status: PackStatus;
  fix: string;
}

export interface Readiness {
  object: 'security_pack_readiness';
  org: string;
  org_name: string;
  period: { from: string; to: string };
  summary: Record<PackStatus, number>;
  rows: ReadinessRow[];
}

export const STATUS_ORDER: readonly PackStatus[] = ['in_place', 'partly', 'not_set_up', 'not_applicable', 'cant_tell'];

/** isGap: the statuses that come with a fix. */
export function isGap(s: PackStatus): boolean {
  return s === 'partly' || s === 'not_set_up' || s === 'cant_tell';
}

/** chipClass: the existing state colours (no new token): teal, amber, red, neutral, violet. */
export function chipClass(s: PackStatus): string {
  switch (s) {
    case 'in_place': return 'chip chip-verified';
    case 'partly': return 'chip chip-unverifiable';
    case 'not_set_up': return 'chip chip-mismatch';
    case 'cant_tell': return 'chip chip-pending';
    default: return 'chip chip-neutral';
  }
}

/** summaryParts: "17 in place", "4 partly"… with zero counts left out (in place always shown). */
export function summaryParts(sum: Record<PackStatus, number>, words: Record<PackStatus, string>): string[] {
  return STATUS_ORDER.filter((s) => s === 'in_place' || (sum[s] ?? 0) > 0).map((s) => `${sum[s] ?? 0} ${words[s].toLowerCase()}`);
}

/** groups: rows by group, in the order the API sends them. */
export function groups(rows: ReadinessRow[]): { id: string; title: string; rows: ReadinessRow[] }[] {
  const out: { id: string; title: string; rows: ReadinessRow[] }[] = [];
  for (const r of rows) {
    let g = out.find((x) => x.id === r.group);
    if (!g) {
      g = { id: r.group, title: r.group_title, rows: [] };
      out.push(g);
    }
    g.rows.push(r);
  }
  return out;
}

/** gapsFirst: not set up, partly, can't tell, in the API's order within each. */
export function gaps(rows: ReadinessRow[]): ReadinessRow[] {
  const rank: Record<PackStatus, number> = { not_set_up: 0, partly: 1, cant_tell: 2, in_place: 3, not_applicable: 4 };
  return rows.filter((r) => isGap(r.status)).map((r, i) => ({ r, i })).sort((a, b) => rank[a.r.status] - rank[b.r.status] || a.i - b.i).map((x) => x.r);
}

/** plainText: the readiness list to paste into a ticket or a chat, the same lines the CLI prints. */
export function plainText(rd: Readiness, header: string, words: Record<PackStatus, string>): string {
  const lines = [header, summaryParts(rd.summary, words).join(' · '), ''];
  for (const g of groups(rd.rows)) {
    lines.push(g.title);
    for (const r of g.rows) {
      lines.push(`  ${words[r.status].padEnd(15)}${r.question}`);
      if (r.fix) lines.push(`  ${''.padEnd(15)}${r.fix}`);
    }
    lines.push('');
  }
  return lines.join('\n').trimEnd() + '\n';
}
