package signaling

import (
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/httplog/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger(t *testing.T) *httplog.Logger {
	t.Helper()
	return httplog.NewLogger("test", httplog.Options{LogLevel: slog.LevelError})
}

func newTestHub(t *testing.T, cfg *Config) *Hub {
	t.Helper()
	if cfg == nil {
		cfg = &Config{}
	}
	if cfg.Log == nil {
		cfg.Log = testLogger(t)
	}
	hub, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(hub.Close)
	return hub
}

// closeRecord is written from the lonely timer's goroutine and read from the
// test goroutine, so it carries its own lock.
type closeRecord struct {
	mu     sync.Mutex
	called bool
	reason string
}

func (r *closeRecord) record(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = true
	r.reason = reason
}

func (r *closeRecord) wasCalled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

// joinTest registers a peer and returns it plus the record of close calls made
// against it. It is the state machine's only seam: a buffered channel and a
// close func, no socket.
func joinTest(t *testing.T, hub *Hub, role, session string) (*peer, *closeRecord) {
	t.Helper()
	rec := &closeRecord{}
	p, err := hub.join(joinMessage{Type: typeJoin, Role: role, Session: session}, "203.0.113.9:5555", rec.record)
	require.NoError(t, err)
	return p, rec
}

// drain reads every frame already queued for a peer without blocking.
func drain(p *peer) []map[string]any {
	var out []map[string]any
	for {
		select {
		case raw := <-p.out:
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				m = map[string]any{"__raw": string(raw)}
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

func typesOf(frames []map[string]any) []string {
	types := make([]string, 0, len(frames))
	for _, f := range frames {
		t, _ := f["type"].(string)
		types = append(types, t)
	}
	return types
}

func firstOfType(frames []map[string]any, want string) map[string]any {
	for _, f := range frames {
		if t, _ := f["type"].(string); t == want {
			return f
		}
	}
	return nil
}

func TestJoinRejectsBadInput(t *testing.T) {
	hub := newTestHub(t, nil)

	tests := []struct {
		name string
		msg  joinMessage
	}{
		{name: "empty session", msg: joinMessage{Role: roleDevice, Session: ""}},
		{name: "unknown role", msg: joinMessage{Role: "operator", Session: "s1"}},
		{name: "empty role", msg: joinMessage{Role: "", Session: "s1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := hub.join(tt.msg, "203.0.113.9:5555", func(string) {})
			assert.Error(t, err)
			assert.Nil(t, p)
		})
	}
}

func TestJoinAcknowledgesWithForwardMode(t *testing.T) {
	hub := newTestHub(t, nil)

	device, _ := joinTest(t, hub, roleDevice, "s1")

	frames := drain(device)
	require.Len(t, frames, 1)
	assert.Equal(t, typeJoined, frames[0]["type"])
	assert.Equal(t, modeForward, frames[0]["mode"])
}

func TestSessionIDsAreCaseInsensitive(t *testing.T) {
	hub := newTestHub(t, nil)

	device, _ := joinTest(t, hub, roleDevice, "Session-ONE")
	phone, _ := joinTest(t, hub, rolePhone, "session-one")

	assert.Contains(t, typesOf(drain(device)), typeReady)
	assert.Contains(t, typesOf(drain(phone)), typeReady)
}

func TestPhoneIsTheInitiator(t *testing.T) {
	hub := newTestHub(t, &Config{StunURL: "stun:example:3478"})

	device, _ := joinTest(t, hub, roleDevice, "s1")
	phone, _ := joinTest(t, hub, rolePhone, "s1")

	deviceReady := firstOfType(drain(device), typeReady)
	phoneReady := firstOfType(drain(phone), typeReady)
	require.NotNil(t, deviceReady)
	require.NotNil(t, phoneReady)

	assert.Equal(t, false, deviceReady["initiator"])
	assert.Equal(t, true, phoneReady["initiator"])
	assert.Equal(t, "stun:example:3478", phoneReady["stun"])
}

func TestReadyIsSentOncePerPairing(t *testing.T) {
	hub := newTestHub(t, nil)

	device, _ := joinTest(t, hub, roleDevice, "s1")
	phone, _ := joinTest(t, hub, rolePhone, "s1")
	drain(device)
	drain(phone)

	hub.forward(phone, typeOffer, []byte(`{"type":"offer","sdp":"v=0"}`))

	assert.NotContains(t, typesOf(drain(device)), typeReady)
	assert.Empty(t, drain(phone))
}

func TestSecondJoinOfSameRoleReplacesTheFirst(t *testing.T) {
	hub := newTestHub(t, nil)

	firstDevice, firstClosed := joinTest(t, hub, roleDevice, "s1")
	phone, _ := joinTest(t, hub, rolePhone, "s1")
	drain(firstDevice)
	drain(phone)

	secondDevice, _ := joinTest(t, hub, roleDevice, "s1")

	phoneFrames := typesOf(drain(phone))
	assert.True(t, firstClosed.wasCalled(), "the replaced device should be closed")
	assert.NotContains(t, phoneFrames, typeBye, "the surviving phone must not see bye on a replace")
	assert.Contains(t, phoneFrames, typeReady, "the new pairing re-arms ready")
	assert.Contains(t, typesOf(drain(secondDevice)), typeReady)
}

func TestLeaveNotifiesTheSurvivingPeer(t *testing.T) {
	hub := newTestHub(t, nil)

	device, _ := joinTest(t, hub, roleDevice, "s1")
	phone, _ := joinTest(t, hub, rolePhone, "s1")
	drain(device)
	drain(phone)

	hub.leave(device)

	assert.Contains(t, typesOf(drain(phone)), typeBye)
}

func TestLeaveDropsTheEmptySession(t *testing.T) {
	hub := newTestHub(t, nil)

	device, _ := joinTest(t, hub, roleDevice, "s1")
	hub.leave(device)

	assert.Equal(t, 0, hub.sessionCount())
}

func TestLeaveOnAReplacedPeerIsANoOp(t *testing.T) {
	hub := newTestHub(t, nil)

	firstDevice, _ := joinTest(t, hub, roleDevice, "s1")
	phone, _ := joinTest(t, hub, rolePhone, "s1")
	joinTest(t, hub, roleDevice, "s1")
	drain(phone)

	hub.leave(firstDevice)

	assert.NotContains(t, typesOf(drain(phone)), typeBye)
	assert.Equal(t, 1, hub.sessionCount())
}

func TestForwardDeliversTheFrameByteForByte(t *testing.T) {
	hub := newTestHub(t, nil)

	device, _ := joinTest(t, hub, roleDevice, "s1")
	phone, _ := joinTest(t, hub, rolePhone, "s1")
	drain(device)
	drain(phone)

	sent := []byte(`{"type":"offer","sdp":"v=0\r\n","unmodelledField":{"deep":[1,2]}}`)
	hub.forward(phone, typeOffer, sent)

	select {
	case got := <-device.out:
		assert.Equal(t, string(sent), string(got))
	default:
		t.Fatal("device received nothing")
	}
}

func TestForwardWithNoCounterpartIsDropped(t *testing.T) {
	hub := newTestHub(t, nil)

	phone, _ := joinTest(t, hub, rolePhone, "s1")
	drain(phone)

	hub.forward(phone, typeOffer, []byte(`{"type":"offer","sdp":"v=0"}`))

	assert.Empty(t, drain(phone))
}

func TestLonelyDeviceTimesOut(t *testing.T) {
	hub := newTestHub(t, &Config{NoReceiverTimeout: 20 * time.Millisecond})

	device, closed := joinTest(t, hub, roleDevice, "s1")
	drain(device)

	assert.Eventually(t, func() bool { return closed.wasCalled() }, time.Second, 2*time.Millisecond)
	assert.Contains(t, typesOf(drain(device)), typeBye)
	assert.Equal(t, 0, hub.sessionCount())
}

func TestDeviceWithAPhoneDoesNotTimeOut(t *testing.T) {
	hub := newTestHub(t, &Config{NoReceiverTimeout: 20 * time.Millisecond})

	device, closed := joinTest(t, hub, roleDevice, "s1")
	joinTest(t, hub, rolePhone, "s1")
	drain(device)

	time.Sleep(60 * time.Millisecond)

	assert.False(t, closed.wasCalled())
	assert.NotContains(t, typesOf(drain(device)), typeBye)
}

func TestLonePhoneDoesNotTimeOut(t *testing.T) {
	hub := newTestHub(t, &Config{NoReceiverTimeout: 20 * time.Millisecond})

	phone, closed := joinTest(t, hub, rolePhone, "s1")
	drain(phone)

	time.Sleep(60 * time.Millisecond)

	assert.False(t, closed.wasCalled())
}

func TestSlowPeerIsClosedRatherThanBlocking(t *testing.T) {
	hub := newTestHub(t, &Config{SendBuffer: 1})

	_, deviceClosed := joinTest(t, hub, roleDevice, "s1")
	phone, _ := joinTest(t, hub, rolePhone, "s1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10 {
			hub.forward(phone, typeCandidate, []byte(`{"type":"candidate","candidate":"x"}`))
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("forward blocked on a peer that is not draining its queue")
	}

	assert.True(t, deviceClosed.wasCalled())
}

func TestNewRejectsUnimplementedTurnSecret(t *testing.T) {
	_, err := New(&Config{TurnSecret: "shared", Log: testLogger(t)})
	assert.Error(t, err)
}

func TestJoinBroadcastsRemoteJoinedToOtherPeers(t *testing.T) {
	hub := newTestHub(t, nil)

	watcher, _ := joinTest(t, hub, roleDevice, "watching-session")
	drain(watcher)

	joinTest(t, hub, rolePhone, "ad92f4bf-02e1-4167-863d-da417c5e131a")

	frames := drain(watcher)
	notice := firstOfType(frames, typeRemoteJoined)
	require.NotNil(t, notice, "an existing peer should be told about a join elsewhere; got %v", typesOf(frames))
	assert.Equal(t, "ad92f4bf-02e1-4167-863d-da417c5e131a", notice["session"])
}

func TestJoinerDoesNotReceiveItsOwnRemoteJoined(t *testing.T) {
	hub := newTestHub(t, nil)

	joiner, _ := joinTest(t, hub, roleDevice, "s1")

	assert.NotContains(t, typesOf(drain(joiner)), typeRemoteJoined)
}

func TestRemoteJoinedReachesBothSidesOfAnotherSession(t *testing.T) {
	hub := newTestHub(t, nil)

	device, _ := joinTest(t, hub, roleDevice, "pair")
	phone, _ := joinTest(t, hub, rolePhone, "pair")
	drain(device)
	drain(phone)

	joinTest(t, hub, roleDevice, "newcomer")

	assert.Contains(t, typesOf(drain(device)), typeRemoteJoined)
	assert.Contains(t, typesOf(drain(phone)), typeRemoteJoined)
}

// The counterpart in the same session is told too: it is a peer like any other,
// and the notice carries the session id it already knows, so it is harmless.
func TestRemoteJoinedUsesTheSessionIDAsWritten(t *testing.T) {
	hub := newTestHub(t, nil)

	watcher, _ := joinTest(t, hub, roleDevice, "watcher")
	drain(watcher)

	joinTest(t, hub, rolePhone, "MiXeD-CaSe-Id")

	notice := firstOfType(drain(watcher), typeRemoteJoined)
	require.NotNil(t, notice)
	assert.Equal(t, "MiXeD-CaSe-Id", notice["session"], "the notice should carry the id as the client wrote it")
}
