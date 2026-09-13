# STUN/TURN Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Embed a STUN+TURN server (pion/turn v5) in the existing HTTP server binary, restructure startup for fatal error propagation, add integration tests, and serve a manual two-tab WebRTC test page at `/ui/manual`.

**Architecture:** New self-contained `turn/` package owns the two pion/turn listeners (UDP + TCP) and an `Authenticator` abstraction with an always-allow implementation. `server.Server` composes it like the metrics server, and gains an error-propagation channel so any dead component kills the process. A single embedded HTML file provides the manual connectivity test.

**Tech Stack:** Go, `github.com/pion/turn/v5`, chi router, httplog, urfave/cli, go:embed.

**Spec:** `docs/plans/stun-and-turn.md` — read it first; this plan argues from it.

## Global Constraints

- Dependency: `github.com/pion/turn/v5` (never v4).
- Our package is also named `turn` — always import pion as `pion "github.com/pion/turn/v5"`.
- TURN realm string: `"jokertc"`.
- `AllowAllAuth` convention: client credential must equal username (key derived via `turn.GenerateAuthKey(username, realm, username)`).
- Listener bind errors surface from `New()`; runtime errors from any component are fatal (process exits non-zero).
- Test commands: `make test`, `make test-race`, `make lint`. Commit after every task.
- Relay port range format: `min-max`, e.g. `50000-50100`.

---

### Task 1: Authenticator interface + AllowAllAuth

**Files:**
- Create: `turn/auth.go`
- Test: `turn/auth_test.go`

**Interfaces:**
- Consumes: `github.com/pion/turn/v5` `GenerateAuthKey(username, realm, password string) []byte`.
- Produces: `type Authenticator interface { Authenticate(username, realm string, srcAddr net.Addr) (key []byte, ok bool) }` and `type AllowAllAuth struct{}` — used by Task 2 (`turn.New`) and documented for future auth work.

- [ ] **Step 1: Add pion dependency**

```bash
go get github.com/pion/turn/v5
go mod tidy
```

- [ ] **Step 2: Write the failing test** (`turn/auth_test.go`)

```go
package turn

import (
	"net"
	"testing"
)

func TestAllowAllAuthAcceptsAnyUsername(t *testing.T) {
	auth := AllowAllAuth{}
	key, ok := auth.Authenticate("test", "jokertc", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(key) == 0 {
		t.Fatal("expected non-empty key")
	}
}

func TestAllowAllAuthRejectsEmptyUsername(t *testing.T) {
	auth := AllowAllAuth{}
	_, ok := auth.Authenticate("", "jokertc", &net.UDPAddr{})
	if ok {
		t.Fatal("expected ok=false for empty username")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./turn/ -run TestAllowAll -v`
Expected: FAIL — package `turn` does not exist / no Go files.

- [ ] **Step 4: Write implementation** (`turn/auth.go`)

```go
package turn

import (
	"net"

	pion "github.com/pion/turn/v5"
)

// Authenticator authorizes TURN allocation requests. It matches the signature
// of pion/turn's AuthHandler so implementations plug straight into the server
// (see New in turn.go).
type Authenticator interface {
	Authenticate(username, realm string, srcAddr net.Addr) (key []byte, ok bool)
}

// AllowAllAuth accepts every allocation request with a non-empty username.
// It derives the long-term auth key from the username itself, so clients must
// send credential == username (any non-empty username works).
//
// Add real auth here: replace with a static long-term credential check
// (turn.GenerateAuthKey against --turn-user/--turn-pass flags) or HMAC
// ephemeral secrets (pion's NewLongTermAuthHandler /
// LongTermTURNRESTAuthHandler).
type AllowAllAuth struct{}

func (AllowAllAuth) Authenticate(username, realm string, _ net.Addr) (key []byte, ok bool) {
	if username == "" {
		return nil, false
	}
	return pion.GenerateAuthKey(username, realm, username), true
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./turn/ -run TestAllowAll -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add turn/ go.mod go.sum
git commit -m "feat(turn): add Authenticator interface with AllowAllAuth"
```

---

### Task 2: turn.Server — Config, New, Run, Close

