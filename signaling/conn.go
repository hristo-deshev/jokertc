package signaling

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/coder/websocket"
)

const (
	// maxFrameBytes bounds one signaling frame. An SDP with many codecs and
	// candidates comfortably exceeds the library's 32 KiB default.
	maxFrameBytes = 256 * 1024

	// joinTimeout bounds how long a connection may sit without sending its
	// join frame, so an idle dialer cannot hold a goroutine indefinitely.
	joinTimeout = 30 * time.Second

	// closeGrace bounds the flush of already-queued frames when a peer is
	// closed, so a stalled client cannot hold the writer open.
	closeGrace = 5 * time.Second
)

var errClientBye = errors.New("client sent bye")

type conn struct {
	hub     *Hub
	ws      *websocket.Conn
	peer    *peer
	addr    string
	closing chan string
}

// handleConn serves one WebSocket until it closes. The first frame must be a
// valid join; everything after it is dispatched to the state machine.
func (s *Hub) handleConn(parent context.Context, ws *websocket.Conn, addr string) {
	s.wg.Add(1)
	defer s.wg.Done()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// Cancels this connection when the server shuts down. http.Server.Shutdown
	// neither waits for nor closes hijacked connections, so this is the only
	// thing that ends a live socket at shutdown.
	//nolint:contextcheck // baseCtx is deliberately not the request context: it
	// is the server lifetime, and this hook is what ends a live socket at
	// shutdown.
	stop := context.AfterFunc(s.baseCtx, func() { _ = ws.CloseNow() })
	defer stop()

	ws.SetReadLimit(maxFrameBytes)

	c := &conn{hub: s, ws: ws, addr: addr, closing: make(chan string, 1)}

	p, err := c.awaitJoin(ctx)
	if err != nil {
		s.log.Info("signaling connection rejected", "err", err, "remoteAddr", addr)
		_ = ws.Close(websocket.StatusPolicyViolation, "join required")
		return
	}
	c.peer = p
	defer s.leave(p)

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer cancel()
		c.writeLoop(ctx)
	}()

	readErr := c.readLoop(ctx)
	cancel()
	<-writerDone

	// The writer only closes the socket when the state machine asked it to. A
	// read loop that ends on its own — client bye, or a read error — still owns
	// the close. Without this the connection lingers until the process exits.
	_ = ws.Close(websocket.StatusNormalClosure, "bye")

	if !errors.Is(readErr, errClientBye) {
		s.log.Debug("signaling read loop ended", "role", p.role, "session", p.label, "remoteAddr", addr, "err", readErr)
	}
}

// awaitJoin reads frames until one is a valid join. A frame that is not a join
// ends the connection: the contract allows nothing before it.
func (c *conn) awaitJoin(ctx context.Context) (*peer, error) {
	ctx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()

	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}

	c.logFrame("in", data)

	ft, ok := frameType(data)
	if !ok {
		return nil, errors.New("first frame is not JSON")
	}
	if ft != typeJoin {
		return nil, errors.New("first frame must be join, got " + ft)
	}

	var m joinMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return c.hub.join(m, c.addr, c.requestClose)
}

func (c *conn) requestClose(reason string) {
	select {
	case c.closing <- reason:
	default:
	}
}

// logFrame records every frame crossing this socket. The type, direction and
// size go at info; the body only at debug, because SDP and ICE candidates carry
// both peers' network topology and would swamp a normal log.
func (c *conn) logFrame(direction string, raw []byte) {
	ft, ok := frameType(raw)
	if !ok {
		ft = "(not json)"
	}
	role, session := "", ""
	if c.peer != nil {
		role, session = c.peer.role, c.peer.label
	}

	c.hub.log.Info("signaling frame",
		"dir", direction, "type", ft, "role", role, "session", session,
		"remoteAddr", c.addr, "bytes", len(raw))
	c.hub.log.Debug("signaling frame body",
		"dir", direction, "type", ft, "session", session, "body", string(raw))
}

func (c *conn) readLoop(ctx context.Context) error {
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		if err := c.handleFrame(data); err != nil {
			return err
		}
	}
}

func (c *conn) handleFrame(data []byte) error {
	c.logFrame("in", data)

	ft, ok := frameType(data)
	if !ok {
		return nil
	}

	switch ft {
	case typeOffer, typeAnswer, typeCandidate:
		c.hub.forward(c.peer, ft, data)
	case typeBye:
		return errClientBye
	default:
		c.hub.log.Debug("signaling frame ignored", "type", ft, "role", c.peer.role, "session", c.peer.label)
	}
	return nil
}

// writeLoop is the sole owner of the socket's write side, so peers need no
// write lock. It also runs the keepalive ping.
func (c *conn) writeLoop(ctx context.Context) {
	var ping <-chan time.Time
	if c.hub.cfg.PingInterval > 0 {
		ticker := time.NewTicker(c.hub.cfg.PingInterval)
		defer ticker.Stop()
		ping = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.peer.out:
			c.logFrame("out", msg)
			if err := c.ws.Write(ctx, websocket.MessageText, msg); err != nil {
				return
			}
		case reason := <-c.closing:
			c.flushAndClose(reason)
			return
		case <-ping:
			// Ping is a round trip: it blocks until the pong arrives. Bound it,
			// or one silent peer stalls its own outbound queue indefinitely.
			pctx, cancel := context.WithTimeout(ctx, c.hub.cfg.PingTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// flushAndClose delivers frames already queued before the close was requested,
// so a bye sent alongside a close still reaches the client.
// The connection context is already cancelled by the time a close is
// requested, so the flush needs a fresh, bounded one or the queued frames
// could never be written.
//
//nolint:contextcheck // see above: an inherited context here is always dead.
func (c *conn) flushAndClose(reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), closeGrace)
	defer cancel()

	for {
		select {
		case msg := <-c.peer.out:
			c.logFrame("out", msg)
			if err := c.ws.Write(ctx, websocket.MessageText, msg); err != nil {
				_ = c.ws.CloseNow()
				return
			}
		default:
			_ = c.ws.Close(websocket.StatusNormalClosure, truncateReason(reason))
			return
		}
	}
}

// truncateReason keeps the close reason inside the 123-byte limit a WebSocket
// close frame allows.
func truncateReason(reason string) string {
	const maxReason = 120
	if len(reason) > maxReason {
		return reason[:maxReason]
	}
	return reason
}
