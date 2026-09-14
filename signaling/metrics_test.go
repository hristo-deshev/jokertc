package signaling

import (
	"strconv"
	"strings"
	"testing"
	"time"

	victoriaMetrics "github.com/VictoriaMetrics/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func exposedMetrics() string {
	var sb strings.Builder
	victoriaMetrics.WritePrometheus(&sb, false)
	return sb.String()
}

func TestSessionAndPeerMetricsAreExposed(t *testing.T) {
	hub := newTestHub(t, nil)

	device, _ := joinTest(t, hub, roleDevice, "metrics-1")
	phone, _ := joinTest(t, hub, rolePhone, "metrics-1")
	hub.forward(phone, typeOffer, []byte(`{"type":"offer","sdp":"v=0"}`))

	exposed := exposedMetrics()
	assert.Contains(t, exposed, `signaling_peers_total{role="device"}`)
	assert.Contains(t, exposed, `signaling_peers_total{role="phone"}`)
	assert.Contains(t, exposed, `signaling_messages_total{type="offer"}`)
	assert.Contains(t, exposed, "signaling_sessions_active")

	hub.leave(device)
	hub.leave(phone)
}

func TestSessionsActiveTracksLiveSessions(t *testing.T) {
	hub := newTestHub(t, nil)

	device, _ := joinTest(t, hub, roleDevice, "metrics-gauge")
	assert.Equal(t, 1, hub.sessionCount())

	hub.leave(device)
	assert.Equal(t, 0, hub.sessionCount())
	assert.Contains(t, exposedMetrics(), "signaling_sessions_active 0")
}

func TestSlowPeerDropIsCounted(t *testing.T) {
	hub := newTestHub(t, &Config{SendBuffer: 1})

	joinTest(t, hub, roleDevice, "metrics-slow")
	phone, _ := joinTest(t, hub, rolePhone, "metrics-slow")
	for range 10 {
		hub.forward(phone, typeCandidate, []byte(`{"type":"candidate","candidate":"x"}`))
	}

	assert.Contains(t, exposedMetrics(), "signaling_slow_peer_drops_total")
}

func TestNoReceiverTimeoutIsCounted(t *testing.T) {
	hub := newTestHub(t, &Config{NoReceiverTimeout: 20 * time.Millisecond})
	before := metricValue(t, "signaling_no_receiver_timeouts_total")

	_, closed := joinTest(t, hub, roleDevice, "metrics-timeout")

	assert.Eventually(t, func() bool { return closed.wasCalled() }, time.Second, 2*time.Millisecond)
	assert.InDelta(t, before+1, metricValue(t, "signaling_no_receiver_timeouts_total"), 0.001)
}

// metricValue reads one unlabelled metric out of the Prometheus exposition.
func metricValue(t *testing.T, name string) float64 {
	t.Helper()
	for line := range strings.SplitSeq(exposedMetrics(), "\n") {
		metric, raw, found := strings.Cut(line, " ")
		if !found || metric != name {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		require.NoError(t, err)
		return v
	}
	return 0
}