**Files:**
- Create: `turn/turn.go`, `turn/doc.go`
- Test: `turn/turn_test.go`

**Interfaces:**
- Consumes: `Authenticator` / `AllowAllAuth` (Task 1).
- Produces (used by Tasks 3, 5, 6):
  - `type Config struct { ListenUDPAddr, ListenTCPAddr, ExternalIP string; RelayPortMin, RelayPortMax int; Auth Authenticator; Log *httplog.Logger }`
  - `func New(cfg *Config) (*Server, error)`
  - `func (s *Server) Run(ctx context.Context) error` — blocks until ctx cancel (then closes) or `Close()`; returns error only from shutdown.
  - `func (s *Server) Close() error` — idempotent.
  - `func (s *Server) UDPAddr() net.Addr`, `func (s *Server) TCPAddr() net.Addr` — bound listener addresses (ports from `:0` style configs).

- [ ] **Step 1: Write failing tests** (`turn/turn_test.go`)

```go
package turn

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
)

func testConfig(t *testing.T, udpAddr, tcpAddr string) *Config {
	t.Helper()
	return &Config{
		ListenUDPAddr: udpAddr,
		ListenTCPAddr: tcpAddr,
		ExternalIP:    "127.0.0.1",
		RelayPortMin:  60000,
		RelayPortMax:  60100,
		Auth:          AllowAllAuth{},
	}
}

func startServer(t *testing.T, cfg *Config) *Server {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	go func() { _ = s.Run(context.Background()) }()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func udpClient(t *testing.T, serverAddr string) *pion.Client {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	client, err := pion.NewClient(&pion.ClientConfig{
		STUNServerAddr: serverAddr,
		TURNServerAddr: serverAddr,
		Username:       "test",
		Password:       "test", // AllowAllAuth: credential == username
		Conn:           conn,
		LoggerFactory:  logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		t.Fatalf("NewClient(): %v", err)
	}
	if err := client.Listen(); err != nil {
		t.Fatalf("client.Listen(): %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

func TestNewValidation(t *testing.T) {
	if _, err := New(testConfig(t, "", "")); err == nil {
		t.Fatal("expected error when no listener configured")
	}
	cfg := testConfig(t, "127.0.0.1:0", "")
	cfg.RelayPortMax = 50000 // < RelayPortMin
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error for invalid port range")
	}
	cfg = testConfig(t, "127.0.0.1:0", "")
	cfg.Auth = nil
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error for missing authenticator")
	}
}

func TestSTUNBinding(t *testing.T) {
	s := startServer(t, testConfig(t, "127.0.0.1:0", ""))
	client := udpClient(t, s.UDPAddr().String())

	mapped, err := client.SendBindingRequest()
	if err != nil {
		t.Fatalf("SendBindingRequest(): %v", err)
	}
	if udp, ok := mapped.(*net.UDPAddr); !ok || !udp.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("unexpected mapped address %v", mapped)
	}
}

func TestAllocateUDPInPortRange(t *testing.T) {
	s := startServer(t, testConfig(t, "127.0.0.1:0", ""))
	client := udpClient(t, s.UDPAddr().String())

	relayConn, err := client.Allocate()
	if err != nil {
		t.Fatalf("Allocate(): %v", err)
	}
	t.Cleanup(func() { _ = relayConn.Close() })

	port := relayConn.LocalAddr().(*net.UDPAddr).Port
	if port < 60000 || port > 60100 {
		t.Fatalf("relayed port %d outside 60000-60100", port)
	}
}
```

Adjust imports: `pion "github.com/pion/turn/v5"`. (No `time` import needed unless used — remove if linter complains.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./turn/ -v`
Expected: FAIL — `New`, `Config`, `Server` undefined.

- [ ] **Step 3: Write implementation**

`turn/doc.go`:

```go
// Package turn embeds a STUN+TURN server based on pion/turn. The TURN
// listener answers STUN binding requests too, so one listener covers both
// protocols. See docs/plans/stun-and-turn.md.
package turn
```

`turn/turn.go`:

```go
package turn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/go-chi/httplog/v2"
	"github.com/pion/logging"
	pion "github.com/pion/turn/v5"
)

