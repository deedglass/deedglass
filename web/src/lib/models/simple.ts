// models/simple.ts: the DL-143 Simple view's pure model. It turns the API's actions, approvals and
// deeds into plain sentences (who, what, on which record, result) with the vocabulary in
// DECISION.md §4, and never decides a verdict: results come straight from the server's witness
// verdict, decision and state. Pure: no DOM, no fetch; test/simple.test.ts covers it on real
// captured answers.

import { fill } from '../copy.ts';
import { simpleCopy as c } from '../copy-simple.ts';
import type { Action, DeedBody } from '../types.ts';
import type { ApprovalFull } from './approval.ts';

export type Result = 'checked' | 'problem' | 'waiting' | 'stopped' | 'sentBack' | 'couldnt' | 'done' | 'failed' | 'running' | 'expired' | 'cancelled';
export type Tone = 'verified' | 'mismatch' | 'pending' | 'denied' | 'unverifiable' | 'neutral';

/** TONE maps a result to its chip colour pair (DESIGN.md: verdict and state colours). */
export const TONE: Record<Result, Tone> = {
  checked: 'verified', problem: 'mismatch', waiting: 'pending', stopped: 'denied', sentBack: 'denied',
  couldnt: 'unverifiable', done: 'neutral', failed: 'neutral', running: 'neutral', expired: 'neutral', cancelled: 'neutral',
};

/** ICON is the icon each result carries next to its word (colour never carries meaning alone). */
export const ICON: Record<Result, string> = {
  checked: 'check', problem: 'circle-alert', waiting: 'clock', stopped: 'ban', sentBack: 'ban',
  couldnt: 'circle-dashed', done: 'circle', failed: 'circle-slash', running: 'circle-dashed', expired: 'clock', cancelled: 'circle-slash',
};

type ActionLike = Pick<Action, 'state' | 'status' | 'witness' | 'decision' | 'approval'>;

/** resultOf reads an action's plain result from the server's fields. */
export function resultOf(a: ActionLike): Result {
  const st = a.status;
  if (st === 'pending_approval' || a.state === 'pending_approval') return 'waiting';
  if (st === 'denied') return a.approval && a.approval.status === 'denied' ? 'sentBack' : 'stopped';
  if (st === 'expired') return 'expired';
  if (st === 'cancelled') return 'cancelled';
  if (st === 'failed') return 'failed';
  const v = a.witness?.verdict;
  if (v === 'VERIFIED') return 'checked';
  if (v === 'MISMATCH') return 'problem';
  if (v === 'UNVERIFIABLE') return 'couldnt';
  if (st === 'executed') return a.state === 'sealed' ? 'done' : 'running';
  return 'running';
}

export type Filter = 'all' | 'waiting' | 'problems' | 'stopped';

export function matchesFilter(r: Result, f: Filter): boolean {
  if (f === 'all') return true;
  if (f === 'waiting') return r === 'waiting';
  if (f === 'problems') return r === 'problem' || r === 'couldnt' || r === 'failed';
  return r === 'stopped' || r === 'sentBack';
}

// ---------- the record an action touched ----------

const obj = (v: unknown): Record<string, unknown> | null => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null);
const str = (v: unknown): string => (typeof v === 'string' ? v : typeof v === 'number' ? String(v) : '');

/** plainRef turns a FHIR reference into words: "Patient/0193" → "Patient 0193". */
export function plainRef(ref: string): string {
  const m = /^(?:.*\/)?([A-Z][A-Za-z]+)\/([A-Za-z0-9.\-]{1,64})(?:\/_history\/.*)?$/.exec(ref.trim());
  return m ? `${m[1]} ${m[2]}` : ref.trim();
}

/** SQL_TABLE finds the first table a statement writes or reads. */
const SQL_TABLE = /\b(?:insert\s+into|update|delete\s+from|from|into|truncate(?:\s+table)?)\s+((?:"[^"]+"|[a-z_][a-z0-9_$]*)(?:\.(?:"[^"]+"|[a-z_][a-z0-9_$]*))?)/i;

export function sqlKind(sql: string): 'insert' | 'update' | 'delete' | 'select' | 'other' {
  const m = /^\s*(?:with\b[\s\S]*?\)\s*)?(insert|update|delete|select)\b/i.exec(sql);
  return (m?.[1]?.toLowerCase() as 'insert' | 'update' | 'delete' | 'select' | undefined) ?? 'other';
}

