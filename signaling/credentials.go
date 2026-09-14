package signaling

import (
	"crypto/rand"
	"encoding/hex"
)

// newTurnToken returns the per-session TURN credential. The embedded TURN
// server runs turn.AllowAllAuth, which derives its key from the username and
// accepts an allocation when the credential equals that username, so the token
// serves as both halves of the pair.
func newTurnToken() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("signaling: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}

// credentialFor builds the turn field of a ready message. It returns nil when
// no TURN server is configured, which marshals to a null turn field.
func (s *Hub) credentialFor(sess *session) *turnCredential {
	if s.cfg.TurnURL == "" {
		return nil
	}
	return &turnCredential{
		URL:  s.cfg.TurnURL,
		User: sess.turnToken,
		Pass: sess.turnToken,
	}
}