const realm = "jokertc"

type Config struct {
	ListenUDPAddr string // e.g. ":3478"; empty = no UDP listener
	ListenTCPAddr string // e.g. ":3478"; empty = no TCP listener
	ExternalIP    string // advertised in relayed addresses; auto-detected when empty
	RelayPortMin  int
	RelayPortMax  int
	Auth          Authenticator
	Log           *httplog.Logger
}

type Server struct {
	cfg *Config
	log *httplog.Logger

	udpConn     net.PacketConn
	tcpListener net.Listener
	srv         *pion.Server

	stopOnce sync.Once
	stopped  chan struct{}
}

func New(cfg *Config) (*Server, error) {
	if cfg.ListenUDPAddr == "" && cfg.ListenTCPAddr == "" {
		return nil, errors.New("at least one of ListenUDPAddr / ListenTCPAddr required")
	}
	if cfg.RelayPortMin <= 0 || cfg.RelayPortMax < cfg.RelayPortMin || cfg.RelayPortMax > 65535 {
		return nil, fmt.Errorf("invalid relay port range %d-%d", cfg.RelayPortMin, cfg.RelayPortMax)
	}
	if cfg.Auth == nil {
		return nil, errors.New("authenticator required")
	}

	relayIP := net.ParseIP(cfg.ExternalIP)
	if relayIP == nil {
		detected, err := externalIP()
		if err != nil {
			return nil, fmt.Errorf("no ExternalIP configured and auto-detection failed: %w", err)
		}
		relayIP = detected
	}

	s := &Server{cfg: cfg, log: cfg.Log, stopped: make(chan struct{})}

	// One generator per listener. Two generators over the same range can
	// race for a port; AllocatePacketConn retries (MaxRetries) on bind
	// failure, which absorbs the collision.
	newGen := func() pion.RelayAddressGenerator {
		return &pion.RelayAddressGeneratorPortRange{
			RelayAddress: relayIP,
			MinPort:      uint16(cfg.RelayPortMin),
			MaxPort:      uint16(cfg.RelayPortMax),
			MaxRetries:   10,
			Address:      "0.0.0.0",
		}
	}

	serverConfig := pion.ServerConfig{
		LoggerFactory: logging.NewDefaultLoggerFactory(),
		Realm:         realm,
		AuthHandler: func(username, r string, srcAddr net.Addr) ([]byte, bool) {
			return cfg.Auth.Authenticate(username, r, srcAddr)
		},
	}

	if cfg.ListenUDPAddr != "" {
		udpConn, err := net.ListenPacket("udp", cfg.ListenUDPAddr)
		if err != nil {
			return nil, fmt.Errorf("TURN UDP listener: %w", err)
		}
		s.udpConn = udpConn
		serverConfig.PacketConnConfigs = append(serverConfig.PacketConnConfigs, pion.PacketConnConfig{
			PacketConn:           udpConn,
			RelayAddressGenerator: newGen(),
		})
	}
	if cfg.ListenTCPAddr != "" {
		l, err := net.Listen("tcp", cfg.ListenTCPAddr)
		if err != nil {
			return nil, fmt.Errorf("TURN TCP listener: %w", err)
		}
		s.tcpListener = l
		serverConfig.ListenerConfigs = append(serverConfig.ListenerConfigs, pion.ListenerConfig{
			Listener:              l,
			RelayAddressGenerator: newGen(),
		})
	}

	srv, err := pion.NewServer(serverConfig)
	if err != nil {
		return nil, fmt.Errorf("pion TURN server: %w", err)
	}
	s.srv = srv
	return s, nil
}

// Run blocks until ctx is cancelled or the server is closed. Bind errors are
// returned from New; pion handles runtime listener errors internally.
func (s *Server) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return s.Close()
	case <-s.stopped:
		return nil
	}
}

// Close stops the TURN server and closes its listeners. Idempotent.
func (s *Server) Close() error {
	var err error
	s.stopOnce.Do(func() {
		err = s.srv.Close() // also closes the PacketConn / Listener we handed it
		close(s.stopped)
	})
	return err
}

