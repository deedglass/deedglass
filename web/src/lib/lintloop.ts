// lintloop.ts: the editor's lint loop (S26 §3.2). 400 ms after the last change it sends
// POST /v1/policies/lint {source}; a newer run aborts the older one, so never more than one call is
// in flight. A 429 pauses linting for 20 seconds, then the latest text is checked. A policy over
// 256 KiB (UTF-8) is refused here and nothing is sent. Timers and the call are injected, so tests
// drive it with fake timers.

import { api } from './api.ts';
import { editorCopy as c } from './copy-editor.ts';
import type { PolicyLintResult } from './policyfix.ts';

export const LINT_DEBOUNCE_MS = 400;
export const LINT_PAUSE_MS = 20_000;
export const MAX_POLICY_BYTES = 256 * 1024;

export type LintState =
  | { kind: 'idle' }
  | { kind: 'pending' }
  | { kind: 'linting' }
  | { kind: 'result'; result: PolicyLintResult; source: string }
  | { kind: 'too_large'; message: string; bytes: number }
  | { kind: 'paused'; message: string; until: number }
  | { kind: 'error'; message: string };

export interface LintDeps {
  lint: (source: string, signal: AbortSignal) => Promise<PolicyLintResult>;
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (id: unknown) => void;
  now?: () => number;
}

export interface LintLoop {
  /** The text changed: debounce, then lint. */
  change(source: string): void;
  /** Lint now, skipping the debounce (on mount, or after a fix). */
  flush(source?: string): void;
  dispose(): void;
}

/** defaultLint: the real call. */
export const defaultLint = (source: string, signal: AbortSignal) =>
  api<PolicyLintResult>('POST', '/v1/policies/lint', { body: { source }, signal });

/** utf8Bytes counts UTF-8 bytes without allocating for the common short case. */
export function utf8Bytes(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const ch = s.charCodeAt(i);
    if (ch < 0x80) n += 1;
    else if (ch < 0x800) n += 2;
    else if (ch >= 0xd800 && ch <= 0xdbff && i + 1 < s.length && (s.charCodeAt(i + 1) & 0xfc00) === 0xdc00) { n += 4; i++; }
    else n += 3;
  }
  return n;
}

/** tooLarge: over 256 KiB of UTF-8. A string's UTF-8 size is at least its UTF-16 length. */
export function tooLarge(s: string): number | null {
  if (s.length * 3 <= MAX_POLICY_BYTES) return null; // at most 3 bytes per UTF-16 unit
  const b = utf8Bytes(s);
  return b > MAX_POLICY_BYTES ? b : null;
}

export function createLintLoop(deps: LintDeps, onState: (s: LintState) => void): LintLoop {
  const now = deps.now ?? (() => Date.now());
  let timer: unknown = null;
  let pauseTimer: unknown = null;
  let inflight: AbortController | null = null;
  let latest: string | null = null;
  let lintedSource: string | null = null;
  let paused = false;
  let disposed = false;

  const stopTimer = () => {
    if (timer !== null) deps.clearTimeout(timer);
    timer = null;
  };
  const abort = () => {
    inflight?.abort();
    inflight = null;
  };

  const run = () => {
    timer = null;
    if (disposed || paused || latest === null) return;
    const source = latest;
    abort();
    const ctl = new AbortController();
    inflight = ctl;
    onState({ kind: 'linting' });
    deps.lint(source, ctl.signal).then(
      (result) => {
        if (ctl.signal.aborted || inflight !== ctl || disposed) return;
        inflight = null;
        lintedSource = source;
        onState({ kind: 'result', result, source });
      },
      (err: unknown) => {
        if (ctl.signal.aborted || inflight !== ctl || disposed) return;
        inflight = null;
        const status = (err as { status?: number } | null)?.status;
        if (status === 429) {
          paused = true;
          onState({ kind: 'paused', message: c.lintPaused, until: now() + LINT_PAUSE_MS });
          pauseTimer = deps.setTimeout(() => {
            pauseTimer = null;
            paused = false;
            if (!disposed && latest !== null && latest !== lintedSource) run();
            else if (!disposed) onState({ kind: 'idle' });
          }, LINT_PAUSE_MS);
          return;
        }
        onState({ kind: 'error', message: err instanceof Error ? err.message : String(err) });
      },
    );
  };

  const accept = (source: string): boolean => {
    latest = source;
    const big = tooLarge(source);
    if (big !== null) {
      stopTimer();
      abort();
      onState({ kind: 'too_large', message: c.tooLarge, bytes: big });
      return false;
    }
    return true;
  };

  return {
    change(source) {
      if (disposed) return;
      if (!accept(source)) return;
      if (paused) return; // the pause timer lints the latest text when it ends
      stopTimer();
      onState({ kind: 'pending' });
      timer = deps.setTimeout(run, LINT_DEBOUNCE_MS);
    },
    flush(source) {
      if (disposed) return;
      const s = source ?? latest;
      if (s === null || !accept(s)) return;
      if (paused) return;
      stopTimer();
      run();
    },
    dispose() {
      disposed = true;
      stopTimer();
      abort();
      if (pauseTimer !== null) deps.clearTimeout(pauseTimer);
    },
  };
}
