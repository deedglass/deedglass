# Deedglass product repo. `make help` lists the targets; (ci) marks what the lint-unit CI job runs.
GO ?= go

VERSION  ?= 0.1.0-dev
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
MODIFIED := $(shell if [ -n "$$(git status --porcelain 2>/dev/null)" ]; then echo true; else echo false; fi)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BI       := github.com/deedglass/deedglass/internal/buildinfo
LDFLAGS  := -s -w -X $(BI).Version=$(VERSION) -X $(BI).Commit=$(COMMIT) -X $(BI).Modified=$(MODIFIED) -X $(BI).Date=$(DATE)

.PHONY: all help ci build test test-short vet fmt fmt-check tidy-check testdata-check rename-guard banned-words nocgo \
	integration matrix fuzz bench bench-store cover clean

all: ci

help: ## list the targets
	@grep -E '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-15s %s\n", $$1, $$2}'

ci: fmt-check tidy-check vet test testdata-check rename-guard banned-words nocgo ## everything the lint-unit CI job runs

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

fuzz: ## 30 s fuzz of the strict intent parser and the id parser
	$(GO) test ./internal/intent -run '^$$' -fuzz FuzzParse -fuzztime 30s
	$(GO) test ./internal/ids -run '^$$' -fuzz FuzzParse -fuzztime 30s

bench: ## benchmarks for ids and config
	$(GO) test -run '^$$' -bench . -benchmem -cpu 1,8 ./internal/ids ./internal/config

bench-store: ## S02 store budget (OrgTx, insert, list) on a throwaway PG 18; raw go test -json in bench/results/
	DEEDGLASS_IT_BENCH=1 DEEDGLASS_IT_BENCH_OUT="$(CURDIR)/bench/results/$$(date +%F)-$$(go env GOOS)-$$(go env GOARCH)-store.json" \
		scripts/integration_local.sh

cover: ## coverage of the packages with targets (ids 95%, config 90%, cmd/deedglass 80%)
	$(GO) test -count=1 -cover ./internal/ids ./internal/config ./cmd/deedglass

clean: ## remove build output and throwaway clusters
	rm -rf bin .tmp
