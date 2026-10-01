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
	web-verify-install web-verify-test web-verify-build web-verify-serve gencopy

all: ci

help: ## list the targets
	@grep -E '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-15s %s\n", $$1, $$2}'

ci: fmt-check tidy-check vet test testdata-check rename-guard banned-words nocgo web-verify-test ## everything the lint-unit and web-verify CI jobs run

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

matrix: ## integration on 16, 17 and 18
	@for v in 16 17 18; do echo "=== PostgreSQL $$v"; DEEDGLASS_IT_PG=$$v scripts/integration_local.sh || exit 1; done

fuzz: ## 30 s each: the strict intent parser, the id parser, and the deed envelope, verifier and key-set parsers
	$(GO) test ./internal/intent -run '^$$' -fuzz FuzzParse -fuzztime 30s
	$(GO) test ./internal/ids -run '^$$' -fuzz FuzzParse -fuzztime 30s
	$(GO) test ./internal/deed -run '^$$' -fuzz '^FuzzParseEnvelope$$' -fuzztime 30s
	$(GO) test ./internal/deed -run '^$$' -fuzz '^FuzzVerifyEnvelope$$' -fuzztime 30s
	$(GO) test ./internal/deed -run '^$$' -fuzz '^FuzzParseKeySet$$' -fuzztime 30s
	$(GO) test ./internal/deed -run '^$$' -fuzz '^FuzzVerifyStream$$' -fuzztime 30s
	$(GO) test ./internal/deed/archive -run '^$$' -fuzz '^FuzzArchiveOpen$$' -fuzztime 30s

bench: ## benchmarks for ids and config
	$(GO) test -run '^$$' -bench . -benchmem -cpu 1,8 ./internal/ids ./internal/config

bench-store: ## S02 store budget (OrgTx, insert, list) on a throwaway PG 18; raw go test -json in bench/results/
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_IT_BENCH_OUT="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-store.json" \
		scripts/integration_local.sh

cover: ## coverage of the packages with targets (ids 95%, config 90%, deed 90%, deed/archive 85%, cmd/deedglass 80%)
	$(GO) test -count=1 -cover ./internal/ids ./internal/config ./internal/deed ./internal/deed/archive ./cmd/deedglass

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
