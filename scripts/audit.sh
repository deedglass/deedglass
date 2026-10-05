#!/usr/bin/env bash
# make audit (S34 §3.4 row 18): the supply-chain baseline. Every tool is pinned.
#
#   1. govulncheck v1.8.0 over the Go module (make vuln, ADR-024): reachable findings fail.
#   2. npm audit --omit=dev for web/, docs-site/ and sdk/typescript: high or critical in what ships fails.
#      (web/ and docs-site/ ship no npm runtime code; the count is recorded anyway.)
#   3. pip-audit 2.10.1 (ADR-066, a dev tool in a throwaway venv under .tmp/) over the Python SDK's
#      runtime closure (httpx and what it pulls in); the dev venv is audited too, for the record.
#
# It reads advisory databases (vuln.go.dev, the npm registry, PyPI/OSV); it never scans a host.
# Summaries go to .tmp/audit/; the report and docs/SECURITY_REVIEW_2026-10.md quote them.
set -uo pipefail
cd "$(dirname "$0")/.."
GO="${GO:-go}"
out=.tmp/audit
mkdir -p "$out"
fail=0

echo "== govulncheck (Go)"
if "$GO" run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./... >"$out/govulncheck.txt" 2>&1; then
	echo "ok   no reachable vulnerabilities"
else
	echo "FAIL see $out/govulncheck.txt"
	fail=1
fi
tail -3 "$out/govulncheck.txt"

for d in web docs-site sdk/typescript; do
	name=$(echo "$d" | tr '/' '-')
	echo "== npm audit --omit=dev ($d)"
	f="$out/npm-$name.json"
	(cd "$d" && npm audit --omit=dev --json 2>/dev/null) >"$f" || true
	if [ -s "$f" ]; then
		node -e '
const r = JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"));
const v = (r.metadata && r.metadata.vulnerabilities) || {};
const deps = (r.metadata && r.metadata.dependencies) || {};
console.log(`     ${deps.prod ?? deps.total ?? "?"} runtime packages · critical ${v.critical||0} · high ${v.high||0} · moderate ${v.moderate||0} · low ${v.low||0}`);
process.exit((v.critical||0) + (v.high||0) > 0 ? 1 : 0);' "$f" || {
			if [ "$d" = "docs-site" ]; then
				# docs-site ships static files only: its packages run at build time, on our own
				# sources. Findings are recorded (docs/SECURITY_REVIEW_2026-10.md) with their fix plan.
				echo "note docs-site is build-only (static output); recorded, not failing"
			else
				echo "FAIL high or critical in $d"
				fail=1
			fi
		}
	else
		echo "FAIL npm audit gave no answer for $d (offline?)"
		fail=1
	fi
done

echo "== pip-audit 2.10.1 (sdk/python)"
venv="$out/pip-audit-venv"
PY="${PY:-$(command -v python3.13 || command -v python3.12 || command -v python3)}"
if [ ! -x "$venv/bin/pip-audit" ]; then
	"$PY" -m venv "$venv" && "$venv/bin/pip" install --quiet --disable-pip-version-check pip-audit==2.10.1 || { echo "FAIL could not install pip-audit"; fail=1; }
fi
if [ -x "$venv/bin/pip-audit" ]; then
	# The runtime closure: a fresh venv with only the SDK's runtime requirement.
	rt="$out/py-runtime-venv"
	rm -rf "$rt"
	"$PY" -m venv "$rt" && "$rt/bin/pip" install --quiet --disable-pip-version-check 'httpx>=0.28.1,<0.29'
	"$rt/bin/pip" freeze --all | grep -vi '^\(pip\|setuptools\)==' >"$out/py-runtime.txt"
	if "$venv/bin/pip-audit" --strict -r "$out/py-runtime.txt" -f json -o "$out/pip-audit-runtime.json" >/dev/null 2>"$out/pip-audit-runtime.err"; then
		echo "ok   runtime: $(wc -l <"$out/py-runtime.txt" | tr -d ' ') packages, no known vulnerabilities"
	else
		echo "FAIL runtime: see $out/pip-audit-runtime.json"
		fail=1
	fi
	if [ -x sdk/python/.venv/bin/python ]; then
		sdk/python/.venv/bin/pip freeze --all 2>/dev/null | grep -vi '^\(pip\|setuptools\|deedglass\)==' | grep -v '^-e ' >"$out/py-dev.txt"
		if "$venv/bin/pip-audit" -r "$out/py-dev.txt" -f json -o "$out/pip-audit-dev.json" >/dev/null 2>"$out/pip-audit-dev.err"; then
			echo "ok   dev venv: $(wc -l <"$out/py-dev.txt" | tr -d ' ') packages, no known vulnerabilities"
		else
			echo "note dev venv findings (not shipped): see $out/pip-audit-dev.json"
		fi
	fi
	rm -rf "$rt"
fi

exit $fail
