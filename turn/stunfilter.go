package turn

import (
	"net"
	"sync/atomic"

	"github.com/go-chi/httplog/v2"
)

// stunFilterConn drops STUN Binding requests before pion sees them, which
// leaves the port answering TURN and nothing else.
//
// pion has no option for this: internal/server/server.go always routes method
// Binding to handleBindingRequest. Filtering the PacketConn is the only place
// to intervene without forking the library.
//
// Only UDP is filtered. A server-reflexive candidate is gathered over UDP, so
// that is enough to stop a peer learning its mapped address here. TURN over
// TCP still answers Binding requests, and only yields relay candidates anyway.
//
// This stops neither a host candidate nor a direct path. Two peers on one LAN
// exchange host candidates in their SDP and pair them without asking any
// server, so forcing a relay is a decision for the peers, not for this server.
type stunFilterConn struct {
	net.PacketConn
	log     *httplog.Logger
	dropped atomic.Int64
}

func (c *stunFilterConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(p)
		if err != nil {
			return n, addr, err
		}
		if !isBindingRequest(p[:n]) {
			return n, addr, nil
		}
		total := c.dropped.Add(1)
		if c.log != nil { // Config.Log is optional
			c.log.Debug("dropped a STUN binding request", "remoteAddr", addr.String(), "droppedTotal", total)
		}
	}
}
