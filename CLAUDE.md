# MySQL Lock Monitor

## Build & Run
- `make build` - Build the binary
- `make test` - Run unit tests
- `make test-integration` - Run integration tests (requires Docker)
- `make lint` - Run linter (golangci-lint)
- `go run ./cmd/mysqlmonitoring` - Run directly

## Project Structure
- `cmd/mysqlmonitoring/` - CLI entrypoint using Cobra
- `internal/db/` - Database interface, MySQL implementation, DSN handling, InnoDB status parser
- `internal/detector/` - Lock contention, long transaction, DDL conflict, deadlock detectors
- `internal/monitor/` - Polling loop that assembles snapshots and runs detectors
- `internal/killer/` - Kill connections with safety checks
- `internal/tui/` - Bubbletea TUI (dashboard, lock tree, process list)
- `internal/output/` - Text and JSON output formatters
- `tests/integration/` - Integration tests using testcontainers (MySQL 5.7, 8.0, MariaDB)

## Key Interfaces
- `db.DB` - Abstraction over MySQL queries, mockable for unit tests
- `detector.Detector` - Each detector implements `Detect(snapshot) []Issue`

## Design Principles
1. Version-aware queries (MySQL 5.7 vs 8.0+ lock tables)
2. CGO_ENABLED=0 - pure Go MySQL driver
3. Safety checks on kill (refuse system threads, replication, self)
4. Monitor sends Snapshots on channel, TUI/output consumes them

## Conventions
- Use `internal/` for private packages
- Context for cancellation and timeouts
- Testify for assertions
- testcontainers-go for integration tests
