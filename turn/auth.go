package turn

import (
	"net"

	pion "github.com/pion/turn/v5"
)

// Authenticator authorizes TURN allocation requests. It is adapted onto
// pion/turn's AuthHandler inside New (pion v5 passes a RequestAttributes
// struct; we keep the flat signature).
type Authenticator interface {
	Authenticate(username, realm string, srcAddr net.Addr) (key []byte, ok bool)
}

// AllowAllAuth accepts every allocation request with a non-empty username.
// It derives the long-term auth key from the username itself, so clients must
// send credential == username (any non-empty username works).
//
// Add real auth here: replace with a static long-term credential check
// (turn.GenerateAuthKey against --turn-user/--turn-pass flags) or HMAC
// ephemeral secrets (pion's NewLongTermAuthHandler /
// LongTermTURNRESTAuthHandler).
type AllowAllAuth struct{}

func (AllowAllAuth) Authenticate(username, realm string, _ net.Addr) (key []byte, ok bool) {
	if username == "" {
		return nil, false
	}
	return pion.GenerateAuthKey(username, realm, username), true
}
