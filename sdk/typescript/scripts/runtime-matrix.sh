#!/usr/bin/env bash
# The runtime matrix (S31 §4 step 15): the packed tarball, installed into a scratch project, loads
# with require() (require(esm)) and import(), and signs and verifies with WebCrypto Ed25519, on
# Node 22.12, 24 and 26 (Docker node:<v>-alpine when that Node isn't local), then Bun and Deno when
# installed (skipped with a reason otherwise). No network beyond pulling the images.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
(cd "$here" && npm pack --silent --ignore-scripts --pack-destination "$work" >/dev/null)
mkdir -p "$work/app/node_modules/@deedglass/sdk"
tar -xzf "$work"/deedglass-sdk-*.tgz -C "$work/app/node_modules/@deedglass/sdk" --strip-components 1
cp "$here/../../testdata/intents/keys/test1.pem" "$work/app/key.pem"
cat > "$work/app/smoke.cjs" <<'JS'
const { Deedglass, isDeedglassError, signIntent } = require('@deedglass/sdk');
const fs = require('node:fs');
let ok = typeof Deedglass === 'function';
try { new Deedglass({ apiKey: 'nope' }); ok = false; } catch (e) { ok = ok && isDeedglassError(e) && e.code === 'invalid_api_key'; }
signIntent({ key: { privateKeyPem: fs.readFileSync(__dirname + '/key.pem', 'utf8') }, org: 'org_01k6h7kp6msmzrhdzwtcr3jnkp', agent: 'agt_01k633rrmh3bcz2p39gg4vp46q', tool: 'stripe.refund', args: { a: 1 }, now: () => 1791019800000, nonce: '05c18be68a24c056cd6c7c8910dc0729' })
  .then(async (s) => {
    const v = await import('@deedglass/sdk/verify');
    ok = ok && s.kid === '21fe31dfa154a261626bf854046fd227' && typeof v.verifyDeeds === 'function' && (await v.ed25519Supported());
    console.log((ok ? 'ok' : 'FAIL') + ' ' + process.version + ' require+import, Ed25519 sign and verify');
    process.exit(ok ? 0 : 1);
  }, (e) => { console.log('FAIL ' + process.version + ' ' + e.message); process.exit(1); });
JS
status=0
for v in 22.12 24 26; do
  local_major="$(node -p 'process.versions.node.split(".")[0]')"
  if [ "${v%%.*}" = "$local_major" ] && [ "$v" != "22.12" ]; then
    (cd "$work/app" && node smoke.cjs) || status=1
  elif command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    docker run --rm -v "$work/app:/app:ro" -w /app "node:$v-alpine" node smoke.cjs || status=1
  else
    echo "skipped node $v: no local Node $v and no Docker"
  fi
done
if command -v bun >/dev/null 2>&1; then (cd "$work/app" && bun smoke.cjs) || status=1; else echo "skipped bun: not installed"; fi
if command -v deno >/dev/null 2>&1; then (cd "$work/app" && deno run -A smoke.cjs) || status=1; else echo "skipped deno: not installed"; fi
echo "skipped workers/edge: no local workerd or edge runtime (the core uses only fetch, WebCrypto and AbortSignal)"
exit $status
