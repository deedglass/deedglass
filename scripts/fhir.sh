#!/usr/bin/env bash
# A throwaway FHIR R4 server (HC-P00): HAPI FHIR JPA in Docker on a free localhost port, with its
# in-memory database. Synthetic data only; everything is gone when the container is removed.
#
#   scripts/fhir.sh up     start it (or reuse it), wait for /fhir/metadata, print the base URL
#   scripts/fhir.sh down   remove the container
#   scripts/fhir.sh it     start it, run cmd/deedglass TestFHIRRestLive and TestFHIRConnectorLive (HC-P02; since HC-P10 it also
#                          exports the records as AuditEvent and Provenance and validates each with HAPI's $validate) on a throwaway Postgres
#                          (scripts/integration_local.sh), then remove the container
#
# The image is pinned by the digest of its multi-arch index (ADR-024).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="hapiproject/hapi:v8.12.0-2@sha256:0ff86aac4c7bd13a1ee226444a70263bb46ed1c28ef543c30a44f865b80ca04e"
NAME="${DEEDGLASS_FHIR_CONTAINER:-deedglass-fhir}"
STATE="$ROOT/.tmp/fhir.port"

TEAL="" RED="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then TEAL=$'\033[36m' RED=$'\033[31m' RESET=$'\033[0m'; fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

need_docker() {
  command -v docker >/dev/null || fail "docker not found. Install Docker Desktop, then run this again."
  docker info >/dev/null 2>&1 || fail "The Docker daemon isn't running. Start Docker Desktop, then run this again."
}

# free_port asks the OS for a free localhost port, avoiding the ports other tools here use.
free_port() {
  local p
  for _ in 1 2 3 4 5; do
    p="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
    case "$p" in 8080|8090|8091|55432) continue ;; esac
    echo "$p"; return
  done
  fail "No free port found."
}

up() {
  need_docker
  mkdir -p "$ROOT/.tmp"
  if docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null | grep -q true && [ -f "$STATE" ]; then
    PORT="$(cat "$STATE")"
  else
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    PORT="$(free_port)"
    docker run -d --name "$NAME" -p "127.0.0.1:$PORT:8080" "$IMAGE" >/dev/null
    echo "$PORT" > "$STATE"
  fi
  local url="http://127.0.0.1:$PORT/fhir"
  for _ in $(seq 1 180); do
    if curl -fsS -o /dev/null "$url/metadata" 2>/dev/null; then
      ok "HAPI FHIR JPA is up: $url"
      echo "$url"
      return
    fi
    docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null | grep -q true || { docker logs --tail 40 "$NAME" >&2 || true; fail "HAPI stopped while starting."; }
    sleep 1
  done
  docker logs --tail 40 "$NAME" >&2 || true
  fail "HAPI didn't answer $url/metadata within 3 minutes."
}

down() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  rm -f "$STATE"
  ok "Removed $NAME."
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  it)
    NAME="deedglass-fhir-it-$$"
    STATE="$ROOT/.tmp/fhir-it-$$.port"
    trap down EXIT
    url="$(up | tail -1)"
    DEEDGLASS_IT_FHIR_URL="$url" DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run ^(TestFHIRRestLive|TestFHIRConnectorLive)$" \
      "$ROOT/scripts/integration_local.sh"
    ;;
  *) echo "usage: scripts/fhir.sh up|down|it" >&2; exit 2 ;;
esac
