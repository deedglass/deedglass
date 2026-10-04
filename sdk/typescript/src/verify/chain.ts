// Copied from web/verify/src/chain.ts by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:
// edit web/verify/src/chain.ts, then run npm run sync-verify. make sdk-sync-verify fails on drift.
// The deed chain, a port of deed.ChainChecker: seq continuity, prev_hash links, org and time order.

import type { DeedResult } from './deed.js';
import { genesis } from './deed.js';
import { VerifyError } from './errors.js';

export interface Head {
  seq: number;
  bodyHash: string;
  issuedAt?: number;
}

export class ChainChecker {
  private prev: DeedResult | null = null;
  private anchor: Head | null;
  private org: string;

  constructor(opts: { org?: string; anchor?: Head | null } = {}) {
    this.org = opts.org ?? '';
    this.anchor = opts.anchor ?? null;
  }

  /** Checks r against the previous deed. Throws VerifyError; after an error the last good deed is kept. */
  async next(r: DeedResult): Promise<void> {
    const b = r.envelope.body;
    const fail = (code: VerifyError['code'], init: Partial<VerifyError> = {}) => {
      const e = new VerifyError(code, { seq: r.seq, deedId: r.deedId, ...init });
      if (this.prev && init.prev === undefined) e.prev = this.prev.seq;
      return e;
    };
    const wantOrg = this.prev ? this.prev.org : this.org;
    if (wantOrg !== '' && r.org !== wantOrg) throw fail('chain_org_mismatch', { org: r.org, wantOrg });
    if (this.prev === null && this.anchor === null) {
      if (r.seq !== 1) throw fail('chain_seq_gap', { want: 1 });
      if (b.prev_hash !== (await genesis(r.org))) throw fail('chain_genesis_mismatch');
    } else {
      const [prevSeq, prevHash, prevTime] = this.prev
        ? [this.prev.seq, this.prev.bodyHash, this.prev.issuedAt]
        : [this.anchor!.seq, this.anchor!.bodyHash, this.anchor!.issuedAt ?? -Infinity];
      if (r.seq <= prevSeq) throw fail('chain_seq_out_of_order', { prev: prevSeq });
      if (r.seq > prevSeq + 1) throw fail('chain_seq_gap', { prev: prevSeq, want: prevSeq + 1 });
      if (b.prev_hash !== prevHash) throw fail('chain_prev_hash_mismatch', { prev: prevSeq });
      if (r.issuedAt < prevTime) throw fail('chain_time_regression', { prev: prevSeq });
    }
    this.prev = r;
  }

  /** Makes r the previous deed without checking it (--all keeps checking links from it). */
  continueFrom(r: DeedResult): void {
    this.prev = r;
  }

  /** Accepts next as a new starting point (after an unreadable line). */
  restart(next: DeedResult): void {
    this.prev = null;
    this.anchor = { seq: next.seq - 1, bodyHash: next.envelope.body.prev_hash };
  }
}