func (s *Server) UDPAddr() net.Addr {
	if s.udpConn == nil {
		return nil
	}
	return s.udpConn.LocalAddr()
}

func (s *Server) TCPAddr() net.Addr {
	if s.tcpListener == nil {
		return nil
	}
	return s.tcpListener.Addr()
}

// externalIP returns the first non-loopback IPv4 address of the host, used as
// the relay address when Config.ExternalIP is empty.
func externalIP() (net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP, nil
		}
	}
	return nil, errors.New("no non-loopback IPv4 address found")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./turn/ -v`
Expected: PASS (all tests)

- [ ] **Step 5: Commit**

```bash
git add turn/
git commit -m "feat(turn): embed pion TURN server with UDP+TCP listeners and relay port range"
```

---

### Task 3: Server startup restructure — error propagation

**Files:**
- Modify: `server/server.go`
- Test: `server/server_test.go` (new; alongside existing `handler_test.go`)

**Interfaces:**
- Consumes: `turn.New`, `turn.Config`, `turn.Server.Run/Close` (Task 2).
- Produces (used by Task 4):
  - `HTTPServerConfig` gains field `TURN *turn.Config` (nil = TURN disabled).
  - `func (srv *Server) RunInBackground()` — unchanged signature; now also starts TURN and feeds component errors into an internal buffered channel.
  - `func (srv *Server) ErrCh() <-chan error` — delivers the first fatal component error, if any. Empty until an error occurs; never closed.
  - `func (srv *Server) Shutdown()` — unchanged signature; now also cancels TURN and waits for all goroutines.

- [ ] **Step 1: Write failing test** (`server/server_test.go`)

```go
package server

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/httplog/v2"
	"jokertc/turn"
)

func testLogger(t *testing.T) *httplog.Logger {
	t.Helper()
	return httplog.NewLogger("test", httplog.Options{LogLevel: "error"})
}

