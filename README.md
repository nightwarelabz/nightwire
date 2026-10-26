# nightwire

nightwire is the WebRTC transport for Nexuiz inside Blacksite OS — an RTCDataChannel road between the player's browser and the game server, running beside the WebSocket bridge it relieves: same datagrams, no TCP queue.

A NIGHTWARE LABZ project.

This repo holds the gateway half, `sparta-rtc` (below). The client half is a WebSocket-compatible shim in the site repo (nightwarelabz/nosuchmachine.net, `public/nexuiz/play.js`): house peers try the DataChannel first and land on the bridge socket, unchanged, on any failure. Deployment lives in DEPLOY.md; the proof harness and its results live in `proof/`.

---
The house WebSocket bridge (`sparta-bridge.py`) carries web players
to the DarkPlaces server over TCP: one stalled snapshot queues every
datagram behind it. This gateway is the same dumb pipe over an
unreliable transport — an RTCDataChannel with
`{ordered:false, maxRetransmits:0}` — so stale snapshots are dropped by
the transport instead of queueing.

```
wasm client ──RTCDataChannel──► sparta-rtc ──UDP──► 127.0.0.1:26000 (DP server)
              ◄─signaling WS──►  127.0.0.1:26003 (Funnel mounts it at /rtc)
```

One signaling WebSocket == one RTCPeerConnection == one DataChannel ==
one ephemeral, *connected* UDP socket to the game server. DataChannel
messages become UDP datagrams and UDP datagrams become DataChannel
messages, byte-identical, 1:1, payload-blind — including the Emscripten
SOCKFS `"\xff\xff\xff\xffport"` first packet, forwarded verbatim like
everything else.

## Client contract (for the browser shim)

1. Open a WebSocket to the signaling endpoint — the same public
   door as the bridge with the `/rtc` path appended (production:
   `wss://chases-macbook-air-1.taildc9dec.ts.net/rtc`, derived from
   the dealt bridge URL; the gateway accepts upgrades on ANY path —
   the Funnel mount decides). Browser Origin must be
   `https://nosuchmachine.net`; non-browser clients may send no Origin.
2. Create an `RTCPeerConnection` (STUN `stun:stun.l.google.com:19302`,
   no TURN) and a DataChannel labeled **`sparta`** with
   `{ordered:false, maxRetransmits:0}` — the CLIENT creates the
   channel; the gateway accepts it via `OnDataChannel`.
3. Signaling is JSON over the WS, trickle ICE both ways:
   - send `{"type":"offer","sdp":...}`
   - receive `{"type":"answer","sdp":...}`
   - both directions: `{"type":"ice","candidate":...,"sdpMid":...,"sdpMLineIndex":...}`
4. When the channel opens, the gateway dials the game server. From
   then on: one DC message == one UDP datagram, both ways, up to
   65507 bytes.

## Guards (identical law to the bridge)

- **Origin**: site origin or absent; anything else is 403 before upgrade.
- **Session cap**: 64 (`--max-sessions`); over-cap signaling is refused 503.
- **Rate sanity**: per-session client→server 300 pps sliding window
  (`--max-pps`); excess dropped; the session is closed after 10
  abusive drops (`rate_limit`).
- **Idle reaper**: ~120 s (`--idle-timeout`) without traffic either way.
- **Setup deadline**: 15 s (`--setup-timeout 15s`) from signaling
  acceptance, including clients that never send an offer. Signaling
  traffic cannot extend this budget or keep an idle game session alive.
- **Signaling bounds**: 64 KiB per message, 128 ICE candidates per
  session, 4 KiB per candidate, and 5 s write deadlines. One offer and
  one `sparta` channel with the documented unreliable settings are accepted.
- **Backpressure**: at most 256 KiB of queued DataChannel payloads
  (`--max-buffered-bytes`). New server datagrams that would exceed this
  budget are dropped and counted; the session stays open.
- **Message ceiling**: 65507 bytes.
- **Logging**: structured one-line events (`session_open`,
  `session_close` with packet/byte totals and backpressure drops, `rate_drop`, …), metadata
  only — payloads are never logged.

## ICE / firewall notes

- STUN only, no TURN (`--stun`, default `stun:stun.l.google.com:19302`).
- The ICE ephemeral port range is pinned to **UDP 26100–26299** via
  pion's `SettingEngine.SetEphemeralUDPPortRange`. Media does NOT ride
  the Tailscale Funnel (Funnel is TCP); peers reach the gateway over
  UDP directly, so the game host's firewall must allow inbound UDP
  26100–26299 for off-LAN play. The signaling port (26003) stays
  loopback-only behind the Funnel.

## Build & deploy

```sh
./build.sh            # -> dist/sparta-rtc-darwin-arm64 (Mac deliverable)
                      #    dist/sparta-rtc-linux-amd64 (lab proof)
./run-gateway-mac.sh  # on the Mac: exec the darwin binary, house flags,
                      # logs to gateway.log beside the script
```

Single static binary, `CGO_ENABLED=0`, Go + pion/webrtc (versions
pinned in `go.mod`/`go.sum`). Flags mirror the bridge:
`--bind --port --server-host --server-port --max-sessions
--idle-timeout --max-pps --stun --allow-origin --log-file`, plus
`--setup-timeout` and `--max-buffered-bytes`. Build targets are explicit
on every host; `dist/SHA256SUMS` records the resulting binaries.

## Checks

```sh
go test -race -count=1 ./...
go vet ./...
node --test proof/validate-results.test.mjs
python3 -m unittest discover -s proof -p 'test_*.py'
```

The Go suite uses real Pion peers, a signaling WebSocket, and loopback
UDP sockets to check byte-exact datagrams through 65,507 bytes, the
rate-limit close, oversized-message rejection, and session lifecycle
races. It advertises a fixed loopback interface so runners do not need
permission to enumerate host NICs. No STUN or external game server is
needed. CI runs race checks on Go 1.24 and 1.27 and verifies both build
targets from Linux and macOS hosts.

## Proof

`proof/` also holds the browser proof (`run-proof.sh`): a UDP echo
sink stands in for the game server; a real headless Chromium drives
the signaling + workload page; the same workload runs through the real
`sparta-bridge.py` for comparison. It requires Go, Node 22+, Python 3,
Chromium, a native built gateway, and the separate bridge checkout:

```sh
PROOF_BRIDGE_SCRIPT=/absolute/path/to/sparta-bridge.py \
CHROME_BIN=/absolute/path/to/chromium ./proof/run-proof.sh
```

`GO` and `PROOF_RTC_BIN` override tool/binary discovery. Test failures
return a nonzero exit status. An unavailable RTC media path fails by
default; `PROOF_ALLOW_BLOCKED_RTC=1` permits a diagnostic run that marks
those checks **blocked**, never passed. Reports are generated from the
current measurements without assuming a particular network restriction.
The committed `proof/results.md` and `REPORT.md` preserve the original
lab run; they do not establish current WAN or production performance.
