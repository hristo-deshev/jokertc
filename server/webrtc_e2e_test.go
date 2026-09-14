package server

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"jokertc/signaling"
	"jokertc/turn"
)

// e2eRelayPortMin/Max are distinct from the range turn_integration_test.go
// claims, so the two tests cannot compete for the same relay ports.
const (
	e2eRelayPortMin = 60200
	e2eRelayPortMax = 60300
)

// startCallServer builds the production composition: signaling and the
// embedded STUN/TURN server in one process, exactly as cmd/server wires them.
func startCallServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv, err := New(&HTTPServerConfig{
		ListenAddr: "127.0.0.1:0",
		Log:        testLogger(t),
		Signaling: &signaling.Config{
			// The real TURN port is not known until New returns, so the URL
			// advertised here is a placeholder. The test dials TURN at the
			// address the server actually bound, and asserts the *credential*
			// from the ready frame. Do not "fix" this by hardcoding a port.
			TurnURL: "turn:127.0.0.1:3478",
		},
		TURN: &turn.Config{
			ListenUDPAddr: "127.0.0.1:0",
			ExternalIP:    "127.0.0.1",
			RelayPortMin:  e2eRelayPortMin,
			RelayPortMax:  e2eRelayPortMax,
			Auth:          turn.AllowAllAuth{},
			Log:           testLogger(t),
		},
		ReadTimeout:              productionReadTimeout,
		WriteTimeout:             productionWriteTimeout,
		GracefulShutdownDuration: 5 * time.Second,
	})
	require.NoError(t, err)

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", srv.cfg.ListenAddr)
	require.NoError(t, err)
	go func() { _ = srv.srv.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	srv.RunInBackground()
	t.Cleanup(func() {
		if srv.turnSrv != nil {
			_ = srv.turnSrv.Close()
		}
	})

	return srv, "ws://" + ln.Addr().String() + "/ws"
}

// relayOnlyPeer builds a peer connection that can only connect through TURN.
// Relay policy discards host and server-reflexive candidates, so a successful
// call proves the peer allocated on the embedded TURN server using the
// credential the signaling server issued.
func relayOnlyPeer(t *testing.T, turnAddr string, ready map[string]any) *webrtc.PeerConnection {
	t.Helper()

	cred, ok := ready["turn"].(map[string]any)
	require.True(t, ok, "ready frame carried no TURN credential")
	user, _ := cred["user"].(string)
	pass, _ := cred["pass"].(string)
	require.NotEmpty(t, user)
	require.Equal(t, user, pass, "AllowAllAuth requires credential == username")

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{
			URLs:           []string{"turn:" + turnAddr + "?transport=udp"},
			Username:       user,
			Credential:     pass,
			CredentialType: webrtc.ICECredentialTypePassword,
		}},
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

// trickle forwards a peer connection's local candidates to the signaling
// server as they are gathered.
func trickle(pc *webrtc.PeerConnection, client *wsClient) {
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		raw, err := json.Marshal(map[string]any{
			"type":      "candidate",
			"candidate": c.ToJSON().Candidate,
			"sdpMid":    c.ToJSON().SDPMid,
		})
		if err != nil {
			return
		}
		client.send(string(raw))
	})
}

// pumpSignaling applies every frame the server forwards to this peer until the
// test ends.
func pumpSignaling(t *testing.T, pc *webrtc.PeerConnection, client *wsClient, onOffer func(webrtc.SessionDescription)) {
	t.Helper()
	go func() {
		for {
			raw, err := client.readQuiet()
			if err != nil {
				return
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				continue
			}
			switch m["type"] {
			case "offer":
				sdp, _ := m["sdp"].(string)
				if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
					return
				}
				onOffer(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp})
			case "answer":
				sdp, _ := m["sdp"].(string)
				_ = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
			case "candidate":
				cand, _ := m["candidate"].(string)
				if cand == "" {
					continue
				}
				var mid *string
				if s, ok := m["sdpMid"].(string); ok {
					mid = &s
				}
				_ = pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: cand, SDPMid: mid})
			}
		}
	}()
}

func sendSDP(client *wsClient, kind string, sdp webrtc.SessionDescription) {
	raw, err := json.Marshal(map[string]any{"type": kind, "sdp": sdp.SDP})
	if err != nil {
		return
	}
	client.send(string(raw))
}