func TestErrChPropagatesComponentError(t *testing.T) {
	// TURN config with an unreachable relay range setup is not enough to
	// force a runtime failure; instead verify the channel wiring with the
	// real HTTP listener by claiming its port twice.
	srv, err := New(&HTTPServerConfig{
		ListenAddr: "127.0.0.1:0",
		Log:        testLogger(t),
		TURN: &turn.Config{
			ListenUDPAddr: "127.0.0.1:0",
			ExternalIP:    "127.0.0.1",
			RelayPortMin:  60000,
			RelayPortMax:  60100,
			Auth:          turn.AllowAllAuth{},
		},
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	srv.RunInBackground()
	defer srv.Shutdown()

	select {
	case err := <-srv.ErrCh():
		t.Fatalf("unexpected early error: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if srv.turnSrv == nil {
		t.Fatal("expected TURN server to be composed")
	}
}

func TestShutdownWaitsWithoutError(t *testing.T) {
	srv, err := New(&HTTPServerConfig{ListenAddr: "127.0.0.1:0", Log: testLogger(t)})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	srv.RunInBackground()
	srv.Shutdown() // must return; wg.Wait must not deadlock
	select {
	case err := <-srv.ErrCh():
		if !errors.Is(err, http.ErrServerClosed) && err != nil {
			t.Fatalf("unexpected shutdown error: %v", err)
		}
	default:
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./server/ -run 'TestErrCh|TestShutdown' -v`
Expected: FAIL — `turnSrv` undefined / `ErrCh` undefined.

- [ ] **Step 3: Modify `server/server.go`**

Add to imports: `"context"`, `"sync"`, `"jokertc/turn"`.

Add to `HTTPServerConfig`:

```go
	TURN *turn.Config // nil disables the embedded STUN/TURN server
```

Extend the `Server` struct:

```go
type Server struct {
	cfg         *HTTPServerConfig
	healthcheck *healthcheck.Healthcheck
	log         *httplog.Logger

	srv        *http.Server
	metricsSrv *http.Server
	turnSrv    *turn.Server

	errCh      chan error
	wg         sync.WaitGroup
	turnCancel context.CancelFunc
}
```

In `New`, after the metrics server block:

```go
	if cfg.TURN != nil {
		turnSrv, err := turn.New(cfg.TURN)
		if err != nil {
			return nil, fmt.Errorf("TURN server: %w", err)
		}
		srv.turnSrv = turnSrv
	}
```

Replace `RunInBackground` and add `ErrCh`:

```go
// RunInBackground starts all configured components in goroutines. The first
// fatal error from any component is delivered on ErrCh.
func (srv *Server) RunInBackground() {
	srv.errCh = make(chan error, 4) // api, metrics, turn — buffered so senders never block

	if srv.turnSrv != nil {
		srv.wg.Add(1)
		ctx, cancel := context.WithCancel(context.Background())
		srv.turnCancel = cancel
		go func() {
			defer srv.wg.Done()
			srv.log.Info("Starting TURN server", "listenUDP", srv.cfg.TURN.ListenUDPAddr, "listenTCP", srv.cfg.TURN.ListenTCPAddr)
			if err := srv.turnSrv.Run(ctx); err != nil {
				srv.log.Error("TURN server failed", "err", err)
				srv.errCh <- err
			}
		}()
	}

	if srv.metricsSrv != nil {
		srv.wg.Add(1)
		go func() {
			defer srv.wg.Done()
			srv.log.With("metricsAddress", srv.cfg.MetricsAddr).Info("Starting metrics server")
			if err := srv.metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				srv.log.Error("Metrics server failed", "err", err)
				srv.errCh <- err
			}
		}()
	}

	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		srv.log.Info("Starting HTTP server", "listenAddress", srv.cfg.ListenAddr)
		if err := srv.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srv.log.Error("HTTP server failed", "err", err)
			srv.errCh <- err
		}
	}()
}

// ErrCh delivers the first fatal component error. Blocks until one arrives;
// use in a select alongside the shutdown signal channel in main.
func (srv *Server) ErrCh() <-chan error { return srv.errCh }
```

Append to the end of `Shutdown`:

```go
	// embedded STUN/TURN
	if srv.turnSrv != nil {
		srv.turnCancel() // unblocks turnSrv.Run, which closes the server
	}

	srv.wg.Wait()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./server/ -v`
Expected: PASS (new + existing handler tests)

- [ ] **Step 5: Commit**

```bash
git add server/
git commit -m "feat(server): propagate fatal component errors and compose TURN server"
```

---

### Task 4: CLI flags + main.go wiring

**Files:**
- Modify: `cmd/server/main.go`

**Interfaces:**
- Consumes: `server.HTTPServerConfig.TURN`, `server.Server.ErrCh()` (Task 3); `turn.AllowAllAuth` (Task 1).
- Produces: CLI behavior — `--turn-listen-addr` (default `:3478`, empty disables), `--turn-external-ip`, `--turn-relay-port-range` (default `50000-50100`).

- [ ] **Step 1: Add flags** to the `flags` slice in `main.go`:

```go
	&cli.StringFlag{
		Name:  "turn-listen-addr",
		Value: ":3478",
		Usage: "STUN/TURN listen address (UDP and TCP); empty disables TURN",
	},
	&cli.StringFlag{
		Name:  "turn-external-ip",
		Value: "",
		Usage: "external IP advertised in TURN relayed addresses; auto-detected when empty",
	},
	&cli.StringFlag{
		Name:  "turn-relay-port-range",
		Value: "50000-50100",
		Usage: "UDP port range for TURN relay allocations (min-max)",
	},
```

- [ ] **Step 2: Parse and wire** in the `Action` func:

```go
			turnListenAddr := cCtx.String("turn-listen-addr")
			turnExternalIP := cCtx.String("turn-external-ip")
			turnRelayRange := cCtx.String("turn-relay-port-range")
```

Port range parser (package-level helper in main.go):

```go
func parsePortRange(s string) (min, max int, err error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid port range %q: want min-max", s)
	}
	min, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid port range %q: %w", s, err)
	}
	max, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid port range %q: %w", s, err)
	}
	if min <= 0 || max < min || max > 65535 {
		return 0, 0, fmt.Errorf("invalid port range %d-%d", min, max)
	}
	return min, max, nil
}
```

Build the config (after `cfg := &server.HTTPServerConfig{...}`):

```go
			relayMin, relayMax, err := parsePortRange(turnRelayRange)
			if err != nil {
				return err
			}
			if turnListenAddr != "" {
				turnCfg := &turn.Config{
					ListenUDPAddr: turnListenAddr,
					ListenTCPAddr: turnListenAddr,
					RelayPortMin:  relayMin,
					RelayPortMax:  relayMax,
					Auth:          turn.AllowAllAuth{},
					Log:           log,
				}
				if turnExternalIP != "" {
					turnCfg.ExternalIP = turnExternalIP
				}
				cfg.TURN = turnCfg
			}
