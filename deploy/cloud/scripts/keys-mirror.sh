#!/usr/bin/env bash
# keys-mirror.sh (S35): the seal keys the API publishes must equal the copy on deedglass.com, which is
# what auditors pin (SEAL_KEYS.md step 4). Compares the two keys.json documents byte for byte after
# normalising whitespace; exit 1 on drift or when either can't be fetched.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
a="$(opsval KEYS_API_URL)"; b="$(opsval KEYS_SITE_URL)"
a="${a:-https://api.deedglass.com/.well-known/deedglass/keys.json}"; b="${b:-https://deedglass.com/.well-known/deedglass/keys.json}"
ka="$(curl -fsS -m 10 "$a" | jq -S -c .)" || { echo "✕ Couldn't fetch $a"; exit 1; }
kb="$(curl -fsS -m 10 "$b" | jq -S -c .)" || { echo "✕ Couldn't fetch $b"; echo "  next  Copy $a into site/public/.well-known/deedglass/keys.json and deploy the site."; exit 1; }
if [ "$ka" = "$kb" ]; then echo "✓ Keys mirror · $(jq '.data | length' <<<"$ka" 2>/dev/null || echo ?) seal keys · identical"; else
  echo "✕ Keys mirror · $b differs from $a"; echo "  next  Copy the API's keys.json into the site (SEAL_KEYS.md step 4) and deploy it."; exit 1; fi
