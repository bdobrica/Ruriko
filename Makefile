.PHONY: all build build-ruriko build-gitai build-tools test test-integration test-integration-nlp test-saito-scheduling test-saito-scheduling-live-precheck test-saito-scheduling-live test-ruriko-saito-operator-live test-canonical-workflow-live-provisioning test-canonical-workflow-live-admin-room test-canonical-workflow-live-compose test-canonical-workflow-live-compose-3cycles test-canonical-workflow-live-security test-canonical-workflow-live test-kumo-live-compose test-kumo-live-compose-summary lint fmt clean run-ruriko run-gitai install help

# Build variables
BINARY_DIR := bin
RURIKO_BINARY := $(BINARY_DIR)/ruriko
GITAI_BINARY := $(BINARY_DIR)/gitai
GO := go
GOFLAGS := -v
LDFLAGS := -ldflags "-s -w"

# Get git info for version
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
VERSION_LDFLAGS := -X github.com/bdobrica/Ruriko/common/version.GitCommit=$(GIT_COMMIT) \
                   -X github.com/bdobrica/Ruriko/common/version.Version=$(GIT_TAG) \
                   -X github.com/bdobrica/Ruriko/common/version.BuildTime=$(BUILD_TIME)

all: build ## Build all binaries

build: build-ruriko build-gitai build-tools ## Build all binaries

build-ruriko: ## Build Ruriko control plane binary
	@echo "Building Ruriko..."
	@mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) -ldflags "$(VERSION_LDFLAGS) -s -w" -o $(RURIKO_BINARY) ./cmd/ruriko

build-gitai: ## Build Gitai agent runtime binary
	@echo "Building Gitai..."
	@mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) -ldflags "$(VERSION_LDFLAGS) -s -w" -o $(GITAI_BINARY) ./cmd/gitai

build-tools: ## Build utility tools
	@echo "Building tools..."
	@mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) -o $(BINARY_DIR)/gosuto-validate ./cmd/tools/gosuto-validate || true
	$(GO) build $(GOFLAGS) -o $(BINARY_DIR)/envelope-lint ./cmd/tools/envelope-lint || true
	$(GO) build $(GOFLAGS) -o $(BINARY_DIR)/keygen ./cmd/tools/keygen || true

install: ## Install binaries to $GOPATH/bin
	@echo "Installing binaries..."
	$(GO) install $(GOFLAGS) -ldflags "$(VERSION_LDFLAGS)" ./cmd/ruriko
	$(GO) install $(GOFLAGS) -ldflags "$(VERSION_LDFLAGS)" ./cmd/gitai

test: ## Run tests
	@echo "Running tests..."
	$(GO) test -v -race -coverprofile=coverage.out ./...

test-coverage: test ## Generate test coverage report
	@echo "Generating coverage report..."
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

test-integration: test-saito-scheduling test-integration-nlp ## Run integration tests (Saito scheduling + live NLP)

test-integration-nlp: ## Run live LLM integration tests (requires RURIKO_NLP_API_KEY)
	@echo "Running integration tests..."
	@KEY=$${RURIKO_NLP_API_KEY:-$$(grep '^RURIKO_NLP_API_KEY=' examples/docker-compose/.env 2>/dev/null | cut -d= -f2)}; \
	RURIKO_NLP_API_KEY=$$KEY $(GO) test -v -race -run TestR16 ./internal/ruriko/nlp/

test-saito-scheduling: ## Run deterministic Saito scheduling integration checks
	@echo "Running deterministic Saito scheduling integration test..."
	./test/integration/test-saito-scheduling.sh

test-saito-scheduling-live-precheck: ## Check live Saito scheduling prerequisites (compose/env + provisioned Saito/Kairo)
	@echo "Running live Saito scheduling precheck..."
	./test/integration/test-saito-scheduling-live-precheck.sh

test-saito-scheduling-live: test-saito-scheduling-live-precheck ## Run live compose-backed Saito scheduling validation (requires provisioned Saito/Kairo)
	@echo "Running live Saito scheduling validation..."
	./test/integration/test-saito-scheduling-live-compose.sh

test-ruriko-saito-operator-live: ## Run deterministic live operator->Ruriko->Saito schedule flow (2 cron cycles, 5m timeout)
	@echo "Running live Ruriko/Saito/operator deterministic flow..."
	./test/integration/test-ruriko-saito-operator-live-compose.sh

test-canonical-workflow-live-provisioning: ## Check canonical provisioning prerequisites (DB rows, containers, LLM keys)
	@echo "Running canonical provisioning precheck..."
	bash ./test/integration/test-canonical-workflow-live-provisioning.sh

