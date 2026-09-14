# WebRTC Signaling over WebSocket (`/ws`) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a WebSocket signaling endpoint at `/ws` that pairs a device with a phone and passes SDP offers, answers and ICE candidates between them. It must speak the exact message contract that `../M2M.Tunnel.Relay` speaks today, so existing firmware and phone clients work against it unchanged.

**Architecture:** A new `signaling/` route module, composed by `server.Server` next to `api/`, `healthcheck/`, `metrics/` and `turn/`. Media never touches this server — ICE runs peer to peer, with the embedded STUN/TURN server as the fallback path.

**Tech Stack:** Go, chi router, `github.com/coder/websocket`, httplog, VictoriaMetrics metrics, `encoding/json`. Tests additionally use `github.com/pion/webrtc/v4` (Task 9) — the production code never imports it.

**Protocol source:** `../M2M.Tunnel.Relay/M2M.Relay.Server/Services/WebRtc/WebRtcRelayService.cs`. Read it for the *message contract only*. The internal design in this plan is deliberately different — do not mirror the C# class structure.

---

## Scope

### In scope — signaling only

The C# service has two modes:

| C# mode | What it does | Port? |
|---|---|---|
| `Forward` | Relay passes signaling JSON between device and phone. Media is peer to peer. | **Yes** |
| `Terminate` | Relay is a WebRTC peer of both sides (SIPSorcery `RTCPeerConnection`) and copies RTP video and audio plus data-channel messages between them. | **No** |

This plan ports `Forward` only. That is the part the request describes: send and receive SDP offers and responses.

`Terminate` is a selective forwarding unit. A Go port needs `github.com/pion/webrtc/v4`, H.264 and PCMU track setup, RTP forwarding and a data-channel bridge. Separate, much larger work. Task 9 pulls `pion/webrtc` in **as a test dependency only**, to act as the two client peers; that is not a start on `Terminate`, and no production file may import it. The `signaling/` package built here stays the transport layer underneath it, so nothing in this plan must be undone if it is added later.

Consequence to accept: the `mode` field in the `joined` message is always `"forward"`.

### Out of scope

- `Terminate` mode.
- Authentication of the WebSocket. Any client that knows a session id can join it. The C# service has the same property. See "Open questions".
- Validation of `imei`. It is carried and logged only.

---

## Wire protocol — the contract

JSON text frames, camelCase field names, one message per frame. This is the part that must not drift.

### Client to server

| `type` | Fields | Meaning |
|---|---|---|
| `join` | `role` (`"device"` or `"phone"`), `session` (string), `imei` (string, optional) | Must be the first message on the socket. |
| `offer` | `sdp` | Passed to the other peer unchanged. |
| `answer` | `sdp` | Passed to the other peer unchanged. |
| `candidate` | `candidate`, `sdpMid`, `sdpMLineIndex` | Passed to the other peer unchanged. |
| `bye` | — | Client leaves. Server closes the socket. |

### Server to client

| `type` | Fields | When |
|---|---|---|
| `joined` | `mode` (always `"forward"`) | Right after a valid `join`. |
| `ready` | `initiator` (bool), `stun` (string), `turn` (`{url, user, pass}` or null) | Sent once to each side when both sides are present. |
| `offer` / `answer` / `candidate` | verbatim copy of the other side's frame | On every forwarded message. |
| `bye` | — | The other side left, or the no-receiver timeout fired. |

### Behaviour the contract requires

Each item is observable by a client, so each one needs a test. Reasons are given because several look arbitrary and are not.

1. **First message must be `join`.** Anything else closes the socket with no reply.
2. **A `join` with an empty session id, or a role other than `device` or `phone`, closes the socket.**
3. **Session ids compare case-insensitively.**
4. **The phone is the initiator; the device is not.** `ready` carries `initiator: true` to the phone, `false` to the device. Reason, from the C# source: the device firmware's own offer hard-codes H.264 Main profile (`4d001f`), which libwebrtc rejects. As the answerer the device picks Constrained Baseline out of the phone's offer. Reversing this breaks video on real hardware.
5. **`ready` is sent once per pairing**, not once per message.
6. **A second `join` with the same role replaces the earlier connection.** The replaced client's socket is closed and the *surviving* peer gets no `bye` — from its point of view nothing happened. The next pairing sends `ready` again.
7. **No-receiver timeout.** A `device` alone for `NoReceiverTimeout` (default 20 s) gets `bye` and its socket closes. `0` disables it. It must not fire if the phone arrived first, or if that device already left or was replaced.
8. **When one side goes away the other gets `bye`.**
9. **A frame that is not JSON is logged and ignored.** The socket stays open.
10. **An unknown `type` is logged at debug level and ignored.**
11. **Forwarded frames are byte-identical.** Do not decode and re-encode them; clients send fields this server does not model.

