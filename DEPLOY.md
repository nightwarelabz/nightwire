# DEPLOY — sparta-rtc gateway + the /rtc door (parent-run, in order)

Everything below runs on Chase's M1 MacBook Air unless a step says
otherwise. Nothing here has been run yet. The live bridge door (`/`
→ 127.0.0.1:26001) and the running game server are never touched by
this deploy; the WebRTC road is additive, and the client falls back
to the bridge socket on any failure — including the gateway simply
not running.

House access: `~/workspace/thisissparta2/mac-ssh.sh '<cmd>'`.
Tailscale CLI on the Mac:
`/Applications/Tailscale.app/Contents/MacOS/Tailscale` (v1.104.2).

## 0. What lands on the Mac

From this directory (`~/workspace/thisissparta2/webrtc/` in the
workspace) copy to `~/thisissparta2/webrtc/` on the Mac, preserving
layout:

- `dist/sparta-rtc-darwin-arm64` and `dist/SHA256SUMS` from the same
  `./build.sh` run (Mach-O arm64; verify its hash against the manifest
  with `shasum -a 256` before running it). Historical report hashes
  describe the original binary, not a rebuilt gateway.
- `run-gateway-mac.sh` (make it executable)
- `proof/` (for step 3's completion run; optional but recommended)

## 1. Start the gateway

```sh
cd ~/thisissparta2/webrtc
chmod +x run-gateway-mac.sh dist/sparta-rtc-darwin-arm64
nohup ./run-gateway-mac.sh >> gateway-console.log 2>&1 &
tail -f gateway.log   # expect a listening line for 127.0.0.1:26003
```

Local sanity (plain GET is not an upgrade — the gateway must refuse
it exactly like the bridge does):

```sh
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:26003/rtc
# expect: 400
```

## 2. Mount /rtc on the public door

The door today (verify before changing anything):

```sh
/Applications/Tailscale.app/Contents/MacOS/Tailscale funnel status
# https://chases-macbook-air-1.taildc9dec.ts.net (Funnel on)
# |-- / proxy http://127.0.0.1:26001
```

Add the path mount (Funnel in 1.104.2 carries `--set-path` itself):

```sh
/Applications/Tailscale.app/Contents/MacOS/Tailscale funnel --bg --yes \
  --set-path /rtc http://127.0.0.1:26003
/Applications/Tailscale.app/Contents/MacOS/Tailscale funnel status
# expect BOTH lines:
# |-- /    proxy http://127.0.0.1:26001
# |-- /rtc proxy http://127.0.0.1:26003
```

**Never `tailscale funnel reset`** — it would drop the live `/`
bridge mount with it. If the status does not show both mounts, stop
and re-check before proceeding.

Outside probe (any machine off the tailnet; from the sandbox use an
outside path — sandbox egress mangles direct probes):

```sh
curl -s -o /dev/null -w '%{http_code}\n' https://chases-macbook-air-1.taildc9dec.ts.net/rtc
# expect: 400 (the gateway's not_a_websocket_upgrade refusal, via the door)
```

Origin law through the door is the gateway's own (foreign Origin →
403 before upgrade; https://nosuchmachine.net or absent → upgrade),
already proven against the binary in the lab.

## 3. Complete rung (a) on the Mac — the media-path proof

The lab sandbox forbids sends on unconnected UDP sockets (EPERM),
which ICE requires, so the DataChannel media path has never carried
a byte yet. The Mac is the first surface that can prove it. The
worker's harness is turnkey:

```sh
cd ~/thisissparta2/webrtc/proof
PROOF_BRIDGE_SCRIPT=/absolute/path/to/sparta-bridge.py \
CHROME_BIN='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' \
./run-proof.sh    # native Mac gateway selected automatically
```

Expect, per the lab's WS-side numbers: the DC workload completes
300/300 with 0 checksum / 0 byte-equality / 0 boundary failures, and
the gateway's own `rate_limit` close fires end-to-end on the blast.
The harness now fails if any required check fails; the report labels
explicitly allowed diagnostic media failures as blocked. `go test -race
./...` independently checks the real local DataChannel/UDP pipe without
the browser or bridge checkout. If ICE stalls at `checking` on the Mac, read `gateway.log`
first — that would be a NAT/firewall fact to chase (the runbook
suspect is inbound UDP 26100–26299, the pinned ICE ephemeral range),
not a client defect: the client falls back to the bridge meanwhile.

## 4. Live join proof (needs the site PR landed + serving)

1. Open https://nosuchmachine.net in a desktop browser on the Mac
   (or any machine), open Nexuiz, click to play.
2. On the Mac: `tail ~/thisissparta2/webrtc/gateway.log` — expect
   `session_open`, DataChannel traffic counters in `session_close`,
   and the game server's log showing the join.
   `~/thisissparta2/bridge/bridge.log` must show **no** new session
   for that player — proof the fast road carried it.
3. Fallback proof in production shape: stop the gateway
   (`pkill -f sparta-rtc-darwin-arm64`), join again from a fresh
   page — the player must land in the room via the bridge exactly as
   today (bridge log shows the session), a few seconds slower at
   most (the client's RTC setup budget is 8 s; a refused signaling
   door fails in well under that).

## 5. Rollback

```sh
pkill -f sparta-rtc-darwin-arm64
/Applications/Tailscale.app/Contents/MacOS/Tailscale funnel --bg --yes \
  --set-path /rtc off
/Applications/Tailscale.app/Contents/MacOS/Tailscale funnel status
# expect ONLY: |-- / proxy http://127.0.0.1:26001
```

If the `off` form is refused by the CLI, remove just the mount via
`tailscale serve status` / `set-config` — do not use `funnel reset`
(it takes the bridge door down with it). With the mount gone and
the gateway stopped, the system is byte-for-byte the pre-WebRTC
one; the site shim, if already landed, simply falls back on every
socket.
