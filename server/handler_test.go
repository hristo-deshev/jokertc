package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/httplog/v2"
	"github.com/stretchr/testify/require"
	"jokertc/common"
)

func getTestLogger() *httplog.Logger {
	return common.SetupLogger(&common.LoggingOpts{
		Debug:   true,
		JSON:    false,
		Service: "test",
		Version: "test",
	})
}

func Test_Handlers_Simple(t *testing.T) {
	// This test doesn't need the server to actually start and serve. Instead it just tests the handlers.
	//nolint: exhaustruct
	srv, err := New(&HTTPServerConfig{
		Log: getTestLogger(),
	})
	require.NoError(t, err)

	{ // Check health (mounted via the healthcheck module)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil) //nolint:goconst,nolintlint
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		srv.getRouter().ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}

	{ // Check liveness (mounted via the healthcheck module)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		srv.getRouter().ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}

	{ // Check API
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/api", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		srv.getRouter().ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
	}
}