---

## Internal design

Not a transliteration of the C# service. The shape below is chosen for Go.

### One mutex, and sends that cannot block

The whole pairing state is a `map[string]*session` behind one `sync.Mutex` on `signaling.Server`. A mutex-guarded map is the ordinary Go answer for a registry, and this one is small and uncontended.

What makes that safe is the second half: **a peer is written to through a buffered channel, never through its socket.** Notifying a peer is a channel send into a buffer, which is a memory write, so it cannot block on a slow or dead network client. That means the critical sections stay short and readable, and "never do I/O while holding a lock" is satisfied by construction rather than by discipline.

If a peer's buffer is full, that peer is not keeping up and gets closed. This is the `closeSlow` pattern from `coder/websocket`'s own chat example.

```go
type peer struct {
	role, sessionID string
	out   chan []byte   // buffered; writer goroutine drains it
	close func(reason string)
}

func (p *peer) send(msg []byte) {
	select {
	case p.out <- msg:
	default:
		p.close("send buffer full")
	}
}
```

### The state machine never touches a socket

`peer` above has no `*websocket.Conn` in it. The registry only ever calls `send` and `close`. So the entire pairing state machine — join, replace, pair, forward, leave, timeout — is unit-testable with no network, no `httptest`, no goroutine timing. Only the connection layer needs a real socket, and it is thin.

Do not let a `*websocket.Conn` leak into `session` or into the registry methods. That is the single design constraint that keeps this package cheap to test.

### One writer goroutine per connection

The socket's write side has exactly one owner: a loop that drains `out` and also fires the keepalive ping. No write mutex, no semaphore around sends.

```go
func (c *conn) writeLoop(ctx context.Context) error {
	ping := time.NewTicker(c.pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-c.peer.out:
			if err := c.ws.Write(ctx, websocket.MessageText, msg); err != nil {
				return err
			}
		case <-ping.C:
			// Ping is a round trip: it blocks until the pong arrives. Bound it,
			// or one silent peer stalls its own outbound queue indefinitely.
			pctx, cancel := context.WithTimeout(ctx, c.pingTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}
```

A ping timeout is the drop signal for a peer that is still connected at the TCP level but no longer answering — the case a plain read deadline does not catch.

The HTTP handler goroutine runs the read loop, because a blocking read is what a handler goroutine is for. When either loop returns, it cancels the connection's context and the other unwinds.

### Cleanup is `defer`, and identity replaces the detached flag

The C# code carries an `Interlocked` "detached" flag on every leg to make cleanup idempotent. Not needed here. The handler does:

```go
defer s.leave(p)
```

and `leave` removes the peer only if the map still points at *that exact pointer*:

```go
func (s *Server) leave(p *peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[p.sessionID]
	if sess == nil || sess.slot(p.role) != p {
		return // already replaced by a newer connection
	}
	...
}
```

A replaced peer therefore cleans up to a no-op without any flag, and rule 6 falls out of the same check instead of needing its own code path.

### Shutdown: `context.AfterFunc`, no socket sweep

`signaling.New` makes one base context. Each connection registers a cancel hook on it:

```go
stop := context.AfterFunc(s.baseCtx, func() { ws.CloseNow() })
defer stop()
```

`Close()` cancels the base context and waits on a `sync.WaitGroup`. No list of open sockets to walk, no second lock, and the hook is unregistered when the connection ends normally, so a long-lived server does not accumulate them. Go 1.21+; this repo is on 1.27.

This is required, not a nicety: `http.Server.Shutdown` neither waits for nor closes hijacked connections, so without it every open WebSocket outlives shutdown.

### The lonely-device timeout is a `time.Timer` on the session

```go
sess.lonely = time.AfterFunc(d, func() { s.expireLonely(sess.id, p) })
```

Stopped under the lock when the phone joins. `expireLonely` re-takes the lock and re-checks pointer identity, the same check `leave` uses. No polling, no "did the world change" re-validation spread across a goroutine.

### Naming

Consistent with the existing modules (`turn.Server`, `healthcheck.Healthcheck`):

