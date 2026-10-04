// Copied from web/verify/src/deed.ts by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:
// edit web/verify/src/deed.ts, then run npm run sync-verify. make sdk-sync-verify fails on drift.
// One deed: the closed schema by kind, the hash and the seal, in exactly the order of Go's
// deed.VerifyEnvelope (DEED_SPEC.md §11). The first failure wins, and every path that is not a
// clean pass is a VerifyError.

import { decodeB64url } from './b64url.js';
import { malformed, schemaField, unsupported, VerifyError } from './errors.js';
import { canonicalize } from './jcs.js';
import type { SealKeySet } from './keys.js';
import { digest, subtle, utf8Bytes, utf8Length } from './sha256.js';
import {
  asObj,
  closed,
  Decoder,
  isID,
  type Obj,
  parseTime,
  plainText,
  reAdminAct,
  reCurrency,
  reDigest,
  reErrorCode,
  reHeaderRef,
  reMethod,
  reRefID,
  reRefType,
  reRuleID,
  reSeal,
} from './schema.js';
import { decodeUTF8, type JsonObject, type JsonValue, malformedFrom, parseStrictValue, StrictError } from './strict.js';

export const MAX_INPUT_BYTES: number = 64 * 1024;
export const MAX_ENVELOPE_BYTES: number = 16 * 1024;
export const TAG_DEED: string = 'deedglass/deed/v1\n';
export const TAG_GENESIS: string = 'deedglass/genesis\n';

export interface Ref {
  type: string;
  id: string;
}

/** Deed v1 body (DEED_SPEC.md §5). */
export interface Body {
  v: number;
  kind: string;
  deed_id: string;
  org: string;
  seq: number;
  prev_hash: string;
  issued_at: string;
  agent: { id: string; name: string } | null;
  on_behalf_of: string | null;
  session: string | null;
  action: {
    id: string;
    tool: string;
    connector: string | null;
    mode: string;
    request_digest: string;
    intent_hash: string | null;
    intent_signed: boolean;
  } | null;
  policy: {
    id: string | null;
    version_id: string | null;
    version_hash: string | null;
    mode: string;
    decision: string;
    rule_id: string | null;
  } | null;
  approval: { id: string; outcome: string; decided_by: string[]; decided_at: string | null; deed: string | null } | null;
  execution: {
    status: string;
    started_at: string;
    finished_at: string;
    latency_ms: number;
    result_digest: string | null;
    error_code: string | null;
  } | null;
  witness: { level: string; verdict: string; method: string; detail_digest: string | null } | null;
  cost: { amount: number; currency: string } | null;
  admin: {
    event: string;
    action: string;
    actor: Ref;
    target: Ref | null;
    reason: string | null;
    data_digest: string | null;
  } | null;
  amends: string | null;
  seal_key_id: string;
}

export interface Envelope {
  object: 'deed';
  id: string;
  body: Body;
  body_hash: string;
  seal: string;
  seal_key_id: string;
}

/** A deed that passed every check. */
export interface DeedResult {
  envelope: Envelope;
  bodyHash: string;
  seq: number;
  deedId: string;
  org: string;
  issuedAt: number;
  sealKeyId: string;
  canonical: boolean;
}

const envelopeKeys = ['object', 'id', 'body', 'body_hash', 'seal', 'seal_key_id'];
const bodyKeys = [
  'v', 'kind', 'deed_id', 'org', 'seq', 'prev_hash', 'issued_at', 'agent', 'on_behalf_of', 'session', 'action', 'policy',
  'approval', 'execution', 'witness', 'cost', 'admin', 'amends', 'seal_key_id',
];
const agentKeys = ['id', 'name'];
const actionKeys = ['id', 'tool', 'connector', 'mode', 'request_digest', 'intent_hash', 'intent_signed'];
const policyKeys = ['id', 'version_id', 'version_hash', 'mode', 'decision', 'rule_id'];
const approvalKeys = ['id', 'outcome', 'decided_by', 'decided_at', 'deed'];
const executionKeys = ['status', 'started_at', 'finished_at', 'latency_ms', 'result_digest', 'error_code'];
const witnessKeys = ['level', 'verdict', 'method', 'detail_digest'];
const costKeys = ['amount', 'currency'];
const adminKeys = ['event', 'action', 'actor', 'target', 'reason', 'data_digest'];
const refKeys = ['type', 'id'];

