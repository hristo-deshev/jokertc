package signaling

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readyTurn(t *testing.T, frames []map[string]any) map[string]any {
	t.Helper()
	ready := firstOfType(frames, typeReady)
	require.NotNil(t, ready)
	turn, _ := ready["turn"].(map[string]any)
	return turn
}

func TestReadyCarriesNoTurnWhenUnconfigured(t *testing.T) {
	hub := newTestHub(t, nil)

	device, _ := joinTest(t, hub, roleDevice, "s1")
	joinTest(t, hub, rolePhone, "s1")

	ready := firstOfType(drain(device), typeReady)
	require.NotNil(t, ready)
	assert.Nil(t, ready["turn"])
}

// The embedded TURN server runs turn.AllowAllAuth, which accepts an allocation
// when the credential equals the username. The credential this server issues
// must therefore be a matched pair, or the peers cannot allocate.
func TestReadyCarriesAUsableTurnCredential(t *testing.T) {
	hub := newTestHub(t, &Config{TurnURL: "turn:turn.example:3478"})

	device, _ := joinTest(t, hub, roleDevice, "s1")
	phone, _ := joinTest(t, hub, rolePhone, "s1")

	deviceTurn := readyTurn(t, drain(device))
	phoneTurn := readyTurn(t, drain(phone))

	require.NotNil(t, deviceTurn)
	assert.Equal(t, "turn:turn.example:3478", deviceTurn["url"])
	assert.NotEmpty(t, deviceTurn["user"])
	assert.Equal(t, deviceTurn["user"], deviceTurn["pass"], "AllowAllAuth requires credential == username")

	assert.Equal(t, deviceTurn["user"], phoneTurn["user"], "both sides of one session share a credential")
}

func TestEachSessionGetsItsOwnTurnCredential(t *testing.T) {
	hub := newTestHub(t, &Config{TurnURL: "turn:turn.example:3478"})

	deviceA, _ := joinTest(t, hub, roleDevice, "session-a")
	joinTest(t, hub, rolePhone, "session-a")
	deviceB, _ := joinTest(t, hub, roleDevice, "session-b")
	joinTest(t, hub, rolePhone, "session-b")

	turnA := readyTurn(t, drain(deviceA))
	turnB := readyTurn(t, drain(deviceB))

	assert.NotEqual(t, turnA["user"], turnB["user"])
}