| Type | Role |
|---|---|
| `signaling.Server` | Package entry point. Owns config, logger, the session map and the base context. Has `RegisterRoutes(chi.Router)` and `Close()`. |
| `session` (unexported) | One pairing: a device slot, a phone slot, imei, ready flag, lonely timer. |
| `peer` (unexported) | One connected client as the state machine sees it: role, session id, `out` channel, `close` func. |
| `conn` (unexported) | The socket layer: read loop, write loop, JSON framing. |

### Files

```
signaling/doc.go          package doc, protocol summary, pointer to this plan
signaling/config.go       Config
signaling/protocol.go     message structs, type sniffing
signaling/server.go       Server, session, peer — the state machine, no sockets
signaling/conn.go         websocket accept, read loop, write loop, ping
signaling/routes.go       RegisterRoutes, the /ws handler
signaling/credentials.go  TURN credential for the ready message
```

---

## Server changes

`server/server.go`, five edits:

1. `HTTPServerConfig` gains `Signaling *signaling.Config`. `nil` disables the endpoint, same shape as `TURN *turn.Config`.
2. `Server` gains `signaling *signaling.Server`.
3. `New` builds it when `cfg.Signaling != nil`.
4. `getRouter` calls `srv.signaling.RegisterRoutes(mux)` when it exists.
5. `Shutdown` calls `srv.signaling.Close()` **before** `srv.srv.Shutdown(ctx)`, for the reason given above.

`cmd/server/main.go` gains flags:

| Flag | Default | Purpose |
|---|---|---|
| `--signaling` | `true` | Master switch for `/ws`. |
| `--signaling-stun-url` | `""` | STUN URL advertised in `ready`. |
| `--signaling-turn-url` | `""` | TURN URL advertised in `ready`. Empty sends `null`. |
| `--signaling-no-receiver-seconds` | `20` | Lonely-device timeout. `0` disables. |
| `--signaling-ping-seconds` | `30` | Keepalive ping interval. `0` disables. |

---

## Global constraints

- Import path `jokertc/signaling`.
- No `*websocket.Conn` in `server.go`. Enforced by review; it is what keeps the tests socket-free.
- Never log SDP bodies or ICE candidates above `Debug`. Log type, roles, session id and byte count at `Info`.
- Never log the TURN credential.
- `make test-race` is mandatory for this package.
- The feature is not done until **both** integration tests pass: Task 7 (the contract over a real listener) and Task 9 (a real WebRTC call, relayed through the embedded TURN server, carrying data). A signaling test alone cannot tell a usable credential from a well-formed one.
- Commit after every task.

---

### Task 1: Dependency, package skeleton, config

**Files:** create `signaling/doc.go`, `signaling/config.go`

- [x] **Step 1: Add the dependency**

```bash
go get github.com/coder/websocket
go mod tidy
```

- [x] **Step 2: `doc.go`** — what the package does, the message contract in brief, a pointer to this plan, and the "no sockets in the state machine" rule.

- [x] **Step 3: `config.go`**

```go
type Config struct {
	StunURL           string
	TurnURL           string
	NoReceiverTimeout time.Duration // 0 disables
	PingInterval      time.Duration // 0 disables
	PingTimeout       time.Duration // pong deadline; default 10s
	SendBuffer        int           // outbound queue depth per peer; default 16
	Log               *httplog.Logger
}
```

- [x] **Step 4: Verify** — `make lint && make test`

---

### Task 2: Protocol types

**Files:** create `signaling/protocol.go`, `signaling/protocol_test.go`

**Produces:** `joinMessage`, `joinedMessage`, `readyMessage`, `turnCredential`, and `frameType(raw []byte) (string, bool)`.

- [x] **Step 1: Failing tests first**

- `ready` with no TURN URL marshals `"turn": null`; with one, marshals the nested object.
- Field names are camelCase.
- `frameType` returns the type for valid JSON, `false` for garbage.

- [x] **Step 2: Implement**

Incoming frames are sniffed for `type` only. `offer`, `answer` and `candidate` are forwarded as the original `[]byte` — contract rule 11.

- [x] **Step 3: Verify** — `go test ./signaling/...`

---

### Task 3: State machine

**Files:** create `signaling/server.go`, `signaling/server_test.go`

**Produces:** `New(*Config) (*Server, error)`, `Close()`, and unexported `join`, `leave`, `forward`, `expireLonely`, plus `session` and `peer`.

