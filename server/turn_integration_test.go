package server

import (
	"net"
	"testing"

	"github.com/pion/logging"
	pion "github.com/pion/turn/v5"
	"jokertc/turn"
)

// TestServerComposesTURN proves the full composition path: the same code
// path production uses starts TURN alongside HTTP without errors, and the
// embedded TURN server answers a real allocation.
func TestServerComposesTURN(t *testing.T) {
	srv, err := New(&HTTPServerConfig{
		ListenAddr: "127.0.0.1:0",
		Log:        testLogger(t),
		TURN: &turn.Config{
			ListenUDPAddr: "127.0.0.1:0",
			ListenTCPAddr: "127.0.0.1:0",
			ExternalIP:    "127.0.0.1",
			RelayPortMin:  60000,
			RelayPortMax:  60100,
			Auth:          turn.AllowAllAuth{},
			Log:           testLogger(t),
		},
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	srv.RunInBackground()
	defer srv.Shutdown()

	var lc net.ListenConfig
	conn, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	turnAddr := turnUDPAddr(t, srv.turnSrv)
	client, err := pion.NewClient(&pion.ClientConfig{
		STUNServerAddr: turnAddr,
		TURNServerAddr: turnAddr,
		Username:       "test",
		Password:       "test",
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

	if _, err := client.Allocate(); err != nil {
		t.Fatalf("Allocate(): %v", err)
	}
}

// turnUDPAddr nil-checks UDPAddr so nilaway can narrow the deref.
func turnUDPAddr(t *testing.T, s *turn.Server) string {
	t.Helper()
	a := s.UDPAddr()
	if a == nil {
		t.Fatal("TURN UDP listener missing")
	}
	return a.String()
}
