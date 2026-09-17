package turn

import (
	"encoding/binary"
	"strings"
	"testing"
)

// xorMappedResponse builds a binding success response carrying addr, so the
// decoder is checked against a message that was not produced by the decoder.
func xorMappedResponse(ip []byte, port uint16, txID []byte) []byte {
	value := make([]byte, 4+len(ip))
	value[1] = addressFamilyIPv4
	if len(ip) == 16 {
		value[1] = addressFamilyIPv6
	}
	binary.BigEndian.PutUint16(value[2:4], port^(stunMagicCookie>>16))

	var mask [20]byte
	binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
	copy(mask[4:], txID)
	for i, b := range ip {
		value[4+i] = b ^ mask[i]
	}

	msg := make([]byte, stunHeaderLen)
	binary.BigEndian.PutUint16(msg[0:2], bindingSuccessType)
	binary.BigEndian.PutUint16(msg[2:4], uint16(4+len(value)))
	binary.BigEndian.PutUint32(msg[4:8], stunMagicCookie)
	copy(msg[8:20], txID)

	attr := make([]byte, 4)
	binary.BigEndian.PutUint16(attr[0:2], attrXORMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], uint16(len(value)))
	return append(msg, append(attr, value...)...)
}

func TestMappedAddressDecodesXORMappedAddress(t *testing.T) {
	txID := []byte("0123456789ab")

	v4 := xorMappedResponse([]byte{203, 0, 113, 9}, 54321, txID)
	if got := mappedAddress(v4); got != "203.0.113.9:54321" {
		t.Fatalf("IPv4 mapped address = %q", got)
	}

	ip6 := []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	v6 := xorMappedResponse(ip6, 3478, txID)
	if got := mappedAddress(v6); got != "[2001:db8::1]:3478" {
		t.Fatalf("IPv6 mapped address = %q", got)
	}
}

// A truncated or attribute-free message must return an empty string rather than
// panic. The port is open to anyone, so the parser sees whatever is sent to it.
func TestMappedAddressRejectsMalformedMessages(t *testing.T) {
	txID := []byte("0123456789ab")
	full := xorMappedResponse([]byte{203, 0, 113, 9}, 54321, txID)

	for name, msg := range map[string][]byte{
		"empty":            {},
		"header only":      full[:stunHeaderLen],
		"cut mid header":   full[:8],
		"cut mid attr":     full[:stunHeaderLen+3],
		"cut mid value":    full[:len(full)-2],
		"length overstate": overstateLength(full),
	} {
		if got := mappedAddress(msg); got != "" {
			t.Fatalf("%s: mappedAddress() = %q, want empty", name, got)
		}
	}
}

func overstateLength(msg []byte) []byte {
	out := make([]byte, len(msg))
	copy(out, msg)
	binary.BigEndian.PutUint16(out[stunHeaderLen+2:stunHeaderLen+4], 0xffff)
	return out
}

func TestBindingClassification(t *testing.T) {
	txID := []byte("0123456789ab")
	success := xorMappedResponse([]byte{203, 0, 113, 9}, 1234, txID)

	if !isBindingRequest(bindingRequest(t)) {
		t.Fatal("a binding request was not recognised")
	}
	if isBindingRequest(success) {
		t.Fatal("a success response was taken for a request")
	}

	response, ok := isBindingResponse(success)
	if !response || !ok {
		t.Fatalf("success response classified as response=%v success=%v", response, ok)
	}
	if response, _ := isBindingResponse(bindingRequest(t)); response {
		t.Fatal("a request was taken for a response")
	}

	// A message without the magic cookie is not STUN, whatever its first bytes
	// say. Anything may arrive on an open UDP port.
	notSTUN := make([]byte, 20)
	binary.BigEndian.PutUint16(notSTUN[0:2], bindingRequestType)
	if isBindingRequest(notSTUN) {
		t.Fatal("a message with no magic cookie was taken for STUN")
	}
}

// TestSTUNBindingIsLogged drives a real binding exchange through the server and
// checks the log names both halves and the address the peer was told.
func TestSTUNBindingIsLogged(t *testing.T) {
	out := &safeBuffer{}
	cfg := testConfig(t, "127.0.0.1:0", "")
	cfg.Log = capturingLogger(out)
	s := startServer(t, cfg)

	client := udpClient(t, mustAddr(t, s.UDPAddr()))
	mapped, err := client.SendBindingRequest()
	if err != nil {
		t.Fatalf("SendBindingRequest(): %v", err)
	}

	waitForLog(t, out, "STUN binding request", "STUN binding success", mapped.String())
}

// A repeat from the same address must not be logged at info again: an agent
// re-sends binding requests for as long as it holds a NAT mapping open.
func TestRepeatBindingRequestsAreDemotedToDebug(t *testing.T) {
	out := &safeBuffer{}
	conn := newSTUNLogConn(nil, capturingLogger(out))

	if got := conn.count("198.51.100.4:1111"); got != 1 {
		t.Fatalf("first attempt counted as %d", got)
	}
	if got := conn.count("198.51.100.4:1111"); got != 2 {
		t.Fatalf("second attempt counted as %d", got)
	}
	if got := conn.count("198.51.100.5:2222"); got != 1 {
		t.Fatalf("first attempt from a second address counted as %d", got)
	}
	if got := conn.attemptsFor("198.51.100.4:1111"); got != 2 {
		t.Fatalf("attemptsFor() = %d, want 2", got)
	}
}

// The table must not grow with the traffic: the port is unauthenticated and
// anyone can send to it from any address.
func TestAttemptTableIsCapped(t *testing.T) {
	conn := newSTUNLogConn(nil, nil)
	for i := range maxTrackedSources + 100 {
		conn.count(strings.Repeat("x", 3) + string(rune(i)))
	}
	if got := len(conn.attempts); got > maxTrackedSources {
		t.Fatalf("attempt table holds %d entries, cap is %d", got, maxTrackedSources)
	}
}
