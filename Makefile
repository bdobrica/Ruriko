.PHONY: all build build-ruriko build-gitai build-tools test lint fmt clean run-ruriko run-gitai install help

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
	docker build -f deploy/docker/Dockerfile.ruriko -t ruriko:latest .

docker-build-gitai: ## Build Gitai Docker image
	@echo "Building Gitai Docker image..."
	docker build -f deploy/docker/Dockerfile.gitai -t gitai:latest .

docker-build: docker-build-ruriko docker-build-gitai ## Build all Docker images

help: ## Show this help message
	@echo "Ruriko - Distributed AI Agent Control Plane"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'
