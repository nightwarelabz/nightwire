#!/bin/sh
# run-proof.sh — rung-(a) lab proof for sparta-rtc, all on loopback:
#
#   udp_echo.py (:26099) stands in for the DarkPlaces server.
#   sparta-rtc linux binary (:26003, --server-port 26099) — RTC path.
#   real sparta-bridge.py (:26011, --server-port 26099) — WS comparison.
#   proof-page.html loaded via file:// (the project rig pattern);
#   Chromium sends the literal Origin "null" (serialized opaque
#   origin) for its sockets, which is explicitly allowed on BOTH
#   proof instances (production default stays
#   https://nosuchmachine.net only). drive-proof.mjs runs the same
#   workload through both paths in headless Chromium and writes
#   proof/results.json.
set -eu
cd "$(dirname "$0")"
mkdir -p out

# Preflight: a stale instance on any proof port would silently shadow
# this run (the new gateway would fail to bind and the tests would hit
# the old one). Refuse to run in that state.
for p in 26003 26011 26013 26099; do
    if python3 -c "import socket,sys; s=socket.socket(); sys.exit(0 if s.connect_ex(('127.0.0.1',$p))==0 else 1)"; then
        echo "run-proof: port $p is already in use — kill the stale process first" >&2
        exit 1
    fi
done

RTC_BIN="../dist/sparta-rtc-linux-amd64"
BRIDGE="../../bridge/sparta-bridge.py"
PAGE_ORIGIN="null"
PIDS=""
cleanup() {
    # shellcheck disable=SC2086
    [ -n "$PIDS" ] && kill $PIDS 2>/dev/null || true
}
trap cleanup EXIT INT TERM

python3 udp_echo.py 26099 > out/echo.log 2>&1 &
PIDS="$PIDS $!"
PION_LOG_DEBUG=all "$RTC_BIN" --bind 127.0.0.1 --port 26003 \
    --server-host 127.0.0.1 --server-port 26099 \
    --allow-origin https://nosuchmachine.net --allow-origin "$PAGE_ORIGIN" \
    --log-file "$PWD/out/gateway.log" > out/gateway-stdout.log 2>&1 &
PIDS="$PIDS $!"
# Second instance with a tiny cap, for the end-to-end session-cap smoke.
"$RTC_BIN" --bind 127.0.0.1 --port 26013 \
    --server-host 127.0.0.1 --server-port 26099 \
    --max-sessions 2 \
    --log-file "$PWD/out/gateway-cap.log" > out/gateway-cap-stdout.log 2>&1 &
PIDS="$PIDS $!"
python3 "$BRIDGE" --bind 127.0.0.1 --port 26011 \
    --server-host 127.0.0.1 --server-port 26099 \
    --allow-origin "$PAGE_ORIGIN" \
    --log-file "$PWD/out/bridge.log" > out/bridge-stdout.log 2>&1 &
PIDS="$PIDS $!"
sleep 1

node drive-proof.mjs

# Gateway guard unit tests (rate window, registry cap, origin law).
GO="$HOME/workspace/.tools/go/bin/go"
if [ -x "$GO" ]; then
    (cd .. && GOPATH="$HOME/workspace/.tools/gopath" \
        GOCACHE="$HOME/workspace/.tools/gocache" \
        "$GO" test -count=1 -v .) > out/go-test.log 2>&1 || true
fi
python3 summarize.py
echo "run-proof: results in proof/results.json + proof/results.md; logs in proof/out/"
