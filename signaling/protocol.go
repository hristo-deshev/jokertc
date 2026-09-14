package signaling

import (
	"encoding/json"
)

const (
	typeJoin      = "join"
	typeJoined    = "joined"
	typeReady     = "ready"
	typeOffer     = "offer"
	typeAnswer    = "answer"
	typeCandidate = "candidate"
	typeBye       = "bye"

	// typeRemoteJoined announces a join to every other connected client, so a
	// test page can discover a session id it was not told out of band.
	typeRemoteJoined = "remote_joined"

	roleDevice = "device"
	rolePhone  = "phone"

	modeForward = "forward"
)

type joinMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Session string `json:"session"`
	Imei    string `json:"imei"`
}

type joinedMessage struct {
	Type string `json:"type"`
	Mode string `json:"mode"`
}

type readyMessage struct {
	Type      string          `json:"type"`
	Initiator bool            `json:"initiator"`
	Stun      string          `json:"stun"`
	Turn      *turnCredential `json:"turn"`
}

type turnCredential struct {
	URL  string `json:"url"`
	User string `json:"user"`
	Pass string `json:"pass"`
}

type byeMessage struct {
	Type string `json:"type"`
}

type remoteJoinedMessage struct {
	Type    string `json:"type"`
	Session string `json:"session"`
}

// frameType reports the "type" field of a client frame. ok is false when the
// frame is not a JSON object; a JSON object with no "type" yields an empty
// string and ok true, so the caller can log it as an unknown message rather
// than as malformed input.
func frameType(raw []byte) (string, bool) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", false
	}
	return envelope.Type, true
}

func mustMarshal(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