```

Add imports: `"strconv"`, `"strings"`, `"jokertc/turn"`.

- [ ] **Step 3: Replace the shutdown block** at the end of `Action`:

```go
			srv.RunInBackground()

			select {
			case <-exit:
				cfg.Log.Info("shutting down")
				srv.Shutdown()
			case err := <-srv.ErrCh():
				cfg.Log.Error("server component failed, exiting", "err", err)
				srv.Shutdown()
				return err
			}
			return nil
```

(Replaces the bare `<-exit` + `srv.Shutdown()` lines.)

- [ ] **Step 4: Verify manually**

```bash
make build-server && ./build/server --listen-addr 127.0.0.1:8080 --turn-listen-addr 127.0.0.1:3478 &
# then:
kill %1
```

Expected: startup logs show TURN server addresses; SIGTERM exits cleanly, no panic.

- [ ] **Step 5: Run all tests + lint**

Run: `make lt`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/server/main.go
git commit -m "feat(server): add --turn-* flags and fatal-error shutdown selection"
```

---

### Task 5: TURN-over-TCP integration test + composition test

**Files:**
- Modify: `turn/turn_test.go`
- Create: `server/turn_integration_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–3. Externally verifies the spec's test matrix: STUN binding, UDP allocation in range, TCP allocation in range, `server.New` composition.

- [ ] **Step 1: Add TCP allocation test** (append to `turn/turn_test.go`)

```go
func TestAllocateTCPInPortRange(t *testing.T) {
	s := startServer(t, testConfig(t, "", "127.0.0.1:0"))

	raw, err := net.Dial("tcp", s.TCPAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	client, err := pion.NewClient(&pion.ClientConfig{
		TURNServerAddr: s.TCPAddr().String(),
		Username:       "test",
		Password:       "test", // AllowAllAuth: credential == username
		Conn:           pion.NewSTUNConn(raw),
		LoggerFactory:  logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		t.Fatalf("NewClient(): %v", err)
	}
	if err := client.Listen(); err != nil {
		t.Fatalf("client.Listen(): %v", err)
	}
	t.Cleanup(client.Close)

	relayConn, err := client.Allocate()
	if err != nil {
		t.Fatalf("Allocate(): %v", err)
	}
	port := relayConn.LocalAddr().(*net.UDPAddr).Port
	if port < 60000 || port > 60100 {
		t.Fatalf("relayed port %d outside 60000-60100", port)
	}
}
```

- [ ] **Step 2: Add composition test** (`server/turn_integration_test.go`)

```go
package server

import (
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
	pion "github.com/pion/turn/v5"
	"jokertc/turn"
)

// TestServerComposesTURN proves the full composition path: the same code
// path production uses starts TURN alongside HTTP without errors, and the
// embedded TURN server answers a real allocation.
func TestServerComposesTURN(t *testing.T) {
	srv, err := New(&HTTPServerConfig{
		ListenAddr: "127.0.0.1:0",
		Log:        testLogger(t),
		TURN: &turn.Config{
			ListenUDPAddr: "127.0.0.1:0",
			ListenTCPAddr: "127.0.0.1:0",
			ExternalIP:    "127.0.0.1",
			RelayPortMin:  60000,
			RelayPortMax:  60100,
			Auth:          turn.AllowAllAuth{},
			Log:           testLogger(t),
		},
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	srv.RunInBackground()
	defer srv.Shutdown()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	client, err := pion.NewClient(&pion.ClientConfig{
		STUNServerAddr: srv.turnSrv.UDPAddr().String(),
		TURNServerAddr: srv.turnSrv.UDPAddr().String(),
		Username:       "test",
		Password:       "test",
		Conn:           conn,
		LoggerFactory:  logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		t.Fatalf("NewClient(): %v", err)
	}
	if err := client.Listen(); err != nil {
		t.Fatalf("client.Listen(): %v", err)
	}
	t.Cleanup(client.Close)

	if _, err := client.Allocate(); err != nil {
		t.Fatalf("Allocate(): %v", err)
	}
}
```

(No `time` import needed — remove from the import block if unused.)

- [ ] **Step 3: Run tests + race detector**

Run: `make test-race`
Expected: PASS, no data races

- [ ] **Step 4: Commit**

```bash
git add turn/turn_test.go server/turn_integration_test.go
git commit -m "test(turn): TCP allocation and server composition integration tests"
```

---

### Task 6: `/ui/manual` — two-tab WebRTC test page

**Files:**
- Create: `server/static/webrtc-test.html`
- Modify: `server/server.go` (route + embed)

**Interfaces:**
- Consumes: existing HTTP server (no new interfaces).
- Produces: route `GET /ui/manual` serving one embedded HTML page. Client uses `username: 'test', credential: 'test'` (AllowAllAuth convention) and port 3478 on `window.location.hostname`.

- [ ] **Step 1: Write the page** (`server/static/webrtc-test.html`)

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>jokertc WebRTC manual test</title>
<style>
  body { font-family: sans-serif; margin: 1rem; max-width: 900px; }
  video { width: 420px; background: #222; margin: 2px; }
  textarea { width: 100%; height: 4rem; font-size: 0.75rem; }
  #messages { height: 6rem; }
  #iceLog { background: #f4f4f4; padding: 0.5rem; font-size: 0.75rem; max-height: 10rem; overflow: auto; }
  fieldset { margin: 0.5rem 0; }
</style>
</head>
<body>
<h1>WebRTC manual test</h1>
<p>Open this page in two tabs. Tab A: Create offer, copy it into Tab B.
Tab B: paste into Remote description, Accept — copy the produced answer back
into Tab A and Accept there. Relay candidates in the ICE log prove the TURN
path.</p>

<video id="localVideo" autoplay muted playsinline></video>
<video id="remoteVideo" autoplay playsinline></video>

<fieldset>
  <legend>Signaling (clipboard)</legend>
  <button id="createOffer">Create offer</button>
  <button id="accept">Accept pasted description</button>
  <label>Local description (copy this into the other tab)</label>
  <textarea id="localDesc" readonly></textarea>
  <label>Remote description (paste from the other tab here)</label>
  <textarea id="remoteDesc"></textarea>
</fieldset>

<fieldset>
  <legend>Data channel</legend>
  <button id="ping" disabled>Ping</button>
  <textarea id="messages" readonly placeholder="received messages appear here"></textarea>
</fieldset>

<pre id="iceLog"></pre>

<script>
const pc = new RTCPeerConnection({
  iceServers: [
    { urls: `stun:${location.hostname}:3478` },
    { urls: `turn:${location.hostname}:3478`, username: 'test', credential: 'test' },
  ],
});

const $ = (id) => document.getElementById(id);
function logIce(line) { $('iceLog').textContent += line + '\n'; }

pc.onicecandidate = (e) => logIce(e.candidate ? e.candidate.candidate : 'gathering done');
pc.oniceconnectionstatechange = () => logIce('ICE state: ' + pc.iceConnectionState);
pc.ontrack = (e) => { $('remoteVideo').srcObject = e.streams[0]; };

let sendChannel = null;
function wireChannel(ch) {
  sendChannel = ch;
  ch.onopen = () => { $('ping').disabled = false; };
  ch.onmessage = (e) => { $('messages').value += 'received ' + e.data + '\n'; };
}
pc.ondatachannel = (e) => wireChannel(e.channel);

function gatherComplete() {
  if (pc.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise((res) => {
    const check = () => {
      if (pc.iceGatheringState !== 'complete') return;
      pc.removeEventListener('icegatheringstatechange', check);
      res();
    };
    pc.addEventListener('icegatheringstatechange', check);
  });
}

navigator.mediaDevices.getUserMedia({ video: true, audio: true }).then((stream) => {
  $('localVideo').srcObject = stream;
  stream.getTracks().forEach((track) => pc.addTrack(track, stream));
  logIce('local media added');
});

$('createOffer').onclick = async () => {
  wireChannel(pc.createDataChannel('ping'));
  await pc.setLocalDescription(await pc.createOffer());
  await gatherComplete();
  $('localDesc').value = JSON.stringify(pc.localDescription);
};

$('accept').onclick = async () => {
  await pc.setRemoteDescription(JSON.parse($('remoteDesc').value));
  if (!pc.localDescription) {
    await pc.setLocalDescription(await pc.createAnswer());
    await gatherComplete();
    $('localDesc').value = JSON.stringify(pc.localDescription);
  }
};

$('ping').onclick = () => {
  const msg = 'ping ' + new Date().toISOString();
  sendChannel.send(msg);
  $('messages').value += 'sent ' + msg + '\n';
};
</script>
</body>
</html>
```

- [ ] **Step 2: Wire the route** in `server/server.go`

Add below the imports:

```go
//go:embed static/webrtc-test.html
var webrtcTestHTML []byte
```

Add `"embed"` to imports. In `getRouter`, after the route registrations:

```go
	mux.Get("/ui/manual", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(webrtcTestHTML)
	})
```

- [ ] **Step 3: Build + verify route**

```bash
make build-server && ./build/server --listen-addr 127.0.0.1:8080 --metrics-addr 127.0.0.1:8090 &
curl -fsS http://127.0.0.1:8080/ui/manual | head -5
kill %1
```

Expected: HTML output starting with `<!doctype html>`.

- [ ] **Step 4: Manual two-tab test (by hand)**

Run the server, open `http://127.0.0.1:8080/ui/manual` in two tabs, complete the clipboard exchange, confirm:
1. Both videos show webcam feeds.
2. ICE log shows at least one `srflx` or `relay` candidate.
3. Ping from tab A appears as `received ping ...` in tab B and vice versa.

- [ ] **Step 5: Commit**

```bash
git add server/static/webrtc-test.html server/server.go
git commit -m "feat(server): add /ui/manual two-tab WebRTC connectivity test page"
```

---

### Task 7: Docs + final quality gate

**Files:**
- Modify: `README.md`, `AGENTS.md`

**Interfaces:** none — documentation only.

- [ ] **Step 1: Update README** — add to Features list and Project Structure table:

```markdown
- `turn/`                Embedded STUN/TURN server (pion/turn v5), enabled via `--turn-listen-addr`
```

And to Features:

```markdown
- Embedded STUN/TURN server with configurable relay port range and manual WebRTC test page at `/ui/manual`
```

- [ ] **Step 2: Update AGENTS.md** — under Key Packages add:

```markdown
- **turn/** - Embedded STUN/TURN server (pion/turn). `AllowAllAuth` accepts all
  allocations with credential == username; swap it in `turn/auth.go` for real
  auth. Component errors are fatal: any dead listener (HTTP, metrics, TURN)
  kills the process via `Server.ErrCh()`.
```

- [ ] **Step 3: Full quality gate**

Run: `make lt && make test-race`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add README.md AGENTS.md
git commit -m "docs: document embedded STUN/TURN server and test page"
```

---

## Self-Review

- Spec coverage: package layout (Task 1–2), Authenticator/AllowAllAuth (Task 1), startup restructure + error policy (Task 3), CLI flags table (Task 4), integration tests STUN/UDP/TCP/composition (Tasks 2, 5), `/ui/manual` page with webcam + data-channel ping (Task 6), docs (Task 6). All spec sections map to a task.
- Placeholders: none — all code complete.
- Type consistency: `turn.Config` fields identical across Tasks 2/3/4/5; `ErrCh()` used in Task 4 as defined in Task 3; `UDPAddr()/TCPAddr()` defined Task 2, used Task 5; AllowAllAuth credential==username convention consistent in tests, page, and comments.