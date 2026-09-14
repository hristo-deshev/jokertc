package turn

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// bindingRequest builds the 20-byte STUN Binding request header with no
// attributes, which is all a peer needs to send to learn its mapped address.
func bindingRequest(t *testing.T) []byte {
	t.Helper()
	msg := make([]byte, 20)
	binary.BigEndian.PutUint16(msg[0:2], bindingRequestType)
	binary.BigEndian.PutUint16(msg[2:4], 0) // no attributes
	binary.BigEndian.PutUint32(msg[4:8], stunMagicCookie)
	if _, err := rand.Read(msg[8:20]); err != nil {
		t.Fatalf("transaction id: %v", err)
	}
	return msg
}

// bindingReply sends a Binding request to addr and reports whether an answer
// came back inside the timeout.
func bindingReply(t *testing.T, addr string, timeout time.Duration) bool {
	t.Helper()
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = conn.Close() }()

	server, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve %q: %v", addr, err)
	}
	if _, err := conn.WriteTo(bindingRequest(t), server); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("deadline: %v", err)
	}

	buf := make([]byte, 1500)
	n, _, err := conn.ReadFrom(buf)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return false
	}
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// 0x0101 is class Success Response, method Binding.
	if got := binary.BigEndian.Uint16(buf[0:2]); got != 0x0101 {
		t.Fatalf("answer is not a binding success response: type 0x%04x, %d bytes", got, n)
	}
	return true
}

// The two halves of this test are a pair: the first pins down that the server
// answers binding requests normally, so the second one failing to get an answer
// means the filter did it and not the test.
func TestSTUNBindingIsAnsweredByDefault(t *testing.T) {
	cfg := testConfig(t, "127.0.0.1:0", "")
	s := startServer(t, cfg)

	if !bindingReply(t, mustAddr(t, s.UDPAddr()), 2*time.Second) {
		t.Fatal("no answer to a STUN binding request with DisableSTUN off")
	}
}

func TestDisableSTUNDropsBindingRequests(t *testing.T) {
	cfg := testConfig(t, "127.0.0.1:0", "")
	cfg.DisableSTUN = true
	s := startServer(t, cfg)

	if bindingReply(t, mustAddr(t, s.UDPAddr()), 500*time.Millisecond) {
		t.Fatal("STUN binding request was answered although DisableSTUN is set")
	}
}

// Dropping binding requests must not touch TURN: the allocation path is the
// whole point of leaving the listener up.
func TestDisableSTUNKeepsAllocationsWorking(t *testing.T) {
	cfg := testConfig(t, "127.0.0.1:0", "")
	cfg.DisableSTUN = true
	s := startServer(t, cfg)

	client := udpClient(t, mustAddr(t, s.UDPAddr()))
	relayConn, err := client.Allocate()
	if err != nil {
		t.Fatalf("Allocate() with DisableSTUN set: %v", err)
	}
	defer func() { _ = relayConn.Close() }()

	if relayConn.LocalAddr() == nil {
		t.Fatal("allocation returned no relayed address")
	}
}
