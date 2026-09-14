package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"jokertc/signaling"
)

// productionTimeouts mirrors what cmd/server sets, so the integration tests
// run against the real server shape rather than a zeroed one.
const (
	productionReadTimeout  = 60 * time.Second
	productionWriteTimeout = 30 * time.Second
)

func startSignalingServer(t *testing.T, cfg *signaling.Config) (*Server, string) {
	t.Helper()
	if cfg == nil {
		cfg = &signaling.Config{}
	}
	srv, err := New(&HTTPServerConfig{
		ListenAddr:               "127.0.0.1:0",
		Log:                      testLogger(t),
		Signaling:                cfg,
		ReadTimeout:              productionReadTimeout,
		WriteTimeout:             productionWriteTimeout,
		GracefulShutdownDuration: 5 * time.Second,
	})
	require.NoError(t, err)

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", srv.cfg.ListenAddr)
	require.NoError(t, err)
	go func() { _ = srv.srv.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	return srv, "ws://" + ln.Addr().String() + "/ws"
}

type wsClient struct {
	t  *testing.T
	ws *websocket.Conn
}

func dialWS(t *testing.T, url string) *wsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return &wsClient{t: t, ws: ws}
}

func (c *wsClient) send(raw string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(c.t, c.ws.Write(ctx, websocket.MessageText, []byte(raw)))
}

func (c *wsClient) join(role, session string) {
	c.t.Helper()
	c.send(`{"type":"join","role":"` + role + `","session":"` + session + `","imei":"351234567890123"}`)
}

// readQuiet returns the next frame or the read error, for callers that drive a
// background pump and must exit cleanly when the socket ends.
func (c *wsClient) readQuiet() ([]byte, error) {
	ctx, cancel := context.WithTimeout(c.t.Context(), 60*time.Second)
	defer cancel()
	_, data, err := c.ws.Read(ctx)
	return data, err
}

func (c *wsClient) readRaw() []byte {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 10*time.Second)
	defer cancel()
	_, data, err := c.ws.Read(ctx)
	require.NoError(c.t, err)
	return data
}

// expect reads until a frame of the wanted type arrives, returning it raw and
// decoded so callers can assert on either.
func (c *wsClient) expect(want string) ([]byte, map[string]any) {
	c.t.Helper()
	for range 10 {
		raw := c.readRaw()
		var m map[string]any
		require.NoError(c.t, json.Unmarshal(raw, &m))
		if m["type"] == want {
			return raw, m
		}
	}
	c.t.Fatalf("no %q frame arrived", want)
	return nil, nil
}

// expectClosed fails unless the server actually closes the connection. A read
// that merely times out is a failure, not a pass: treating the deadline error
// as "closed" would make this assertion accept a socket that outlived
// shutdown, which is the exact defect it exists to catch.
func (c *wsClient) expectClosed() {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 5*time.Second)
	defer cancel()
	for range 10 {
		_, _, err := c.ws.Read(ctx)
		if err == nil {
			continue
		}
		if errors.Is(err, context.DeadlineExceeded) {
			c.t.Fatal("connection stayed open: the read timed out instead of the server closing it")
		}
		return
	}
	c.t.Fatal("connection stayed open")
}

// pair joins a device and a phone to one session and consumes both ready
// frames, leaving each client at the start of the call.
func pair(t *testing.T, url, session string) (device, phone *wsClient) {
	t.Helper()
	device = dialWS(t, url)
	device.join("device", session)
	_, joined := device.expect("joined")
	assert.Equal(t, "forward", joined["mode"])

	phone = dialWS(t, url)
	phone.join("phone", session)
	phone.expect("joined")

	_, deviceReady := device.expect("ready")
	_, phoneReady := phone.expect("ready")
	assert.Equal(t, false, deviceReady["initiator"], "the device must answer, not offer")
	assert.Equal(t, true, phoneReady["initiator"], "the phone must be the initiator")
	return device, phone
}

func TestSignalingPairsDeviceAndPhone(t *testing.T) {
	_, url := startSignalingServer(t, &signaling.Config{StunURL: "stun:stun.example:3478"})

	device, phone := pair(t, url, "call-1")

	// ready is sent once per pairing: a further frame must not produce another.
	phone.send(`{"type":"offer","sdp":"v=0"}`)
	raw := device.readRaw()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, "offer", m["type"])
}

func TestSignalingAdvertisesTheConfiguredStunURL(t *testing.T) {
	_, url := startSignalingServer(t, &signaling.Config{StunURL: "stun:stun.example:3478"})

	device := dialWS(t, url)
	device.join("device", "call-stun-2")
	device.expect("joined")
	other := dialWS(t, url)
	other.join("phone", "call-stun-2")
	_, ready := other.expect("ready")

	assert.Equal(t, "stun:stun.example:3478", ready["stun"])
}

