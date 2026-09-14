package signaling

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsTestServer exposes the connection layer over a real listener. Task 5 adds
// the production route; this helper accepts on the same two calls so the tests
// here stay at the socket seam without depending on routing.
func wsTestServer(t *testing.T, hub *Hub) string {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		hub.handleConn(r.Context(), ws)
	}))
	t.Cleanup(hs.Close)
	return "ws" + strings.TrimPrefix(hs.URL, "http")
}

type testClient struct {
	t  *testing.T
	ws *websocket.Conn
}

func dial(t *testing.T, url string) *testClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return &testClient{t: t, ws: ws}
}

func (c *testClient) send(raw string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(c.t, c.ws.Write(ctx, websocket.MessageText, []byte(raw)))
}

func (c *testClient) join(role, session string) {
	c.t.Helper()
	c.send(`{"type":"join","role":"` + role + `","session":"` + session + `"}`)
}

// readRaw returns the next frame, or fails the test if none arrives in time.
func (c *testClient) readRaw() []byte {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 5*time.Second)
	defer cancel()
	_, data, err := c.ws.Read(ctx)
	require.NoError(c.t, err)
	return data
}

func (c *testClient) read() map[string]any {
	c.t.Helper()
	var m map[string]any
	require.NoError(c.t, json.Unmarshal(c.readRaw(), &m))
	return m
}

// expect reads until a frame of the wanted type arrives.
func (c *testClient) expect(want string) map[string]any {
	c.t.Helper()
	for range 10 {
		m := c.read()
		if m["type"] == want {
			return m
		}
	}
	c.t.Fatalf("no %q frame arrived", want)
	return nil
}

// readErr expects the connection to end, and returns the error.
func (c *testClient) readErr() error {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 5*time.Second)
	defer cancel()
	for range 10 {
		if _, _, err := c.ws.Read(ctx); err != nil {
			return err
		}
	}
	c.t.Fatal("connection stayed open")
	return nil
}

func TestFirstFrameMustBeJoin(t *testing.T) {
	hub := newTestHub(t, nil)
	url := wsTestServer(t, hub)

	tests := []struct {
		name  string
		frame string
	}{
		{name: "offer before join", frame: `{"type":"offer","sdp":"v=0"}`},
		{name: "empty session", frame: `{"type":"join","role":"device","session":""}`},
		{name: "unknown role", frame: `{"type":"join","role":"operator","session":"s1"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := dial(t, url)
			client.send(tt.frame)
			assert.Error(t, client.readErr())
		})
	}
}

func TestJoinIsAcknowledgedOverTheSocket(t *testing.T) {
	hub := newTestHub(t, nil)
	client := dial(t, wsTestServer(t, hub))

	client.join(roleDevice, "s1")

	joined := client.expect(typeJoined)
	assert.Equal(t, modeForward, joined["mode"])
}

func TestMalformedFrameDoesNotCloseTheSocket(t *testing.T) {
	hub := newTestHub(t, nil)
	url := wsTestServer(t, hub)
	device := dial(t, url)
	device.join(roleDevice, "s1")
	device.expect(typeJoined)

	device.send(`this is not json at all`)
	device.send(`{"type":"candidate"`)

	// A counterpart joining proves the device socket is still serving: only a
	// live connection receives ready.
	phone := dial(t, url)
	phone.join(rolePhone, "s1")

	assert.Equal(t, false, device.expect(typeReady)["initiator"])
}

func TestUnknownFrameTypeIsIgnored(t *testing.T) {
	hub := newTestHub(t, nil)
	url := wsTestServer(t, hub)
	device := dial(t, url)
	device.join(roleDevice, "s1")
	device.expect(typeJoined)

	device.send(`{"type":"somethingElse","payload":1}`)

	phone := dial(t, url)
	phone.join(rolePhone, "s1")

	assert.Equal(t, false, device.expect(typeReady)["initiator"])
}

func TestByeClosesTheSocket(t *testing.T) {
	hub := newTestHub(t, nil)
	client := dial(t, wsTestServer(t, hub))
	client.join(roleDevice, "s1")
	client.expect(typeJoined)

	client.send(`{"type":"bye"}`)

	assert.Error(t, client.readErr())
}

func TestQueuedByeIsDeliveredBeforeTheSocketCloses(t *testing.T) {
	hub := newTestHub(t, &Config{NoReceiverTimeout: 30 * time.Millisecond})
	client := dial(t, wsTestServer(t, hub))
	client.join(roleDevice, "s1")
	client.expect(typeJoined)

	assert.Equal(t, typeBye, client.expect(typeBye)["type"])
	assert.Error(t, client.readErr())
}

func TestServerCloseEndsLiveConnections(t *testing.T) {
	hub := newTestHub(t, nil)
	client := dial(t, wsTestServer(t, hub))
	client.join(roleDevice, "s1")
	client.expect(typeJoined)

	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.Close()
	}()

	assert.Error(t, client.readErr())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return")
	}
}
