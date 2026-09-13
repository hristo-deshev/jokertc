package turn

import (
	"context"
	"net"
	"testing"

	"github.com/pion/logging"
	pion "github.com/pion/turn/v5"
)

func testConfig(t *testing.T, udpAddr, tcpAddr string) *Config {
	t.Helper()
	return &Config{
		ListenUDPAddr: udpAddr,
		ListenTCPAddr: tcpAddr,
		ExternalIP:    "127.0.0.1",
		RelayPortMin:  60000,
		RelayPortMax:  60100,
		Auth:          AllowAllAuth{},
	}
}

func startServer(t *testing.T, cfg *Config) *Server {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	go func() { _ = s.Run(context.Background()) }()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustAddr(t *testing.T, a net.Addr) string {
	t.Helper()
	if a == nil {
		t.Fatal("nil listener address")
	}
	return a.String()
}

func udpClient(t *testing.T, serverAddr string) *pion.Client {
	t.Helper()
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	client, err := pion.NewClient(&pion.ClientConfig{
		STUNServerAddr: serverAddr,
		TURNServerAddr: serverAddr,
		Username:       "test",
		Password:       "test", // AllowAllAuth: credential == username
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

func TestNewValidation(t *testing.T) {
	if _, err := New(testConfig(t, "", "")); err == nil {
		t.Fatal("expected error when no listener configured")
	}
	cfg := testConfig(t, "127.0.0.1:0", "")
	cfg.RelayPortMax = 50000 // < RelayPortMin
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error for invalid port range")
	}
	cfg = testConfig(t, "127.0.0.1:0", "")
	cfg.Auth = nil
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error for missing authenticator")
	}
}

func TestSTUNBinding(t *testing.T) {
	s := startServer(t, testConfig(t, "127.0.0.1:0", ""))
	client := udpClient(t, mustAddr(t, s.UDPAddr()))

	mapped, err := client.SendBindingRequest()
	if err != nil {
		t.Fatalf("SendBindingRequest(): %v", err)
	}
	if udp, ok := mapped.(*net.UDPAddr); !ok || !udp.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("unexpected mapped address %v", mapped)
	}
}

func TestAllocateUDPInPortRange(t *testing.T) {
	s := startServer(t, testConfig(t, "127.0.0.1:0", ""))
	client := udpClient(t, mustAddr(t, s.UDPAddr()))

	relayConn, err := client.Allocate()
	if err != nil {
		t.Fatalf("Allocate(): %v", err)
	}
	t.Cleanup(func() { _ = relayConn.Close() })

	port := relayConn.LocalAddr().(*net.UDPAddr).Port
	if port < 60000 || port > 60100 {
		t.Fatalf("relayed port %d outside 60000-60100", port)
	}
}

func TestAllocateTCPInPortRange(t *testing.T) {
	s := startServer(t, testConfig(t, "", "127.0.0.1:0"))

	var d net.Dialer
	raw, err := d.DialContext(t.Context(), "tcp", mustAddr(t, s.TCPAddr()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	client, err := pion.NewClient(&pion.ClientConfig{
		TURNServerAddr: mustAddr(t, s.TCPAddr()),
		Username:       "test",
		Password:       "test", // AllowAllAuth: credential == username
		Conn:           pion.NewSTUNConn(raw),
		LoggerFactory:  logging.NewDefaultLoggerFactory(),
	})
	if err != nil {
		t.Fatalf("NewClient(): %v", err)
	}
	if err := client.Listen(); err != nil {
		t.Fatalf("client.Listen(): %v", err)
	}
	t.Cleanup(client.Close)

	relayConn, err := client.Allocate()
	if err != nil {
		t.Fatalf("Allocate(): %v", err)
	}
	port := relayConn.LocalAddr().(*net.UDPAddr).Port
	if port < 60000 || port > 60100 {
		t.Fatalf("relayed port %d outside 60000-60100", port)
	}
}
