# Nexuiz WebRTC transport — full-stack proof (sandbox, local production topology)

Run 2026-10-26 (UTC 04:43–05:07) on the sandbox VM only. Nothing on the
Mac, GitHub, or any live service was touched. All artifacts under
`webrtc/proof/fullstack/` (`logs/`, `shots/`, `scripts/`, `server/` staging,
`profile/` warm browser profile, `control-dist/`).

## Verdict

Every rung that this sandbox's network law allows **passed**, including the
one this proof was built for: with a genuine, un-simulated RTC failure, the
shipped shim in worker B's `play.js` fell back to the WebSocket road and the
real engine's behavior on that road was **byte-identical to the pre-shim
build** (same bridge packet counts, same bytes, byte-identical engine
console transcript). The final join rung — the server answering the client
and the client entering the world — is **blocked by the sandbox itself**,
by the same EPERM law that blocks ICE, this time on the server's side
(§3). It is not blocked by the shim, the gateway, the bridge, or the
server build. First run on a host without this filter (the Mac) closes it.

## 1. Topology under test (mirrors production)

```
headless Chromium (real dist play page, mode=join)
  bridge dealt: ws://127.0.0.1:26002          <- "the door" (scripts/door.mjs)
    path /rtc  -> sparta-rtc gateway  127.0.0.1:26003 (worker A binary, --server-port 26000)
    all else   -> sparta-bridge.py    127.0.0.1:26001 -> UDP 127.0.0.1:26000
  site+data:   http://127.0.0.1:18770 (scripts/site.mjs, dist = rtc-tree/dist)
    /api/nexuiz-data -> scripts/span-server.mjs (rig pattern): the pinned
    packs' raw-deflate spans read from the local, hash-verified
    nexuiz-252.zip — the same chunk contract as functions/api/nexuiz-data.ts
  DP server:   server/nexuiz-dedicated (see §2), house thisissparta2.cfg,
               map evilspace, UDP 26000
```

Query contract, read from `play.js` itself: `mode=join`,
`bridge=ws://127.0.0.1:26002`, `server=127.0.0.1:26000` (the `server`
parameter's own sanitizer admits dots and a colon, so `play.js` pushes
`+connect 127.0.0.1:26000` natively — no `extra` detour needed),
`name=proofpilot`. Origin law honored for real: bridge and gateway were
started with `--allow-origin http://127.0.0.1:18770` and accepted the
browser's true Origin (see logs; no `*` anywhere).

## 2. DarkPlaces server (task steps 1–2a)

No rebuild was needed: the house dedicated binary already exists —
`thisissparta2/server/nexuiz-dedicated`, built from the house DP tree
(the full build transcript is `thisissparta2/proof/dp-build.log`; its tail
is the `sv` link line producing this binary, banner `DarkPlaces Linux
dedicated d93f9c4`). I verified the artifact instead of recompiling it:
staged under `fullstack/server/` (house `server/data` packs by symlink +
`build/sparta-evilspace.pk3` + the house `thisissparta2.cfg` verbatim) it
boots and plays:

- `Server listening on address 0.0.0.0:26000`
- `:gamestart:ctf_evilspace:0.0.345060` (phase 2: `0.0.687798`)
- 6 havocbots fill via `minplayers 6` and join (`:join:1:1:bot:[BOT]Airhead
  connected and joined the Red Team`, …), frags logged throughout —
  MinstaHook CTF on evilspace, exactly the house configuration.

Logs: `fullstack/logs/dp-server.log`, `dp-server2.log`.

## 3. Environment law — restated, and confirmed twice in this run

This sandbox's egress filter returns `EPERM` ("Operation not permitted")
for **every send on an unconnected UDP socket**; only `connect()`ed UDP
sockets may send. Measured fresh in this run (`fullstack/logs/probe.log`,
`scripts/probe.py`, stdlib Python):

```
unconnected sendto: OSError errno=1 Operation not permitted
connected send: SUCCEEDED
```

Two consequences, both observed end-to-end in this run:

1. **ICE cannot complete** — ICE sends STUN checks from unconnected
   sockets. In the drives the gateway's peer connection never left
   `pc_state state=connecting` and no DataChannel ever opened (§4).
2. **The DP server cannot answer** — the dedicated server replies from
   its bound (unconnected) socket. A DP `getinfo` sent through the real
   door→bridge reached the server (bridge: `to_server_pkts=1
   to_server_bytes=13`, subprotocol `sparta`) and **zero frames came
   back**; the server's own console records why, repeatedly (12× in the
   phase-1 log, 20× in phase 2):

   ```
   LHNET_Write: sendto returned error: Operation not permitted
   ```

   This is the bridge crew's earlier finding (bridge/REPORT.md) reproduced
   against the full stack. The client→server direction works (the bridge
   sends on connected sockets); the server→client direction is severed
   by the sandbox, for WebSocket and WebRTC clients alike. A completed
   join in this sandbox is impossible **by environment, not by defect**.

## 4. The drive — shim build (task steps 3 + 4 i/ii)

