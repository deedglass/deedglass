// The verification_report (the same JSON field names as Go's deed.Report) and the Verifier that
// builds it: deeds checked in batches (WebCrypto runs in parallel), the chain in order.

import { ChainChecker, type Head } from './chain.js';
import { computedBodyHash, type DeedResult, type Envelope, genesis, MAX_INPUT_BYTES, verifyEnvelope } from './deed.js';
import { UnsupportedError, VerifyError } from './errors.js';
import { TEST_KEY_THUMBPRINTS } from './keys.gen.js';
import { SealKeySet } from './keys.js';
import { MESSAGES, type VerifyCode } from './messages.gen.js';
import { parseTime } from './schema.js';
import { utf8Length } from './sha256.js';
import { decodeUTF8 } from './strict.js';

export const MAX_FAILURES = 100;

export interface Failure {
  code: VerifyCode;
  seq: number | null;
  deed: string | null;
  input: string | null;
  line: number | null;
  field: string | null;
  expected: string | null;
  computed: string | null;
  message: string;
  next: string;
}

export interface Warning {
  code: string;
  message: string;
  next: string;
}

export interface KeyUse {
  id: string;
  thumbprint: string;
  deeds: number;
  first_seq: number;
  last_seq: number;
  source: string;
}

export interface Report {
  object: 'verification_report';
  result: 'valid' | 'invalid' | 'cannot_determine';
  deeds: number;
  intact: number;
  org: string | null;
  first_seq: number | null;
  last_seq: number | null;
  from: string | null;
  to: string | null;
  chain: { status: 'intact' | 'broken' | 'not_checked'; anchor: { kind: 'genesis' | 'deed' | 'none'; seq: number | null; body_hash: string | null } };
  seal_keys: KeyUse[];
  keys_pinned: boolean;
  kinds: Record<string, number>;
  verdicts_recorded: Record<string, number>;
  bundle: null;
  failures: Failure[];
  warnings: Warning[];
  duration_ms: number;
  verifier: string;
}

export const VERIFIER = 'deedglass-verify-web 0.1.0';

/** Codes that mean "cannot decide" (exit 2 in the CLI), not "failed". */
export function cannotDetermine(code: string): boolean {
  return code === 'keys_missing' || code === 'unsupported_version';
}

// --- copy (rendered from the templates Go generates) ---

function messageKey(e: VerifyError): string {
  if (e.code === 'chain_seq_gap' && e.prev === 0 && e.want === 1) return 'chain_seq_gap.start';
  return MESSAGES[e.code] ? e.code : 'unknown';
}

function messageVars(e: VerifyError): Record<string, string> {
  const ref = e.seq > 0 ? `Deed ${e.seq}` : 'The deed';
  const dash = (s: string) => (s === '' ? '—' : s);
  let detail = e.detail;
  if (detail === '') detail = e.code === 'bundle_invalid' ? 'This file is not a valid deed bundle.' : 'unreadable';
  return {
    Deed: ref,
    deed: ref[0]!.toLowerCase() + ref.slice(1),
    seq: String(e.seq),
    prev: String(e.prev),
    want: String(e.want),
    prev_anchor: String(Math.max(e.seq - 1, 0)),
    v: String(e.version),
    field: dash(e.field),
    key: dash(e.key),
    issued_at: dash(e.issuedAt),
    org: dash(e.org),
    want_org: dash(e.wantOrg),
    detail,
    code: e.code,
    keys_url: '<issuer>/.well-known/deedglass/keys.json',
  };
}

/** Replaces every {name} with vars[name]; unknown names stay as they are (Go's RenderMessage). */
export function renderMessage(tmpl: string, vars: Record<string, string>): string {
  return tmpl.replace(/\{([^{}]*)\}/g, (m, name: string) => (Object.prototype.hasOwnProperty.call(vars, name) ? vars[name]! : m));
}

/** The one sentence and the one next step a person sees for e: exactly what the CLI prints. */
export function message(e: VerifyError): { sentence: string; next: string } {
  const t = MESSAGES[messageKey(e)]!;
  const v = messageVars(e);
  return { sentence: renderMessage(t.sentence, v), next: renderMessage(t.next, v) };
}

export function warningMessage(code: string, vars: Record<string, string> = {}): { sentence: string; next: string } {
  const t = MESSAGES['warning.' + code]!;
  return { sentence: renderMessage(t.sentence, vars), next: renderMessage(t.next, vars) };
}

export function failureFrom(e: VerifyError): Failure {
  const { sentence, next } = message(e);
  return {
    code: e.code,
    seq: e.seq > 0 ? e.seq : null,
    deed: e.deedId !== '' ? e.deedId : null,
    input: null,
    line: null,
    field: e.field !== '' ? e.field : null,
    expected: null,
    computed: null,
    message: sentence,
    next,
  };
}

// --- the verifier ---

export interface VerifyOptions {
  org?: string;
  anchor?: { seq: number; bodyHash: string };
  strict?: boolean;
  all?: boolean;
  keysPinned?: boolean;
  keySource?: string;
  onProgress?: (n: number) => void;
}

type Line = string | Uint8Array;

