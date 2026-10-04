import type { Transport } from '../http.js';
import type { DeedEnvelope } from '../types.js';
import { verifyDeed } from '../verify/core.js';
import type { DeedResult } from '../verify/deed.js';
import type { SealKeySet } from '../verify/keys.js';
import type { Keys } from './keys.js';

const DEED_ID = /^deed_[0-7][0-9a-hjkmnp-tv-z]{25}$/;

export class Deeds {
  readonly #t: Transport;
  readonly #keys: Keys;
  constructor(t: Transport, keys: Keys) {
    this.#t = t;
    this.#keys = keys;
  }

  /** GET /v1/deeds/{id}: the deed envelope, exactly as sealed. */
  async get(id: string): Promise<DeedEnvelope> {
    if (!DEED_ID.test(id)) throw new TypeError(`${JSON.stringify(id.slice(0, 40))} isn't a deed id (deed_…).`);
    return (await this.#t.request<DeedEnvelope>({ method: 'GET', path: `/v1/deeds/${id}` })).data;
  }

  /** Checks one deed's seal offline. Without keys, they are fetched from the server once. */
  async verify(d: DeedEnvelope | string, keys?: SealKeySet): Promise<DeedResult> {
    const ks = keys ?? (await this.#keys.fetch());
    return verifyDeed(typeof d === 'string' ? d : JSON.stringify(d), ks);
  }
}
