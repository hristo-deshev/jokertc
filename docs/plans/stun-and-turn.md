# STUN and TURN Services — Design

Date: 2026-09-13
Status: Proposed

## Goal

Host a STUN and TURN server so that senders and receivers of video streams can
establish connectivity. Purpose is a development/testing harness today: a
self-hosted ICE infrastructure for local testing and CI, without depending on
public STUN servers or hosted TURN providers. Production hardening (real
authentication) is explicitly deferred but the extension point is defined now.

Stack: [`pion/turn`](https://github.com/pion/turn) (v5, current major). `pion/turn` answers
STUN binding requests itself — STUN is a subset of TURN — so one TURN listener
covers both protocols. No `pion/webrtc` dependency on the server side; that
library belongs to the integration test and the browser test page only.

## Decisions

| Topic         | Decision                                                                    |
| ------------- | --------------------------------------------------------------------------- |
| Deployment    | Same binary as the HTTP server; new `turn/` package + listener in `server.Server` |
| Auth          | `turn.Authenticator` interface with an always-allow implementation; real auth added later |
| Transports    | UDP + TCP. No TLS-over-TCP for now                                          |
| Test page     | Clipboard-connect page at `/ui/manual`; webcam both directions + data channel |

## Package layout

```
jokertc/
├── turn/
│   ├── turn.go          # Config, New(), Run(ctx), Close(), listener management
│   ├── auth.go          # Authenticator interface + AllowAllAuth
│   ├── doc.go
│   └── turn_test.go     # integration tests
└── cmd/server/main.go   # new CLI flags
```

Follows the existing module pattern (`api/`, `healthcheck/`, `metrics/`):
self-contained package, composed in `server/`.

### turn.Config

```go
type Config struct {
    ListenUDPAddr  string        // e.g. ":3478"
    ListenTCPAddr  string        // e.g. ":3478"
    ExternalIP     string        // advertised in relayed addresses; optional behind NAT
    RelayPortMin   int           // e.g. 50000
    RelayPortMax   int           // e.g. 50100
    Auth           Authenticator
    Log            *httplog.Logger
}
```

### turn.Server

Owns two `pion/turn.Server` instances (UDP + TCP) sharing one relay address
generator over the configured port range.

- `New(cfg *Config) (*Server, error)`
- `Run(ctx context.Context)` — blocks until ctx cancel or listener error.
  Listener errors are **returned**, never swallowed.
- `Close()` — idempotent graceful stop of both listeners.

### Authenticator

```go
// Authenticator authorizes TURN allocate requests.
type Authenticator interface {
    // Matches pion/turn's auth.AuthHandler signature.
    Authenticate(username, realm string, srcAddr net.Addr) (key []byte, ok bool)
}

// AllowAllAuth accepts every allocation request.
//
// Add real auth here: static long-term credentials (flags --turn-user /
// --turn-pass) or HMAC ephemeral secrets (shared secret + time-limited
// user/pass, coturn use-auth-secret style).
type AllowAllAuth struct{}
```

## Server startup restructure

Current problem: `server.RunInBackground` fires goroutines and only logs
errors. A dead listener leaves a zombie process.

New shape: error propagation channel.

```go
func (srv *Server) RunInBackground() error {
    errCh := make(chan error, 4) // api, metrics, turn UDP, turn TCP
    // each component: go func() { errCh <- run() }()
    // first error stored and reported via srv.Err()
}
```

- `main.go` selects on signal channel vs `srv.Err()`. First component error
  kills the process.
- Error policy: any component error is fatal — including metrics and TURN.
  A dead TURN listener means the connectivity service is broken; no point
  serving HTTP.
- SIGTERM/SIGINT triggers `Shutdown()`, which stops all components and waits
  for goroutines via a `sync.WaitGroup`.
- HTTP servers keep the existing `http.Server.Shutdown(ctx)` path; TURN
  listeners stop via `turn.Server.Close()`.

## CLI flags

| Flag                     | Default      | Meaning                                   |
| ------------------------ | ------------ | ----------------------------------------- |
| `--turn-listen-addr`     | `:3478`      | STUN/TURN listen address; empty = disabled |
| `--turn-external-ip`     | *(empty)*    | External IP advertised in relayed addresses |
| `--turn-relay-port-range`| `50000-50100`| UDP port range for relay allocations       |

## Integration tests (`turn/turn_test.go`)

1. `New()` a TURN server on `127.0.0.1:0` with an OS-assigned relay range for
   the test.
2. Start via the same code path as production, plus one test wiring it through
   `server.New` to prove composition.
3. pion client over UDP: `turn.Client` + `Allocate()`, assert relayed address
   falls in the configured port range.
4. Same allocation over TCP.
5. STUN check: send a Binding request to the same port, assert
   XOR-MAPPED-ADDRESS response.
6. Race detector applies (`make test-race`).

## UI manual test page (`/ui/manual`)

One static HTML file (`static/webrtc-test.html`), served from the existing
HTTP server at `/ui/manual`. No build step, no framework, no signaling server.

### Media flow

1. Each tab calls `getUserMedia({video: true, audio: true})`, attaches the
   stream to a muted local `<video>`, and adds its tracks to the peer
   connection before creating the offer.
2. Clipboard SDP exchange (full SDP, candidates gathered before display):
   tab 1 `createOffer()` → copy → paste into tab 2 → `setRemoteDescription()`
   → tab 2 `createAnswer()` → copy back → tab 1 `setRemoteDescription()`.
3. ICE gathers host, server-reflexive (our STUN), and relay (our TURN)
   candidates. `iceServers` uses `window.location.hostname` so the page works
   wherever it is served from.
4. `pc.ontrack` attaches the remote stream to a second `<video>` element.
5. Two tabs on one machine usually connect host-to-host (media browser to
   browser, server not in path). Server sees only STUN/TURN control packets
   unless the direct path fails and relay candidates are used. An ICE state
   and candidate log div makes the chosen pair visible — relay candidates in
   the log prove the TURN path.

### Data channel ping

- Tab that creates the offer also calls `pc.createDataChannel("ping")`.
- **Ping** button sends a timestamped message (`Date.now()` ISO string) over
  the channel.
- Both peers append every received message as a new line in a shared
  `<textarea>` at the bottom of the page.
- Answering side picks the channel up via `pc.ondatachannel`.

## Out of scope (later)

- Real authentication (static credentials or HMAC ephemeral secrets).
- TLS-over-TCP TURN transport.
- Separate `cmd/turn` binary — package boundary keeps the split cheap if ever
  needed.
- Automated browser testing of the manual page (manual two-tab testing for
  now; integration tests cover the server side).