Cold boot (phase 1, fresh profile): click at drive +1.5 s; the loader
fetched the 846 MB pack through the local data pipe (82 MB by +6.9 s),
verified it whole against the pinned SHA-256, and the engine started
**+76.7 s after the click**. Warm boots (phase 2, kept pack re-verified
from the profile's Cache Storage — "kept on this machine — no download"):
engine started **+77.1 s** (shim) / **+27.7 s** (control) after click;
the spread is the kept-pack verify+load, same code path both builds.

The RTC attempt and the fallback, from the phase-2 logs (UTC):

```
door     04:59:35.225  upgrade GET /rtc -> backend 26003        (shim signaling)
gateway  04:59:35.295  session_open session=1 origin=http://127.0.0.1:18770 path=/rtc
gateway  04:59:35.726  pc_state session=1 state=connecting      (never progresses — §3.1)
gateway  04:59:43.164  session_close session=1 reason=eof seconds=7.9   (the shim's 8 s budget)
door     04:59:43.176  upgrade GET / -> backend 26001           (fallback, 12 ms later)
bridge   04:59:43.200  session_open session=1 origin=http://127.0.0.1:18770 subprotocol=sparta
```

The engine's second connect attempt repeated the cycle exactly
(gateway session 2, 8.7 s, closed 04:59:58.244; bridge session 2 opened
04:59:58.281). The fallback trigger was the genuine article: a real
signaling session to the real gateway, a real RTCPeerConnection stuck in
ICE `connecting` under the sandbox EPERM law, and the shim's own budget
expiring — nothing was simulated or forced in code.

What the fallback carried (bridge session closes, phase 2):

```
session 1: reason=idle seconds=121.1  to_server_pkts=21 to_server_bytes=360  to_client 0/0
session 2: reason=client_close_1001 seconds=147.7 to_server_pkts=7 to_server_bytes=274 to_client 0/0
```

28 real engine datagrams (634 bytes — the connectionless connect
handshake and its retries) crossed the bridge to the server; the server's
answers died at `LHNET_Write` (§3.2), and the client's console closed
the story: `Connect: failed, no reply from 127.0.0.1:26000`.

Artifacts: `logs/gateway.log` + `gateway2.log`, `logs/bridge.log` +
`bridge2.log`, `logs/door.log` + `door2.log`, `logs/shim-drive.log` +
`shim2-drive.log`, `logs/shim2-console.txt`, shots below.

## 5. Control — pre-shim play.js (task step 4 v)

`fullstack/control-dist/` is a byte copy of the same `dist/` with only
`nexuiz/play.js` replaced by `git show HEAD:public/nexuiz/play.js`
(shim mentions: 0 vs 3 in the shim build; the HEAD→working diff is the
shim block alone, 474 insertions). Same door, same profile, same query:

- Door log: only `GET /` upgrades (05:03:12.609, 05:03:26.864) — **zero
  `/rtc` hits**, as a build without the shim must behave.
- Bridge closes are **numerically identical** to the shim run:
  session 3 = 21 pkts / 360 B, session 4 = 7 pkts / 274 B, 0 back.
- Engine console transcripts are **byte-identical**:
  `diff logs/shim2-console.txt logs/control-console.txt` is clean
  (2,755 bytes each), including the same terminal line,
  `Connect: failed, no reply from 127.0.0.1:26000`.

The shim, when RTC is unavailable, is indistinguishable from the
pre-shim build at every observable surface: bridge wire, packet counts,
and engine behavior.

## 6. Screenshots (task step 4 iv — with its limit stated)

`shots/shim-world.png`, `shots/shim2-console.png`, `shots/control-console.png`:
the real engine running in the page — full DarkPlaces boot on the
verified data, WebGL 2.0 renderer up, house menu program loaded
(`menu: program loaded (crc 16167, size 603K)`), and the red
`Connect: failed, no reply from 127.0.0.1:26000` line at the foot.
There is **no world/scoreboard shot**, because there is no world to
photograph until the server can answer (§3.2). Claiming one would be
padding; the console shot is the honest post-attempt state.

## 7. What was NOT proven here (weaker than production, plainly)

- **No completed join, no `:join:` line for the web client, no gameplay
  traffic.** Blocked by the sandbox severing the server→client UDP
  direction (§3.2), independent of transport. The server's `:join:`
  lines in the logs are its own bots'. Closing this rung needs one run
  on a host without the EPERM filter — the Mac topology as deployed.
- **No DataChannel media.** Same law, ICE side (§3.1). The gateway side
  is otherwise proven here live: signaling session accepted with the
  site Origin, answer/trickle machinery engaged, peer held in
  `connecting`, clean close at the client's budget. (Worker A's rung-(a)
  proof, `webrtc/proof/results.md`, additionally records this same
  gateway binary's pion log emitting the EPERM STUN warnings verbatim;
  in this run pion logged no warning lines — the `connecting`-forever
  state plus the probe in §3 is this run's own evidence.)
- **Loopback only.** RTT/latency say nothing about WAN; the data pipe
  was the local zip, not archive.org (deliberately — same chunk
  contract, no 850 MB egress burn).
- **Server staging** symlinked the house packs and added
  `sparta-evilspace.pk3`; the cfg is the house file verbatim. On the
  Mac the server runs its own deployed data dir.
- One environmental incident: the sandbox VM was replaced mid-run
  (~04:51 UTC) killing the first shim drive after its world screenshot
  and all key log lines were already on disk; phase 2 re-ran the full
  cycle cleanly, so no evidence rests on the interrupted run alone.

## 8. How to re-run

Start, in order: DP server (`fullstack/server/` staging +
`nexuiz-dedicated -userdir ./userdir +exec thisissparta2.cfg +map
evilspace`), `bridge/sparta-bridge.py` (26001), gateway binary (26003),
`scripts/door.mjs` (26002), `scripts/span-server.mjs` (18772),
`scripts/site.mjs <dist>` (18770) — bridge/gateway with
`--allow-origin http://127.0.0.1:18770`. Then
`node scripts/drive.mjs <tag> <playUrl> <profileDir> <maxSeconds>`
(the drive writes its own `file://` boot page: Chrome 152's Local
Network Access checks refuse an `about:blank`/direct navigation to
127.0.0.1 even with the house `--disable-features` pair —
`ERR_BLOCKED_BY_LOCAL_NETWORK_ACCESS_CHECKS`, measured — while the rig's
boot-page `location.replace` road loads; that is a sandbox-browser
quirk, not a site property).
