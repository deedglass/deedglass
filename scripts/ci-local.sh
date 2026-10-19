#!/usr/bin/env bash
# Run the jobs of .github/workflows/ci.yml on this machine, without installing anything (S03 §3.12).
# Bash 3.2 compatible (macOS).
#
#   scripts/ci-local.sh                       every job: lint-unit, integration, docker-smoke, govulncheck
#   scripts/ci-local.sh lint-unit govulncheck only the jobs named
#
# lint-unit     every step of the CI job, in the same order; stops at the first failing step
# integration   make integration (a throwaway Postgres 18: Homebrew if installed, else Docker)
# docker-smoke  needs a running Docker daemon with Compose, and ports 8080 and 55432 free (make dev-down
#               first). It uses its own compose project and env file (.tmp/ci-smoke.env), so it never
#               touches your make dev database, and removes what it started. It tags deedglass:dev.
# govulncheck   needs the network; runs the same pinned version as ci.yml
#
# A job that can't run here prints "skipped: <why>". The exit status is 1 if any job failed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
GO="${GO:-go}"
COMPOSE_FILE="deploy/docker-compose.yml"
SMOKE_PROJECT="deedglass-ci-smoke"
SMOKE_ENV=".tmp/ci-smoke.env"

RESULTS=""
record() { RESULTS="${RESULTS}$(printf '  %-13s %s' "$1" "$2")"$'\n'; }
header() { printf '\n== %s\n' "$1"; }
run_step() { # run_step <label> <command...>: prints the label, runs the command
  printf -- '-- %s\n' "$1"
  shift
  "$@"
}

have_docker() { command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; }
have_compose() { have_docker && docker compose version >/dev/null 2>&1; }
port_busy() {
  if command -v lsof >/dev/null 2>&1; then lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; return; fi
  if command -v nc >/dev/null 2>&1; then nc -z 127.0.0.1 "$1" >/dev/null 2>&1; return; fi
  return 1
}

# --- lint-unit -------------------------------------------------------------------------------------

gofmt_check() {
  local out
  out="$(gofmt -l .)"
  if [ -n "$out" ]; then echo "gofmt needed:"; echo "$out"; return 1; fi
}
testdata_sums() {
  if command -v sha256sum >/dev/null 2>&1; then (cd testdata && sha256sum --quiet -c SHA256SUMS)
  else (cd testdata && shasum -a 256 -c --quiet SHA256SUMS); fi
}
banned_words() {
  if git rev-parse -q --verify HEAD >/dev/null 2>&1; then scripts/banned_words.sh --commits HEAD
  else scripts/banned_words.sh; fi
}

job_lint_unit() {
  header "lint-unit"
  command -v "$GO" >/dev/null 2>&1 || { record lint-unit "skipped: go isn't installed"; return 0; }
  if run_step "gofmt" gofmt_check &&
    run_step "go.mod and go.sum tidy and verified" sh -c "$GO mod tidy -diff && $GO mod verify" &&
    run_step "go vet" "$GO" vet ./... &&
    run_step "go test" "$GO" test -race -count=1 ./... &&
    run_step "no-cgo build of the binary" env CGO_ENABLED=0 "$GO" build -o /dev/null ./cmd/deedglass &&
    run_step "test vectors unchanged" testdata_sums &&
    run_step "rename guard (retired identifiers, ADR-012)" scripts/rename_guard.sh &&
    run_step "banned words (files and commit messages)" banned_words; then
    record lint-unit "passed"
  else
    record lint-unit "FAILED (see the step above)"
  fi
}

# --- integration -----------------------------------------------------------------------------------

job_integration() {
  header "integration"
  if [ ! -x /opt/homebrew/opt/postgresql@18/bin/postgres ] && ! have_docker; then
    record integration "skipped: no Homebrew postgresql@18 and no running Docker to start a throwaway Postgres"
    return 0
  fi
  if make integration; then record integration "passed"; else record integration "FAILED (see make integration above)"; fi
}

# --- docker-smoke ----------------------------------------------------------------------------------

