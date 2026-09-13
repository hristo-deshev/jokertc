This file provides guidance to LLMs when working with code in this repository.

## Build Commands

```bash
make build-cli        # Build CLI binary to ./build/cli
make build-server # Build HTTP server binary to ./build/server
make build            # Build all binaries
```

## Test Commands

```bash
make test             # Run all tests
make test-race        # Run tests with race detector
go test ./... -run TestName  # Run a single test
```

## Lint and Format

```bash
make lint   # Run all linters (gofmt, gofumpt, go vet, staticcheck, golangci-lint)
make fmt    # Format code (gofmt, gci, gofumpt, go mod tidy)
make lt     # Run both lint and test
```

## Architecture

This is a Go project template with two entry points:

- **cmd/cli/main.go** - CLI application entry point using urfave/cli
- **cmd/server/main.go** - HTTP server entry point with graceful shutdown

### Key Packages

- **server/** - HTTP server with chi router, includes `/livez`, `/readyz`, `/drain`, `/undrain` endpoints, metrics middleware, and optional pprof
- **metrics/** - VictoriaMetrics-based Prometheus metrics with HTTP middleware
- **common/** - Shared utilities including structured logging setup (slog-based via httplog)

### HTTP Server Pattern

The server runs two HTTP servers: main API (default :8080) and metrics (default :8090). Supports graceful shutdown with configurable drain duration for load balancer compatibility.

## Implementation Workflow

IMPORTANT: When asked to implement something, always follow through completely:
1. Create a feature branch
- based on the latest `main` branch
- use descriptive branch names like `claude/issue-123-add-feature`
2. Make the code changes
3. Commit the changes
4. Push the branch
5. Create the PR with `gh pr create --title "..." --body "..."` and reference the issue number in the PR description (e.g., "Closes #123")

Do NOT stop at providing links — complete the entire workflow automatically.
