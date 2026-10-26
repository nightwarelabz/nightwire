#!/bin/sh
# run-gateway-mac.sh — run sparta-rtc on the Mac, beside the game
# server. DELIVERABLE ONLY: this script is deployed with the darwin
# binary; it is not run from the lab.
#
# House style mirrors bridge/deploy-mac.sh and scripts/run-server.sh:
# cd to this directory, exec the binary in the foreground, log to a
# file beside it. State lives beside the script: gateway.log.
#
# Posture: the gateway binds 127.0.0.1:26003 (plain WebSocket
# signaling) and expects Tailscale Funnel to face the public internet
# as wss://, mounted at /rtc — the gateway accepts signaling upgrades
# on ANY path, so no path rewriting is needed at the front. ICE media
# does not ride the Funnel: peers connect over UDP directly, so the
# host firewall must allow inbound UDP 26100-26299 (the pinned ICE
# ephemeral range) for the gateway to be reachable off-LAN.
#
# Environment overrides:
#   SPARTA_RTC_BIND        (default 127.0.0.1)
#   SPARTA_RTC_PORT        (default 26003)
#   SPARTA_SERVER_HOST     (default 127.0.0.1)
#   SPARTA_SERVER_PORT     (default 26000)
#   SPARTA_RTC_STUN        (default stun:stun.l.google.com:19302)
#   SPARTA_RTC_SETUP_TIMEOUT (default 15s)
#   SPARTA_RTC_MAX_BUFFERED_BYTES (default 262144)
set -eu
cd "$(dirname "$0")"

BIND="${SPARTA_RTC_BIND:-127.0.0.1}"
PORT="${SPARTA_RTC_PORT:-26003}"
SERVER_HOST="${SPARTA_SERVER_HOST:-127.0.0.1}"
SERVER_PORT="${SPARTA_SERVER_PORT:-26000}"
STUN="${SPARTA_RTC_STUN:-stun:stun.l.google.com:19302}"
SETUP_TIMEOUT="${SPARTA_RTC_SETUP_TIMEOUT:-15s}"
MAX_BUFFERED_BYTES="${SPARTA_RTC_MAX_BUFFERED_BYTES:-262144}"
LOGFILE="$PWD/gateway.log"

BIN="$PWD/dist/sparta-rtc-darwin-arm64"
[ -x "$BIN" ] || BIN="$PWD/sparta-rtc-darwin-arm64"
if [ ! -x "$BIN" ]; then
    echo "run-gateway-mac.sh: sparta-rtc-darwin-arm64 not found (run ./build.sh first)" >&2
    exit 1
fi

exec "$BIN" \
    --bind "$BIND" --port "$PORT" \
    --server-host "$SERVER_HOST" --server-port "$SERVER_PORT" \
    --stun "$STUN" \
    --setup-timeout "$SETUP_TIMEOUT" --max-buffered-bytes "$MAX_BUFFERED_BYTES" \
    --log-file "$LOGFILE"
