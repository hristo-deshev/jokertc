# go-template

[![Goreport status](https://goreportcard.com/badge/jokertc)](https://goreportcard.com/report/jokertc)
[![Test status](https://jokertc/actions/workflows/checks.yml/badge.svg?branch=main)](https://jokertc/actions?query=workflow%3A%22Checks%22)

Toolbox and building blocks for new Go projects, to get started quickly and right-footed!

Pick and choose whatever is useful to you! Don't feel the need to use everything, or even to follow this structure.

## What's Included

This template provides two entry points:

- **CLI application** ([`cmd/cli/main.go`](/cmd/cli/main.go)) - Command-line tool using [urfave/cli](https://cli.urfave.org/)
- **HTTP server** ([`cmd/server/main.go`](/cmd/server/main.go)) - Web server with graceful shutdown, health checks, and metrics

### Features

- [`Makefile`](https://jokertc/blob/main/Makefile) with `lint`, `test`, `build`, `fmt` and more
- Linting with `gofmt`, `gofumpt`, `go vet`, `staticcheck` and `golangci-lint`
- Logging setup using the [slog logger](https://pkg.go.dev/golang.org/x/exp/slog) (with debug and json logging options)
- [GitHub Workflows](.github/workflows/) for linting and testing, as well as releasing and publishing Docker images
- Webserver with graceful shutdown, health probes, and Prometheus metrics

---

## Quick Start

**Build and run the HTTP server:**

```bash
make build-server
./build/server --listen-addr 127.0.0.1:8080 --metrics-addr 127.0.0.1:8090
```

**Build and run the CLI:**

```bash
make build-cli
./build/cli
```

---

## Project Structure

| Directory              | Description                                                |
| ---------------------- | ---------------------------------------------------------- |
| `cmd/cli/`             | CLI application entry point (urfave/cli)                   |
| `cmd/server/`      | HTTP server entry point                                    |
| `server/`          | HTTP server implementation (chi router, graceful shutdown) |
| `metrics/`             | Prometheus metrics (VictoriaMetrics-based)                 |
| `common/`              | Shared utilities (structured logging)                      |

---

## HTTP Server Endpoints

The server runs two HTTP servers: main API (default `:8080`) and metrics (default `:8090`).

| Endpoint   | Port | Description                                        |
| ---------- | ---- | -------------------------------------------------- |
| `/api`     | 8080 | Main API endpoint                                  |
| `/livez`   | 8080 | Liveness probe for health checks                   |
| `/readyz`  | 8080 | Readiness probe for health checks                  |
| `/drain`   | 8080 | Enable drain mode (for graceful shutdown)          |
| `/undrain` | 8080 | Disable drain mode                                 |
| `/debug/*` | 8080 | pprof debug endpoints (when `--pprof` flag is set) |
| `/metrics` | 8090 | Prometheus metrics                                 |

### CLI Flags

| Flag              | Default          | Description                           |
| ----------------- | ---------------- | ------------------------------------- |
| `--listen-addr`   | `127.0.0.1:8080` | Address for API server                |
| `--metrics-addr`  | `127.0.0.1:8090` | Address for Prometheus metrics        |
| `--log-json`      | `false`          | Log in JSON format                    |
| `--log-debug`     | `false`          | Enable debug logging                  |
| `--log-uid`       | `false`          | Add UUID to all log messages          |
| `--log-service`   | `your-project`   | Service name in logs                  |
| `--pprof`         | `false`          | Enable pprof debug endpoint           |
| `--drain-seconds` | `45`             | Seconds to wait in drain HTTP request |

---

## Development

### Build Commands

```bash
make build-cli        # Build CLI binary to ./build/cli
make build-server # Build HTTP server binary to ./build/server
make build            # Build all binaries
```

### Lint, Test, Format

```bash
make lint   # Run all linters (gofmt, gofumpt, go vet, staticcheck, golangci-lint, nilaway)
make test   # Run all tests
make fmt    # Format code (gofmt, gci, gofumpt, go mod tidy)
make lt     # Run both lint and test
```

### Install Dev Dependencies

Pin to the same versions as CI (`.github/workflows/checks.yml`) for reproducible runs:

```bash
go install mvdan.cc/gofumpt@latest
go install honnef.co/go/tools/cmd/staticcheck@latest
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
go install go.uber.org/nilaway/cmd/nilaway@latest
go install github.com/daixiang0/gci@latest
```

`nilaway` performs static nil-dereference analysis and runs as part of `make lint`.

---

## Related Resources

- [Flashbots Repository Template](https://github.com/flashbots/flashbots-repository-template) - Public project setup
- [go-utils](https://github.com/flashbots/go-utils) - Common Go utilities
- [goperf.dev](https://goperf.dev) - Advanced Golang knowledge, tips & tricks