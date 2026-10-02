# Deedglass product repo. `make help` lists the targets; (ci) marks what the lint-unit CI job runs.
GO ?= go

VERSION  ?= 0.1.0-dev
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
MODIFIED := $(shell if [ -n "$$(git status --porcelain 2>/dev/null)" ]; then echo true; else echo false; fi)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BI       := github.com/deedglass/deedglass/internal/buildinfo
LDFLAGS  := -s -w -X $(BI).Version=$(VERSION) -X $(BI).Commit=$(COMMIT) -X $(BI).Modified=$(MODIFIED) -X $(BI).Date=$(DATE)

.PHONY: all help ci build test test-short vet fmt fmt-check tidy-check testdata-check rename-guard banned-words nocgo \
	integration matrix fuzz bench bench-store bench-deed cover clean \
	dev dev-down dev-reset dev-logs run docker bench-http bench-verify \
	web-verify-install web-verify-test web-verify-build web-verify-serve gencopy genmark bench-mark bench-ledger bench-stop bench-intents bench-gate bench-witness policy-test \
	fuzz-policy fuzz-policy-short bench-policy bench-eval mcp-it bench-mcp \
	demo-up demo-down demo-reset demo-status demo-snippet demo-psql demo-follow demo-verify demo-check demo-smoke bench-gateway \
	demo-trigger-on demo-trigger-off demo-order-reset demo-hero demo-hero-check demo-diff bench-l2 bench-approvals \
	mock-orders bench-connectors bench-postgres bench-decoder attack attack-report vuln

all: ci

help: ## list the targets
	@grep -E '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-15s %s\n", $$1, $$2}'

ci: fmt-check tidy-check vet test fuzz-policy-short policy-test testdata-check rename-guard banned-words nocgo web-verify-test ## everything the lint-unit and web-verify CI jobs run

build: ## build bin/deedglass with version ldflags
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/deedglass ./cmd/deedglass

test: ## (ci) unit tests with the race detector
	$(GO) test -race -count=1 ./...

test-short: ## unit tests without -race, in short mode
	$(GO) test -short -count=1 ./...

vet: ## (ci) go vet
	$(GO) vet ./...

fmt: ## rewrite Go files with gofmt
	gofmt -w $$(git ls-files -co --exclude-standard '*.go')

fmt-check: ## (ci) fail if gofmt would change anything
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy-check: ## (ci) fail if go.mod/go.sum are not tidy
	$(GO) mod tidy -diff

testdata-check: ## (ci) verify the offline test vectors
	cd testdata && shasum -a 256 -c --quiet SHA256SUMS

rename-guard: ## (ci) fail on any retired identifier (docs/NAMING.md, ADR-012)
	@scripts/rename_guard.sh

banned-words: ## (ci) banned words in files and commit messages
	@if git rev-parse -q --verify HEAD >/dev/null; then scripts/banned_words.sh --commits HEAD; else scripts/banned_words.sh; fi

nocgo: ## (ci) the binary builds without cgo
	CGO_ENABLED=0 $(GO) build -o /dev/null ./cmd/deedglass

integration: ## throwaway PG cluster (Homebrew, else Docker): sql/ smoke, control-plane migrations, Go integration suites; DEEDGLASS_IT_PG=18|17|16 DEEDGLASS_IT_MODE=brew|docker
	scripts/integration_local.sh

mcp-it: ## S14: the real @modelcontextprotocol/server-filesystem via npx behind deedglass mcp wrap, on a throwaway PG 18 (needs Node.js; skips without it)
	DEEDGLASS_IT_NODE=1 DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run TestMCPWrap" scripts/integration_local.sh

bench-mcp: ## S14 §3.10: the wrapper's relay, tools/call and tools/list overhead and memory against a real Gate on a throwaway PG 18; JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-mcp.json"; log="$(CURDIR)/.tmp/bench-mcp.log"; mkdir -p "$(CURDIR)/.tmp"; \
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_MCP_BENCH_OUT="$$out" DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-run TestMCPBudget -v" \
	scripts/integration_local.sh > "$$log" 2>&1; st=$$?; grep -E 'PERF|over budget|--- (PASS|FAIL)' "$$log"; echo "raw: $$out"; exit $$st

attack: ## S18: the V7 attack suite on a throwaway PG 18 (a real serve, real Postgres, loopback upstreams); writes attack/results/latest.json, then docs/ATTACK_SUITE.md
	scripts/attack_local.sh
	$(GO) run ./attack/cmd/attack-report

attack-report: ## S18: print the last attack run and regenerate docs/ATTACK_SUITE.md from the meta and attack/results/latest.json (-check in CI)
	$(GO) run ./attack/cmd/attack-report

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

bench: ## benchmarks for ids and config
	$(GO) test -run '^$$' -bench . -benchmem -cpu 1,8 ./internal/ids ./internal/config

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

policy-test: ## (ci) S12: every starter policy's cases pass, every rule covered, no voice warnings
	$(GO) run ./cmd/deedglass policy test --dir examples/policies --require-coverage --voice
	$(GO) run ./cmd/deedglass policy test --dir examples/demo/policies --require-coverage --voice

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

bench-gate: ## S13: Gate overhead (allow, no witness, fake.echo, concurrency 8, 1 org, 50 rules) on a throwaway PG 18: p50 < 5 ms, p99 < 25 ms; raw go test -json in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-gate.json"; \
	DEEDGLASS_IT_PKGS=./internal/gate/ DEEDGLASS_IT_GOFLAGS="-run TestAllowSealsL0Verified" \
	DEEDGLASS_IT_BENCH_OUT="$$out" DEEDGLASS_IT_BENCH_RUN='TestGateBudget' DEEDGLASS_IT_BENCH_PKG=./internal/gate/ scripts/integration_local.sh >/dev/null; st=$$?; \
	grep -o 'PERF {[^}]*}' "$$out" | sed 's/\\"/"/g'; echo "raw: $$out"; exit $$st

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

demo-smoke: ## TestDemoScript against the running demo stack (make demo-up first)
	DEEDGLASS_DEMO=1 $(GO) test -tags demo -count=1 -v -run TestDemoScript ./examples/demo/

bench-gateway: ## S15: /mcp/native fake.echo and the gateway's added latency with an in-process upstream, on a throwaway PG 18; JSON in bench/results/
	@out="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-gateway.json"; log="$(CURDIR)/.tmp/bench-gateway.log"; mkdir -p "$(CURDIR)/.tmp"; \
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_GATEWAY_BENCH_OUT="$$out" DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-run TestGatewayBudget -v" \
	scripts/integration_local.sh > "$$log" 2>&1; st=$$?; grep -E 'PERF|over budget|--- (PASS|FAIL)' "$$log"; echo "raw: $$out"; exit $$st
