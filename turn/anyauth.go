package turn

import (
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec // STUN long-term keys are MD5 by RFC 5389
	"crypto/sha1" //nolint:gosec // STUN MESSAGE-INTEGRITY is HMAC-SHA1 by RFC 5389
	"encoding/binary"
	"net"

	"github.com/go-chi/httplog/v2"
)

// anyCredentialConn accepts any username and any password on a TURN allocation,
// however they pair. It exists because there is no honest way to do this: pion
// checks MESSAGE-INTEGRITY at internal/server/util.go, and that check needs the
// key, which needs the password the client did not share.
//
// So this rewrites the MESSAGE-INTEGRITY attribute on each authenticated
// request before pion reads it. The attribute is recomputed with the key the
// AllowAllAuth handler will derive from the username, which makes pion's own
// check pass whatever the client actually signed with. The client's password is
// never verified, which is the point.
//
// This is a deliberate downgrade to an open relay, gated behind a flag and off
// by default. It does nothing to STUN Binding, which is already unauthenticated.
type anyCredentialConn struct {
	net.PacketConn
	realm string
	log   *httplog.Logger
}

func newAnyCredentialConn(conn net.PacketConn, realm string, log *httplog.Logger) *anyCredentialConn {
	return &anyCredentialConn{PacketConn: conn, realm: realm, log: log}
}

func (c *anyCredentialConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	if err != nil {
		return n, addr, err
	}
	if rewritten := c.forgeIntegrity(p[:n]); rewritten && c.log != nil {
		c.log.Debug("rewrote MESSAGE-INTEGRITY to accept any credential", "remoteAddr", addrString(addr))
	}
	return n, addr, err
}

// forgeIntegrity replaces the HMAC in a request's MESSAGE-INTEGRITY with one
// computed from the key AllowAllAuth derives, so pion's later check passes. It
// reports whether it changed the buffer.
//
// It only touches a message that carries both USERNAME and MESSAGE-INTEGRITY,
// so the first unauthenticated Allocate still gets its 401 challenge and the
// normal nonce handshake is left intact.
func (c *anyCredentialConn) forgeIntegrity(msg []byte) bool {
	var (
		username        []byte
		integrityOffset = -1
		haveMI          bool
	)
	forEachAttribute(msg, func(attrType uint16, offset int, value []byte) bool {
		switch attrType {
		case attrUsername:
			username = value
		case attrMessageIntegrity:
			integrityOffset, haveMI = offset, true
		}
		return true
	})
	if len(username) == 0 || !haveMI || integrityOffset < 0 {
		return false
	}
	if integrityOffset+4+messageIntegritySize > len(msg) {
		return false
	}

	// AllowAllAuth derives its key as MD5(username:realm:username): the
	// credential it expects is the username. Match that here.
	key := longTermKey(string(username), c.realm, string(username))

	// The HMAC input is the message up to the integrity attribute, with the
	// header length rewritten to include only the bytes through that attribute
	// (RFC 5389 section 15.4), which is how pion's Check reconstructs it.
	restore := binary.BigEndian.Uint16(msg[2:4])
	lengthThroughMI := uint16(integrityOffset + 4 + messageIntegritySize - stunHeaderLen)
	binary.BigEndian.PutUint16(msg[2:4], lengthThroughMI)
	mac := hmacSHA1(key, msg[:integrityOffset])
	binary.BigEndian.PutUint16(msg[2:4], restore)

	copy(msg[integrityOffset+4:integrityOffset+4+messageIntegritySize], mac)
	return true
}

func longTermKey(username, realm, password string) []byte {
	h := md5.New() //nolint:gosec,forbidigo // RFC 5389 long-term credential key is MD5
	_, _ = h.Write([]byte(username + ":" + realm + ":" + password))
	return h.Sum(nil)
}

func hmacSHA1(key, data []byte) []byte {
	m := hmac.New(sha1.New, key) //nolint:gosec // RFC 5389 MESSAGE-INTEGRITY
	_, _ = m.Write(data)
	return m.Sum(nil)
}
