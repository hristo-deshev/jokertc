package metrics

import (
	"net/http"

	victoriaMetrics "github.com/VictoriaMetrics/metrics"
	"github.com/go-chi/chi/v5"
)

// Routes returns a router serving Prometheus metrics at /metrics.
func Routes() chi.Router {
	mux := chi.NewRouter()
	mux.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		victoriaMetrics.WritePrometheus(w, true)
	})

	return mux
}