interface Out {
  res?: DeedResult;
  err?: VerifyError;
  env?: Envelope;
  computed?: string;
}

const BATCH = 64;

export class Verifier {
  private rep: Report;
  private started = Date.now();
  private mode: '' | 'chain' | 'loose' = '';
  private cc: ChainChecker | null = null;
  private resync = false;
  private stopped = false;
  private firstOrg = '';
  private orgs = new Set<string>();
  private tally = new Map<string, KeyUse>();
  private empty = new SealKeySet();

  constructor(
    private keys: SealKeySet | null,
    private opts: VerifyOptions = {},
  ) {
    this.rep = {
      object: 'verification_report', result: 'valid', deeds: 0, intact: 0, org: null, first_seq: null, last_seq: null,
      from: null, to: null, chain: { status: 'not_checked', anchor: { kind: 'none', seq: null, body_hash: null } },
      seal_keys: [], keys_pinned: opts.keysPinned ?? false, kinds: {}, verdicts_recorded: {}, bundle: null,
      failures: [], warnings: [], duration_ms: 0, verifier: VERIFIER,
    };
  }

  get isStopped(): boolean {
    return this.stopped;
  }

  /** Verifies lines as one chain (JSONL), continuing earlier calls. */
  async chain(lines: Iterable<Line> | AsyncIterable<Line>, input = '-'): Promise<void> {
    if (this.mode === '') this.mode = 'chain';
    let n = 0;
    let batch: { line: Line; no: number }[] = [];
    const flush = async () => {
      const outs = await Promise.all(batch.map((b) => this.check(b.line, b.no)));
      for (let i = 0; i < outs.length && !this.stopped; i++) await this.consume(outs[i]!, input, batch[i]!.no);
      batch = [];
      this.opts.onProgress?.(this.rep.deeds);
    };
    for await (const line of lines) {
      if (this.stopped) break;
      batch.push({ line, no: ++n });
      if (batch.length === BATCH) await flush();
    }
    if (batch.length > 0 && !this.stopped) await flush();
  }

  /** Verifies one deed on its own: its seal, and the genesis link when its seq is 1. */
  async loose(raw: Line, input = '-'): Promise<void> {
    if (this.stopped) return;
    if (this.mode === '') this.mode = 'loose';
    await this.consume(await this.check(raw, 1, true), input, 1);
  }

  private async check(raw: Line, no: number, loose = false): Promise<Out> {
    const size = typeof raw === 'string' ? utf8Length(raw) : raw.length;
    if (!loose && size > MAX_INPUT_BYTES) return { err: new VerifyError('malformed', { detail: `line ${no} is longer than ${MAX_INPUT_BYTES >> 10} KiB` }) };
    if (size === 0) return { err: new VerifyError('malformed', { detail: `line ${no} is blank` }) };
    const keys = this.keys ?? this.empty;
    try {
      return { res: await verifyEnvelope(raw, keys, { strict: this.opts.strict ?? false }) };
    } catch (e) {
      if (e instanceof UnsupportedError) throw e;
      if (!(e instanceof VerifyError)) throw e;
      let err = e;
      if (this.keys === null && err.code === 'seal_key_unknown') {
        err = new VerifyError('keys_missing', { seq: err.seq, deedId: err.deedId, key: err.key });
      }
      const out: Out = { err };
      if (err.code !== 'malformed') {
        const c = await computedBodyHash(raw);
        if (c) {
          out.env = c.env;
          if (err.code === 'body_hash_mismatch') out.computed = c.computed;
        }
      }
      return out;
    }
  }

  fail(f: Failure): void {
    if (this.rep.failures.length < MAX_FAILURES) this.rep.failures.push(f);
    if (!this.opts.all || cannotDetermine(f.code)) this.stopped = true;
  }

  private async consume(o: Out, input: string, line: number): Promise<void> {
    this.rep.deeds++;
    const at = (f: Failure): Failure => ({ ...f, input, line });
    if (o.err) {
      const f = at(failureFrom(o.err));
      if (o.computed && o.env) {
        f.expected = o.env.body_hash;
        f.computed = o.computed;
      }
      this.fail(f);
      if (this.mode === 'loose') return;
      if (o.env) this.checker().continueFrom(resultOf(o.env));
      else this.resync = true;
      return;
    }
    const r = o.res!;
    if (this.mode === 'loose') {
      let ce: VerifyError | null = null;
      if (this.opts.org && r.org !== this.opts.org) {
        ce = new VerifyError('chain_org_mismatch', { seq: r.seq, deedId: r.deedId, org: r.org, wantOrg: this.opts.org });
      } else if (r.seq === 1 && r.envelope.body.prev_hash !== (await genesis(r.org))) {
        ce = new VerifyError('chain_genesis_mismatch', { seq: r.seq, deedId: r.deedId });
      }
      if (ce) {
        this.fail(at(failureFrom(ce)));
        return;
      }
    } else {
      const cc = this.checker();
      if (this.resync) {
        cc.restart(r);
        this.resync = false;
      }
      try {
        await cc.next(r);
      } catch (e) {
        this.fail(at(failureFrom(e as VerifyError)));
        cc.continueFrom(r);
        return;
      }
    }
    this.intact(r);
  }

