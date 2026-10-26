# Deedglass product repo. `make help` lists the targets; (ci) marks what the lint-unit CI job runs.
GO ?= go

VERSION  ?= 0.1.0-dev
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
MODIFIED := $(shell if [ -n "$$(git status --porcelain 2>/dev/null)" ]; then echo true; else echo false; fi)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BI       := github.com/deedglass/deedglass/internal/buildinfo
GCFLAGS  := $(shell scripts/gcflags.sh)
LDFLAGS  := -s -w -X $(BI).Version=$(VERSION) -X $(BI).Commit=$(COMMIT) -X $(BI).Modified=$(MODIFIED) -X $(BI).Date=$(DATE)

.PHONY: wasm api-gz embed-gz mcpb demo-scan-golden packs-check docs docs-dev docs-check docs-snippets docs-lighthouse py-ci py-build py-examples web-install web-tokens web-build web-test web-e2e web-lighthouse web-dev build-only ops-build bench-undo all help ci build test test-short vet fmt fmt-check tidy-check testdata-check release-bundles independent-verifiers rename-guard banned-words nocgo \
	integration matrix fuzz bench workflows-check npm-dry-run release-check cloud-smoke bench-micro bench-quick bench-soak bench-check bench-report bench-store bench-deed cover clean \
	dev dev-down dev-reset dev-logs run docker bench-http bench-verify \
	web-verify-install web-verify-test sdk-ts sdk-ts-it sdk-sync-verify sdk-runtime sdk-examples web-verify-build web-verify-serve gencopy genmark bench-mark bench-ledger bench-stop bench-intents bench-gate bench-witness policy-test \
	fuzz-policy fuzz-policy-short audit fuzz-all fuzz-regress attack-one bench-policy bench-eval mcp-it bench-mcp \
	demo demo-live demo-launch-check demo-up demo-down demo-reset demo-status demo-snippet demo-psql demo-follow demo-verify demo-check demo-smoke bench-gateway \
	demo-trigger-on demo-trigger-off demo-order-reset demo-hero demo-capture demo-svg social-preview demo-hero-check demo-diff bench-l2 bench-approvals bench-slack \
	mock-orders bench-connectors bench-postgres bench-decoder attack attack-report vuln undo-matrix quickstart-it

all: ci

help: ## list the targets
	@grep -E '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-15s %s\n", $$1, $$2}'

ci: fmt-check tidy-check vet test fuzz-policy-short policy-test testdata-check release-bundles rename-guard banned-words nocgo wasm workflows-check listings-check web-verify-test web-test sdk-ts py-ci ## everything the lint-unit, web-verify, release-bundles and web CI jobs run

build: ## build bin/deedglass with version ldflags (with Node 24 present, the real Console first: make web-build)
	@if command -v node >/dev/null 2>&1; then $(MAKE) --no-print-directory web-build; else echo "build: Node isn't installed, so the binary carries the Console placeholder (dev only; prod refuses it)"; fi
	$(GO) build -trimpath $(GCFLAGS) -ldflags '$(LDFLAGS)' -o bin/deedglass ./cmd/deedglass

test: ## (ci) unit tests with the race detector; then the ops build's outbound_mail connector and cmd guards (S40)
	$(GO) test -race -count=1 ./...
	$(GO) test -race -count=1 -tags outboundmail ./internal/connector/outbound/... ./internal/connector/googleauth/
	$(GO) test -race -count=1 -tags outboundmail ./cmd/deedglass/ -run Outbound

test-short: ## unit tests without -race, in short mode
	$(GO) test -short -count=1 ./...

vet: ## (ci) go vet, in both build modes (the default and the ops build's outboundmail tag, S40)
	$(GO) vet ./...
	$(GO) vet -tags outboundmail ./...

fmt: ## rewrite Go files with gofmt
	gofmt -w $$(git ls-files -co --exclude-standard '*.go')

fmt-check: ## (ci) fail if gofmt would change anything
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy-check: ## (ci) fail if go.mod/go.sum are not tidy
	$(GO) mod tidy -diff

api-gz: ## rewrite api/openapi.yaml.gz from api/openapi.yaml (the binary embeds the gzip; TestSpecGzipCurrent checks it)
	gzip -9n -c api/openapi.yaml > api/openapi.yaml.gz

embed-gz: ## rewrite the gzip archives the binary embeds (internal/store/migrations/sql.tar.gz) from the files beside them; each package's TestArchiveCurrent checks it
	$(GO) run ./internal/embedgz/cmd/embedgz

testdata-check: ## (ci) verify the offline test vectors
	cd testdata && shasum -a 256 -c --quiet SHA256SUMS

release-bundles: ## (ci) TY-11, the 2046 test: every release's bundle in testdata/release-bundles verifies with this tree's CLI (the browser half runs in web-verify-test); a new release adds its own: scripts/release-bundles.sh add vX.Y.Z
	scripts/release-bundles.sh verify

independent-verifiers: ## TY-10: the Python and JS verifiers (verifiers/, written from spec/) reach the same verdict as this tree's CLI on every release bundle, tampered copies and every deed vector (CI workflow independent-verifiers, not required yet)
	go build -o .tmp/deedglass-crosscheck ./cmd/deedglass
	python3 verifiers/crosscheck.py .tmp/deedglass-crosscheck

rename-guard: ## (ci) fail on any retired identifier (docs/NAMING.md, ADR-012)
	@scripts/rename_guard.sh

banned-words: ## (ci) banned words in files and commit messages
	@if git rev-parse -q --verify HEAD >/dev/null; then scripts/banned_words.sh --commits HEAD; else scripts/banned_words.sh; fi

wasm: ## (ci) the browser build behind deedglass.com/check: bin/deedglass.wasm and Go's wasm_exec.js; fails over 4 MB gzipped
	GOOS=js GOARCH=wasm $(GO) build -trimpath -ldflags '-s -w -X main.version=$(VERSION)' -o bin/deedglass.wasm ./cmd/deedglass-wasm
	cp "$$($(GO) env GOROOT)/lib/wasm/wasm_exec.js" bin/wasm_exec.js
	@n=$$(gzip -9c bin/deedglass.wasm | wc -c | tr -d ' '); echo "deedglass.wasm: $$n bytes gzipped"; \
	if [ "$$n" -gt 4194304 ]; then echo "wasm: over the 4 MB gzipped budget"; exit 1; fi

nocgo: ## (ci) the binary builds without cgo
	CGO_ENABLED=0 $(GO) build -o /dev/null ./cmd/deedglass

integration: ## throwaway PG cluster (Homebrew, else Docker): sql/ smoke, control-plane migrations, Go integration suites (S31: with sdk-ts-it's TestSDKTypeScriptLive when sdk/typescript is installed); DEEDGLASS_IT_PG=18|17|16 DEEDGLASS_IT_MODE=brew|docker
	DEEDGLASS_IT_SDK_TS=1 scripts/integration_local.sh

quickstart-it: ## S32/PRD-05: TestInitLive, deedglass init then init --trigger end to end in Docker on its own ports (two postgres:18.6 containers, removed after): smoke actions, the 301-row Doesn't match, the undo preview, the step line
	DEEDGLASS_IT_INIT=1 $(GO) test -tags integration -count=1 -v -timeout 15m ./internal/setup -run TestInitLive