# pins_check: every action pinned to a commit SHA with a version comment, every image to a digest
# (ADR-024). The same commands as the first step of CI's docker-smoke job.
pins_check() {
  local bad
  bad="$( {
    grep -nE '^[[:space:]]*(-[[:space:]]*)?uses:' .github/workflows/*.yml | grep -vE 'uses: [^ ]+@[0-9a-f]{40} # v[0-9]+[.][0-9]+[.][0-9]+$'
    grep -nE '^[[:space:]]*image:' .github/workflows/*.yml deploy/docker-compose.yml | grep -vE '@sha256:[0-9a-f]{64}' | grep -vE 'image: (deedglass:dev|\$\{\{ matrix[.]image \}\})$'
    grep -nE '^FROM ' Dockerfile | grep -vE '@sha256:[0-9a-f]{64}'
  } || true )"
  if [ -n "$bad" ]; then echo "unpinned:"; echo "$bad"; return 1; fi
  echo "pins: ok"
}

smoke_compose() { docker compose -p "$SMOKE_PROJECT" -f "$COMPOSE_FILE" --env-file "$SMOKE_ENV" "$@"; }
smoke_cleanup() { smoke_compose down -v --remove-orphans >/dev/null 2>&1 || true; rm -f "$SMOKE_ENV"; }

health_status() {
  if command -v jq >/dev/null 2>&1; then printf '%s' "$1" | jq -r '.status // empty' 2>/dev/null; return 0; fi
  printf '%s' "$1" | tr -d '\n' | grep -o '"status"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n 1 | sed 's/.*"\([^"]*\)"$/\1/'
}

smoke_image() {
  local user size
  docker build --build-arg VERSION=0.1.0-dev --build-arg COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" -t deedglass:dev . || return 1
  user="$(docker image inspect deedglass:dev -f '{{.Config.User}}')"
  size="$(docker image inspect deedglass:dev -f '{{.Size}}')"
  echo "user=$user size=$size bytes, $((45000000 - size)) bytes under the budget"
  [ "$user" = "65532:65532" ] || { echo "FAIL: .Config.User is '$user', want 65532:65532"; return 1; }
  [ "$size" -lt 45000000 ] || { echo "FAIL: image is $size bytes, want < 45000000 (ADR-106, amending ADR-032)"; return 1; }
}

smoke_health() {
  local i body
  for i in $(seq 1 30); do
    body="$(curl -sS -m 2 http://127.0.0.1:8080/v1/health 2>/dev/null || true)"
    if [ "$(health_status "$body")" = "ok" ]; then
      echo "healthy after ${i} s: $body"
      return 0
    fi
    sleep 1
  done
  echo "FAIL: /v1/health did not report ok within 30 s; last body: $body"
  return 1
}

smoke_checks() {
  local mig code srv
  mig="$(smoke_compose ps -a -q migrate)"
  code="$(docker inspect -f '{{.State.ExitCode}}' "$mig")"
  [ "$code" = "0" ] || { echo "FAIL: migrate exited $code"; return 1; }
  srv="$(smoke_compose ps -q deedglass)"
  [ "$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$srv")" = "true" ] || { echo "FAIL: root filesystem is writable"; return 1; }
  smoke_compose exec -T deedglass /deedglass version --server http://127.0.0.1:8080
}

job_docker_smoke() {
  header "docker-smoke"
  if ! run_step "every action pinned to a commit SHA, every image to a digest (ADR-024)" pins_check; then
    record docker-smoke "FAILED (see the pins step above)"
    return 0
  fi
  if ! command -v docker >/dev/null 2>&1; then record docker-smoke "skipped: Docker isn't installed (pins checked)"; return 0; fi
  if ! have_docker; then record docker-smoke "skipped: Docker isn't running; start Docker Desktop to run it (pins checked)"; return 0; fi
  if ! have_compose; then record docker-smoke "skipped: Docker Compose isn't available (pins checked)"; return 0; fi
  if port_busy 8080 || port_busy 55432; then
    record docker-smoke "skipped: port 8080 or 55432 is in use; run make dev-down, or stop make run, first (pins checked)"
    return 0
  fi
  mkdir -p .tmp
  rm -f "$SMOKE_ENV"
  trap smoke_cleanup EXIT
  if run_step "build deedglass:dev; runs as 65532:65532 and is under 45 MB" smoke_image &&
    run_step ".env with fresh throwaway secrets" env DEEDGLASS_DEV_ENV_FILE="$SMOKE_ENV" scripts/dev.sh env &&
    run_step "start Postgres (init scripts), migrate, then serve" smoke_compose up -d --no-build &&
    run_step "GET /v1/health reports \"status\":\"ok\" within 30 s" smoke_health &&
    run_step "migrate exited 0; the server is read-only and answers its own healthcheck" smoke_checks; then
    record docker-smoke "passed"
  else
    smoke_compose logs --no-color --tail 50 2>/dev/null || true
    record docker-smoke "FAILED (see the step above)"
  fi
  smoke_cleanup
  trap - EXIT
}

# --- govulncheck -----------------------------------------------------------------------------------

job_govulncheck() {
  header "govulncheck"
  local ver
  ver="$(sed -n 's/.*govulncheck@\(v[0-9][0-9.]*\).*/\1/p' .github/workflows/ci.yml | head -n 1)"
  [ -n "$ver" ] || { record govulncheck "FAILED (no govulncheck@version in .github/workflows/ci.yml)"; return 0; }
  command -v "$GO" >/dev/null 2>&1 || { record govulncheck "skipped: go isn't installed"; return 0; }
  if ! curl -fsS -m 5 -o /dev/null https://vuln.go.dev/index/db.json 2>/dev/null; then
    record govulncheck "skipped: no network (vuln.go.dev isn't reachable)"
    return 0
  fi
  if run_step "govulncheck $ver" "$GO" run "golang.org/x/vuln/cmd/govulncheck@$ver" ./...; then
    record govulncheck "passed ($ver)"
  else
    record govulncheck "FAILED (see govulncheck above)"
  fi
}

# --- main ------------------------------------------------------------------------------------------

JOBS="$*"
[ -n "$JOBS" ] || JOBS="lint-unit integration docker-smoke govulncheck"
for j in $JOBS; do
  case "$j" in
    lint-unit|integration|docker-smoke|govulncheck) ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) echo "ci-local: unknown job $j (lint-unit, integration, docker-smoke or govulncheck)" >&2; exit 2 ;;
  esac
done
for j in $JOBS; do
  case "$j" in
    lint-unit) job_lint_unit ;;
    integration) job_integration ;;
    docker-smoke) job_docker_smoke ;;
    govulncheck) job_govulncheck ;;
  esac
done

printf '\n== ci-local summary\n%s' "$RESULTS"
case "$RESULTS" in *FAILED*) exit 1 ;; esac
exit 0
