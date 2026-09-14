package signaling

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"jokertc/metrics"
)

// realRouter mirrors the middleware chain server.getRouter builds. The
// deadline and hijack behaviour under test depends on every wrapper in it, so
// a bare handler would not prove anything.
func realRouter(t *testing.T, hub *Hub) http.Handler {
	t.Helper()
	mux := chi.NewRouter()
	mux.Use(httplog.RequestLogger(testLogger(t)))
	mux.Use(middleware.Recoverer)
	mux.Use(metrics.Middleware)
	hub.RegisterRoutes(mux)
	return mux
}

// listenWithTimeouts serves the real router behind an http.Server carrying
// production-shaped timeouts.
func listenWithTimeouts(t *testing.T, hub *Hub, write, read time.Duration) string {
	t.Helper()
	hs := httptest.NewUnstartedServer(realRouter(t, hub))
	hs.Config.WriteTimeout = write
	hs.Config.ReadTimeout = read
	hs.Start()
	t.Cleanup(hs.Close)
	host, port, err := net.SplitHostPort(hs.Listener.Addr().String())
	require.NoError(t, err)
	return "ws://" + net.JoinHostPort(host, port)
}

func TestPlainHTTPRequestToWSIsRejected(t *testing.T) {
	hub := newTestHub(t, nil)
	hs := httptest.NewServer(realRouter(t, hub))
	t.Cleanup(hs.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/ws", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestRouteWorksThroughTheRealMiddlewareChain(t *testing.T) {
	hub := newTestHub(t, nil)
	client := dial(t, listenWithTimeouts(t, hub, 30*time.Second, 60*time.Second)+"/ws")

	client.join(roleDevice, "s1")

	assert.Equal(t, modeForward, client.expect(typeJoined)["mode"])
}

// TestIdleSocketSurvivesWriteTimeout is the regression test for the net/http
// deadline trap: ReadTimeout and WriteTimeout are applied to the raw
// connection before the handler runs, and hijacking for a WebSocket does not
// clear them. Signaling goes quiet once media flows, so without the fix a call
// dies mid-session.
func TestIdleSocketSurvivesWriteTimeout(t *testing.T) {
	hub := newTestHub(t, nil)
	url := listenWithTimeouts(t, hub, 300*time.Millisecond, 300*time.Millisecond) + "/ws"

	device := dial(t, url)
	device.join(roleDevice, "s1")
	device.expect(typeJoined)

	phone := dial(t, url)
	phone.join(rolePhone, "s1")
	phone.expect(typeReady)
	device.expect(typeReady)

	time.Sleep(900 * time.Millisecond)

	sent := `{"type":"candidate","candidate":"candidate:1 1 udp 1 10.0.0.1 1 typ host"}`
	phone.send(sent)

	assert.Equal(t, sent, string(device.expectRaw(typeCandidate)))
}