.PHONY: fhir-up fhir-down fhir-it corpus corpus-bench fhir-bench
fhir-bench: ## The FHIR write-integrity benchmark (corpus/fhir/BENCHMARK.md): every scenario through each subject (none; http-status, trusts 2xx; deedglass, a real serve from this tree with its Gate and fhir connector in front) on a throwaway Medplum 5.2.6 on PG 18.6 in Docker (database in memory, removed after); scores caught, true effect and checkable record; SUBJECTS=…, RUN=<id>, OUT=<file> (default .tmp/fhir-bench/bench.json, and .md), WRITE_EXPECTED=1; not in CI (TestGoldenReplay replays the committed run)
	scripts/fhir_bench.sh

corpus-bench: ## The corpus benchmark (corpus/BENCHMARK.md): every scenario through each subject (deedglass, a real serve from this tree with its Gate in front; none, the baseline) on a throwaway PostgreSQL 18.6 in Docker; scores caught (before or after commit), true effect and checkable record; SUBJECTS=…, RUN=<id>, OUT=<file> (default .tmp/corpus/bench.json, and .md)
	scripts/corpus_bench.sh

corpus: ## TY-13: the intent-vs-effect corpus (corpus/) on a throwaway PostgreSQL 18.6 in Docker (free localhost port, removed after): what each agent reports, what Postgres commits (its change log, per transaction) and the expected Deedglass verdict; RUN=<id> one scenario, KEEP=1 keeps the container, CORPUS_TOOL=<cmd> runs your tool around each scenario
	scripts/corpus.sh

fhir-up: ## HC-P00: a throwaway FHIR R4 server (HAPI FHIR JPA, pinned image) in Docker on a free localhost port; prints the base URL (synthetic data only)
	scripts/fhir.sh up

fhir-down: ## HC-P00: remove the fhir-up container
	scripts/fhir.sh down

fhir-it: ## HC-P00: TestFHIRRestLive, the REST connector against HAPI FHIR JPA in Docker on a throwaway PG 18 (policy-gated create and update, read-back checks); the container is removed after
	scripts/fhir.sh it

mcp-it: ## S14: the real @modelcontextprotocol/server-filesystem via npx behind deedglass mcp wrap, on a throwaway PG 18 (needs Node.js; skips without it)
	DEEDGLASS_IT_NODE=1 DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run TestMCPWrap" scripts/integration_local.sh

bench-mcp: ## S14 §3.10: the wrapper's relay, tools/call and tools/list overhead and memory against a real Gate on a throwaway PG 18; JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-mcp.json"; log="$(CURDIR)/.tmp/bench-mcp.log"; mkdir -p "$(CURDIR)/.tmp"; \
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_MCP_BENCH_OUT="$$out" DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-run TestMCPBudget -v" \
	scripts/integration_local.sh > "$$log" 2>&1; st=$$?; grep -E 'PERF|over budget|--- (PASS|FAIL)' "$$log"; echo "raw: $$out"; exit $$st

attack: ## S18: the V7 attack suite on a throwaway PG 18 (a real serve, real Postgres, loopback upstreams); writes attack/results/latest.json, then docs/ATTACK_SUITE.md
	scripts/attack_local.sh
	$(GO) run ./attack/cmd/attack-report

attack-one: ## S34: one attack case on a throwaway PG 18: make attack-one CASE=Q01 (any TestAttack subtest name)
	@test -n "$(CASE)" || { echo "make attack-one CASE=<id>, for example CASE=Q01"; exit 2; }
	DEEDGLASS_IT_ATTACK_RUN='TestAttack/$(CASE)$$' scripts/attack_local.sh

attack-report: ## S18: print the last attack run and regenerate docs/ATTACK_SUITE.md from the meta and attack/results/latest.json (-check in CI)
	$(GO) run ./attack/cmd/attack-report

audit: ## S34: the supply-chain baseline: govulncheck, npm audit --omit=dev (web, docs-site, sdk/typescript), pip-audit 2.10.1 (sdk/python runtime); summaries in .tmp/audit
	scripts/audit.sh

fuzz-all: ## S34: the fuzz schedule (S34 §3.6, about two hours, 3 targets at a time); JSON in bench/results/<date>-<os>-<arch>-fuzz.json
	scripts/fuzz_all.sh

fuzz-regress: ## S34: every fuzz target for 10 s: the committed corpus under testdata/fuzz replays, then a short burst (CI job fuzz-regress)
	scripts/fuzz_all.sh regress

vuln: ## S18: govulncheck, pinned (ADR-041); a dev tool, never linked into the binary
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

matrix: ## integration on 16, 17 and 18
	@for v in 16 17 18; do echo "=== PostgreSQL $$v"; DEEDGLASS_IT_PG=$$v scripts/integration_local.sh || exit 1; done

fuzz: ## 30 s each: the strict intent parser, the id parser, and the deed envelope, verifier and key-set parsers
	$(GO) test ./internal/intent -run '^$$' -fuzz FuzzParse -fuzztime 30s
	$(GO) test ./internal/ids -run '^$$' -fuzz FuzzParse -fuzztime 30s
	$(GO) test ./internal/deed -run '^$$' -fuzz '^FuzzParseEnvelope$$' -fuzztime 30s
	$(GO) test ./internal/deed -run '^$$' -fuzz '^FuzzVerifyEnvelope$$' -fuzztime 30s
	$(GO) test ./internal/deed -run '^$$' -fuzz '^FuzzParseKeySet$$' -fuzztime 30s
	$(GO) test ./internal/deed -run '^$$' -fuzz '^FuzzVerifyStream$$' -fuzztime 30s
	$(GO) test ./internal/identity -run '^$$' -fuzz '^FuzzHashToken$$' -fuzztime 30s
	$(GO) test ./internal/identity -run '^$$' -fuzz '^FuzzEmailNormalize$$' -fuzztime 30s
	$(GO) test ./internal/identity -run '^$$' -fuzz '^FuzzParseAPIKey$$' -fuzztime 30s
	$(GO) test ./internal/deed -run '^$$' -fuzz '^FuzzCanonical$$' -fuzztime 30s
	$(GO) test ./internal/deed/archive -run '^$$' -fuzz '^FuzzArchiveOpen$$' -fuzztime 30s
	$(GO) test ./internal/intent -run '^$$' -fuzz '^FuzzArgsDigest$$' -fuzztime 30s
	$(GO) test ./internal/policy -run '^$$' -fuzz '^FuzzCompile$$' -fuzztime 30s
	$(GO) test ./internal/policy -run '^$$' -fuzz '^FuzzYAMLReader$$' -fuzztime 30s
	$(GO) test ./internal/identity -run '^$$' -fuzz '^FuzzIntentHeader$$' -fuzztime 30s
	$(GO) test ./internal/connector/rest -run '^$$' -fuzz '^FuzzTemplatePath$$' -fuzztime 30s
	$(GO) test ./internal/connector/egress -run '^$$' -fuzz '^FuzzCheckURL$$' -fuzztime 30s
	$(GO) test ./internal/vault -run '^$$' -fuzz '^FuzzRedactor$$' -fuzztime 30s

mock-orders: ## S16: the mock orders API on 127.0.0.1:8090 (MOCK_ORDERS_TOKEN required; MOCK_ORDERS_MODE=lying for S19)
	$(GO) run ./examples/mock-orders

.PHONY: fhir-proven-it
fhir-proven-it: ## HC-P05: TestFHIRProvenLive, Proven (L2) against HAPI FHIR JPA and Medplum, each on PostgreSQL 18 in Docker (pinned images, free ports, witness roles and publications), on a throwaway control-plane PG; containers removed after (KEEP=1 keeps them)
	scripts/fhir_proven.sh

