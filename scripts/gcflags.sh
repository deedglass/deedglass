#!/bin/sh
# The -gcflags every release build of bin/deedglass uses (make build, the Dockerfile; .goreleaser.yaml
# lists the same flags, and TestReleaseGcflags in cmd/deedglass keeps the three in step).
#
# Inlining is off for code that only the CLI, setup and reports run: the command wiring, scan and its
# reports, init/setup and its planner, doctor, the simulator and the fake-action demo, migrate. None
# of it is on the request path of serve, so nothing gets slower where it counts, and the binary is
# about 0.5 MB smaller (the image budget, ADR-032 as amended by DL-031). S51's security review pack
# (readiness, the pack build, its check) is a rare request, not a hot path, so it joins the list.
# FEAT-07's shell guard runs once per hook call in its own process, and play, the policy packs and
# the agent script are CLI-only too; they joined to keep the image under budget.
set -eu
P=github.com/deedglass/deedglass
for p in cmd/deedglass/... internal/scan ee/scanfull internal/setup internal/doctor \
  internal/connector/postgres/setup internal/learn/sim internal/ledger/fake internal/installlinks \
  internal/cli/... internal/store/migrate internal/mcp/snippet internal/securitypack/... ee/securitypack \
  internal/securitypackapi internal/weekly/... internal/weeklyapi ee/agency \
  internal/mailer internal/caught internal/observe internal/observeapi ee/undosession internal/undoapi internal/witness/undo internal/shellguard \
  internal/play internal/packs internal/agentscript; do
  printf -- '-gcflags=%s/%s=-l\n' "$P" "$p"
done
