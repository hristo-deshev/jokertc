package signaling

import (
	"fmt"
	"sync/atomic"

	victoriaMetrics "github.com/VictoriaMetrics/metrics"
)

const (
	peersTotalLabel    = `signaling_peers_total{role="%s"}`
	messagesTotalLabel = `signaling_messages_total{type="%s"}`
)

// sessionsActive is process-wide rather than per Hub: metric registration is
// global, so a gauge closed over one Hub instance would report only that
// instance even though every instance publishes to the same registry.
var sessionsActive atomic.Int64

var (
	slowPeerDrops      = victoriaMetrics.NewCounter("signaling_slow_peer_drops_total")
	noReceiverTimeouts = victoriaMetrics.NewCounter("signaling_no_receiver_timeouts_total")
	_                  = victoriaMetrics.NewGauge("signaling_sessions_active", func() float64 {
		return float64(sessionsActive.Load())
	})
)

func countPeerJoined(role string) {
	victoriaMetrics.GetOrCreateCounter(fmt.Sprintf(peersTotalLabel, role)).Inc()
}

func countMessageForwarded(frameType string) {
	victoriaMetrics.GetOrCreateCounter(fmt.Sprintf(messagesTotalLabel, frameType)).Inc()
}
