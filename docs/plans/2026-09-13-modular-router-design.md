# Modular Router Design

Date: 2026-09-13
Status: Accepted

## Goal

Split route registration out of `server` into self-contained route modules.
Each module owns its routes **and the state behind them**. `server` becomes a
thin composition root that builds the mux, applies global middleware, and
mounts modules.

## Module contract

Every route module exposes:

```go
func RegisterRoutes(r chi.Router)
```

registering its endpoints onto the router passed in. (An earlier draft used
`Routes() chi.Router` with per-module subrouters, but chi cannot mount two
subrouters on the same root path — registration on a shared router is the
idiomatic chi pattern.) The composition root applies global middleware
(`httplog`, `Recoverer`, `metrics.Middleware`) before calling
`RegisterRoutes`, so modules never worry about logging or recovery.

## Folder / module structure

```
jokertc/
├── cmd/server/main.go
├── common/                # unchanged
├── api/                   # NEW module
│   ├── api.go             # RegisterRoutes → /api
│   └── api_test.go
├── metrics/
│   ├── metrics.go         # registry + record fns (unchanged)
│   ├── middleware.go      # request-duration middleware (unchanged)
│   ├── doc.go
│   └── routes.go          # Routes() → /metrics (moved from server.getMetricsRouter)
├── healthcheck/           # NEW module
│   ├── doc.go
│   ├── healthcheck.go     # Opts, Healthcheck (owns isReady atomic), RegisterRoutes → /livez, /readyz
│   ├── drain.go           # /drain, /undrain handlers (own isReady + DrainDuration)
│   └── healthcheck_test.go
└── server/
    ├── doc.go
    ├── server.go          # composition root: middleware + module registration, two http.Servers
    └── handler_test.go
```

## Key decisions

- **`isReady` moves from `Server` into `healthcheck`.** `/readyz`, `/drain`,
  `/undrain` are coupled by that state; they belong together. The
  `healthcheck` module owns the atomic and starts ready (matching the old
  `New()` behavior of `isReady.Swap(true)`).
- **Metrics routes live in `metrics/`**, next to the registry and middleware
  they report on. `server` no longer imports VictoriaMetrics directly.
- **`/api` lives in its own `api/` module** (moved out of `server` — a
  follow-up to the initial design, keeping the module contract uniform).
  Further modules can be added the same way; no registry abstraction is
  introduced (YAGNI).
- **Registration:** `api.RegisterRoutes(mux)` and
  `srv.healthcheck.RegisterRoutes(mux)` inside `getRouter()`; the metrics
  server is built as `metrics.Routes()` when `MetricsAddr` is set (it is a
  standalone router, so it keeps the `Routes()` shape).
- **Tests:** the handler-level drain/readiness behavior test moves to
  `healthcheck` (it tests module state transitions). The `server` test keeps
  an end-to-end check through `getRouter()` hitting `/readyz`.

## Error handling

Unchanged: handlers return `200`/`503` statuses; drain sleeps
`DrainDuration` for LB detection; panics are recovered by `middleware.Recoverer`
applied at the composition root.