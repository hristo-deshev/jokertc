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

func (s *Hub) handleWS(w http.ResponseWriter, r *http.Request) {
	// websocket.Accept answers a plain request with 426 Upgrade Required. The
	// contract inherited from the C# relay is 400, so screen the request here.
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "expected a WebSocket upgrade", http.StatusBadRequest)
		return
	}

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Info("signaling upgrade failed", "err", err)
		return
	}
	s.handleConn(r.Context(), ws)
}
