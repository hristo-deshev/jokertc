package turn

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/pion/logging"
	pion "github.com/pion/turn/v5"
)

// mismatchedClient builds a TURN client whose password does not match its
// username, which AllowAllAuth rejects: it derives the key from the username and
// checks the client signed with that.
func mismatchedClient(t *testing.T, serverAddr string) *pion.Client {
	t.Helper()
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	client, err := pion.NewClient(&pion.ClientConfig{
		TURNServerAddr: serverAddr,
		Username:       "alice",
		Password:       "not-alice", // deliberately != username
		Conn:           conn,
		LoggerFactory:  logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		t.Fatalf("NewClient(): %v", err)
	}
	if err := client.Listen(); err != nil {
		t.Fatalf("client.Listen(): %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

// The negative half: without the flag, a mismatched credential is rejected.
// This is what says the positive half is the flag's doing and not a server that
// accepts everyone anyway.
func TestMismatchedCredentialIsRejectedByDefault(t *testing.T) {
	cfg := testConfig(t, "127.0.0.1:0", "")
	s := startServer(t, cfg)

	client := mismatchedClient(t, mustAddr(t, s.UDPAddr()))
	if _, err := client.Allocate(); err == nil {
		t.Fatal("a mismatched username/password was allowed to allocate with the flag off")
	}
}

func TestAllowAnyCredentialAcceptsMismatchedPassword(t *testing.T) {
	out := &safeBuffer{}
	cfg := testConfig(t, "127.0.0.1:0", "")
	cfg.AllowAnyCredential = true
	cfg.Log = capturingLogger(out)
	s := startServer(t, cfg)

	client := mismatchedClient(t, mustAddr(t, s.UDPAddr()))
	relay, err := client.Allocate()
	if err != nil {
		t.Fatalf("Allocate() with AllowAnyCredential: %v", err)
	}
	defer func() { _ = relay.Close() }()

	if relay.LocalAddr() == nil {
		t.Fatal("allocation returned no relayed address")
	}
	if !strings.Contains(out.String(), "open relay") {
		t.Fatal("the open-relay warning was not logged")
	}
}

// forgeIntegrity must ignore a message that carries no credential, so the
// unauthenticated first Allocate still gets its 401 challenge.
func TestForgeIntegrityLeavesUnauthenticatedRequestsAlone(t *testing.T) {
	c := newAnyCredentialConn(nil, realm, nil)

	// A bare Allocate request: header only, no USERNAME, no MESSAGE-INTEGRITY.
	msg := make([]byte, stunHeaderLen)
	msg[0], msg[1] = 0x00, 0x03 // Allocate request
	putMagicCookie(msg)

	if c.forgeIntegrity(msg) {
		t.Fatal("forgeIntegrity changed a request that carried no credential")
	}
}

// forgeIntegrity must produce an HMAC that pion's own Check accepts. The check
// is run here against the key AllowAllAuth derives, closing the loop end to end.
func TestForgeIntegrityProducesAKeyAllowAllAuthAccepts(t *testing.T) {
	c := newAnyCredentialConn(nil, realm, nil)
	msg := authedAllocateRequest(t, "carol", "whatever-the-client-signed-with")

	if !c.forgeIntegrity(msg) {
		t.Fatal("forgeIntegrity did not rewrite an authenticated request")
	}

	// AllowAllAuth derives the key from the username. The rewritten HMAC must
	// verify against exactly that key.
	key, ok := AllowAllAuth{}.Authenticate("carol", realm, nil)
	if !ok {
		t.Fatal("AllowAllAuth rejected a non-empty username")
	}
	if got := recomputeIntegrity(t, msg, key); !got {
		t.Fatal("the rewritten MESSAGE-INTEGRITY does not verify against the AllowAllAuth key")
	}
}
