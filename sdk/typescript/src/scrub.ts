// A port of Go's vault.Scrub (internal/vault/redact.go): secrets removed by their shape before an
// error message leaves this process. Keep the two lists in step (a test compares them).

const R = '[redacted]';

export const SCRUB_RULES: readonly [RegExp, string][] = [
  [/dg_(live|test)_[0-9A-Za-z_]+/g, 'dg_$1_' + R],
  [/\b(sk|rk)_(live|test)_[0-9A-Za-z]{8,}/g, '$1_$2_' + R],
  [/\bbearer\s+[A-Za-z0-9._~+/=-]{8,}/gi, 'Bearer ' + R],
  [/:\/\/[^/\s:@]+:[^/\s@]+@/g, '://' + R + '@'],
  [/\b(AKIA|ASIA)[0-9A-Z]{16}\b/g, R],
  [/\bgh[pousr]_[A-Za-z0-9]{36,}/g, R],
  [/\bgithub_pat_[A-Za-z0-9_]{22,}/g, R],
  [/\bxox[abprs]-[A-Za-z0-9-]{10,}/g, R],
  [/\bxapp-[A-Za-z0-9-]{10,}/g, R],
  [/-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----(?:[\s\S]*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----|[\s\S]*)/g, R],
  [/\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{16,}/g, R],
  [/\bya29\.[A-Za-z0-9_-]{16,}/g, R],
  [/\b1\/\/0[A-Za-z0-9_-]{16,}/g, R],
  [/\bGOCSPX-[A-Za-z0-9_-]{16,}/g, R],
  [/\bntn_[A-Za-z0-9]{20,}/g, R],
  [/\bsecret_[A-Za-z0-9]{43}\b/g, R],
];

/** Removes secrets by shape and caps the text at `max` UTF-8 bytes (default 1 KiB). */
export function scrub(s: string, max = 1024): string {
  let out = s;
  for (const [re, repl] of SCRUB_RULES) out = out.replace(re, repl);
  const enc = new TextEncoder();
  if (enc.encode(out).length <= max) return out;
  let cut = out.slice(0, max);
  while (enc.encode(cut).length > max - 3) cut = cut.slice(0, -1);
  return cut + '…';
}