// TestSignalingForwardsFramesByteForByte covers contract rule 11: clients send
// fields this server does not model, and they must survive the relay.
func TestSignalingForwardsFramesByteForByte(t *testing.T) {
	_, url := startSignalingServer(t, nil)
	device, phone := pair(t, url, "call-2")

	offer := `{"type":"offer","sdp":"v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\n","unmodelled":{"deep":[1,2,3]},"order":"preserved"}`
	phone.send(offer)
	assert.Equal(t, offer, string(device.readRaw()))

	answer := `{"type":"answer","sdp":"v=0\r\n","vendorExtension":true}`
	device.send(answer)
	assert.Equal(t, answer, string(phone.readRaw()))

	candidate := `{"type":"candidate","candidate":"candidate:1 1 udp 2130706431 10.0.0.1 54321 typ host","sdpMid":"0","sdpMLineIndex":0,"usernameFragment":"abc"}`
	device.send(candidate)
	assert.Equal(t, candidate, string(phone.readRaw()))
}

func TestSignalingNotifiesTheSurvivorWhenAPeerLeaves(t *testing.T) {
	_, url := startSignalingServer(t, nil)
	device, phone := pair(t, url, "call-3")

	require.NoError(t, device.ws.Close(websocket.StatusNormalClosure, "done"))

	_, bye := phone.expect("bye")
	assert.Equal(t, "bye", bye["type"])
}

// TestSignalingRejoinReplacesWithoutDisturbingTheSurvivor covers contract
// rule 6: the replaced client is closed, the surviving phone sees no bye, and
// the new pairing re-arms ready.
func TestSignalingRejoinReplacesWithoutDisturbingTheSurvivor(t *testing.T) {
	_, url := startSignalingServer(t, nil)
	firstDevice, phone := pair(t, url, "call-4")

	secondDevice := dialWS(t, url)
	secondDevice.join("device", "call-4")
	secondDevice.expect("joined")

	firstDevice.expectClosed()

	_, phoneReady := phone.expect("ready")
	assert.Equal(t, true, phoneReady["initiator"])

	_, deviceReady := secondDevice.expect("ready")
	assert.Equal(t, false, deviceReady["initiator"])
}

func TestSignalingLonelyDeviceTimesOut(t *testing.T) {
	_, url := startSignalingServer(t, &signaling.Config{NoReceiverTimeout: 150 * time.Millisecond})

	device := dialWS(t, url)
	device.join("device", "call-5")
	device.expect("joined")

	_, bye := device.expect("bye")
	assert.Equal(t, "bye", bye["type"])
	device.expectClosed()
}

func TestSignalingDeviceWithAPhoneDoesNotTimeOut(t *testing.T) {
	_, url := startSignalingServer(t, &signaling.Config{NoReceiverTimeout: 150 * time.Millisecond})
	device, phone := pair(t, url, "call-6")

	time.Sleep(400 * time.Millisecond)

	candidate := `{"type":"candidate","candidate":"still here"}`
	phone.send(candidate)
	assert.Equal(t, candidate, string(device.readRaw()))
}

// TestSignalingShutdownClosesLiveSockets is the test that fails if Shutdown
// does not close the signaling server before http.Server.Shutdown, which
// neither waits for nor closes hijacked connections.
func TestSignalingShutdownClosesLiveSockets(t *testing.T) {
	srv, url := startSignalingServer(t, nil)
	device, phone := pair(t, url, "call-7")

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Shutdown()
	}()

	device.expectClosed()
	phone.expectClosed()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown() did not return: a hijacked connection is still held")
	}
}

func getPage(t *testing.T, base, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// The two test pages are deliberately separate documents: one drives /ws, the
// other is copy/paste only. Sharing a page meant sharing one RTCPeerConnection
// between the two modes, where each silently tore down the other's call.
func TestUIPagesAreServedSeparately(t *testing.T) {
	_, wsURL := startSignalingServer(t, nil)
	base := "http:" + strings.TrimPrefix(strings.TrimSuffix(wsURL, "/ws"), "ws:")

	status, manual := getPage(t, base, "/ui/manual")
	assert.Equal(t, http.StatusOK, status)
	assert.NotContains(t, manual, "new WebSocket", "the manual page must not drive /ws")
	assert.Contains(t, manual, "Create offer")

	status, auto := getPage(t, base, "/ui/websocket")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, auto, "new WebSocket")
	assert.NotContains(t, auto, "Accept pasted description", "the websocket page must not carry the copy/paste flow")
}

// /ui/websocket only drives /ws, so serving it without /ws would hand the user
// a page that cannot work.
func TestWebsocketUIIsAbsentWhenSignalingIsDisabled(t *testing.T) {
	srv, err := New(&HTTPServerConfig{ListenAddr: "127.0.0.1:0", Log: testLogger(t)})
	require.NoError(t, err)

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", srv.cfg.ListenAddr)
	require.NoError(t, err)
	go func() { _ = srv.srv.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	base := "http://" + ln.Addr().String()
	status, _ := getPage(t, base, "/ui/websocket")
	assert.Equal(t, http.StatusNotFound, status)

	status, _ = getPage(t, base, "/ui/manual")
	assert.Equal(t, http.StatusOK, status, "the manual page does not depend on signaling")
}
