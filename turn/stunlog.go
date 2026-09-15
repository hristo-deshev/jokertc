package turn

import (
	"net"
	"sync"

	"github.com/go-chi/httplog/v2"
)

// maxTrackedSources caps the address table. STUN is unauthenticated and anyone
// can send to the port, so the table must not grow with the traffic. Past the
// cap every request is treated as a first one, which errs towards logging too
// much rather than hiding a source.
const maxTrackedSources = 1024

// stunLogConn reports STUN binding traffic on the TURN listener: the request a
// peer makes to learn its own address, and the answer it gets back.
//
// pion raises no event for this. Binding is unauthenticated, so it never
// reaches the auth handler, and EventHandler covers the allocation lifecycle
// only. Reading the packets on the way past is the one place to see them.
//
// The first request from an address is logged at info and the rest at debug.
// An agent re-sends binding requests to keep its NAT mapping alive, which over
// hours would bury everything else in the log. The attempt count goes on every
// line, so the repeats are still visible at info through the next response.
//
// Only UDP is covered, which is where a server-reflexive candidate is gathered.
type stunLogConn struct {
	net.PacketConn
	log *httplog.Logger

	mu       sync.Mutex
	attempts map[string]int64
}

func newSTUNLogConn(conn net.PacketConn, log *httplog.Logger) *stunLogConn {
	return &stunLogConn{PacketConn: conn, log: log, attempts: make(map[string]int64)}
}

func (c *stunLogConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	if err != nil || !isBindingRequest(p[:n]) {
		return n, addr, err
	}

	key := addrString(addr)
	count := c.count(key)
	if count == 1 {
		c.log.Info("STUN binding request", "remoteAddr", key, "attempt", count)
	} else {
		c.log.Debug("STUN binding request", "remoteAddr", key, "attempt", count)
	}
	return n, addr, err
}

func (c *stunLogConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	response, success := isBindingResponse(p)
	if !response {
		return c.PacketConn.WriteTo(p, addr)
	}

	key := addrString(addr)
	n, err := c.PacketConn.WriteTo(p, addr)
	switch {
	case err != nil:
		c.log.Warn("STUN binding response failed to send", "remoteAddr", key, "err", err)
	case success:
		c.log.Info("STUN binding success", "remoteAddr", key,
			"mappedAddr", mappedAddress(p), "attempts", c.attemptsFor(key))
	default:
		c.log.Info("STUN binding error response", "remoteAddr", key, "attempts", c.attemptsFor(key))
	}
	return n, err
}

// count records one attempt from key and returns the running total.
func (c *stunLogConn) count(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.attempts) >= maxTrackedSources {
		if _, known := c.attempts[key]; !known {
			return 1
		}
	}
	c.attempts[key]++
	return c.attempts[key]
}

func (c *stunLogConn) attemptsFor(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts[key]
}