No sockets. Tests build a `peer` with a plain channel and a func, and read what the state machine queued.

- [x] **Step 1: Failing tests for contract rules 2, 3, 5, 6, 7, 8**

- Empty session id or bad role is rejected.
- `Device` and `device` land in the same session.
- Both sides present: phone gets `initiator: true`, device gets `false`, once.
- Second `device` join closes the first connection, leaves the phone unnotified, and re-arms `ready`.
- A peer leaving queues `bye` on the other one and drops the empty session.
- Lonely device times out; a device whose phone arrives first does not.
- `leave` on an already-replaced peer is a no-op.

- [x] **Step 2: Failing test for the slow-peer rule**

A peer whose `out` buffer is full gets `close` called rather than blocking the caller. Assert the sender returns promptly.

- [x] **Step 3: Implement**

- [x] **Step 4: Verify** — `make test-race`

---

### Task 4: Connection layer

**Files:** create `signaling/conn.go`, `signaling/conn_test.go`

- [x] **Step 1: Failing tests** (real sockets via `httptest`, thin)

- A first frame that is not `join` closes the socket.
- A valid `join` gets `joined`.
- A non-JSON frame is ignored and the socket survives a following valid frame.
- An unknown `type` is ignored the same way.

- [x] **Step 2: Implement the read loop** — sniff, dispatch, `defer s.leave(p)`.

- [x] **Step 3: Implement the write loop** — single owner, ping ticker in the same select.

- [x] **Step 4: Wire cancellation** — `context.AfterFunc` on the base context; either loop returning cancels the connection context.

- [x] **Step 5: Verify** — `make test-race`

---

### Task 5: HTTP route

**Files:** create `signaling/routes.go`, `signaling/routes_test.go`

> **Correction, recorded during implementation.** Earlier revisions of this plan claimed a `net/http` deadline trap: that `ReadTimeout`/`WriteTimeout` survive the hijack and kill an idle WebSocket. **That is wrong.** `hijackLocked` in `net/http/server.go` calls `rwc.SetDeadline(time.Time{})` as part of hijacking, so the runtime clears both deadlines itself. No `http.NewResponseController` call is needed, and the middleware `Unwrap()` audit this task used to demand is unnecessary. The regression test in Step 2 is kept anyway — it is cheap, and it pins the behaviour if a future library or Go change alters it.

- [x] **Step 1: `RegisterRoutes(chi.Router)` registering `GET /ws`.**

- [x] **Step 2: Idle-socket regression test**

Serve the **real middleware chain** (`httplog.RequestLogger`, `middleware.Recoverer`, `metrics.Middleware`) behind an `http.Server` with a short but non-zero `WriteTimeout`. Pair two clients, go silent for longer than that timeout, then forward a frame and assert it arrives.

- [x] **Step 3: A non-WebSocket request to `/ws` returns 400**

`websocket.Accept` answers a plain request with **426 Upgrade Required**. The C# contract is 400, so the handler screens the `Upgrade` header before calling `Accept`. 426 is the more correct status in HTTP terms; 400 is kept only for contract fidelity, and is worth revisiting if no client depends on it.

- [x] **Step 4: Verify** — `make test-race && make lint`

---

### Task 6: Compose into `server.Server`

**Files:** modify `server/server.go`, `cmd/server/main.go`

- [x] **Step 1: The five `server.go` edits.**

- [x] **Step 2: CLI flags.**

- [x] **Step 3: Verify** — `make test-race && make lint`

The integration test that proves this composition is Task 7. Do not mark Task 6 done on the strength of unit tests.

---

### Task 7: Integration test over a real WebSocket listener

**Files:** create `server/signaling_integration_test.go`

**Required, not optional.** Tasks 3 and 4 test the state machine with no network and the socket layer with a bare handler. Neither exercises the path production actually runs: the real chi router, the real middleware chain, the real `http.Server` with its timeouts, and a real client dialling over TCP. The failure this level exists to catch is hijacked sockets surviving `Shutdown`, which no lower test can see.

Follow the shape of `server/turn_integration_test.go`: build the server through `New(&HTTPServerConfig{...})` with `ListenAddr: "127.0.0.1:0"`, call `RunInBackground()`, `defer srv.Shutdown()`, and drive it with a real client.

**Constraints on this test file:**

