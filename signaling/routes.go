package signaling

import (
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

// RegisterRoutes registers the signaling endpoint onto the given router.
func (s *Hub) RegisterRoutes(r chi.Router) {
	r.Get("/ws", s.handleWS)
}

// clientAddr reports the address to log for a connection. RemoteAddr is the
// socket peer and cannot be forged; behind a proxy that is the proxy, so a
// forwarded-for header is appended when present. That header IS forgeable by
// anything upstream of a trusted proxy, so the two are reported separately
// rather than the header silently replacing the observed address.
func clientAddr(r *http.Request) string {
	addr := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		addr += " (x-forwarded-for: " + fwd + ")"
	}
	return addr
}

func (s *Hub) handleWS(w http.ResponseWriter, r *http.Request) {
	// websocket.Accept answers a plain request with 426 Upgrade Required. The
	// contract inherited from the C# relay is 400, so screen the request here.
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "expected a WebSocket upgrade", http.StatusBadRequest)
		return
	}

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Info("signaling upgrade failed", "err", err, "remoteAddr", r.RemoteAddr)
		return
	}
	s.handleConn(r.Context(), ws, clientAddr(r))
}
