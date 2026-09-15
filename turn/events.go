package turn

import (
	"net"

	"github.com/go-chi/httplog/v2"
	pion "github.com/pion/turn/v5"
)

// newEventHandler returns the callbacks that report the life of a relay
// session. pion calls them from the goroutine that serves the request, so they
// must not block.
//
// The line that says traffic can now flow is "TURN forwarding started", raised
// on a permission or a channel bind. An allocation on its own forwards nothing:
// it reserves a relay port, and the server drops everything from a peer the
// client has not named yet.
//
// Config.Log is optional, and no handler is installed without it.
func newEventHandler(log *httplog.Logger) pion.EventHandler {
	if log == nil {
		return pion.EventHandler{}
	}
	return pion.EventHandler{
		OnAllocationCreated: func(src, dst net.Addr, proto, userID, _ string, relayAddr net.Addr, requestedPort int) {
			log.Info("TURN allocation created",
				"client", addrString(src), "listener", addrString(dst), "proto", proto,
				"user", userID, "relayAddr", addrString(relayAddr), "requestedPort", requestedPort)
		},
		OnAllocationDeleted: func(src, dst net.Addr, proto, userID, _ string) {
			log.Info("TURN allocation deleted",
				"client", addrString(src), "listener", addrString(dst), "proto", proto, "user", userID)
		},
		OnAllocationError: func(src, dst net.Addr, proto, message string) {
			log.Warn("TURN allocation error",
				"client", addrString(src), "listener", addrString(dst), "proto", proto, "err", message)
		},
		OnPermissionCreated: func(src, _ net.Addr, proto, userID, _ string, relayAddr net.Addr, peer net.IP) {
			log.Info("TURN forwarding started",
				"via", "permission", "client", addrString(src), "proto", proto, "user", userID,
				"relayAddr", addrString(relayAddr), "peer", peer.String())
		},
		OnPermissionDeleted: func(src, _ net.Addr, proto, userID, _ string, relayAddr net.Addr, peer net.IP) {
			log.Info("TURN forwarding stopped",
				"via", "permission", "client", addrString(src), "proto", proto, "user", userID,
				"relayAddr", addrString(relayAddr), "peer", peer.String())
		},
		OnChannelCreated: func(src, _ net.Addr, proto, userID, _ string, relayAddr, peer net.Addr, channel uint16) {
			log.Info("TURN forwarding started",
				"via", "channel", "client", addrString(src), "proto", proto, "user", userID,
				"relayAddr", addrString(relayAddr), "peer", addrString(peer), "channel", channel)
		},
		OnChannelDeleted: func(src, _ net.Addr, proto, userID, _ string, relayAddr, peer net.Addr, channel uint16) {
			log.Info("TURN forwarding stopped",
				"via", "channel", "client", addrString(src), "proto", proto, "user", userID,
				"relayAddr", addrString(relayAddr), "peer", addrString(peer), "channel", channel)
		},
		// Debug only: pion answers the first Allocate of every session with a
		// 401 challenge, so a false verdict here is the normal opening move and
		// not a failure worth an operator's attention.
		OnAuth: func(src, _ net.Addr, proto, username, _, method string, verdict bool) {
			log.Debug("TURN auth",
				"client", addrString(src), "proto", proto, "user", username,
				"method", method, "ok", verdict)
		},
	}
}

func addrString(a net.Addr) string {
	if a == nil {
		return ""
	}
	return a.String()
}
