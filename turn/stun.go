package turn

import (
	"encoding/binary"
	"net"
	"strconv"
)

// stunMagicCookie is the fixed value at bytes 4..8 of every STUN message
// (RFC 5389 section 6). It is what separates a STUN packet from anything else
// that arrives on the port.
const stunMagicCookie = 0x2112A442

// STUN message types. The first two bits of a STUN message are zero, and the
// rest encode the class and the method, so one comparison identifies a message.
const (
	bindingRequestType      = 0x0001
	bindingSuccessType      = 0x0101
	bindingErrorType        = 0x0111
	stunHeaderLen           = 20
	attrXORMappedAddress    = 0x0020
	attrMappedAddress       = 0x0001
	addressFamilyIPv4       = 0x01
	addressFamilyIPv6       = 0x02
	transactionIDOffsetHigh = 20
)

func stunMessageType(b []byte) (uint16, bool) {
	if len(b) < stunHeaderLen {
		return 0, false
	}
	if binary.BigEndian.Uint32(b[4:8]) != stunMagicCookie {
		return 0, false
	}
	return binary.BigEndian.Uint16(b[0:2]), true
}

func isBindingRequest(b []byte) bool {
	t, ok := stunMessageType(b)
	return ok && t == bindingRequestType
}

// isBindingResponse reports whether b answers a binding request, and whether
// that answer is a success.
func isBindingResponse(b []byte) (response, success bool) {
	t, ok := stunMessageType(b)
	if !ok {
		return false, false
	}
	return t == bindingSuccessType || t == bindingErrorType, t == bindingSuccessType
}

// mappedAddress returns the reflexive address a binding response carries, which
// is the address the server saw the request come from and the whole point of
// the exchange. It returns an empty string when the message has no such
// attribute, which is the normal case for an error response.
//
// XOR-MAPPED-ADDRESS masks the value with the magic cookie, and for IPv6 with
// the transaction id as well (RFC 5389 section 15.2). MAPPED-ADDRESS is the
// unmasked form kept for older clients.
func mappedAddress(b []byte) string {
	if len(b) < stunHeaderLen {
		return ""
	}
	body := b[stunHeaderLen:]
	if n := int(binary.BigEndian.Uint16(b[2:4])); n <= len(body) {
		body = body[:n]
	}

	for len(body) >= 4 {
		attrType := binary.BigEndian.Uint16(body[0:2])
		attrLen := int(binary.BigEndian.Uint16(body[2:4]))
		if 4+attrLen > len(body) {
			return ""
		}
		value := body[4 : 4+attrLen]

		switch attrType {
		case attrXORMappedAddress:
			if addr := decodeAddress(value, b, true); addr != "" {
				return addr
			}
		case attrMappedAddress:
			if addr := decodeAddress(value, b, false); addr != "" {
				return addr
			}
		}

		// Attributes are padded to a multiple of four bytes.
		advance := 4 + attrLen
		if pad := attrLen % 4; pad != 0 {
			advance += 4 - pad
		}
		if advance > len(body) {
			return ""
		}
		body = body[advance:]
	}
	return ""
}

func decodeAddress(value, msg []byte, xor bool) string {
	if len(value) < 4 {
		return ""
	}
	wantLen := 8
	if value[1] == addressFamilyIPv6 {
		wantLen = 20
	} else if value[1] != addressFamilyIPv4 {
		return ""
	}
	if len(value) < wantLen {
		return ""
	}

	port := binary.BigEndian.Uint16(value[2:4])
	ip := make(net.IP, wantLen-4)
	copy(ip, value[4:wantLen])

	if xor {
		port ^= stunMagicCookie >> 16
		var mask [20]byte
		binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
		copy(mask[4:], msg[8:transactionIDOffsetHigh]) // transaction id, IPv6 only
		for i := range ip {
			ip[i] ^= mask[i]
		}
	}
	// JoinHostPort, not a format string: an IPv6 address has to be bracketed or
	// the result reads as another group of the address itself.
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
}
