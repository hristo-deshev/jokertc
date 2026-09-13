package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func Test_API(t *testing.T) {
	mux := chi.NewRouter()
	RegisterRoutes(mux)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/api", nil)
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}
