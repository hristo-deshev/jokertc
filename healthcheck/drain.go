package healthcheck

import (
	"net/http"
	"time"
)

func (h *Healthcheck) handleDrain(w http.ResponseWriter, r *http.Request) {
	if wasReady := h.isReady.Swap(false); !wasReady {
		return
	}
	h.opts.Log.Info("Server marked as not ready")
	time.Sleep(h.opts.DrainDuration) // Give LB enough time to detect us not ready
}

func (h *Healthcheck) handleUndrain(w http.ResponseWriter, r *http.Request) {
	if wasReady := h.isReady.Swap(true); wasReady {
		return
	}
	h.opts.Log.Info("Server marked as ready")
}