export function sqlTable(sql: string): string | null {
  const m = SQL_TABLE.exec(sql);
  return m ? m[1]!.replace(/"/g, '') : null;
}

export interface Target {
  resource: string | null; // a FHIR resourceType
  record: string | null; // "Patient 0193", a table, or null
  sql: string | null;
}

/** targetOf reads which record a request names (FHIR resources, SQL statements, plain ids). */
export function targetOf(request: unknown): Target {
  const args = obj(request);
  if (!args) return { resource: null, record: null, sql: null };
  const sql = str(args.sql ?? args.query ?? args.statement);
  if (sql) return { resource: null, record: sqlTable(sql), sql };
  const res = obj(args.resource) ?? obj(args.body) ?? (str(args.resourceType) ? args : null);
  const resource = str(res?.resourceType ?? args.resourceType ?? args.type) || null;
  const subject = obj(res?.subject) ?? obj(res?.patient);
  const display = str(subject?.display);
  const ref = str(subject?.reference ?? args.patient ?? args.subject);
  let record: string | null = display || (ref ? plainRef(ref) : null);
  if (!record && resource === 'Patient') {
    const id = str(res?.id ?? args.id);
    if (id) record = `Patient ${id}`;
  }
  if (!record) {
    const id = str(args.id ?? args.reference);
    if (id && resource) record = /\//.test(id) ? plainRef(id) : `${resource} ${id}`;
  }
  return { resource, record, sql: null };
}

// ---------- where a change goes, in words ----------

/** PLACES names a connector type the way a person would (never the raw connector id). */
const PLACES: Record<string, string> = {
  postgres: 'PostgreSQL database', postgresql: 'PostgreSQL database', mysql: 'MySQL database',
  fhir: 'Health record (FHIR)', ehr: 'Health record', emr: 'Health record', scribe: 'Visit notes (scribe)',
  stripe: 'Stripe', github: 'GitHub', slack: 'Slack', gmail: 'Gmail', notion: 'Notion', email: 'Email',
  rest: 'Web service', http: 'Web service', shell: 'Terminal', fake: 'Test connector', filesystem: 'Files',
};

/**
 * placeOf says where a change goes: the connector type's display name when known, else the
 * connector's own name in words ("orders_db" → "Orders db"). Health-sounding names read as a health
 * record. The raw name stays in Technical details.
 */
export function placeOf(type: string | null | undefined, name: string | null | undefined): string {
  const t = (type ?? '').toLowerCase();
  if (t && PLACES[t]) return PLACES[t]!;
  const n = (name ?? '').trim();
  const key = n.toLowerCase();
  if (PLACES[key]) return PLACES[key]!;
  if (/(^|[_\-.])(ehr|emr|fhir)([_\-.]|$)/.test(key)) return PLACES.ehr!;
  if (/(^|[_\-.])(pg|postgres|postgresql)([_\-.]|$)/.test(key)) return PLACES.postgres!;
  return n ? cap(words(n)) : '';
}

const HEALTH_TYPES = new Set(['fhir', 'ehr', 'emr', 'scribe']);

/** isHealth: the request writes a health record (FHIR/EHR/scribe), so the clinical note applies. */
export function isHealth(a: Pick<ApprovalFull, 'tool' | 'context'>): boolean {
  const ctx = a.context;
  const type = (ctx?.action?.connector_type ?? '').toLowerCase();
  if (HEALTH_TYPES.has(type)) return true;
  if (type) return false; // a known non-health type (postgres, stripe…) never gets the clinical line
  const name = (ctx?.action?.connector ?? a.tool.split('.')[0] ?? '').toLowerCase();
  if (/(^|[_\-.])(fhir|ehr|emr|scribe)([_\-.]|$)/.test(name)) return true;
  return targetOf(ctx?.args ?? null).resource !== null;
}

// ---------- the sentence ----------

function words(s: string): string {
  return s.replace(/[_-]+/g, ' ').trim();
}

const article = (w: string) => (/^[aeiou]/i.test(w) ? 'an' : 'a');

/** opOf splits a tool into its verb and the rest: "ehr.update_observation" → update, observation. */
export function opOf(tool: string): { verb: string; rest: string } {
  const op = tool.includes('.') ? tool.slice(tool.indexOf('.') + 1) : tool;
  const [verb = '', ...rest] = op.split(/[_.]/);
  return { verb: verb.toLowerCase(), rest: rest.join(' ') };
}

export interface Plain {
  /** the sentence in the result's tense: "Added a reading for Patient 0193" */
  text: string;
  record: string | null;
  result: Result;
}

/** plainAction writes what an action did, in plain words. The request is optional (expand[]=request). */
export function plainAction(a: ActionLike & Pick<Action, 'tool'>, request?: unknown): Plain {
  const result = resultOf(a);
  const t = targetOf(request);
  let past: string;
  let base: string;
  if (t.sql) {
    const pair = c.feed.sql[sqlKind(t.sql)] ?? c.feed.sql.other!;
    const table = t.record ?? c.feed.aTable;
    past = fill(pair[0], { table });
    base = fill(pair[1], { table });
  } else {
    const { verb, rest } = opOf(a.tool);
    const thing = (t.resource && c.feed.things[t.resource]) || (rest ? `${article(rest)} ${words(rest)}` : '') || (t.resource ? `${article(t.resource)} ${t.resource}` : '');
    const pv = c.feed.verbs[verb];
    const bv = c.feed.base[verb];
    if (pv && bv) {
      past = thing ? `${pv} ${thing}` : `${pv} ${c.feed.something}`;
      base = thing ? `${bv} ${thing}` : `${bv} ${c.feed.something}`;
    } else {
      // An unknown verb: say the tool in words ("create refund" → "Used create refund").
      const w = words(a.tool.includes('.') ? a.tool.slice(a.tool.indexOf('.') + 1) : a.tool);
      past = fill(c.feed.used, { what: w });
      base = fill(c.feed.useBase, { what: w });
    }
    if (t.record) {
      past = fill(c.feed.forRecord, { what: past, target: t.record });
      base = fill(c.feed.forRecord, { what: base, target: t.record });
    } else if (!thing && a.tool.includes('.')) {
      // Nothing more to say about the record: at least say where ("Ran something in postgres").
      const where = placeOf(null, a.tool.slice(0, a.tool.indexOf('.')));
      past = fill(c.feed.inSystem, { what: past, where });
      base = fill(c.feed.inSystem, { what: base, where });
    }
  }
  let text = past;
  if (result === 'waiting') text = fill(c.feed.wants, { what: base });
  else if (result === 'stopped' || result === 'sentBack' || result === 'expired' || result === 'cancelled' || result === 'failed') text = fill(c.feed.tried, { what: base });
  return { text, record: t.record, result };
}

// ---------- what an approval will change ----------

export type ChangeRow = [label: string, value: string];

function codeText(v: unknown): string {
  const o = obj(v);
  if (!o) return str(v);
  const coding = Array.isArray(o.coding) ? (o.coding as unknown[]).map(obj).find((x) => x && (x.display || x.code)) : null;
  return str(o.text) || str(coding?.display) || str(coding?.code);
}

function quantity(v: unknown): string {
  const q = obj(v);
  if (!q) return '';
  const n = str(q.value);
  return n ? [n, str(q.unit ?? q.code)].filter(Boolean).join(' ') : '';
}

/** stripTags turns a FHIR narrative's XHTML into text, for display as a Text node only. */
export function stripTags(x: string): string {
  return x.replace(/<br\s*\/?>/gi, '\n').replace(/<\/(p|div|li|tr)>/gi, '\n').replace(/<[^>]*>/g, '').replace(/&nbsp;/g, ' ').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&amp;/g, '&').replace(/\n{3,}/g, '\n\n').trim();
}

