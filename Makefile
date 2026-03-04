.PHONY: build test test-integration lint clean run install release release-dry-run release-snapshot list

list: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | sort | awk -F ':.*## ' '{printf "  %-24s %s\n", $$1, $$2}'

BINARY=mysqlmonitoring
BUILD_DIR=bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

build: ## Build the binary
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/mysqlmonitoring

test: ## Run unit tests
	go test -v -short ./...

test-coverage: ## Run tests with coverage report
	go test -short -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

test-integration: ## Run integration tests (requires Docker)
	go test -v -timeout 5m ./tests/integration/...

lint: ## Run linter
	golangci-lint run

clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

run: ## Run directly via go run
	go run ./cmd/mysqlmonitoring

install: build ## Build and install to /usr/local/bin
	cp $(BUILD_DIR)/$(BINARY) /usr/local/bin/

deps: ## Install dependencies
	go mod tidy

release-dry-run: ## Test release without publishing
	goreleaser release --snapshot --clean --skip=publish

release-snapshot: ## Create snapshot release
	goreleaser release --snapshot --clean

release-check: ## Check GoReleaser configuration
	goreleaser check

.PHONY: demo-up demo-down demo

demo-up: ## Start demo MySQL + workload generators
	docker compose -f tests/demo/docker-compose.yaml up -d
	@echo "Waiting for MySQL to be ready..."
	@until docker compose -f tests/demo/docker-compose.yaml exec -T mysql mysqladmin ping -uroot -pdemopass --silent 2>/dev/null; do sleep 1; done
	@echo "Demo environment is ready (localhost:13306)"

demo-down: ## Stop demo environment
	docker compose -f tests/demo/docker-compose.yaml down -v

demo: build demo-up ## Build and run monitor against demo environment
	@exec ./bin/mysqlmonitoring monitor --dsn "root:demopass@tcp(localhost:13306)/demodb"