// TestWebRTCCallRelaysDataThroughEmbeddedTURN closes the loop the signaling
// tests leave open: it proves the credential this server issues is usable, that
// both peers allocate on the embedded TURN server, and that application data
// crosses the relayed path in both directions.
func TestWebRTCCallRelaysDataThroughEmbeddedTURN(t *testing.T) {
	srv, url := startCallServer(t)

	turnUDP := srv.turnSrv.UDPAddr()
	require.NotNil(t, turnUDP, "the embedded TURN server has no UDP listener")
	turnAddr := turnUDP.String()

	deviceClient := dialWS(t, url)
	deviceClient.join("device", "e2e-1")
	deviceClient.expect("joined")

	phoneClient := dialWS(t, url)
	phoneClient.join("phone", "e2e-1")
	phoneClient.expect("joined")

	_, deviceReady := deviceClient.expect("ready")
	_, phoneReady := phoneClient.expect("ready")
	require.Equal(t, false, deviceReady["initiator"])
	require.Equal(t, true, phoneReady["initiator"], "the phone drives the offer")

	devicePC := relayOnlyPeer(t, turnAddr, deviceReady)
	phonePC := relayOnlyPeer(t, turnAddr, phoneReady)

	fromDevice := make(chan string, 4)
	devicePC.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() { _ = dc.SendText("hello from device") })
		dc.OnMessage(func(msg webrtc.DataChannelMessage) { fromDevice <- string(msg.Data) })
	})

	fromPhone := make(chan string, 4)
	dc, err := phonePC.CreateDataChannel("m2m", nil)
	require.NoError(t, err)
	dc.OnMessage(func(msg webrtc.DataChannelMessage) { fromPhone <- string(msg.Data) })
	dc.OnOpen(func() { _ = dc.SendText("hello from phone") })

	trickle(devicePC, deviceClient)
	trickle(phonePC, phoneClient)

	deviceConnected := connectionStateWatcher(devicePC)
	phoneConnected := connectionStateWatcher(phonePC)

	// The device answers whatever offer reaches it.
	pumpSignaling(t, devicePC, deviceClient, func(webrtc.SessionDescription) {
		answer, err := devicePC.CreateAnswer(nil)
		if err != nil {
			return
		}
		if err := devicePC.SetLocalDescription(answer); err != nil {
			return
		}
		sendSDP(deviceClient, "answer", answer)
	})
	pumpSignaling(t, phonePC, phoneClient, func(webrtc.SessionDescription) {})

	offer, err := phonePC.CreateOffer(nil)
	require.NoError(t, err)
	require.NoError(t, phonePC.SetLocalDescription(offer))
	sendSDP(phoneClient, "offer", offer)

	awaitConnected(t, "phone", phoneConnected)
	awaitConnected(t, "device", deviceConnected)

	assertSelectedCandidateIsRelay(t, "phone", phonePC)
	assertSelectedCandidateIsRelay(t, "device", devicePC)

	assert.Equal(t, "hello from phone", awaitMessage(t, "device received", fromDevice))
	assert.Equal(t, "hello from device", awaitMessage(t, "phone received", fromPhone))
}

func connectionStateWatcher(pc *webrtc.PeerConnection) chan webrtc.PeerConnectionState {
	states := make(chan webrtc.PeerConnectionState, 8)
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		select {
		case states <- s:
		default:
		}
	})
	return states
}

func awaitConnected(t *testing.T, who string, states chan webrtc.PeerConnectionState) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case s := <-states:
			switch s {
			case webrtc.PeerConnectionStateConnected:
				return
			case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
				t.Fatalf("%s peer connection reached %s instead of connected", who, s)
			}
		case <-deadline:
			t.Fatalf("%s peer connection did not connect", who)
		}
	}
}

func awaitMessage(t *testing.T, what string, ch chan string) string {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(30 * time.Second):
		t.Fatalf("%s: no data channel message arrived", what)
		return ""
	}
}

// assertSelectedCandidateIsRelay reads the nominated pair from the peer
// connection's stats. Relay policy plus a connected state already implies TURN
// was used; naming the leg makes a failure diagnosable instead of a timeout.
func assertSelectedCandidateIsRelay(t *testing.T, who string, pc *webrtc.PeerConnection) {
	t.Helper()
	stats := pc.GetStats()
	for _, s := range stats {
		pair, ok := s.(webrtc.ICECandidatePairStats)
		if !ok || pair.State != webrtc.StatsICECandidatePairStateSucceeded || !pair.Nominated {
			continue
		}
		local, ok := stats[pair.LocalCandidateID].(webrtc.ICECandidateStats)
		require.True(t, ok, "%s: no local candidate stats", who)
		assert.Equal(t, "relay", local.CandidateType.String(), "%s: selected candidate is not relayed", who)
		return
	}
	t.Fatalf("%s: no nominated candidate pair in stats", who)
}
