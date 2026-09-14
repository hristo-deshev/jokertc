package signaling

import (
	"time"

	"github.com/go-chi/httplog/v2"
)

const (
	defaultSendBuffer  = 16
	defaultPingTimeout = 10 * time.Second
)

// Config holds the settings for the signaling endpoint. The zero value is not
// usable; build one through server.HTTPServerConfig or in tests.
type Config struct {
	// StunURL is advertised to both peers in the ready message. Empty sends an
	// empty string, which clients treat as "no STUN server".
	StunURL string

	// TurnURL is advertised to both peers in the ready message. Empty sends a
	// null turn field.
	TurnURL string

	// TurnSecret and TurnCredentialTTL are reserved for coturn-style REST
	// credentials (HMAC-SHA1 over "<expiry>:<user>"). They are NOT implemented:
	// issuing such a credential needs a matching turn.Authenticator in the turn
	// package, and without it the embedded TURN server rejects every credential
	// this package would hand out. New returns an error if TurnSecret is set.
	TurnSecret        string
	TurnCredentialTTL time.Duration

	// NoReceiverTimeout is how long a device may wait alone before it receives
	// bye and its socket closes. Zero disables the timer.
	NoReceiverTimeout time.Duration

	// PingInterval is the keepalive period on an idle socket. Zero disables it.
	PingInterval time.Duration

	// PingTimeout bounds the wait for a pong. A ping is a round trip, so an
	// unbounded one stalls the peer's outbound queue. Defaults to 10s.
	PingTimeout time.Duration

	// SendBuffer is the outbound queue depth per peer. A peer that fills it is
	// not keeping up and gets closed. Defaults to 16.
	SendBuffer int

	Log *httplog.Logger
}
