#!/usr/bin/env bash
#
# Run jokertc bound to all interfaces, with signaling, STUN/TURN, metrics and
# debug logging on.
#
# WARNING: this exposes an unauthenticated service. Anyone who can reach the
# host can join any /ws session whose id they guess and read the other side's
# SDP, and the embedded TURN server runs AllowAllAuth, which accepts any
# allocation where the credential equals the username. That is an open relay.
# Use this on a trusted network or behind a firewall, not on the open internet.
#
# Override any setting with an environment variable, for example:
#   PUBLIC_HOST=relay.example.com ./serve.sh
#   TURN_EXTERNAL_IP=203.0.113.7 ./serve.sh
#   LISTEN_ADDR=0.0.0.0:9090 ./serve.sh

set -euo pipefail

cd "$(dirname "$0")"

LISTEN_ADDR="${LISTEN_ADDR:-0.0.0.0:8080}"
METRICS_ADDR="${METRICS_ADDR:-0.0.0.0:8090}"
TURN_LISTEN_ADDR="${TURN_LISTEN_ADDR:-0.0.0.0:3478}"
TURN_RELAY_PORT_RANGE="${TURN_RELAY_PORT_RANGE:-50000-50100}"

# detect_ip returns the source address the host would use to reach the outside
# world. It is the address clients are told to send STUN/TURN traffic to, so
# behind NAT it is wrong: set PUBLIC_HOST and TURN_EXTERNAL_IP explicitly.
detect_ip() {
	ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") {print $(i + 1); exit}}'
}

DETECTED_IP="$(detect_ip || true)"
if [ -z "${DETECTED_IP}" ]; then
	DETECTED_IP="127.0.0.1"
	echo "serve.sh: could not detect an outbound IP, falling back to ${DETECTED_IP}" >&2
fi

# PUBLIC_HOST goes into the STUN and TURN URLs handed to clients in the ready
# message, so it must be an address those clients can reach.
PUBLIC_HOST="${PUBLIC_HOST:-${DETECTED_IP}}"
TURN_EXTERNAL_IP="${TURN_EXTERNAL_IP:-${DETECTED_IP}}"
TURN_PORT="${TURN_LISTEN_ADDR##*:}"

STUN_URL="${STUN_URL:-stun:${PUBLIC_HOST}:${TURN_PORT}}"
TURN_URL="${TURN_URL:-turn:${PUBLIC_HOST}:${TURN_PORT}}"

BIN="${BIN:-./build/server}"

# Always rebuild. An existing binary says nothing about whether it matches the
# current source, and a stale one fails confusingly: it rejects flags this
# script passes, long after the banner claims the server is starting. Go's
# build cache makes a no-op rebuild cheap. Set SKIP_BUILD=1 to run a binary
# you built yourself.
if [ -z "${SKIP_BUILD:-}" ]; then
	make build-server
fi
if [ ! -x "${BIN}" ]; then
	echo "serve.sh: ${BIN} is missing or not executable" >&2
	exit 1
fi

cat <<EOF
serve.sh: starting jokertc
  API + /ws + UI   http://${PUBLIC_HOST}:${LISTEN_ADDR##*:}
  manual test page http://${PUBLIC_HOST}:${LISTEN_ADDR##*:}/ui/manual
  signaling page   http://${PUBLIC_HOST}:${LISTEN_ADDR##*:}/ui/websocket
  metrics          http://${PUBLIC_HOST}:${METRICS_ADDR##*:}/metrics
  STUN/TURN        ${TURN_LISTEN_ADDR} (UDP and TCP), relay ports ${TURN_RELAY_PORT_RANGE}
  advertised to clients as ${STUN_URL} / ${TURN_URL}

  Unauthenticated. Do not expose this to the open internet.

EOF

exec "${BIN}" \
	--listen-addr "${LISTEN_ADDR}" \
	--metrics-addr "${METRICS_ADDR}" \
	--log-debug \
	--log-uid \
	--log-service jokertc \
	--turn-listen-addr "${TURN_LISTEN_ADDR}" \
	--turn-external-ip "${TURN_EXTERNAL_IP}" \
	--turn-relay-port-range "${TURN_RELAY_PORT_RANGE}" \
	--signaling \
	--signaling-stun-url "${STUN_URL}" \
	--signaling-turn-url "${TURN_URL}" \
	--signaling-no-receiver-seconds 20 \
	--signaling-ping-seconds 30 \
	"$@"