- Bind `127.0.0.1:0` and read the real port back. No fixed ports.
- Configure the server with the **production timeouts** (`ReadTimeout: 60s`, `WriteTimeout: 30s`), not zeroed ones, so the test runs against the real server shape.
- Go through `New()` and `RunInBackground()`. Do not hand-build a router or call the handler directly — the point is the composition path.
- Real `websocket.Dial` clients over `ws://127.0.0.1:<port>/ws`. No in-process fakes.
- Every wait is bounded by `t.Context()` or an explicit timeout. No unbounded reads, no `time.Sleep` as a synchronisation primitive.

- [x] **Step 1: Full call setup between two clients**

Dial two clients. Join as `device` and `phone`. Assert:
- each gets `joined` with `mode: "forward"`;
- the phone's `ready` has `initiator: true`, the device's has `false`;
- each side gets `ready` exactly once.

The credential inside `ready` is asserted in Task 8, which builds it.

- [x] **Step 2: Byte-identical forwarding**

Send an `offer` from the phone containing a field this server does not model. Assert the device receives the frame **byte for byte**, extra field intact. Repeat for `answer` and `candidate` from the device. This is contract rule 11, and it can only be proven end to end.

- [x] **Step 3: Peer loss and re-join**

- One client closes; assert the other receives `bye`.
- A second `device` joins the same session; assert the first device's socket closes, the phone receives **no** `bye`, and the new pairing produces a fresh `ready` on both sides.

- [x] **Step 4: Lonely-device timeout**

Server configured with a short `NoReceiverTimeout`. A lone device receives `bye` and its socket closes. A device whose phone arrives first does not.

- [x] **Step 5: Shutdown closes live sockets**

Open two paired clients, call `srv.Shutdown()`. Assert every client read returns a close error inside the graceful window, and that `Shutdown()` itself returns rather than hanging on `wg.Wait()`. This is the test that fails if step 5 of the `server.go` edits is missing or ordered after `srv.srv.Shutdown(ctx)`.

- [x] **Step 6: Verify** — `make test-race && make lint`

`make test-race` matters most here. This file is the only place where the writer goroutine, the read loop, the lonely timer and shutdown all run concurrently against real sockets.

---

### Task 8: TURN credential in `ready`

**Files:** create `signaling/credentials.go`, `signaling/credentials_test.go`; modify `signaling/config.go`

The C# service points at an external TURN and can mint coturn REST credentials. This repository *embeds* TURN, and `turn.AllowAllAuth` accepts any allocation whose credential equals its username. So phase 1 issues a credential the embedded server already accepts, with no new TURN code.

- [x] **Step 1: Failing tests**

- No `TurnURL` produces `"turn": null`.
- A `TurnURL` produces a credential whose `user` equals its `pass`.
- Two sessions get different credentials.

- [x] **Step 2: Implement** — per-session token from `crypto/rand`, hex encoded.

- [x] **Step 3: Add the HMAC config surface without implementing it**

`TurnSecret string` and `TurnCredentialTTL time.Duration`, documented as unimplemented and naming what is missing: a matching `turn.Authenticator` in `turn/`. `New` returns an error if `TurnSecret` is set, rather than issuing credentials the embedded server would reject.

- [x] **Step 4: Extend the Task 7 integration test**

With a TURN URL configured, assert `ready` reaches both clients carrying a credential whose `user` equals its `pass`, and that a second session gets a different one.

- [x] **Step 5: Verify** — `make test-race`

---

### Task 9: End-to-end WebRTC call over the embedded TURN server

**Files:** create `server/webrtc_e2e_test.go`

**Required.** Task 7 proves the signaling bytes arrive. It does not prove they are *usable*: that two real WebRTC peers can complete ICE using the credential this server hands out, allocate on the embedded TURN server, and move application data. This task closes that loop.

**New dependency:** `github.com/pion/webrtc/v4`, used by tests only. It still lands in `go.mod` as an ordinary `require`. That cost is accepted — there is no way to prove a media path works without a WebRTC stack.

```bash
go get github.com/pion/webrtc/v4
go mod tidy
```

**Shape of the test**

One `server.Server` built through `New()` with **both** signaling and TURN enabled, exactly as production composes them. Two `webrtc.PeerConnection` instances in the test process, each driven by its own WebSocket client speaking the contract from Task 7:

- The **phone** peer is the initiator, per contract rule 4. It creates the offer.
- The **device** peer answers.
- Each side's ICE servers come from the `ready` message it received, not from test constants. That is the assertion that the credential is real.
- Both peers set `ICETransportPolicy: webrtc.ICETransportPolicyRelay`.

