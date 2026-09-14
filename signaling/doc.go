// Package signaling implements the /ws WebRTC signaling endpoint. It pairs a
// device with a phone inside a named session and passes SDP offers, answers
// and ICE candidates between them. Media never touches this server: ICE runs
// peer to peer, with the embedded STUN/TURN server as the fallback path.
//
// The message contract is fixed by existing firmware and phone clients, and is
// described in docs/plans/2026-09-14-webrtc-signaling-ws.md. In brief, over
// JSON text frames:
//
//	client -> server: join, offer, answer, candidate, bye
//	server -> client: joined, ready, offer, answer, candidate, bye
//
// The first frame on a connection must be join. Once both sides of a session
// are present each receives ready, which names the STUN and TURN servers and
// says which side creates the offer. The phone is always the initiator.
// offer, answer and candidate frames are forwarded byte for byte, because
// clients send fields this package does not model.
//
// Design rule: the state machine in server.go never touches a socket. A peer
// is reached through a buffered channel and a close func, so notifying a peer
// cannot block, the whole registry fits behind one mutex, and the pairing
// logic is testable without a network. Keep *websocket.Conn confined to
// conn.go and routes.go.
package signaling
