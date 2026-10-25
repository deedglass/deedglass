#!/usr/bin/env bash
# social-preview.sh: renders docs/assets/social-preview.html to docs/assets/social-preview.png
# (1280x640, GitHub's social preview size) with headless Chrome or Chromium. Uploading it is a
# maintainer step in GitHub: Settings > General > Social preview > Edit > Upload an image.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
CHROME="${CHROME:-}"
if [ -z "$CHROME" ]; then
  for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" "$(command -v chromium || true)" "$(command -v google-chrome || true)"; do
    [ -n "$c" ] && [ -x "$c" ] && CHROME="$c" && break
  done
fi
[ -n "$CHROME" ] || { echo "✕ No Chrome or Chromium found. Next: set CHROME=/path/to/chrome" >&2; exit 1; }
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
"$CHROME" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=1 --user-data-dir="$tmp" \
  --window-size=1280,640 --screenshot="$tmp/shot.png" "file://$PWD/docs/assets/social-preview.html" >/dev/null 2>&1
mv "$tmp/shot.png" docs/assets/social-preview.png
echo "✓ docs/assets/social-preview.png · $(wc -c < docs/assets/social-preview.png | tr -d ' ') bytes"