That last line is the whole point. Relay-only policy discards host and server-reflexive candidates, so the connection **cannot** succeed unless the peer allocated a relay on our embedded TURN server with the credential our signaling server issued. A test without it would pass over loopback host candidates and prove nothing about TURN.

**The TURN address problem, and how the test handles it**

`signaling.Config.TurnURL` is a static string, but the test binds TURN on port 0 and does not know the port until `New()` returns. Do not contort the config to solve this. Split the assertion:

1. **Credential usability** — build the `webrtc.ICEServer` from `srv.turnSrv.UDPAddr()` (the helper `turnUDPAddr` in `server/turn_integration_test.go` already does this) combined with the `username` and `credential` taken from the live `ready` message. This proves the issued credential is accepted by the embedded server.
2. **URL passthrough** — assert separately, in Task 7, that a configured `TurnURL` string arrives in `ready` unchanged.

Note in the test file why it is split, so nobody later "fixes" it by hardcoding a port.

- [x] **Step 1: Wire the two peers and complete the call**

Both clients join one session. Phone creates a data channel, then an offer; device answers; both trickle candidates through `/ws`. Assert both peer connections reach `webrtc.PeerConnectionStateConnected`.

- [x] **Step 2: Assert the path is actually relayed**

Read the nominated candidate pair off each peer connection's stats and assert both selected candidates are of type `relay`. Connected-plus-relay-policy is strong evidence, but reading the pair makes the failure message say *which* leg failed instead of just timing out.

- [x] **Step 3: Exchange data**

Phone sends a payload over the data channel. Device receives it. Assert the bytes match. Then send one back the other way and assert the same. Bidirectional, because a one-way test passes with a half-open allocation.

- [x] **Step 4: Assert the TURN server saw the allocations**

Check the TURN server's allocation count, or the relay-allocation metric, is non-zero while the call is up. This is what distinguishes "ICE succeeded somehow" from "ICE succeeded through our TURN server".

- [x] **Step 5: Bound every wait**

ICE over loopback with a relay policy settles in well under a second, but the test must fail with a clear message rather than hang. Use `t.Context()` plus an explicit timeout in the low tens of seconds on every state transition, and never `time.Sleep` to wait for a state.

- [x] **Step 6: Give TURN its own relay port range**

`server/turn_integration_test.go` already claims `60000-60100`. Use a distinct range here so the two tests cannot collide.

- [x] **Step 7: Verify** — `make test-race && make lint`

**Note on flakiness:** this is the one test in the plan that depends on real network timing and on a port range being free. If it proves unstable in CI, the correct fix is a longer bound or a different port range — not deleting the relay policy, and not dropping to host candidates. Those changes would leave the test passing while it stops testing anything.

---

### Task 10: Metrics

**Files:** modify `signaling/server.go`; create `signaling/metrics_test.go`

- [x] **Step 1: Add via the existing `metrics` package**

- `signaling_sessions_active` (gauge)
- `signaling_peers_total{role=}` (counter)
- `signaling_messages_total{type=,direction=}` (counter)
- `signaling_no_receiver_timeouts_total` (counter)
- `signaling_slow_peer_drops_total` (counter)

The last one matters operationally: it is the only visible symptom of a client that cannot drain its queue.

- [x] **Step 2: Assert the names appear on `/metrics`.**

- [x] **Step 3: Verify** — `make test-race && make lint`

---

### Task 11: Extend `/ui/manual` to drive `/ws`

**Files:** modify `server/static/webrtc-test.html`

The page today only builds a peer connection against a hard-coded STUN/TURN URL. Add a panel that connects to `/ws`, joins with a role and session id from two inputs, and drives a real call between two browser tabs. Fastest way to confirm the port against a real browser before pointing firmware at it.

- [x] **Step 1: Implement**
- [x] **Step 2: Verify by hand — two tabs, one `device`, one `phone`**
- [x] **Step 3: Verify** — `make lint && make test`

---

## Open questions

1. **Authentication.** The endpoint is unauthenticated: anyone who guesses a session id can join as either role and receive the other side's SDP and the TURN credential. The C# service is the same, so this port is not a regression. Decide whether to require a token on `join` before exposing it outside a trusted network.
2. **Session id origin.** Any client-supplied string is accepted. Confirm who generates session ids today and whether they are guessable.
3. **`imei`.** Carried and logged only. Confirm nothing downstream expects this server to validate it.
