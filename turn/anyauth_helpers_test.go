package turn

import (
	"encoding/binary"
	"testing"

	"github.com/pion/stun/v4"
)

func putMagicCookie(msg []byte) {
	binary.BigEndian.PutUint32(msg[4:8], stunMagicCookie)
}

// authedAllocateRequest builds a real Allocate request signed with password,
// using pion's own stun package rather than this package's code, so the tests
// check forgeIntegrity against an independently produced message.
func authedAllocateRequest(t *testing.T, username, password string) []byte {
	t.Helper()
	m := new(stun.Message)
	err := m.Build(
		stun.TransactionID,
		stun.NewType(stun.MethodAllocate, stun.ClassRequest),
		stun.NewUsername(username),
		stun.NewRealm(realm),
		stun.NewNonce("test-nonce"),
		stun.NewLongTermIntegrity(username, realm, password),
	)
	if err != nil {
		t.Fatalf("build allocate request: %v", err)
	}
	out := make([]byte, len(m.Raw))
	copy(out, m.Raw)
	return out
}

// recomputeIntegrity reports whether the MESSAGE-INTEGRITY in msg verifies
// against key, using pion's Check — the exact call the server makes.
func recomputeIntegrity(t *testing.T, msg, key []byte) bool {
	t.Helper()
	m := new(stun.Message)
	m.Raw = append(m.Raw[:0], msg...)
	if err := m.Decode(); err != nil {
		t.Fatalf("decode rewritten message: %v", err)
	}
	return stun.MessageIntegrity(key).Check(m) == nil
}
