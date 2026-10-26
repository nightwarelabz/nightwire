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

GO="${GO:-$(command -v go || true)}"
[ -n "$GO" ] || GO="$HOME/workspace/.tools/go/bin/go"
command -v "$GO" >/dev/null 2>&1 || { echo "run-proof: Go is required; put it on PATH or set GO" >&2; exit 1; }
if [ -n "${PROOF_RTC_BIN:-}" ]; then
    RTC_BIN="$PROOF_RTC_BIN"
else
    case "$(uname -s)-$(uname -m)" in
        Darwin-arm64) RTC_BIN="../dist/sparta-rtc-darwin-arm64" ;;
        Linux-x86_64) RTC_BIN="../dist/sparta-rtc-linux-amd64" ;;
        *) echo "run-proof: set PROOF_RTC_BIN to a native gateway binary" >&2; exit 1 ;;
    esac
fi
BRIDGE="${PROOF_BRIDGE_SCRIPT:-../../bridge/sparta-bridge.py}"
[ -x "$RTC_BIN" ] || { echo "run-proof: build the native gateway first: $RTC_BIN" >&2; exit 1; }
[ -f "$BRIDGE" ] || { echo "run-proof: set PROOF_BRIDGE_SCRIPT to the real sparta-bridge.py checkout" >&2; exit 1; }

# Preflight: a stale instance on any proof port would silently shadow
# this run (the new gateway would fail to bind and the tests would hit
# the old one). Refuse to run in that state.
for p in 26003 26011 26013; do
    if python3 -c "import socket,sys; s=socket.socket(); sys.exit(0 if s.connect_ex(('127.0.0.1',$p))==0 else 1)"; then
        echo "run-proof: port $p is already in use — kill the stale process first" >&2
        exit 1
    fi
done
python3 -c "import socket; s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.bind(('127.0.0.1',26099)); s.close()"

# Old logs must never satisfy a new assertion.
for log in gateway.log gateway-cap.log bridge.log; do
    : > "out/$log"
done
(cd .. && "$GO" test -race -count=1 -v ./...) > out/go-test.log 2>&1 || {
    cat out/go-test.log >&2
    exit 1
}

PAGE_ORIGIN="null"
PIDS=""
cleanup() {
    # shellcheck disable=SC2086
    [ -n "$PIDS" ] && kill $PIDS 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

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
for pid in $PIDS; do
    kill -0 "$pid" 2>/dev/null || { echo "run-proof: a proof service exited; inspect out/ logs" >&2; exit 1; }
done

status=0
rm -f results.json
node drive-proof.mjs || status=$?
if [ -f results.json ]; then
    python3 summarize.py
else
    echo "run-proof: browser driver produced no results" >&2
    status=1
fi
echo "run-proof: results in proof/results.json + proof/results.md; logs in proof/out/"
exit "$status"
