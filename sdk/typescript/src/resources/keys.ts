import type { Transport } from '../http.js';
import { fetchKeys } from '../verify/fetch.js';
import type { SealKeySet } from '../verify/keys.js';

export class Keys {
  readonly #t: Transport;
  readonly #fetch: typeof fetch;
  constructor(t: Transport, f: typeof fetch) {
    this.#t = t;
    this.#fetch = f;
  }

  /** The seal keys: by default <baseUrl>/.well-known/deedglass/keys.json (https, or http on localhost). */
  async fetch(url?: string): Promise<SealKeySet> {
    const u = url ?? this.#t.url('/.well-known/deedglass/keys.json');
    return (await fetchKeys(u, { fetch: this.#fetch })).keys;
  }
}
