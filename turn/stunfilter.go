package turn

import (
	"encoding/binary"
	"net"
	"sync/atomic"

	"github.com/go-chi/httplog/v2"
)

// stunMagicCookie is the fixed value at bytes 4..8 of every STUN message
// (RFC 5389 section 6). It is what separates a STUN packet from anything else
// that arrives on the port.
const stunMagicCookie = 0x2112A442

// bindingRequestType is the STUN message type for class Request, method
// Binding. Allocate, Refresh, CreatePermission and ChannelBind all carry a
// different type, so matching on this value alone leaves TURN untouched.
const bindingRequestType = 0x0001

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

func isBindingRequest(b []byte) bool {
	const headerLen = 20
	if len(b) < headerLen {
		return false
	}
	if binary.BigEndian.Uint16(b[0:2]) != bindingRequestType {
		return false
	}
	return binary.BigEndian.Uint32(b[4:8]) == stunMagicCookie
}
