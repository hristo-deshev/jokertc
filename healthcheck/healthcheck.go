package healthcheck

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httplog/v2"
	"go.uber.org/atomic"
)

type Opts struct {
	Log           *httplog.Logger
	DrainDuration time.Duration
}

type Healthcheck struct {
	opts    *Opts
	isReady atomic.Bool
}

func New(opts *Opts) *Healthcheck {
	h := &Healthcheck{opts: opts}
	h.isReady.Swap(true)

	return h
}

// RegisterRoutes registers the healthcheck endpoints onto the given router.
func (h *Healthcheck) RegisterRoutes(r chi.Router) {
	r.Get("/livez", h.handleLivenessCheck)
	r.Get("/readyz", h.handleReadinessCheck)
	r.Get("/drain", h.handleDrain)
	r.Get("/undrain", h.handleUndrain)
}

func (h *Healthcheck) handleLivenessCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK")) //nolint:errcheck
}

func (h *Healthcheck) handleReadinessCheck(w http.ResponseWriter, r *http.Request) {
	if !h.isReady.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready")) //nolint:errcheck
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK")) //nolint:errcheck
}
