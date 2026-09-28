# txv product repo. Targets used by CI are marked (ci).
GO ?= go

.PHONY: all ci build test test-short vet fmt fmt-check tidy-check testdata-check banned-words integration matrix fuzz clean

all: ci

ci: fmt-check tidy-check vet test testdata-check banned-words ## everything the lint-unit CI job runs

build: ## build bin/txv
	$(GO) build -o bin/txv ./cmd/txv

test: ## (ci) unit tests with the race detector
	$(GO) test -race -count=1 ./...

test-short: ## unit tests without -race
	$(GO) test -count=1 ./...

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

banned-words: ## (ci) plan §1.4 banned words in files and commit messages
	@if git rev-parse -q --verify HEAD >/dev/null; then scripts/banned_words.sh --commits HEAD; else scripts/banned_words.sh; fi

integration: ## throwaway PG cluster (Homebrew, else Docker) + sql/ + smoke; TXV_PG=18|17|16 TXV_IT_MODE=brew|docker
	scripts/integration_local.sh

matrix: ## integration on 16, 17 and 18 (local gate evidence; plan §9.1)
	@for v in 16 17 18; do echo "=== PostgreSQL $$v"; TXV_PG=$$v scripts/integration_local.sh || exit 1; done

fuzz: ## short fuzz run of the strict intent parser
	$(GO) test ./internal/intent -run '^$$' -fuzz FuzzParse -fuzztime 30s

clean:
	rm -rf bin .tmp
