package healthcheck

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
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

func Test_Healthcheck_Drain_Undrain(t *testing.T) {
	const latency = 200 * time.Millisecond

	h := New(&Opts{
		DrainDuration: latency,
		Log:           getTestLogger(),
	})

	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	{ // Check health
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil) //nolint:goconst,nolintlint
		require.NoError(t, err)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "Healthcheck must return `Ok` before draining")
	}

	{ // Drain
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/drain", nil)
		require.NoError(t, err)
		rr := httptest.NewRecorder()
		start := time.Now()
		mux.ServeHTTP(rr, req)
		duration := time.Since(start)
		require.Equal(t, http.StatusOK, rr.Code, "Must return `Ok` for calls to `/drain`")
		require.GreaterOrEqual(t, duration, latency, "Must wait long enough during draining")
	}

	{ // Check health
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil) //nolint:goconst,nolintlint
		require.NoError(t, err)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		require.Equal(t, http.StatusServiceUnavailable, rr.Code, "Healthcheck must return `Service Unavailable` after draining")
	}

	{ // Undrain
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/undrain", nil)
		require.NoError(t, err)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "Must return `Ok` for calls to `/undrain`")
		time.Sleep(latency)
	}

	{ // Check health
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil) //nolint:goconst,nolintlint
		require.NoError(t, err)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "Healthcheck must return `Ok` after undraining")
	}
}
