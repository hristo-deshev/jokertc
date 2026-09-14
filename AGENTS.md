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

- **server/** - Composition root: builds the chi router, applies global middleware (logging, recovery, metrics), and registers route modules; owns the two `http.Server` instances and graceful shutdown. Each route module exposes `RegisterRoutes(chi.Router)`.
- **api/** - Route module owning the `/api` endpoint
- **healthcheck/** - Route module owning `/livez`, `/readyz`, `/drain`, `/undrain` and the readiness state behind them
- **metrics/** - VictoriaMetrics-based Prometheus metrics with HTTP middleware and the `/metrics` route module
- **common/** - Shared utilities including structured logging setup (slog-based via httplog)
- **turn/** - Embedded STUN/TURN server (pion/turn). `AllowAllAuth` accepts all
  allocations with credential == username; swap it in `turn/auth.go` for real
  auth. Component errors are fatal: any dead listener (HTTP, metrics, TURN)
  kills the process via `Server.ErrCh()`.

### HTTP Server Pattern

The server runs two HTTP servers: main API (default :9000) and metrics (default :8090). Supports graceful shutdown with configurable drain duration for load balancer compatibility.
