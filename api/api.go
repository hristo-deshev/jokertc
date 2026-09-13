// Package api implements the /api application endpoint.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// RegisterRoutes registers the API endpoints onto the given router.
func RegisterRoutes(r chi.Router) {
	r.Get("/api", handleAPI)
}

func handleAPI(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
