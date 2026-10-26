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
- **Message ceiling**: 65507 bytes.
- **Logging**: structured one-line events (`session_open`,
  `session_close` with packet/byte totals, `rate_drop`, …), metadata
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
--idle-timeout --max-pps --stun --allow-origin --log-file`.

## Proof

`proof/` holds the rung-(a) lab proof (`run-proof.sh`): a UDP echo
sink stands in for the game server; a real headless Chromium drives
the signaling + workload page; the same workload runs through the real
`sparta-bridge.py` for comparison. See `proof/results.md` — including
why the DataChannel media path itself cannot be exercised inside the
lab sandbox (its egress policy returns EPERM for every send on an
unconnected UDP socket, which is how ICE sends STUN checks; the
gateway's guard arithmetic is pinned by `gateway_test.go` instead).