/** decodeText reads a base64 text attachment (text/* only); anything else stays unread. */
export function decodeText(data: string, contentType: string): string | null {
  if (!/^text\//i.test(contentType) || data.length > 200_000) return null;
  try {
    const bin = atob(data);
    const bytes = Uint8Array.from(bin, (ch) => ch.charCodeAt(0));
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    return null;
  }
}

export interface Change {
  rows: ChangeRow[];
  /** a long text the person reads before deciding (a note, an email body) */
  body: string | null;
  bodyMono: boolean;
  /** the exact SQL statement, for Technical details (the plain rows above say what it does) */
  statement?: string | null;
}

/** splitList splits a SQL list on top-level commas, keeping quoted strings and brackets whole. */
export function splitList(s: string): string[] {
  const out: string[] = [];
  let cur = '';
  let depth = 0;
  let q: string | null = null;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]!;
    if (q) {
      cur += ch;
      if (ch === q) {
        if (s[i + 1] === q) cur += s[++i]!; // '' inside a string
        else q = null;
      }
      continue;
    }
    if (ch === "'" || ch === '"') q = ch;
    else if (ch === '(') depth++;
    else if (ch === ')') depth--;
    else if (ch === ',' && depth === 0) {
      out.push(cur.trim());
      cur = '';
      continue;
    }
    cur += ch;
  }
  if (cur.trim()) out.push(cur.trim());
  return out;
}

