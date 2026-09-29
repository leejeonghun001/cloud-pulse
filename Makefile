export CGO_ENABLED=0

MODULE   := github.com/leejeonghun001/cloud-pulse
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)

.PHONY: help
help: ## Show this help
	@echo "cloud-pulse make targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: hooks
hooks: ## Point git at the versioned hooks in .githooks/
	git config core.hooksPath .githooks

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -l -w .

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run the full pre-commit check suite on demand
	CP_CHECK_ALL=1 .githooks/pre-commit

.PHONY: staticcheck
staticcheck: ## Run pinned staticcheck with the Go 1.25 toolchain
	GOTOOLCHAIN=go1.25.0 go run honnef.co/go/tools/cmd/staticcheck@v0.6.1 ./...

.PHONY: test-py
test-py: ## Compile Python helpers and run stdlib unit tests
	python3 -m py_compile scripts/*.py
	python3 -m unittest discover -s scripts/tests

.PHONY: test-js
test-js: ## Syntax-check dashboard modules and run Node tests
	find web/assets/js -type f -name '*.js' -exec node --check {} +
	node --test web/test/*.test.mjs

.PHONY: test
test: ## Run all tests (CGO always disabled)
	CGO_ENABLED=0 go test ./...

.PHONY: build
build: ## Build hub + agent binaries into ./bin
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/cloud-pulse-hub ./cmd/hub
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/cloud-pulse-agent ./cmd/agent

.PHONY: release
release: ## Build and package release assets for all platforms
	scripts/build-release.sh $(VERSION) dist

.PHONY: css
css: ## Rebuild web/assets/app.css from web/src/input.css via Tailwind CLI
	npx --yes @tailwindcss/cli@4.3.3 -i web/src/input.css -o web/assets/app.css --minify

.PHONY: run-hub
run-hub: ## Run the hub locally with dev-friendly settings (open CIDR, dev token)
	CP_AGENT_TOKEN=dev-token-please-change-me-0000 \
	CP_ALLOWED_CIDRS='*' \
	CP_LISTEN=:8090 \
	CP_DATA_DIR=./data \
	go run ./cmd/hub

.PHONY: run-agent
run-agent: ## Run the agent locally against the dev hub above
	CP_HUB_URL=http://127.0.0.1:8090 \
	CP_AGENT_TOKEN=dev-token-please-change-me-0000 \
	go run ./cmd/agent

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.out