.PHONY: debezium-it
debezium-it: ## TY-05: TestMySQLTriggerLive, the 301-row trigger scene on MySQL 8.4 through Debezium Server 3.3 (deploy/debezium, pinned images, free localhost ports, an HTTP receiver on 127.0.0.1), compared by deedglass witness debezium's code; RECORD=1 rewrites the CI golden, KEEP=1 keeps the containers (not in CI)
	DEEDGLASS_DEBEZIUM_COMPOSE=$(CURDIR)/deploy/debezium/compose.yaml $(GO) test -tags debezium -count=1 -v -timeout 15m -run TestMySQLTriggerLive ./internal/witness/feed/debezium/ $(if $(RECORD),-record,)

.PHONY: medplum-example medplum-example-it
medplum-example: ## The Medplum example (examples/medplum): Medplum 5.2.6 on PG 18 in Docker, a serve from this tree, an agent's create that Matches, a stored-but-different write flagged, an update a person approves, and a Medplum Bot handler; everything removed after (KEEP=1 keeps it)
	examples/medplum/run.sh

medplum-example-it: ## TestMedplumExample: make medplum-example as a test that checks every outcome the README shows (skips without Docker; not in CI)
	DEEDGLASS_IT_MEDPLUM_EXAMPLE=1 $(GO) test -count=1 -v -timeout 15m ./examples/medplum

.PHONY: fhir-spike
fhir-spike: ## HC-P05: the spike as a probe (ADR-105): HAPI FHIR JPA on PostgreSQL in Docker (pinned images, free port), checks what the server stores to tie its rows to an action; fails when a finding stops holding
	scripts/fhir_spike.sh

bench-postgres: ## S17: the Postgres connector's extra latency over raw pgx (p50 ≤ 1.5 ms) and 100 writes/s for 30 s (p99 < 50 ms) on a throwaway PG 18; JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-postgres.json"; log="$(CURDIR)/.tmp/bench-postgres.log"; mkdir -p "$(CURDIR)/.tmp" "$(CURDIR)/bench/results"; \
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_PG_BENCH_OUT="$$out" DEEDGLASS_IT_PKGS=./internal/connector/postgres/ DEEDGLASS_IT_GOFLAGS="-run TestPostgresBudget -v" \
	scripts/integration_local.sh > "$$log" 2>&1; st=$$?; grep -E 'PERF|--- (PASS|FAIL)' "$$log"; echo "raw: $$out"; exit $$st

bench-decoder: ## S20: SafeParse vs Parse, then 100 writes/s for 60 s on a throwaway PG 18 (commit to frame persisted p50 < 250 ms, p99 < 1 s; poll round p50 < 20 ms); JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-decoder.json"; log="$(CURDIR)/.tmp/bench-decoder.log"; mkdir -p "$(CURDIR)/.tmp" "$(CURDIR)/bench/results"; \
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_DECODER_BENCH_OUT="$$out" DEEDGLASS_IT_PKGS=./internal/decoder/ DEEDGLASS_IT_GOFLAGS="-run TestDecoderBudget -v -timeout 900s" \
	scripts/integration_local.sh > "$$log" 2>&1; st=$$?; grep -E 'PERF|--- (PASS|FAIL)' "$$log"; echo "raw: $$out"; exit $$st

bench-l2: ## S21: Derive, Compare (1,000 images; 200,000 rows), the render, then 100 writes/s for 60 s through a real serve on a throwaway PG 18 (commit to verdict p50 < 1 s); JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-l2.json"; log="$(CURDIR)/.tmp/bench-l2.log"; mkdir -p "$(CURDIR)/.tmp" "$(CURDIR)/bench/results"; \
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_L2_BENCH_OUT="$$out" DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-run TestL2Budget -v -timeout 900s" \
	scripts/integration_local.sh > "$$log" 2>&1; st=$$?; grep -E 'PERF|--- (PASS|FAIL)' "$$log"; echo "raw: $$out"; exit $$st

