// The Deedglass client (S31 §3.3).

import { canonicalArgs, checkArgs } from './args.js';
import { copy } from './copy.js';
import { AuthenticationError, DeedglassError, InvalidRequestError } from './errors.js';
import { Transport, withMeta } from './http.js';
import { newIdempotencyKey, validIdempotencyKey } from './idempotency.js';
import { type IntentKey, REF, signIntent } from './intent.js';
import { Actions } from './resources/actions.js';
import { Deeds } from './resources/deeds.js';
import { Keys } from './resources/keys.js';
import { Tools } from './resources/tools.js';
import { env, isBrowser } from './runtime.js';
import type { Action, JsonObject, WhoAmI } from './types.js';
import { CLIENT_NAME, VERSION } from './version.js';

/** dg_live_ or dg_test_, an 8-character base62 prefix, then a 32-character base62 secret (S08). */
export const API_KEY: RegExp = /^dg_(live|test)_[0-9A-Za-z]{8}_[0-9A-Za-z]{32}$/;
/** A tool name: a connector, a dot, and the MCP tool charset (S13). */
export const TOOL_NAME: RegExp = /^[a-z][a-z0-9-]{1,31}\.[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/;
export const DEFAULT_BASE_URL: string = 'http://127.0.0.1:8080';
const WHOAMI_TTL_MS = 5 * 60_000;
const INSPECT = Symbol.for('nodejs.util.inspect.custom');

export interface DeedglassOptions {
  /** Default: DEEDGLASS_API_KEY, when the runtime has a process environment. */
  apiKey?: string;
  /** Default: DEEDGLASS_SERVER, else http://127.0.0.1:8080 (cloud: https://api.deedglass.com). */
  baseUrl?: string;
  /** Per request, default 30,000. */
  timeoutMs?: number;
  /** Default 2. */
  maxRetries?: number;
  /** Injectable: tests, proxies, edge runtimes. */
  fetch?: typeof fetch;
  /** Sign every act() and wrapTool call (S09 intents). */
  intentKey?: IntentKey;
  appInfo?: { name: string; version?: string };
  dangerouslyAllowBrowser?: boolean;
  logger?: Pick<Console, 'warn' | 'debug'>;
}

export interface ActOptions {
  /** Default: a new key, stable across this call's retries. */
  idempotencyKey?: string;
  onBehalfOf?: string;
  session?: string;
  /** Server-side wait for the outcome, at most 25,000 ms. Default 10,000. */
  waitMs?: number;
  /** wait: true long-polls a pending approval until a person decides (or timeoutMs passes). */
  approval?: { wait: boolean; timeoutMs?: number };
  signal?: AbortSignal;
}

/** Internal: a proxy-mode submission (wrapTool and the adapters). */
export interface ProxyOptions extends Omit<ActOptions, 'approval'> {
  connectorType?: string;
}

const KEYS = new WeakMap<object, string>();

/** @internal The API key, for adapters that hand it to a framework's own transport (the Claude MCP config). */
export function apiKeyOf(c: Deedglass): string {
  const k = KEYS.get(c);
  if (!k) throw new TypeError('Not a Deedglass client.');
  return k;
}

function keyHint(k: string): string {
  return k.length > 17 ? k.slice(0, 17) + '…' : '…';
}

export class Deedglass {
  readonly #key: string;
  readonly #t: Transport;
  readonly #intentKey: IntentKey | undefined;
  #whoami: { at: number; p: Promise<WhoAmI> } | null = null;
  readonly logger: Pick<Console, 'warn' | 'debug'>;
  readonly actions: Actions;
  readonly tools: Tools;
  readonly deeds: Deeds;
  readonly keys: Keys;

  constructor(opts: DeedglassOptions = {}) {
    if (isBrowser() && !opts.dangerouslyAllowBrowser) {
      throw new DeedglassError({ type: 'client_error', code: 'browser_not_supported', message: copy.browserNotSupported });
    }
    const key = opts.apiKey ?? env('DEEDGLASS_API_KEY');
    if (!key) throw new AuthenticationError({ type: 'authentication_error', code: 'invalid_api_key', message: copy.missingApiKey });
    if (!API_KEY.test(key)) throw new AuthenticationError({ type: 'authentication_error', code: 'invalid_api_key', message: copy.invalidApiKey });
    this.#key = key;
    KEYS.set(this, key);
    const baseUrl = (opts.baseUrl ?? env('DEEDGLASS_SERVER') ?? DEFAULT_BASE_URL).replace(/\/+$/, '');
    const f = opts.fetch ?? globalThis.fetch.bind(globalThis);
    this.#t = new Transport({
      baseUrl,
      auth: () => this.#key,
      timeoutMs: opts.timeoutMs ?? 30_000,
      maxRetries: opts.maxRetries ?? 2,
      fetch: f,
      appInfo: opts.appInfo,
    });
    this.#intentKey = opts.intentKey;
    this.logger = opts.logger ?? console;
    this.actions = new Actions(this.#t);
    this.tools = new Tools(this.#t);
    this.keys = new Keys(this.#t, f);
    this.deeds = new Deeds(this.#t, this.keys);
  }

  get baseUrl(): string {
    return this.#t.baseUrl;
  }

  /** Runs a connector tool through Deedglass (gateway mode): decided, approved if a rule says so, run, witnessed, sealed. */
  async act<R = unknown>(tool: string, args: JsonObject, opts: ActOptions = {}): Promise<Action<R>> {
    const a = await this.#submit<R>('gateway', tool, args, opts);
    if (a.status === 'pending_approval' && opts.approval?.wait) {
      return this.actions.waitUntilSettled<R>(a.id, { ...(opts.approval.timeoutMs !== undefined ? { timeoutMs: opts.approval.timeoutMs } : {}), ...(opts.signal ? { signal: opts.signal } : {}) });
    }
    return a;
  }

  /** Proxy mode: Deedglass decides, your code runs. Used by wrapTool and the adapters. */
  submitProxy(tool: string, args: JsonObject, opts: ProxyOptions = {}): Promise<Action> {
    return this.#submit('proxy', tool, args, opts);
  }

  async #submit<R>(mode: 'gateway' | 'proxy', tool: string, args: JsonObject, opts: ProxyOptions): Promise<Action<R>> {
    if (typeof tool !== 'string' || tool.length > 200 || !TOOL_NAME.test(tool)) {
      throw new InvalidRequestError({ type: 'client_error', code: 'validation_failed', message: copy.badToolName(String(tool)), param: 'tool' });
    }
    const checked = checkArgs(args);
    if (checked === null || typeof checked !== 'object' || Array.isArray(checked)) {
      throw new InvalidRequestError({ type: 'client_error', code: 'validation_failed', message: copy.argsInvalid('they must be a JSON object'), param: 'args' });
    }
    canonicalArgs(checked); // the 256 KiB cap
    for (const [name, v] of [['onBehalfOf', opts.onBehalfOf], ['session', opts.session]] as const) {
      if (v !== undefined && !REF.test(v)) throw new InvalidRequestError({ type: 'client_error', code: 'validation_failed', message: copy.badRef(name), param: name });
    }
    const key = opts.idempotencyKey ?? newIdempotencyKey();
    if (!validIdempotencyKey(key)) {
      throw new InvalidRequestError({ type: 'client_error', code: 'idempotency_key_invalid', message: 'idempotencyKey must be 1 to 255 printable ASCII characters.', param: 'idempotencyKey' });
    }
    const waitMs = Math.max(0, Math.min(opts.waitMs ?? 10_000, 25_000));
    const body: Record<string, unknown> = {
      tool,
      args: checked,
      mode,
      wait: waitMs / 1000,
      // The 2026-10-01 API's context is exactly { client: { name, version }, transport }: unknown
      // fields are refused, so framework ids (a tool call id) aren't sent.
      context: { client: { name: CLIENT_NAME, version: VERSION }, transport: 'sdk' },
    };
    if (mode === 'proxy') body.connector_type = opts.connectorType ?? 'sdk';
    const headers: Record<string, string | undefined> = { 'Deedglass-On-Behalf-Of': opts.onBehalfOf, 'Deedglass-Session': opts.session };
    if (this.#intentKey) headers['Deedglass-Intent'] = await this.#sign(tool, checked, opts);
    const r = await this.#t.request<Action<R>>({
      method: 'POST',
      path: '/v1/actions',
      body,
      headers,
      idempotencyKey: key,
      signal: opts.signal,
      timeoutMs: waitMs + 30_000,
    });
    return withMeta(r.data, r.meta);
  }

  async #sign(tool: string, args: JsonObject, opts: ProxyOptions): Promise<string> {
    const k = this.#intentKey!;
    let org = k.org;
    let agent = k.agent;
    if (!org || !agent) {
      const w = await this.whoami();
      org ??= w.org.id;
      agent ??= w.agent?.id;
    }
    const s = await signIntent({
      key: k,
      org: org ?? '',
      agent: agent ?? '',
      tool,
      args,
      ...(opts.onBehalfOf !== undefined ? { onBehalfOf: opts.onBehalfOf } : {}),
      ...(opts.session !== undefined ? { session: opts.session } : {}),
    });
    return s.header;
  }

  /** GET /v1/whoami: this key's org, agent and mode. Cached for 5 minutes. */
  whoami(): Promise<WhoAmI> {
    const now = Date.now();
    if (!this.#whoami || now - this.#whoami.at > WHOAMI_TTL_MS) {
      const p = this.#t.request<WhoAmI>({ method: 'GET', path: '/v1/whoami' }).then((r) => r.data);
      this.#whoami = { at: now, p };
      p.catch(() => {
        this.#whoami = null;
      });
    }
    return this.#whoami.p;
  }

  toString(): string {
    return `Deedglass { baseUrl: ${JSON.stringify(this.baseUrl)}, apiKey: '${keyHint(this.#key)}' }`;
  }

  toJSON(): { baseUrl: string; apiKey: string } {
    return { baseUrl: this.baseUrl, apiKey: keyHint(this.#key) };
  }

}

// util.inspect prints the redacted form (never the key). Defined outside the class body so the
// declarations stay explicit (isolatedDeclarations).
Object.defineProperty(Deedglass.prototype, INSPECT, {
  value(this: Deedglass): string {
    return this.toString();
  },
});