/** "sha256:" + hex(SHA-256("deedglass/genesis\n" + org)): the prev_hash of an org's first deed. */
export function genesis(org: string): Promise<string> {
  return digest(TAG_GENESIS + org);
}

function isObject(v: JsonValue | undefined): v is JsonObject {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** Copies the deed's seq and id into e when the body has them ("Deed 812" even when unreadable). */
function withRefs(e: VerifyError, body: JsonObject | null): VerifyError {
  if (!body) return e;
  const seq = body['seq'];
  if (typeof seq === 'number' && seq > 0 && e.seq === 0) e.seq = seq;
  const id = body['deed_id'];
  if (typeof id === 'string' && e.deedId === '' && isID('deed', id)) e.deedId = id;
  return e;
}

/** Reads a body from the strict tree: types, closed key sets and nullability (port of bodyFromTree). */
function bodyFromTree(v: JsonValue | undefined): Body {
  let o: Obj;
  try {
    o = asObj(v, '');
  } catch {
    throw schemaField('body', 'must be an object');
  }
  const ver = o.m['v'];
  if (typeof ver === 'number' && ver !== 1) throw unsupported(ver);
  closed(o, bodyKeys);
  const d = new Decoder();
  const b = {
    v: d.i(o, 'v'),
    kind: d.s(o, 'kind'),
    deed_id: d.s(o, 'deed_id'),
    org: d.s(o, 'org'),
    seq: d.i(o, 'seq'),
    prev_hash: d.s(o, 'prev_hash'),
    issued_at: d.s(o, 'issued_at'),
    on_behalf_of: d.ns(o, 'on_behalf_of'),
    session: d.ns(o, 'session'),
    amends: d.ns(o, 'amends'),
    seal_key_id: d.s(o, 'seal_key_id'),
    agent: null,
    action: null,
    policy: null,
    approval: null,
    execution: null,
    witness: null,
    cost: null,
    admin: null,
  } as Body;
  let a = d.obj(o, 'agent', agentKeys);
  if (a) b.agent = { id: d.s(a, 'id'), name: d.s(a, 'name') };
  a = d.obj(o, 'action', actionKeys);
  if (a) {
    b.action = {
      id: d.s(a, 'id'),
      tool: d.s(a, 'tool'),
      connector: d.ns(a, 'connector'),
      mode: d.s(a, 'mode'),
      request_digest: d.s(a, 'request_digest'),
      intent_hash: d.ns(a, 'intent_hash'),
      intent_signed: d.b(a, 'intent_signed'),
    };
  }
  const p = d.obj(o, 'policy', policyKeys);
  if (p) {
    b.policy = {
      id: d.ns(p, 'id'),
      version_id: d.ns(p, 'version_id'),
      version_hash: d.ns(p, 'version_hash'),
      mode: d.s(p, 'mode'),
      decision: d.s(p, 'decision'),
      rule_id: d.ns(p, 'rule_id'),
    };
  }
  a = d.obj(o, 'approval', approvalKeys);
  if (a) {
    b.approval = { id: d.s(a, 'id'), outcome: d.s(a, 'outcome'), decided_at: d.ns(a, 'decided_at'), deed: d.ns(a, 'deed'), decided_by: [] };
    if (d.err === null) b.approval.decided_by = d.strs(a, 'decided_by');
  }
  const e = d.obj(o, 'execution', executionKeys);
  if (e) {
    b.execution = {
      status: d.s(e, 'status'),
      started_at: d.s(e, 'started_at'),
      finished_at: d.s(e, 'finished_at'),
      latency_ms: d.i(e, 'latency_ms'),
      result_digest: d.ns(e, 'result_digest'),
      error_code: d.ns(e, 'error_code'),
    };
  }
  const w = d.obj(o, 'witness', witnessKeys);
  if (w) b.witness = { level: d.s(w, 'level'), verdict: d.s(w, 'verdict'), method: d.s(w, 'method'), detail_digest: d.ns(w, 'detail_digest') };
  const c = d.obj(o, 'cost', costKeys);
  if (c) b.cost = { amount: d.i(c, 'amount'), currency: d.s(c, 'currency') };
  a = d.obj(o, 'admin', adminKeys);
  if (a) {
    const adm = {
      event: d.s(a, 'event'),
      action: d.s(a, 'action'),
      reason: d.ns(a, 'reason'),
      data_digest: d.ns(a, 'data_digest'),
      actor: { type: '', id: '' },
      target: null as Ref | null,
    };
    b.admin = adm;
    if (d.err === null) {
      try {
        const actor = asObj(a.m['actor'], `${a.path}.actor`);
        closed(actor, refKeys);
        adm.actor = { type: d.s(actor, 'type'), id: d.s(actor, 'id') };
      } catch (err) {
        d.err = err as VerifyError;
      }
    }
    const t = d.obj(a, 'target', refKeys);
    if (t) adm.target = { type: d.s(t, 'type'), id: d.s(t, 'id') };
  }
  d.check();
  return b;
}

/** The closed schema by kind: enums, patterns, lengths and which sections must be null (Body.Validate). */
function validate(b: Body): void {
  if (b.v !== 1) throw unsupported(b.v);
  if (b.kind !== 'action' && b.kind !== 'admin' && b.kind !== 'amendment') {
    throw schemaField('kind', 'must be action, admin or amendment');
  }
  if (!isID('deed', b.deed_id)) throw schemaField('deed_id', 'must be a deed_ id');
  if (b.seq < 1 || b.seq > 9007199254740991) throw schemaField('seq', 'must be 1 to 2^53-1');
  if (!reDigest.test(b.prev_hash)) throw schemaField('prev_hash', 'must be sha256:<64 hex>');
  if (parseTime(b.issued_at) === null) throw schemaField('issued_at', 'must be YYYY-MM-DDTHH:MM:SS.mmmZ');
  if (!isID('sk', b.seal_key_id)) throw schemaField('seal_key_id', 'must be an sk_ id');
  if (!isID('org', b.org)) throw schemaField('org', 'must be an org_ id');

  const action = b.kind === 'action';
  const admin = b.kind === 'admin';
  const amend = b.kind === 'amendment';
  if (b.agent === null) {
    if (!admin) throw schemaField('agent', `is required for ${b.kind} deeds`);
  } else {
    if (!isID('agt', b.agent.id)) throw schemaField('agent.id', 'must be an agt_ id');
    if (!plainText(b.agent.name, 1, 64, false)) throw schemaField('agent.name', 'must be 1-64 characters of text');
  }
  for (const [name, v] of [['on_behalf_of', b.on_behalf_of], ['session', b.session]] as const) {
    if (v === null) continue;
    if (admin) throw schemaField(name, 'must be null in admin deeds');
    if (!reHeaderRef.test(v)) throw schemaField(name, 'must be 1-256 characters of A-Z a-z 0-9 . _ ~ : @ / + = -');
  }
  if (b.action === null) {
    if (!admin) throw schemaField('action', `is required for ${b.kind} deeds`);
  } else {
    if (admin) throw schemaField('action', 'must be null in admin deeds');
    const a = b.action;
    if (!isID('act', a.id)) throw schemaField('action.id', 'must be an act_ id');
    const toolBytes = utf8Length(a.tool);
    if (toolBytes === 0 || toolBytes > 200 || !plainText(a.tool, 1, 200, false)) throw schemaField('action.tool', 'must be 1-200 bytes of text');
    if (a.connector !== null && !isID('con', a.connector)) throw schemaField('action.connector', 'must be a con_ id or null');
    if (a.mode !== 'gateway' && a.mode !== 'proxy') throw schemaField('action.mode', 'must be gateway or proxy');
    if (!reDigest.test(a.request_digest)) throw schemaField('action.request_digest', 'must be sha256:<64 hex>');
    if (a.intent_hash !== null && !reDigest.test(a.intent_hash)) throw schemaField('action.intent_hash', 'must be sha256:<64 hex> or null');
    if (a.intent_signed && a.intent_hash === null) throw schemaField('action.intent_hash', 'is required when intent_signed is true');
  }
  if (b.policy === null && action) throw schemaField('policy', 'is required for action deeds');
  if (b.policy !== null && !action) throw schemaField('policy', `must be null in ${b.kind} deeds`);
  if (b.policy !== null) {
    const p = b.policy;
    if (p.id !== null && !isID('pol', p.id)) throw schemaField('policy.id', 'must be a pol_ id or null');
    if (p.version_id !== null && !isID('pv', p.version_id)) throw schemaField('policy.version_id', 'must be a pv_ id or null');
    if (p.version_hash !== null && !reDigest.test(p.version_hash)) throw schemaField('policy.version_hash', 'must be sha256:<64 hex> or null');
    if (p.version_id !== null && p.version_hash === null) throw schemaField('policy.version_hash', 'is required when version_id is set');
    if (p.mode !== 'enforce' && p.mode !== 'shadow') throw schemaField('policy.mode', 'must be enforce or shadow');
    if (!['allow', 'deny', 'require_approval'].includes(p.decision)) throw schemaField('policy.decision', 'must be allow, deny or require_approval');
    if (p.rule_id !== null && !reRuleID.test(p.rule_id)) throw schemaField('policy.rule_id', 'must be 1-128 characters of A-Z a-z 0-9 . _ : / -');
  }
  if (b.approval !== null) {
    if (!action) throw schemaField('approval', `must be null in ${b.kind} deeds`);
    const a = b.approval;
    if (!isID('apr', a.id)) throw schemaField('approval.id', 'must be an apr_ id');
    if (!['approved', 'denied', 'expired', 'cancelled'].includes(a.outcome)) {
      throw schemaField('approval.outcome', 'must be approved, denied, expired or cancelled');
    }
    for (let i = 0; i < a.decided_by.length; i++) {
      const u = a.decided_by[i]!;
      if (!isID('usr', u)) throw schemaField(`approval.decided_by[${i}]`, 'must be a usr_ id');
      if (i > 0 && a.decided_by[i - 1]! >= u) throw schemaField(`approval.decided_by[${i}]`, 'must be sorted with no duplicates');
    }
    if ((a.outcome === 'approved' || a.outcome === 'denied') && a.decided_by.length === 0) {
      throw schemaField('approval.decided_by', 'must name who decided');
    }
    if (a.outcome === 'expired' && a.decided_by.length !== 0) throw schemaField('approval.decided_by', 'must be empty when the approval expired');
    if (a.decided_at !== null && parseTime(a.decided_at) === null) throw schemaField('approval.decided_at', 'must be YYYY-MM-DDTHH:MM:SS.mmmZ or null');
    if (a.deed !== null && !isID('deed', a.deed)) throw schemaField('approval.deed', 'must be a deed_ id or null');
  }
  if (b.execution !== null) {
    if (!action) throw schemaField('execution', `must be null in ${b.kind} deeds`);
    const e = b.execution;
    if (!['succeeded', 'failed', 'unknown'].includes(e.status)) throw schemaField('execution.status', 'must be succeeded, failed or unknown');
    const start = parseTime(e.started_at);
    if (start === null) throw schemaField('execution.started_at', 'must be YYYY-MM-DDTHH:MM:SS.mmmZ');
    const end = parseTime(e.finished_at);
    if (end === null) throw schemaField('execution.finished_at', 'must be YYYY-MM-DDTHH:MM:SS.mmmZ');
    if (end < start) throw schemaField('execution.finished_at', 'must not be before started_at');
    if (e.latency_ms < 0 || e.latency_ms > 9007199254740991) throw schemaField('execution.latency_ms', 'must be 0 to 2^53-1');
    if (e.result_digest !== null && !reDigest.test(e.result_digest)) throw schemaField('execution.result_digest', 'must be sha256:<64 hex> or null');
    if (e.error_code !== null && !reErrorCode.test(e.error_code)) {
      throw schemaField('execution.error_code', 'must be a snake_case code of at most 64 characters, or null');
    }
  }
  if (b.witness === null && amend) throw schemaField('witness', 'is required for amendment deeds');
  if (b.witness !== null && admin) throw schemaField('witness', 'must be null in admin deeds');
  if (b.witness !== null) {
    const w = b.witness;
    if (!['L0', 'L1', 'L2'].includes(w.level)) throw schemaField('witness.level', 'must be L0, L1 or L2');
    if (!['VERIFIED', 'MISMATCH', 'UNVERIFIABLE'].includes(w.verdict)) throw schemaField('witness.verdict', 'must be VERIFIED, MISMATCH or UNVERIFIABLE');
    if (!reMethod.test(w.method)) throw schemaField('witness.method', 'must be 1-64 characters of a-z 0-9 . _ -');
    if (w.detail_digest !== null && !reDigest.test(w.detail_digest)) throw schemaField('witness.detail_digest', 'must be sha256:<64 hex> or null');
  }
  if (b.cost !== null) {
    if (!action) throw schemaField('cost', `must be null in ${b.kind} deeds`);
    if (b.cost.amount < 0 || b.cost.amount > 9007199254740991) throw schemaField('cost.amount', 'must be 0 to 2^53-1 minor units');
    if (!reCurrency.test(b.cost.currency)) throw schemaField('cost.currency', 'must be a lowercase ISO 4217 code');
  }
  if (b.admin === null && admin) throw schemaField('admin', 'is required for admin deeds');
  if (b.admin !== null && !admin) throw schemaField('admin', `must be null in ${b.kind} deeds`);
  if (b.admin !== null) {
    const a = b.admin;
    if (!isID('adm', a.event)) throw schemaField('admin.event', 'must be an adm_ id');
    if (!reAdminAct.test(a.action)) throw schemaField('admin.action', 'must be <object>.<past verb>, for example agent.stopped');
    validateRef(a.actor, 'admin.actor');
    if (a.target !== null) validateRef(a.target, 'admin.target');
    if (a.reason !== null && !plainText(a.reason, 1, 280, true)) throw schemaField('admin.reason', 'must be 1-280 characters of text or null');
    if (a.data_digest !== null && !reDigest.test(a.data_digest)) throw schemaField('admin.data_digest', 'must be sha256:<64 hex> or null');
  }
  if (b.amends === null && amend) throw schemaField('amends', 'is required for amendment deeds');
  if (b.amends !== null && !amend) throw schemaField('amends', `must be null in ${b.kind} deeds`);
  if (b.amends !== null && !isID('deed', b.amends)) throw schemaField('amends', 'must be a deed_ id');
}

function validateRef(r: Ref, path: string): void {
  if (!reRefType.test(r.type)) throw schemaField(`${path}.type`, 'must match ^[a-z][a-z_]{0,31}$');
  if (!reRefID.test(r.id)) throw schemaField(`${path}.id`, 'must be an id or a lowercase name of at most 64 characters');
}

interface Parsed {
  env: Envelope;
  bodyJCS: string;
  envJCS: string;
}

/** Reads an envelope in the verifier's order (port of parseEnvelope). Throws VerifyError. */
export function parseEnvelope(raw: string | Uint8Array): Parsed {
  const size = typeof raw === 'string' ? utf8Length(raw) : raw.length;
  if (size > MAX_INPUT_BYTES) throw malformed(`larger than ${MAX_INPUT_BYTES >> 10} KiB`);
  let tree: JsonValue;
  try {
    tree = parseStrictValue(typeof raw === 'string' ? raw : decodeUTF8(raw), true);
  } catch (e) {
    if (e instanceof StrictError) throw malformedFrom(e);
    throw e;
  }
  if (!isObject(tree)) throw malformed('a deed must be a JSON object');
  const top = tree;
  if (top['object'] !== 'deed') throw schemaField('object', 'must be "deed"');
  const bodyTree = isObject(top['body']) ? top['body'] : null;
  const id = top['id'];
  if (typeof id === 'string' && bodyTree) {
    const did = bodyTree['deed_id'];
    if (typeof did === 'string' && did !== id) {
      throw withRefs(new VerifyError('id_mismatch', { detail: `envelope id ${id}, body deed_id ${did}` }), bodyTree);
    }
  }
  if (bodyTree) {
    const n = bodyTree['v'];
    if (typeof n === 'number' && n !== 1) throw withRefs(unsupported(n), bodyTree);
  }
  const o: Obj = { m: top, path: '' };
  try {
    closed(o, envelopeKeys);
  } catch (e) {
    throw withRefs(e as VerifyError, bodyTree);
  }
  const d = new Decoder();
  const env = { object: 'deed', id: d.s(o, 'id'), body_hash: d.s(o, 'body_hash'), seal: d.s(o, 'seal'), seal_key_id: d.s(o, 'seal_key_id') } as Envelope;
  if (d.err) throw withRefs(d.err, bodyTree);
  let body: Body;
  try {
    body = bodyFromTree(top['body']);
  } catch (e) {
    const ve = e as VerifyError;
    if (ve.code === 'schema_invalid' && ve.field !== '' && ve.field !== 'body') ve.field = 'body.' + ve.field;
    throw withRefs(ve, bodyTree);
  }
  env.body = body;
  try {
    validate(body);
  } catch (e) {
    const ve = e as VerifyError;
    if (ve.code === 'schema_invalid') ve.field = 'body.' + ve.field;
    throw withRefs(ve, bodyTree);
  }
  if (!reDigest.test(env.body_hash)) throw withRefs(schemaField('body_hash', 'must be sha256:<64 hex>'), bodyTree);
  if (!reSeal.test(env.seal)) throw withRefs(schemaField('seal', 'must be ed25519:<86 base64url characters>'), bodyTree);
  if (!isID('sk', env.seal_key_id)) throw withRefs(schemaField('seal_key_id', 'must be an sk_ id'), bodyTree);
  const bodyJCS = canonicalize(top['body']!);
  const envJCS =
    '{"body":' + bodyJCS + ',"body_hash":"' + env.body_hash + '","id":"' + env.id + '","object":"deed","seal":"' + env.seal +
    '","seal_key_id":"' + env.seal_key_id + '"}';
  if (utf8Length(envJCS) > MAX_ENVELOPE_BYTES) {
    throw withRefs(malformed(`its canonical form is larger than ${MAX_ENVELOPE_BYTES >> 10} KiB`), bodyTree);
  }
  return { env, bodyJCS, envJCS };
}

// L, the order of the Ed25519 base point. A seal with s ≥ L is refused before WebCrypto, so every
// engine agrees with Go (RFC 8032 §5.1.7).
const L = 2n ** 252n + 27742317777372353535851937790883648493n;

/** Checks one Ed25519 seal over msg. Throws UnsupportedError when WebCrypto lacks Ed25519. */
export async function openSeal(keys: SealKeySet, keyId: string, msg: string, seal: string): Promise<boolean> {
  if (!reSeal.test(seal)) return false;
  const sig = decodeB64url(seal.slice('ed25519:'.length));
  if (sig === null || sig.length !== 64) return false;
  return ed25519Verify(await keys.cryptoKey(keyId), utf8Bytes(msg), sig);
}

/** Pure Ed25519 (RFC 8032) with s < L checked first, so every engine agrees with Go. */
export async function ed25519Verify(key: CryptoKey, msg: Uint8Array, sig: Uint8Array): Promise<boolean> {
  if (sig.length !== 64) return false;
  let s = 0n;
  for (let i = 63; i >= 32; i--) s = (s << 8n) | BigInt(sig[i]!);
  if (s >= L) return false;
  try {
    return await subtle().verify({ name: 'Ed25519' }, key, sig as BufferSource, msg as BufferSource);
  } catch {
    return false; // an engine that refuses odd input refuses it: never a pass
  }
}

/**
 * Checks one deed against a key set, in Go's order: strict JSON, object, id, version, schema,
 * body_hash, seal key id, key known, not revoked, active at issued_at, Ed25519 seal, and with strict
 * canonical bytes. Throws VerifyError on the first failure.
 */
export async function verifyEnvelope(raw: string | Uint8Array, keys: SealKeySet, opts: { strict?: boolean } = {}): Promise<DeedResult> {
  const p = parseEnvelope(raw);
  const { env } = p;
  const b = env.body;
  const fail = (code: VerifyError['code']) => new VerifyError(code, { seq: b.seq, deedId: b.deed_id, key: env.seal_key_id, issuedAt: b.issued_at });
  if ((await digest(p.bodyJCS)) !== env.body_hash) throw fail('body_hash_mismatch');
  if (env.seal_key_id !== b.seal_key_id) throw fail('seal_key_mismatch');
  if (!keys.lookup(env.seal_key_id)) throw fail('seal_key_unknown');
  const issued = parseTime(b.issued_at)!;
  const c = keys.validAt(env.seal_key_id, issued);
  if (c !== '') throw fail(c);
  if (!(await openSeal(keys, env.seal_key_id, TAG_DEED + env.body_hash, env.seal))) throw fail('seal_invalid');
  const canonical = typeof raw === 'string' ? raw === p.envJCS : equalBytes(raw, utf8Bytes(p.envJCS));
  if (opts.strict && !canonical) throw fail('non_canonical');
  return { envelope: env, bodyHash: env.body_hash, seq: b.seq, deedId: b.deed_id, org: b.org, issuedAt: issued, sealKeyId: env.seal_key_id, canonical };
}

/** The hash of the body as received (for body_hash_mismatch: expected vs computed). */
export async function computedBodyHash(raw: string | Uint8Array): Promise<{ env: Envelope; computed: string } | null> {
  try {
    const p = parseEnvelope(raw);
    return { env: p.env, computed: await digest(p.bodyJCS) };
  } catch {
    return null;
  }
}

function equalBytes(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}