bench-connectors: ## S16: vault open, redaction, egress classification and REST overhead (no network); JSON in bench/results/
	@mkdir -p bench/results; out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-connectors.json"; \
	$(GO) test -run '^$$' -bench . -benchmem -count 5 -json ./internal/vault ./internal/connector/egress ./internal/connector/rest > "$$out"; st=$$?; \
	grep -o '"Output":"Benchmark[^"]*' "$$out" | sed 's/"Output":"//; s/\\t/ /g; s/\\n//' | sort | uniq | head -40; echo "raw: $$out"; exit $$st

workflows-check: ## (ci) S35: the release workflow's gates, environments, permissions and SHA pins, actionlint, a self-test that a removed gate fails, and the npm publish script's tests (fixtures, no network)
	scripts/check-workflows.sh && scripts/check-workflows.sh --self-test
	node --test scripts/npm-publish.test.mjs

NPM_SNAPSHOT ?= .tmp/release/run1
npm-dry-run: ## release.yml's npm-stage on this machine: the seven npm packages staged from the GoReleaser snapshot in NPM_SNAPSHOT (make release-check) and the SDK packed from this tree, then the publish plan; nothing reaches npm
	@test -f "$(NPM_SNAPSHOT)/checksums.txt" || { echo "✕ no GoReleaser snapshot in $(NPM_SNAPSHOT). Next: make release-check (or NPM_SNAPSHOT=<dir>)"; exit 1; }
	@test -d $(SDK_TS)/node_modules || { echo "✕ $(SDK_TS) has no node_modules. Next: make sdk-ts"; exit 1; }
	@set -e; v="$$(node -p 'require("./npm/deedglass/package.json").version')"; \
	av="$$(node -p 'require(process.argv[1]).version' "$(abspath $(NPM_SNAPSHOT))/metadata.json")"; \
	out="$(CURDIR)/.tmp/npm-dry-run"; rm -rf "$$out"; mkdir -p "$$out/sdk"; \
	(cd $(SDK_TS) && npm run --silent build >/dev/null && npm pack --silent --pack-destination "$$out/sdk" >/dev/null); \
	node scripts/npm-publish.mjs stage --version="$$v" --archives="$(NPM_SNAPSHOT)" --archive-version="$$av" \
	  --sdk="$$out/sdk/deedglass-sdk-$$v.tgz" --out="$$out/staged"; \
	node scripts/npm-publish.mjs publish --out="$$out/staged" --dry-run

listings-check: ## (ci) S47: every listing kit (Claude Code plugin, Cursor, Supabase, Neon) installs the one server object, fits its limits and holds no secret; the generated install links and playground data are current
	node scripts/check-listings.mjs
	go run ./internal/installlinks/cmd/geninstall -check
	go run ./cmd/playgen -check

mcpb: ## S47B: the three Claude Desktop bundles in .tmp/mcpb, each manifest checked by the pinned @anthropic-ai/mcpb 2.1.2 (deploy/mcpb, ADR-090); nothing is published
	scripts/mcpb.sh

packs-check: ## S47B: the embedded policy-pack index is current and every pack passes its cases (in ci via go test ./internal/packs)
	$(GO) run ./cmd/packgen -check

release-check: ## S35: the release dry run: the Console build, two GoReleaser snapshots (reproducible linux/amd64 binary), 5 archives with completions, checksums, an SBOM each, the two-platform image (size, user, labels); nothing is pushed or signed
	@if command -v node >/dev/null 2>&1; then $(MAKE) -s web-build >/dev/null; fi
	scripts/verify-snapshot.sh
	node scripts/check-listings.mjs

cloud-smoke: ## S35: the cloud kit end to end on this machine (prod mode, Caddy's internal CA on 127.0.0.1:8443): health and routing, two seeded orgs, backup, destroy, restore into a fresh volume, export identical, chains intact; needs Docker and age
	DEEDGLASS_IT_THROWAWAY=1 deploy/cloud/scripts/smoke.sh

bench-micro: ## Go microbenchmarks for ids and config
	$(GO) test -run '^$$' -bench . -benchmem -cpu 1,8 ./internal/ids ./internal/config

DGBENCH    := $(GO) run ./bench/cmd/dgbench
DGBENCH_PG := scripts/bench_pg.sh $(GO) run -tags integration ./bench/cmd/dgbench

bench: ## S35: every suite (about 45 min, the soak is bench-soak): policy, verify, quickstart, then gate, stop, deed, wal, mcp and witness on a throwaway PG 18; JSON in bench/results/<date>-<host>-bench-<suite>.json, then bench-report
	$(DGBENCH) policy && $(DGBENCH) verify && DEEDGLASS_IT_THROWAWAY=1 $(DGBENCH) quickstart && \
	for s in gate stop deed wal mcp witness; do $(DGBENCH_PG) $$s || exit 1; done && $(DGBENCH) report

bench-quick: ## S35 (about 6 min, the release candidate): gate allow, policy 50, verify 100k, stop (5 trials); 15 s per scenario, one run; writes …-quick.json
	$(DGBENCH) policy --quick && $(DGBENCH) verify --quick && $(DGBENCH_PG) gate --quick && $(DGBENCH_PG) stop --quick

bench-soak: ## S35: 30 min at 50 calls/s (70% allow, 20% deny, 10% approval): RSS growth after minute 5, open files, connections (DEEDGLASS_BENCH_SOAK=10m shortens it)
	$(DGBENCH_PG) soak

bench-check: ## S35: the six contract §12 targets on the newest results (exit 1 on a miss, a battery or unknown-power run, or a stale file); BENCH_QUICK=1 also accepts quick files (the RC)
	$(DGBENCH) check $(if $(BENCH_QUICK),--quick,)

bench-report: ## S35: print the table, write bench/results/SUMMARY.json and regenerate the block in docs/BENCHMARKS.md (numbers are never typed)
	$(DGBENCH) report

bench-store: ## S02 store budget (OrgTx, insert, list) on a throwaway PG 18; raw go test -json in bench/results/
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_IT_BENCH_OUT="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-store.json" \
		scripts/integration_local.sh

cover: ## coverage of the packages with targets (ids 95%, config 90%, deed 90%, deed/archive 85%, policy 85%, sqlguard 90%, cmd/deedglass 80%)
	$(GO) test -count=1 -cover ./internal/ids ./internal/config ./internal/deed ./internal/deed/archive ./internal/policy ./internal/sqlguard ./cmd/deedglass

fuzz-policy: ## S10-S11: the policy compiler, the safe YAML reader, the evaluator and sqlguard, 60 s each
	$(GO) test ./internal/policy -run '^$$' -fuzz '^FuzzCompile$$' -fuzztime 60s
	$(GO) test ./internal/policy -run '^$$' -fuzz '^FuzzYAMLReader$$' -fuzztime 60s
	$(GO) test ./internal/policy -run '^$$' -fuzz '^FuzzEvaluate$$' -fuzztime 60s
	$(GO) test ./internal/sqlguard -run '^$$' -fuzz '^FuzzAnalyze$$' -fuzztime 60s
	$(GO) test ./internal/sqlguard -run '^$$' -fuzz '^FuzzTokenize$$' -fuzztime 60s

policy-test: ## (ci) S12: every starter policy's and every policy pack's cases pass, every rule covered, no voice warnings
	$(GO) run ./cmd/deedglass policy test --dir examples/policies --require-coverage --voice
	$(GO) run ./cmd/deedglass policy test --dir examples/demo/policies --require-coverage --voice
	$(GO) run ./cmd/deedglass policy test $(foreach d,$(wildcard internal/packs/snapshot/*/pack.yaml),$(dir $(d))$(notdir $(patsubst %/,%,$(dir $(d)))).yaml) --require-coverage --voice

fuzz-policy-short: ## (ci) the same policy and sqlguard fuzz targets, 10 s each (5 s for the tokenizer)
	$(GO) test ./internal/policy -run '^$$' -fuzz '^FuzzCompile$$' -fuzztime 10s
	$(GO) test ./internal/policy -run '^$$' -fuzz '^FuzzYAMLReader$$' -fuzztime 10s
	$(GO) test ./internal/policy -run '^$$' -fuzz '^FuzzEvaluate$$' -fuzztime 10s
	$(GO) test ./internal/sqlguard -run '^$$' -fuzz '^FuzzAnalyze$$' -fuzztime 10s
	$(GO) test ./internal/sqlguard -run '^$$' -fuzz '^FuzzTokenize$$' -fuzztime 5s

bench-eval: ## S11 budget: Evaluate p50/p99 at 50 and 500 rules (with and without explain) and sqlguard.Analyze at 200 B, 4 KiB, 256 KiB; JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-eval.json"; \
	DEEDGLASS_BENCH_OUT="$$out" $(GO) test -tags bench -count=1 -run '^TestLatency$$' -v ./internal/policy | grep -E 'µs|ok|FAIL'; st=$$?; \
	$(GO) test -count=1 -run '^$$' -bench '^Benchmark(Evaluate|Analyze)' -benchmem ./internal/policy ./internal/sqlguard | grep Benchmark; \
	echo "raw: $$out"; exit $$st

bench-policy: ## S10 budget: compile a 50-rule (< 20 ms) and a 500-rule (< 250 ms) policy; raw go test -json in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-policy.json"; \
	$(GO) test -json -count=3 -run '^$$' -bench '^BenchmarkCompile(50|500)$$' -benchmem -benchtime 3s ./internal/policy > "$$out"; st=$$?; \
	grep -ho '"Output":"BenchmarkCompile[^"]*' "$$out" | sed 's/"Output":"//; s/\\t/  /g; s/\\n//'; echo "raw: $$out"; exit $$st

bench-ledger: ## S06 Ledger budget at 100,000 actions (lists, get, chain check, export memory, seal overhead, EXPLAIN plans) on a throwaway PG 18; raw go test -json in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-ledger.json"; \
	DEEDGLASS_IT_BENCH_OUT="$$out" DEEDGLASS_IT_BENCH_RUN='TestLedgerBudget' DEEDGLASS_IT_BENCH_PKG=./internal/ledger/ scripts/integration_local.sh >/dev/null; st=$$?; \
	grep -o 'PERF {[^}]*}[^\\]*' "$$out" | sed 's/\\"/"/g'; echo "raw: $$out"; exit $$st

bench-stop: ## S08: Stop propagation across two instances (p50/p99) and the API-key budget, on a throwaway PG 18; JSON in bench/results/
	@d="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)"; \
	DEEDGLASS_IT_PKGS=./internal/identity/ DEEDGLASS_IT_GOFLAGS="-run TestStopPropagation" DEEDGLASS_IT_PROPAGATION_OUT="$$d-stop.json" \
	DEEDGLASS_IT_BENCH_OUT="$$d-keyauth.json" DEEDGLASS_IT_BENCH_RUN='TestKeyAuthBudget' DEEDGLASS_IT_BENCH_PKG=./internal/identity/ \
	scripts/integration_local.sh >/dev/null; st=$$?; \
	cat "$$d-stop.json"; grep -o 'PERF {[^}]*}[^\\]*' "$$d-keyauth.json" | sed 's/\\"/"/g'; echo "raw: $$d-stop.json $$d-keyauth.json"; exit $$st

bench-witness: ## S19: Decide (< 2 µs), L0 with a 64 KiB schema check, expect over 32 fields, a CEL postcondition; raw go test -json in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-witness.json"; \
	$(GO) test -json -count=3 -run '^$$' -bench '^Benchmark' -benchmem -benchtime 1s ./internal/witness > "$$out"; st=$$?; \
	grep -ho '"Output":"Benchmark[^"]*' "$$out" | sed 's/"Output":"//; s/\\t/  /g; s/\\n//'; echo "raw: $$out"; exit $$st

bench-approvals: ## S22: submit to pending, Decide, decide to worker pickup, the approval long poll's wake (300 approvals) on a throwaway PG 18; JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-approvals.json"; \
	DEEDGLASS_IT_PKGS=./internal/approve/ DEEDGLASS_IT_GOFLAGS="-run TestLinks/one_person" \
	DEEDGLASS_IT_BENCH_OUT="$$out" DEEDGLASS_IT_BENCH_RUN='TestApprovalsBudget' DEEDGLASS_IT_BENCH_PKG=./internal/approve/ scripts/integration_local.sh >/dev/null; st=$$?; \
	grep -o 'PERF {[^}]*}' "$$out" | sed 's/\\"/"/g'; echo "raw: $$out"; exit $$st

bench-slack: ## S29: Slack signature check (4 KiB), the approval card, GitHub execute overhead, the interaction handler's latency on fixtures; raw go test -json in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-s29.json"; \
	go test -json -run '^TestInteractLatency$$' -bench 'BenchmarkSlackVerify|BenchmarkGitHubExecute|BenchmarkBuildApprovalCard' -benchmem -count=1 -v \
	  ./internal/connector/slack/ ./internal/connector/github/ ./internal/notify/slack/ > "$$out"; st=$$?; \
	grep -o '"Output":"\(Benchmark[^"]*\|[^"]*handler [a-z]*: [^"]*\)' "$$out" | sed 's/"Output":"//; s/\\t/ /g; s/\\n//g'; echo "raw: $$out"; exit $$st

bench-gate: ## S13: Gate overhead (allow, no witness, fake.echo, concurrency 8, 1 org, 50 rules) on a throwaway PG 18: p50 < 5 ms, p99 < 25 ms; raw go test -json in bench/results/; PROFILE=1 also writes a CPU profile to .tmp/gate.cpu.pprof and prints its top (S36)
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-gate.json"; prof="$(CURDIR)/.tmp/gate.cpu.pprof"; mkdir -p "$(CURDIR)/.tmp"; \
	DEEDGLASS_IT_PKGS=./internal/gate/ DEEDGLASS_IT_GOFLAGS="-run TestAllowSealsL0Verified" \
	DEEDGLASS_IT_BENCH_GOFLAGS="$(if $(PROFILE),-cpuprofile $$prof,)" \
	DEEDGLASS_IT_BENCH_OUT="$$out" DEEDGLASS_IT_BENCH_RUN='TestGateBudget' DEEDGLASS_IT_BENCH_PKG=./internal/gate/ scripts/integration_local.sh >/dev/null; st=$$?; \
	grep -o 'PERF {[^}]*}' "$$out" | sed 's/\\"/"/g'; echo "raw: $$out"; \
	if [ -n "$(PROFILE)" ] && [ -f "$$prof" ]; then $(GO) tool pprof -top -nodecount=25 "$$prof" 2>/dev/null | head -35; echo "profile: $$prof"; fi; exit $$st

bench-intents: ## S09: signed-intent checks without the database (p99, 4 KiB args) and with the nonce insert on a throwaway PG 18; JSON in bench/results/
	@d="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)"; \
	DEEDGLASS_IT_BENCH=1 $(GO) test -json -count=1 -run '^TestIntentCheckBudget$$' -bench '^BenchmarkIntentCheck$$' -benchmem ./internal/identity > "$$d-intents.json"; st=$$?; \
	DEEDGLASS_IT_PKGS=./internal/identity/ DEEDGLASS_IT_GOFLAGS="-run TestIntentRoundTrip" \
	DEEDGLASS_IT_BENCH_OUT="$$d-intents-pg.json" DEEDGLASS_IT_BENCH_RUN='TestIntentBudget' DEEDGLASS_IT_BENCH_PKG=./internal/identity/ scripts/integration_local.sh >/dev/null || st=1; \
	grep -ho 'PERF {[^}]*}[^\\]*\|"Output":"BenchmarkIntentCheck[^"]*' "$$d-intents.json" "$$d-intents-pg.json" | sed 's/"Output":"//; s/\\t/  /g; s/\\n//; s/\\"/"/g'; \
	echo "raw: $$d-intents.json $$d-intents-pg.json"; exit $$st

bench-identity: ## S07 budget: session middleware, the login handler and an invite with its deed, on a throwaway PG 18; raw go test -json in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-identity.json"; \
	DEEDGLASS_IT_PKGS=./internal/identity/ DEEDGLASS_IT_BENCH_OUT="$$out" DEEDGLASS_IT_BENCH_RUN='TestIdentityBudget' DEEDGLASS_IT_BENCH_PKG=./internal/identity/ scripts/integration_local.sh >/dev/null; st=$$?; \
	grep -o 'PERF {[^}]*}[^\\]*' "$$out" | sed 's/\\"/"/g'; echo "raw: $$out"; exit $$st

bench-deed: ## S04 deed budget: SealBody, VerifyEnvelope (1 core) and the Postgres Sealer p50/p99 on a throwaway PG 18; raw go test -json in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-deed.json"; \
	$(GO) test -json -run '^$$' -bench 'SealBody|VerifyEnvelope|SealerMemStore|ChainCheck' -benchmem -cpu 1 -count 5 ./internal/deed > "$$out" && \
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_IT_BENCH_OUT="$$out.pg" DEEDGLASS_IT_BENCH_RUN='TestSealerBudget' scripts/integration_local.sh >/dev/null && \
	cat "$$out.pg" >> "$$out" && rm -f "$$out.pg" && \
	grep -o '"Output":"Benchmark[^"]*\|PERF {[^}]*}' "$$out" | sed 's/"Output":"//; s/\\t/  /g; s/\\n//; s/\\"/"/g'; echo "raw: $$out"

clean: ## remove build output and throwaway clusters
	rm -rf bin .tmp

dev: ## local stack in Docker: Postgres 18.6, migrations and the server on 127.0.0.1:8080 (creates .env on first run)
	@scripts/dev.sh up

dev-down: ## stop the local stack; the database volume is kept
	@scripts/dev.sh down

dev-reset: ## delete the local stack and its database volume (asks you to type reset)
	@scripts/dev.sh reset

dev-logs: ## follow the local stack's logs
	@scripts/dev.sh logs

run: ## the server from source against the compose Postgres, applying pending migrations first
	@scripts/dev.sh db
	$(GO) run ./cmd/deedglass serve --migrate

docker: ## build the deedglass:dev image from the root Dockerfile
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t deedglass:dev .

bench-http: ## S03 HTTP budget: pipeline, 404, cached health (50,000 runs) and the 50×2,000 load test; raw go test -json in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-http.json"; \
	{ $(GO) test -json -run '^$$' -bench 'PipelineVersion|PipelineNotFound|HealthCached' -benchmem -benchtime 50000x ./internal/httpx; \
	  $(GO) test -json -run '^$$' -bench 'PipelineLoad' -benchtime 1x ./internal/httpx; } > "$$out" && \
	grep -o '"Output":"Benchmark[^"]*' "$$out" | sed 's/"Output":"//; s/\\t/  /g; s/\\n//'; echo "raw: $$out"

bench-verify: ## S05 budget: deedglass verify on 100,000 deeds (.jsonl and bundle) and one deed cold; raw JSON in bench/results/
	bench/verify_100k.sh

genmark: ## regenerate the terminal mark's pixel grid (internal/cli/mark/orb_gen.go); TestGeneratedFresh fails on drift
	$(GO) generate ./internal/cli/mark

bench-mark: ## benchmark the terminal banner (budget < 200 µs, < 50 allocs)
	$(GO) test -run '^$$' -bench Banner -benchmem -count 5 ./internal/cli/mark

gencopy: ## regenerate the verification copy, key lists and sample for the browser verifier from Go
	$(GO) run ./internal/deed/cmd/gencopy

# The browser verifier (web/verify): TypeScript and Vitest are dev-only tools (ADR-027); no runtime dependencies.
WEB_VERIFY := web/verify
WEB := web

# --- the Console (S24): Astro 7, static, embedded from internal/console/dist (ADR-053) ---
web-install: ## npm ci for web/ (the Console's exact pins from package-lock.json, no install scripts)
	cd $(WEB) && npm ci --ignore-scripts --no-audit --no-fund

$(WEB)/node_modules/.package-lock.json: $(WEB)/package-lock.json
	cd $(WEB) && npm ci --ignore-scripts --no-audit --no-fund

web-tokens: $(WEB)/node_modules/.package-lock.json ## regenerate the token CSS, the verify token block, the favicon and the icon sprite from web/tokens/tokens.json
	cd $(WEB) && node scripts/gen-tokens.mjs && node scripts/icons.mjs

web-build: $(WEB)/node_modules/.package-lock.json ## tokens → icons → astro build → .br/.gz → check-dist (budgets, no inline code); writes internal/console/dist
	cd $(WEB) && ASTRO_TELEMETRY_DISABLED=1 npm run build

web-test: $(WEB)/node_modules/.package-lock.json ## (ci) typecheck, Vitest (api, stream, format, palette, dom, tokens, contrast, copy, icons, sinks, types)
	cd $(WEB) && npx tsc -p . --noEmit && npx vitest run && node scripts/contrast.mjs | tail -1

web-e2e: web-build build-only ## Playwright (system Chrome) against a throwaway PG 18 and a real serve: shell, keyboard, palette, Stop all, live events, axe, CSP, screenshots
	DEEDGLASS_IT_THROWAWAY=1 scripts/web_e2e.sh test

web-lighthouse: web-build build-only ## Lighthouse 13 (accessibility and best practices ≥ 95) on /app, /app/_kit and /login, desktop and mobile; JSON in ~/txv/ops/plan/prompts-v7/reports/lighthouse/S24
	DEEDGLASS_IT_THROWAWAY=1 scripts/web_e2e.sh lighthouse

web-dev: $(WEB)/node_modules/.package-lock.json ## astro dev on :4321, proxying /v1, /login, /auth and /.well-known to :8080 (start serve with DEEDGLASS_BASE_URL=http://localhost:4321)
	cd $(WEB) && ASTRO_TELEMETRY_DISABLED=1 npx astro dev --port 4321

ops-build: ## (S40) bin/deedglass-ops: the ops build with the outbound_mail connector (-tags outboundmail); never released
	$(GO) build -trimpath -tags outboundmail -ldflags '$(LDFLAGS)' -o bin/deedglass-ops ./cmd/deedglass

build-only: ## bin/deedglass from the current internal/console/dist (no web build)
	$(GO) build -trimpath $(GCFLAGS) -ldflags '$(LDFLAGS)' -o bin/deedglass ./cmd/deedglass

web-verify-install: ## npm ci for web/verify (exact pins from package-lock.json, no install scripts)
	cd $(WEB_VERIFY) && npm ci --ignore-scripts --no-audit --no-fund

$(WEB_VERIFY)/node_modules/.package-lock.json: $(WEB_VERIFY)/package-lock.json
	cd $(WEB_VERIFY) && npm ci --ignore-scripts --no-audit --no-fund

web-verify-test: $(WEB_VERIFY)/node_modules/.package-lock.json ## (ci) typecheck and the Vitest suites (vectors, parity 500/500, strict, JCS, keys, page safety)
	cd $(WEB_VERIFY) && npx tsc -p . --noEmit && npx vitest run

web-verify-build: $(WEB_VERIFY)/node_modules/.package-lock.json ## compile the page's modules into web/verify/dist
	cd $(WEB_VERIFY) && rm -rf dist && npx tsc -p tsconfig.build.json

web-verify-serve: web-verify-build ## the verify page on http://127.0.0.1:5178 (no dependency server)
	node $(WEB_VERIFY)/scripts/serve.mjs

# --- the TypeScript SDK (S31): @deedglass/sdk, zero runtime dependencies; dev tools pinned in sdk/typescript/package-lock.json (ADR-062) ---
SDK_TS := sdk/typescript

# S33: docs.deedglass.com (docs-site/, Astro + Starlight, ADR-064). Dev and build tools only, exact pins,
# installed without install scripts; nothing from npm runs on the published site.
DOCS := docs-site
$(DOCS)/node_modules/.package-lock.json: $(DOCS)/package-lock.json
	cd $(DOCS) && npm ci --ignore-scripts --no-audit --no-fund

docs: $(DOCS)/node_modules/.package-lock.json ## S33: build docs-site/dist (imports docs/*.md, generates /errors, CLI, config and attack pages, postbuild headers and redirects)
	cd $(DOCS) && npm run build --silent

docs-dev: $(DOCS)/node_modules/.package-lock.json ## S33: the docs dev server on http://127.0.0.1:4321 (drafts shown)
	cd $(DOCS) && npm run dev

docs-check: docs ## S33: every offline docs gate: generated data fresh, unit tests, spelling, banned words, pages, drafts, pricing, claims, css, install links, workflows, errors, links and dist
	cd $(DOCS) && node scripts/check-all.mjs --offline

docs-snippets: $(DOCS)/node_modules/.package-lock.json ## S33: run the docs' sh test blocks against the demo stack (make demo-up DEMO_PORT=8091 first; the Quickstart runs deedglass init in Docker) and type-check python/ts blocks
	@test -f examples/demo/.demo-keys || { echo "✕ The demo stack isn't up. Next: make demo-up DEMO_PORT=8091"; exit 1; }
	$(MAKE) --no-print-directory build-only
	cd $(DOCS) && DOCS_SERVER=http://127.0.0.1:$${DEMO_PORT:-8091} node scripts/test-snippets.mjs

docs-lighthouse: docs ## S33: Lighthouse 13 (from web/) on the six docs pages, desktop and mobile; JSON in ~/txv/ops/plan/prompts-v7/reports/lighthouse/S33
	cd $(DOCS) && node scripts/lighthouse.mjs

$(SDK_TS)/node_modules/.package-lock.json: $(SDK_TS)/package-lock.json
	cd $(SDK_TS) && npm ci --ignore-scripts --no-audit --no-fund

sdk-ts: $(SDK_TS)/node_modules/.package-lock.json ## (ci) S31: the verify copy is current, tsc, Vitest (transport, wrapTool, intents and deed vectors, parity 500/500, the four adapters on their real frameworks, coverage), build, deps, pack golden, publint, attw, size budgets
	cd $(SDK_TS) && node scripts/sync-verify.mjs --check && npx tsc -p . --noEmit && npx vitest run --project unit --coverage && npm run build --silent \
	  && node scripts/deps-check.mjs && node scripts/pack-check.mjs && npx publint --strict && npx attw --pack . --profile node16 --ignore-rules cjs-resolves-to-esm --quiet && node scripts/size.mjs

sdk-ts-it: $(SDK_TS)/node_modules/.package-lock.json ## S31: the SDK against a real serve on a throwaway PG 18 (act, denial, pending → approve, wrapTool, intents conformance, deeds verify)
	DEEDGLASS_IT_SDK_TS=1 DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run ^TestSDKTypeScriptLive$$" scripts/integration_local.sh

sdk-sync-verify: ## S31: fail when sdk/typescript/src/verify drifted from web/verify/src (npm run sync-verify there to copy)
	node $(SDK_TS)/scripts/sync-verify.mjs --check

sdk-runtime: $(SDK_TS)/node_modules/.package-lock.json ## S31: the packed SDK on Node 22.12, 24 and 26 (Docker when not local), Bun and Deno when installed
	cd $(SDK_TS) && npm run build --silent && scripts/runtime-matrix.sh

sdk-examples: $(SDK_TS)/node_modules/.package-lock.json ## S31: every examples/ts example in scripted mode against make demo-up (sdk-demo.yaml active; DEEDGLASS_API_KEY an agent key)
	cd $(SDK_TS) && npm run build --silent
	scripts/sdk_examples.sh

# --- the Acme demo stack (S15; examples/demo/README.md) ---

demo-up: ## S15: the Acme demo stack (orders DB, Deedglass, policy, agents); HTTP=1 adds the everything upstream; DEMO_PORT=8091 if 8080 is taken
	@examples/demo/scripts/demo.sh up

demo-down: ## stop the demo stack (data kept)
	@examples/demo/scripts/demo.sh down

demo-reset: ## reload the orders database and /tmp/deedglass-demo for a new take; HARD=1 also wipes the Ledger
	@examples/demo/scripts/demo.sh reset

demo-status: ## one ✓ or ✕ line per part of the demo stack
	@examples/demo/scripts/demo.sh status

demo-snippet: ## MCP client config for the demo: CLIENT=claude-desktop|claude-code|cursor|vscode|windsurf [KEYS=1 fills the agent key]
	@examples/demo/scripts/demo.sh snippet

demo-psql: ## psql on the Acme orders database as acme_owner
	@examples/demo/scripts/demo.sh psql

demo-follow: ## the demo terminal: deedglass actions list --follow --connector orders
	@examples/demo/scripts/demo.sh follow

demo-verify: ## export the demo's recent deeds (SINCE=10m) and verify the chain
	@examples/demo/scripts/demo.sh verify

demo-check: ## S17: the Postgres connector's readiness report on the demo's orders database (make demo-up first)
	@examples/demo/scripts/demo.sh check

demo-trigger-on: ## S21: the hero's legacy trigger on the demo's orders-db (it ships 300 more orders when one ships)
	@examples/demo/scripts/demo.sh trigger-on

demo-trigger-off: ## S21: drop the hero's trigger
	@examples/demo/scripts/demo.sh trigger-off

demo-order-reset: ## S21: ORDER=42 sets one order back to paid; no ORDER puts every order back to the seed's status
	@examples/demo/scripts/demo.sh order-reset

demo-hero: ## S21: the L2 hero in one command (VERIFIED, then MISMATCH with 301 rows, the diff, verify); HERO_PACE=2 for recording
	@examples/demo/scripts/demo.sh hero

demo-capture: ## Record the README demo: the real make demo-hero, each line with its time, into docs/assets/demo-hero.cast.json (make demo-up DEMO_PORT=8091 first; a clean tree)
	node scripts/demo/capture.mjs

demo-svg: ## Draw the README demo (dark and light SVG) from docs/assets/demo-hero.cast.json; no stack needed
	node scripts/demo/cast2svg.mjs docs/assets/demo-hero.cast.json docs/assets/demo-hero.svg
	node scripts/demo/cast2svg.mjs --theme light docs/assets/demo-hero.cast.json docs/assets/demo-hero-light.svg

social-preview: ## Render docs/assets/social-preview.png (1280x640) from docs/assets/social-preview.html with headless Chrome
	scripts/demo/social-preview.sh

demo-scan-golden: build-only ## S47B: the demo's blast-radius score golden (scan as acme_owner, trigger on) against the running stack (make demo-up first)
	DEEDGLASS_DEMO=1 $(GO) test -tags demo -count=1 -run TestDemoScanScore ./examples/demo/

demo-diff: ## S21: deedglass actions show --diff for ID=act_… (default: the newest postgres.execute action)
	@examples/demo/scripts/demo.sh diff

demo-approvals-on: ## S22: enforce acme-demo-approvals.yaml (Claude Desktop's writes to the orders database wait for a person)
	@examples/demo/scripts/demo.sh approvals-on

demo-approvals-off: ## S22: enforce acme-demo.yaml again
	@examples/demo/scripts/demo.sh approvals-off

demo-approver: ## S22: EMAIL=you@… becomes an admin of the demo org, so approval emails reach it
	@EMAIL="$(EMAIL)" examples/demo/scripts/demo.sh approver

demo-lan: ## S22: serve the demo on this Mac's Wi-Fi address so a phone can open approval links (anyone on the Wi-Fi can reach it)
	@examples/demo/scripts/demo.sh lan

demo-lan-off: ## S22: back to 127.0.0.1 only
	@examples/demo/scripts/demo.sh lan-off

demo-hero-check: ## S21: TestHeroScript, hero.sh twice with demo-reset between (make demo-up first)
	DEEDGLASS_DEMO=1 $(GO) test -tags demo -count=1 -v -run TestHeroScript ./examples/demo/

demo: ## S37: the launch demo offline: a fresh stack (demo-reset HARD=1), approvals off, examples/demo/launch.yaml (7 scenes); DEMO_PORT=8091 if 8080 is taken
	@examples/demo/scripts/demo.sh demo

demo-live: ## S37: the same script with Slack, Stripe test mode and a person (preflight first; --live --step)
	@examples/demo/scripts/demo.sh live

demo-launch-check: ## S37: TestLaunchDemo, make demo twice, normalized output against examples/demo/testdata/launch.golden (UPDATE=1 rewrites it)
	DEEDGLASS_DEMO=1 $(GO) test -tags demo -count=1 -v -timeout 30m -run TestLaunchDemo ./examples/demo/

demo-smoke: ## TestDemoScript against the running demo stack (make demo-up first)
	DEEDGLASS_DEMO=1 $(GO) test -tags demo -count=1 -v -run TestDemoScript ./examples/demo/

bench-gateway: ## S15: /mcp/native fake.echo and the gateway's added latency with an in-process upstream, on a throwaway PG 18; JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-gateway.json"; log="$(CURDIR)/.tmp/bench-gateway.log"; mkdir -p "$(CURDIR)/.tmp"; \
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_GATEWAY_BENCH_OUT="$$out" DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-run TestGatewayBudget -v" \
	scripts/integration_local.sh > "$$log" 2>&1; st=$$?; grep -E 'PERF|over budget|--- (PASS|FAIL)' "$$log"; echo "raw: $$out"; exit $$st

undo-matrix: ## S23: the undo round trips and the property test (2,000 seeds) on throwaway PG 16, 17 and 18 in turn; logs in .tmp/undo-matrix-<v>.log
	@mkdir -p "$(CURDIR)/.tmp"; for v in 16 17 18; do echo "=== PostgreSQL $$v"; log="$(CURDIR)/.tmp/undo-matrix-$$v.log"; \
	DEEDGLASS_IT_PG=$$v DEEDGLASS_UNDO_SEEDS=2000 DEEDGLASS_IT_PKGS=./internal/connector/postgres/ DEEDGLASS_IT_GOFLAGS="-run TestUndoRoundTrip -v -timeout 1800s" \
	scripts/integration_local.sh > "$$log" 2>&1; st=$$?; grep -E 'UNDO-PROPERTY|KNOWN ISSUE|--- (FAIL|SKIP)|^(ok|FAIL)[[:space:]]' "$$log"; echo "log: $$log"; \
	[ $$st -eq 0 ] || exit $$st; done

bench-undo: ## S23 §3.10: plan 1,000 rows, preview 301, RunUndo 301 and 1,000, commit to frame, on a throwaway PG 18; JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-undo.json"; mkdir -p "$(CURDIR)/bench/results"; \
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_UNDO_BENCH_OUT="$$out" DEEDGLASS_IT_PKGS=./internal/connector/postgres/ DEEDGLASS_IT_GOFLAGS="-run TestUndoBudget -v -timeout 900s" \
	scripts/integration_local.sh && echo "wrote $$out"

.PHONY: bench-events
bench-events: ## S24 §3.12: the event hub, 1,000 events/s to 200 SSE streams (p99 enqueue→write < 50 ms) and memory per stream (< 64 KiB); JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-events.json"; mkdir -p "$(CURDIR)/bench/results"; \
	DEEDGLASS_EVENTS_BENCH_OUT="$$out" $(GO) test -count=1 -run '^TestHubBudget$$' -v ./internal/console/ > "$$out.log" 2>&1; st=$$?; \
	grep -E '^(--- |ok|FAIL)|"(p50|p99|kib_per_subscriber|events_per_s)"' "$$out.log"; rm -f "$$out.log"; echo "raw: $$out"; exit $$st

.PHONY: bench-insights
bench-insights: ## S27: replay, stats (p99 + EXPLAIN), detect and FactsCache at 100k actions / 20 agents, then S13's gate budget with the anomaly opt-in on, on a throwaway PG 18; raw go test -json in bench/results/
	@d="$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)"; out="$(CURDIR)/bench/results/$$d-insights.json"; gout="$(CURDIR)/bench/results/$$d-gate-anomalies.json"; mkdir -p "$(CURDIR)/bench/results"; \
	DEEDGLASS_IT_BENCH_OUT="$$out" DEEDGLASS_IT_BENCH_RUN='TestInsightsBudget' DEEDGLASS_IT_BENCH_PKG=./internal/learn/ \
	DEEDGLASS_IT_PKGS=./internal/learn/ DEEDGLASS_IT_GOFLAGS='-run ^$$' scripts/integration_local.sh >/dev/null; st=$$?; \
	grep -o 'PERF {[^}]*}' "$$out" | sed 's/\\"/"/g'; echo "raw: $$out"; st1=$$st; \
	DEEDGLASS_IT_BENCH_ANOMALIES=1 DEEDGLASS_IT_BENCH_OUT="$$gout" DEEDGLASS_IT_BENCH_RUN='TestGateBudget' DEEDGLASS_IT_BENCH_PKG=./internal/gate/ \
	DEEDGLASS_IT_PKGS=./internal/gate/ DEEDGLASS_IT_GOFLAGS='-run ^$$' scripts/integration_local.sh >/dev/null; st=$$?; \
	grep -o 'PERF {[^}]*}' "$$gout" | sed 's/\\"/"/g'; echo "raw: $$gout"; [ $$st1 -eq 0 ] && exit $$st || exit $$st1

.PHONY: bench-overview
bench-overview: ## S25 §3.8: GET /v1/overview's read (cache bypassed) at 100,000 actions in 24 h, both windows (p99 < 80 ms) and the EXPLAIN plans, on a throwaway PG 18; raw go test -json in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-overview.json"; mkdir -p "$(CURDIR)/bench/results"; \
	DEEDGLASS_IT_BENCH_OUT="$$out" DEEDGLASS_IT_BENCH_RUN='TestOverviewBudget' DEEDGLASS_IT_BENCH_PKG=./internal/ledger/ \
	DEEDGLASS_IT_PKGS=./internal/ledger/ DEEDGLASS_IT_GOFLAGS='-run ^$$' scripts/integration_local.sh >/dev/null; st=$$?; \
	grep -o 'PERF {[^}]*}[^\\]*' "$$out" | sed 's/\\"/"/g'; echo "raw: $$out"; exit $$st

# --- S32: the Python SDK (sdk/python), ADR-063 ---
PY_SDK := sdk/python
PY ?= $(shell for p in python3.13 python3.14 python3.12 python3.11 python3.10 python3; do command -v $$p >/dev/null 2>&1 && { echo $$p; break; }; done)

$(PY_SDK)/.venv/.installed: $(PY_SDK)/requirements-dev.txt $(PY_SDK)/pyproject.toml
	@test -n "$(PY)" || { echo "py: Python 3.10 or newer isn't installed. Install it (brew install python@3.13), then run make py-ci again."; exit 1; }
	$(PY) -m venv $(PY_SDK)/.venv
	$(PY_SDK)/.venv/bin/pip install -q --disable-pip-version-check -r $(PY_SDK)/requirements-dev.txt
	$(PY_SDK)/.venv/bin/pip install -q --disable-pip-version-check --no-deps -e $(PY_SDK)
	@touch $@

py-ci: $(PY_SDK)/.venv/.installed ## (ci) S32: the Python SDK: ruff, ruff format --check, mypy --strict, pytest (vectors, parity 500/500, intents, client table sync and async, adapters on LangChain and the Claude Agent SDK) with coverage >= 90%
	cd $(PY_SDK) && .venv/bin/ruff check src tests && .venv/bin/ruff format --check src tests && .venv/bin/mypy && .venv/bin/python -m pytest -q --cov --cov-report=term:skip-covered

py-build: $(PY_SDK)/.venv/.installed ## S32: the wheel and sdist (python -m build), twine check, and the wheel holds py.typed and no tests
	rm -rf $(PY_SDK)/dist && cd $(PY_SDK) && .venv/bin/python -m build -q . && .venv/bin/twine check dist/*
	@unzip -l $(PY_SDK)/dist/*.whl | grep -q 'deedglass/py.typed' || { echo "py-build: the wheel lacks deedglass/py.typed"; exit 1; }
	@! unzip -l $(PY_SDK)/dist/*.whl | grep -q 'tests/' || { echo "py-build: the wheel holds tests"; exit 1; }
	@echo "py-build: $$(ls $(PY_SDK)/dist)"

py-examples: $(PY_SDK)/.venv/.installed ## S32: examples/python in scripted mode against DEEDGLASS_SERVER (make demo-up; DEEDGLASS_API_KEY an agent key)
	scripts/py_examples.sh