test-canonical-workflow-live-admin-room: ## Check canonical Matrix admin-room token validity + join flow
	@echo "Running canonical admin-room join check..."
	bash ./test/integration/test-canonical-workflow-live-admin-room.sh

test-canonical-workflow-live-compose: ## Run canonical live compose suite (provisioning + admin-room + cycle)
	@echo "Running canonical live compose suite..."
	bash ./test/integration/test-canonical-workflow-live-compose-suite.sh

test-canonical-workflow-live-compose-3cycles: ## Run full canonical compose chain verification with 3 required cycles
	@echo "Running canonical live compose chain check (3 cycles)..."
	CANONICAL_REQUIRED_CYCLES=3 CANONICAL_VERIFY_STAGE=full bash ./test/integration/test-canonical-workflow-live-compose.sh

test-canonical-workflow-live-security: ## Run canonical live security checks (secrets/logs, workflow MCP-bypass guard, approval ledger)
	@echo "Running canonical live security checks..."
	bash ./test/integration/test-canonical-workflow-live-security.sh

test-canonical-workflow-live: test-canonical-workflow-live-compose test-canonical-workflow-live-security ## Run full canonical live verification bundle

test-kumo-live-compose: ## Run standalone Kumo + Tuwunel live workflow probe (captures OpenAI payloads, verifies Brave invocation path)
	@echo "Running standalone Kumo live compose check..."
	bash ./test/integration/test-kumo-live-compose.sh

test-kumo-live-compose-summary: ## Run standalone Kumo live probe and require final KUMO_NEWS_RESPONSE delivery
	@echo "Running standalone Kumo live compose check with summary requirement..."
	KUMO_LIVE_REQUIRE_SUMMARY=1 bash ./test/integration/test-kumo-live-compose.sh

lint: ## Run linter
	@echo "Running linter..."
	@which golangci-lint > /dev/null || (echo "golangci-lint not installed. Run: curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin" && exit 1)
	golangci-lint run ./...

fmt: ## Format code
	@echo "Formatting code..."
	$(GO) fmt ./...
	gofmt -s -w .

vet: ## Run go vet
	@echo "Running go vet..."
	$(GO) vet ./...

clean: ## Clean build artifacts
	@echo "Cleaning..."
	rm -rf $(BINARY_DIR)
	rm -f coverage.out coverage.html
	rm -f *.db *.db-shm *.db-wal
	find . -name "*.test" -delete

run-ruriko: build-ruriko ## Build and run Ruriko
	@echo "Running Ruriko..."
	$(RURIKO_BINARY)

run-gitai: build-gitai ## Build and run Gitai
	@echo "Running Gitai..."
	$(GITAI_BINARY)

mod-download: ## Download Go module dependencies
	@echo "Downloading dependencies..."
	$(GO) mod download

mod-tidy: ## Tidy Go module dependencies
	@echo "Tidying dependencies..."
	$(GO) mod tidy

mod-verify: ## Verify Go module dependencies
	@echo "Verifying dependencies..."
	$(GO) mod verify

docker-build-ruriko: ## Build Ruriko Docker image
	@echo "Building Ruriko Docker image..."
	docker build -f deploy/docker/Dockerfile.ruriko \
		--build-arg GIT_COMMIT=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown) \
		--build-arg GIT_TAG=$$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0) \
		--build-arg BUILD_TIME=$$(date -u '+%Y-%m-%d_%H:%M:%S') \
		-t ruriko:latest .

docker-build-gitai: ## Build Gitai Docker image
	@echo "Building Gitai Docker image..."
	docker build -f deploy/docker/Dockerfile.gitai \
		--build-arg GIT_COMMIT=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown) \
		--build-arg GIT_TAG=$$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0) \
		--build-arg BUILD_TIME=$$(date -u '+%Y-%m-%d_%H:%M:%S') \
		-t gitai:latest .

docker-build: docker-build-ruriko docker-build-gitai ## Build all Docker images

test-gateway-binaries: docker-build-gitai ## Build Gitai image and verify gateway binary paths
	@echo "Testing gateway binary paths in Gitai image..."
	SKIP_BUILD=1 ./test/integration/test-gateway-binaries.sh gitai:latest

compose-up: docker-build ## Build images and start the full stack
	@echo "Starting Ruriko stack..."
	cd examples/docker-compose && docker compose up -d

compose-down: ## Stop the stack
	cd examples/docker-compose && docker compose down

compose-logs: ## Tail logs from all services
	cd examples/docker-compose && docker compose logs -f

compose-ps: ## Show service status
	cd examples/docker-compose && docker compose ps

help: ## Show this help message
	@echo "Ruriko - Distributed AI Agent Control Plane"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'
