package signaling

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadyMarshalsTurnAsNullWhenAbsent(t *testing.T) {
	raw, err := json.Marshal(readyMessage{Type: typeReady, Initiator: true, Stun: "stun:example:3478"})
	require.NoError(t, err)

	assert.JSONEq(t, `{"type":"ready","initiator":true,"stun":"stun:example:3478","turn":null}`, string(raw))
}

func TestReadyMarshalsTurnCredential(t *testing.T) {
	raw, err := json.Marshal(readyMessage{
		Type: typeReady,
		Stun: "",
		Turn: &turnCredential{URL: "turn:example:3478", User: "u", Pass: "p"},
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{"type":"ready","initiator":false,"stun":"","turn":{"url":"turn:example:3478","user":"u","pass":"p"}}`, string(raw))
}

func TestJoinedMarshalsForwardMode(t *testing.T) {
	raw, err := json.Marshal(joinedMessage{Type: typeJoined, Mode: modeForward})
	require.NoError(t, err)

	assert.JSONEq(t, `{"type":"joined","mode":"forward"}`, string(raw))
}

func TestJoinUnmarshalsCamelCaseFields(t *testing.T) {
	var join joinMessage
	require.NoError(t, json.Unmarshal([]byte(`{"type":"join","role":"device","session":"S1","imei":"123"}`), &join))

	assert.Equal(t, "device", join.Role)
	assert.Equal(t, "S1", join.Session)
	assert.Equal(t, "123", join.Imei)
}

func TestFrameType(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "join", raw: `{"type":"join","role":"phone"}`, want: "join", ok: true},
		{name: "candidate with extra fields", raw: `{"type":"candidate","sdpMid":"0","x":1}`, want: "candidate", ok: true},
		{name: "not json", raw: `this is not json`, ok: false},
		{name: "json but not an object", raw: `[1,2,3]`, ok: false},
		{name: "object without a type", raw: `{"sdp":"v=0"}`, want: "", ok: true},
		{name: "empty", raw: ``, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := frameType([]byte(tt.raw))
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
