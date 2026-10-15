#!/bin/sh
# The -gcflags every release build of bin/deedglass uses (make build, the Dockerfile; .goreleaser.yaml
# lists the same flags, and TestReleaseGcflags in cmd/deedglass keeps the three in step).
#
# Inlining is off for code that only the CLI, setup and reports run: the command wiring, scan and its
# reports, init/setup and its planner, doctor, the simulator and the fake-action demo, migrate. None
# of it is on the request path of serve, so nothing gets slower where it counts, and the binary is
# about 0.5 MB smaller (the image budget, ADR-032 as amended by DL-031).
set -eu
P=github.com/deedglass/deedglass
for p in cmd/deedglass/... internal/scan ee/scanfull internal/setup internal/doctor \
  internal/connector/postgres/setup internal/learn/sim internal/ledger/fake internal/installlinks \
  internal/cli/... internal/store/migrate internal/mcp/snippet; do
  printf -- '-gcflags=%s/%s=-l\n' "$P" "$p"
done
