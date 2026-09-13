package turn

import (
	"net"
	"testing"
)

func TestAllowAllAuthAcceptsAnyUsername(t *testing.T) {
	auth := AllowAllAuth{}
	key, ok := auth.Authenticate("test", "jokertc", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(key) == 0 {
		t.Fatal("expected non-empty key")
	}
}

func TestAllowAllAuthRejectsEmptyUsername(t *testing.T) {
	auth := AllowAllAuth{}
	_, ok := auth.Authenticate("", "jokertc", &net.UDPAddr{})
	if ok {
		t.Fatal("expected ok=false for empty username")
	}
}