  private checker(): ChainChecker {
    if (!this.cc) {
      const a = this.opts.anchor;
      const anchor: Head | null = a ? { seq: a.seq, bodyHash: a.bodyHash } : null;
      this.cc = new ChainChecker({ org: this.opts.org ?? '', anchor });
    }
    return this.cc;
  }

  private intact(r: DeedResult): void {
    const rep = this.rep;
    rep.intact++;
    if (this.firstOrg === '') this.firstOrg = r.org;
    this.orgs.add(r.org);
    if (rep.first_seq === null || r.seq < rep.first_seq) rep.first_seq = r.seq;
    if (rep.last_seq === null || r.seq > rep.last_seq) rep.last_seq = r.seq;
    const t = r.envelope.body.issued_at;
    if (rep.from === null || t < rep.from) rep.from = t;
    if (rep.to === null || t > rep.to) rep.to = t;
    const b = r.envelope.body;
    rep.kinds[b.kind] = (rep.kinds[b.kind] ?? 0) + 1;
    const verdict = b.witness ? b.witness.verdict : 'none';
    rep.verdicts_recorded[verdict] = (rep.verdicts_recorded[verdict] ?? 0) + 1;
    let k = this.tally.get(r.sealKeyId);
    if (!k) {
      k = { id: r.sealKeyId, thumbprint: this.keys?.lookup(r.sealKeyId)?.key.thumbprint ?? '', deeds: 0, first_seq: r.seq, last_seq: r.seq, source: this.opts.keySource ?? '' };
      this.tally.set(r.sealKeyId, k);
    }
    k.deeds++;
    k.first_seq = Math.min(k.first_seq, r.seq);
    k.last_seq = Math.max(k.last_seq, r.seq);
  }

  /** Finishes the verification and returns the report. */
  async report(): Promise<Report> {
    const rep = this.rep;
    rep.seal_keys = [...this.tally.values()].sort((a, b) => a.first_seq - b.first_seq);
    if (this.orgs.size === 1) rep.org = this.firstOrg;
    else if (this.orgs.size === 0 && this.opts.org) rep.org = this.opts.org;
    if (this.mode === 'chain') {
      rep.chain.status = 'intact';
      if (this.opts.anchor) rep.chain.anchor = { kind: 'deed', seq: this.opts.anchor.seq, body_hash: this.opts.anchor.bodyHash };
      else rep.chain.anchor = { kind: 'genesis', seq: 0, body_hash: rep.org !== null ? await genesis(rep.org) : null };
    }
    rep.failures.sort((a, b) => (a.input === b.input ? (a.line ?? 0) - (b.line ?? 0) : 0));
    rep.result = 'valid';
    for (const f of rep.failures) {
      if (cannotDetermine(f.code)) {
        if (rep.result === 'valid') rep.result = 'cannot_determine';
      } else rep.result = 'invalid';
    }
    if (rep.result === 'invalid' && rep.chain.status === 'intact') rep.chain.status = 'broken';
    if (rep.result === 'cannot_determine' && rep.chain.status === 'intact') rep.chain.status = 'not_checked';
    if (rep.deeds === 0 && rep.failures.length === 0) rep.chain.status = 'not_checked';
    if (rep.seal_keys.some((k) => TEST_KEY_THUMBPRINTS.includes(k.thumbprint)) && !rep.warnings.some((w) => w.code === 'test_keys')) {
      const { sentence, next } = warningMessage('test_keys');
      rep.warnings.push({ code: 'test_keys', message: sentence, next });
    }
    rep.duration_ms = Date.now() - this.started;
    return rep;
  }
}

function resultOf(env: Envelope): DeedResult {
  return {
    envelope: env, bodyHash: env.body_hash, seq: env.body.seq, deedId: env.body.deed_id, org: env.body.org,
    issuedAt: parseTime(env.body.issued_at) ?? 0, sealKeyId: env.seal_key_id, canonical: false,
  };
}

/** Splits bytes into lines on '\n' (the last line may lack it; "\r" is kept, as Go keeps it). */
export function splitLines(b: Uint8Array): Uint8Array[] {
  const out: Uint8Array[] = [];
  let start = 0;
  for (let i = 0; i < b.length; i++) {
    if (b[i] === 0x0a) {
      out.push(b.subarray(start, i));
      start = i + 1;
    }
  }
  if (start < b.length) out.push(b.subarray(start));
  return out;
}

/** Splits text into lines the same way. */
export function splitTextLines(s: string): string[] {
  const parts = s.split('\n');
  if (parts[parts.length - 1] === '') parts.pop();
  return parts;
}

/** True when text looks like JSONL: a first line that is a whole object, then another object. */
export function manyLines(s: string): boolean {
  const t = s.trim();
  const i = t.indexOf('\n');
  if (i < 0) return false;
  const first = t.slice(0, i).trim();
  const rest = t.slice(i).trim();
  return first.length > 1 && first.startsWith('{') && first.endsWith('}') && rest.startsWith('{');
}

export { decodeUTF8 };