/** sqlValue shows a literal as a person reads it: 'Damaged' → Damaged; now() → now; NULL → empty. */
export function sqlValue(v: string): string {
  const t = v.trim();
  const m = /^'((?:[^']|'')*)'(?:::[a-z_ ]+)?$/i.exec(t);
  if (m) return m[1]!.replace(/''/g, "'");
  if (/^null$/i.test(t)) return c.waiting.sqlEmpty;
  if (/^(now|current_timestamp|current_date)(\(\))?$/i.test(t)) return c.waiting.sqlNow;
  return t.replace(/"/g, '');
}

const COL = (x: string) => x.trim().replace(/"/g, '');

/** sqlPairs reads the column → value pairs a single-row INSERT or an UPDATE … SET writes. */
export function sqlPairs(sql: string): ChangeRow[] {
  const ins = /^\s*insert\s+into\s+[^\s(]+\s*\(([^)]*)\)\s*values\s*\(([\s\S]*)\)\s*(?:returning\b[\s\S]*|on\s+conflict\b[\s\S]*)?;?\s*$/i.exec(sql);
  if (ins) {
    const cols = splitList(ins[1]!);
    const vals = splitList(ins[2]!);
    if (cols.length && cols.length === vals.length) return cols.map((col, i) => [COL(col), sqlValue(vals[i]!)] as ChangeRow);
    return [];
  }
  const up = /^\s*update\s+\S+\s+set\s+([\s\S]*?)(?:\s+where\s+[\s\S]*|\s+returning\s+[\s\S]*)?;?\s*$/i.exec(sql);
  if (up) {
    const pairs: ChangeRow[] = [];
    for (const part of splitList(up[1]!)) {
      const eq = part.indexOf('=');
      if (eq <= 0) return [];
      pairs.push([COL(part.slice(0, eq)), sqlValue(part.slice(eq + 1))]);
    }
    return pairs;
  }
  return [];
}

/** changeOf lists exactly what an approval will write, from S22's server-computed context. */
export function changeOf(a: Pick<ApprovalFull, 'context' | 'tool'>): Change {
  const f = c.waiting.fields;
  const ctx = a.context;
  const args = obj(ctx?.args) ?? {};
  const rows: ChangeRow[] = [];
  const email = obj(ctx?.sections?.email_preview);
  if (email) {
    if (str(email.to)) rows.push([f.to!, str(email.to)]);
    if (str(email.subject)) rows.push([f.subject_line!, str(email.subject)]);
    return { rows, body: str(email.body ?? email.text) || null, bodyMono: false };
  }
  const stripe = obj(ctx?.sections?.stripe);
  if (stripe && str(stripe.amount_display)) {
    rows.push([f.amount!, str(stripe.amount_display)]);
    if (str(stripe.customer)) rows.push([f.to!, str(stripe.customer)]);
    return { rows, body: null, bodyMono: false };
  }
  const sql = str(args.sql ?? args.query ?? args.statement);
  if (sql) {
    const tables = ctx?.sql?.tables?.length ? ctx.sql.tables.join(', ') : sqlTable(sql);
    if (tables) rows.push([f.table!, tables]);
    const kind = sqlKind(sql);
    const what = c.waiting.sqlKinds[kind];
    if (what) rows.push([f.change!, what]);
    const est = ctx?.sql?.estimate?.rows;
    if (typeof est === 'number') rows.push([f.rows!, fill(c.waiting.aboutRows, { n: est })]);
    for (const pair of sqlPairs(sql).slice(0, 12)) rows.push([cap(words(pair[0])), pair[1]]);
    return { rows, body: null, bodyMono: false, statement: sql };
  }
  const res = obj(args.resource) ?? obj(args.body) ?? (str(args.resourceType) ? args : null);
  if (res) {
    const t = targetOf(args);
    const type = str(res.resourceType);
    if (type) rows.push([f.resourceType!, c.feed.things[type] ? `${cap(c.feed.things[type]!.replace(/^(a|an) /, ''))} (${type})` : type]);
    if (t.record) rows.push([f.patient!, t.record]);
    const what = codeText(res.code) || codeText(res.type) || str(res.description);
    if (what) rows.push([f.code!, what]);
    const val = quantity(res.valueQuantity) || str(res.valueString) || codeText(res.valueCodeableConcept);
    if (val) rows.push([f.value!, val]);
    const when = str(res.effectiveDateTime ?? res.date ?? res.authoredOn);
    if (when) rows.push([f.effective!, when]);
    if (str(res.status)) rows.push([f.status!, str(res.status)]);
    let body: string | null = null;
    const content = Array.isArray(res.content) ? obj((res.content as unknown[])[0]) : null;
    const att = obj(content?.attachment);
    if (att && str(att.data)) body = decodeText(str(att.data), str(att.contentType) || 'text/plain');
    if (!body && Array.isArray(res.section)) {
      body = (res.section as unknown[]).map(obj).filter(Boolean).map((s) => [str(s!.title), stripTags(str(obj(s!.text)?.div))].filter(Boolean).join('\n')).join('\n\n') || null;
    }
    if (!body && obj(res.text) && str(obj(res.text)!.div)) body = stripTags(str(obj(res.text)!.div));
    if (!body && Array.isArray(res.note)) body = (res.note as unknown[]).map((n) => str(obj(n)?.text)).filter(Boolean).join('\n') || null;
    return { rows, body, bodyMono: false };
  }
  // Anything else: the arguments' top-level plain values, as data.
  for (const [k, v] of Object.entries(args).slice(0, 12)) {
    if (typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean') rows.push([cap(words(k)), String(v)]);
  }
  return { rows, body: null, bodyMono: false };
}

function cap(s: string): string {
  return s ? s[0]!.toUpperCase() + s.slice(1) : s;
}

/** headlineOf is the approval in one sentence: who wants to do what, on which record. */
export function headlineOf(a: Pick<ApprovalFull, 'tool' | 'context' | 'agent'>): string {
  const p = plainAction({ tool: a.tool, state: 'pending_approval', status: 'pending_approval', witness: null, decision: null, approval: null }, a.context?.args ?? null);
  return `${a.agent.name}: ${p.text.replace(/^Wants/, 'wants')}`;
}

// ---------- locked records ----------

export interface PlainRecord {
  title: string;
  who: string;
  approval: string;
  check: string;
  level: string | null;
}

/** plainRecord writes a deed in plain words. The deed holds digests, not arguments, so the title says the tool. */
export function plainRecord(b: DeedBody): PlainRecord {
  const r = c.records;
  if (b.kind === 'admin' && b.admin) {
    const who = b.admin.actor.name ?? b.admin.actor.type;
    return { title: cap(words(b.admin.action.replace(/\./g, ' '))), who, approval: r.approvalNone, check: r.kind.admin!, level: null };
  }
  const tool = b.action?.tool ?? '';
  const status = b.execution?.status ?? '';
  const decision = b.policy?.decision ?? '';
  const fake: ActionLike & { tool: string } = {
    tool,
    state: 'sealed',
    status: decision === 'deny' ? 'denied' : status || 'executed',
    witness: b.witness ? { object: 'witness', level: b.witness.level, verdict: b.witness.verdict, method: b.witness.method, required_level: null, detail_digest: b.witness.detail_digest } : null,
    decision: null,
    approval: null,
  };
  const ap = obj(b.approval);
  const approvers = Array.isArray(ap?.countersigned) ? (ap!.countersigned as unknown[]).map((x) => str(obj(obj(x)?.user)?.name ?? obj(x)?.name)).filter(Boolean) : [];
  const apStatus = str(ap?.status);
  let approval = r.approvalNone;
  if (decision === 'deny') approval = r.approvalStopped;
  else if (apStatus === 'denied') {
    const by = str(obj(ap?.denied_by)?.name);
    approval = by ? fill(r.approvalDeclined, { who: by }) : r.approvalDeclinedAnon;
    fake.status = 'denied';
    fake.approval = { id: '', status: 'denied', expires_at: '' };
  } else if (approvers.length) approval = fill(r.approvalBy, { who: approvers.join(', ') });
  const p = plainAction(fake);
  const check = decision === 'deny' || fake.status === 'denied' ? r.nothingChanged : r.checkResult[b.witness?.verdict ?? 'none'] ?? r.checkResult.none!;
  if (b.kind === 'amendment') return { title: r.kind.amendment!, who: b.agent?.name ?? '', approval, check, level: b.witness ? r.levels[b.witness.level] ?? null : null };
  return { title: p.text, who: b.agent?.name ?? b.agent?.id ?? '', approval, check, level: b.witness ? r.levels[b.witness.level] ?? null : null };
}
