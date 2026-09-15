package turn

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/httplog/v2"
)

// safeBuffer collects log output. pion raises events from the goroutine serving
// the request, so the test goroutine cannot read the buffer without a lock.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func capturingLogger(w *safeBuffer) *httplog.Logger {
	return httplog.NewLogger("turn-test", httplog.Options{
		JSON:     true,
		LogLevel: slog.LevelDebug,
		Writer:   w,
	})
}

// TestRelaySessionIsLogged drives a real allocation and permission through the
// server and checks the operator can see, from the log alone, that a relay
// session started and which peer it forwards to.
func TestRelaySessionIsLogged(t *testing.T) {
	out := &safeBuffer{}
	cfg := testConfig(t, "127.0.0.1:0", "")
	cfg.Log = capturingLogger(out)
	s := startServer(t, cfg)

	client := udpClient(t, mustAddr(t, s.UDPAddr()))
	relayConn, err := client.Allocate()
	if err != nil {
		t.Fatalf("Allocate(): %v", err)
	}
	defer func() { _ = relayConn.Close() }()

	// A permission is what opens the path to one peer, and it is created by
	// sending to that peer for the first time.
	var lc net.ListenConfig
	peer, err := lc.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("peer listen: %v", err)
	}
	defer func() { _ = peer.Close() }()
	if _, err := relayConn.WriteTo([]byte("hello"), peer.LocalAddr()); err != nil {
		t.Fatalf("write to peer: %v", err)
	}

	relayAddr := relayConn.LocalAddr()
	if relayAddr == nil {
		t.Fatal("allocation returned no relayed address")
	}

	logged := out.String()
	for _, want := range []string{
		"TURN allocation created",
		"TURN forwarding started",
		relayAddr.String(),
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log does not mention %q\n--- log ---\n%s", want, logged)
		}
	}
}

// A nil Config.Log is allowed, and installing no callbacks must not stop the
// server from serving.
func TestNilLogInstallsNoEventHandler(t *testing.T) {
	cfg := testConfig(t, "127.0.0.1:0", "")
	cfg.Log = nil
	s := startServer(t, cfg)

	client := udpClient(t, mustAddr(t, s.UDPAddr()))
	relayConn, err := client.Allocate()
	if err != nil {
		t.Fatalf("Allocate() with a nil logger: %v", err)
	}
	_ = relayConn.Close()
}

func TestAddrStringHandlesNil(t *testing.T) {
	if got := addrString(nil); got != "" {
		t.Fatalf("addrString(nil) = %q, want empty", got)
	}
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:3478")
	if err != nil {
		t.Fatal(err)
	}
	if got := addrString(addr); got != "127.0.0.1:3478" {
		t.Fatalf("addrString() = %q", got)
	}
}
