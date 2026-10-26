# nightwire — build report (2026-10-25/26)

The WebRTC transport for Nexuiz-in-Blacksite, built in full and
staged, nothing deployed. Two halves:

- **Gateway (`sparta-rtc`)** — Go 1.27 + pion/webrtc v4.2.25, one
  static binary. Source here (`main.go`, ~20 KB); darwin/arm64
  deliverable in `dist/` (10,890,210 B, sha256
  `8117b13e420db2e32190996bf95d3e4dd5a0d28a3be1fcd7e594ab051acf0bb1`;
  `build.sh` reproduces it byte-identically). It terminates WebRTC
  beside the Python bridge on the Mac, opens one `connect()`ed UDP
  socket per session to 127.0.0.1:26000, and pipes DataChannel
  messages ⇄ datagrams byte-for-byte, payload-blind — the bridge's
  exact session shape and guards (Origin law, 64-session cap,
  300 pps sanity window with drop-then-close, ~120 s idle reaper,
  65,507 B ceiling), structured logs in the bridge's voice.
- **Client shim** — in the site repo's `public/nexuiz/play.js`
  (staged separately, see the parent report). Emscripten SOCKFS
  dials the page-global `WebSocket` constructor at call time with
  the dealt `Module.websocket` URL/subprotocol; the shim replaces
  that constructor for the play page only. House-URL constructions
  get an RTC-first socket (signaling to the same door at `/rtc`,
  STUN `stun:stun.l.google.com:19302`, no TURN, DataChannel
  `sparta` with `{ordered:false, maxRetransmits:0}`, 8,000 ms setup
  budget); any other URL gets a real WebSocket, untouched. No
  engine change, no relink, `os-nexuiz.ts` untouched.

Signaling rides the existing public door: the Mac's Tailscale
(1.104.2) carries `--set-path` on `funnel` itself (verified
read-only), and the live config is a single `/` mount to the
bridge. Deploy adds `/rtc` → 127.0.0.1:26003 (gateway) — commands,
probes, and rollback in DEPLOY.md.

## Proof census, rung by rung

**(a) Lab loop.** The WS comparison path ran the full workload in
real headless Chromium against the real `sparta-bridge.py` and a
UDP echo sink: **300/300 messages, 795,876 B each way, 0 checksum /
0 byte-equality / 0 boundary failures**; RTT mean 0.85 ms, p50 0.5,
p95 2.5, p99 3.6, max 7.1 (loopback — framing/queueing overhead,
not WAN physics). The gateway's signaling leg ran in the same
browser: answer received, candidates trickled both ways,
`session_open` logged. Origin law proven live against the binary
(foreign 403 / absent 101 / site 101, any path); session cap proven
live (101 / 101 / 503 at cap 2); rate-limit logic pinned by Go
tests and by the bridge's identical logic end-to-end (blast of
2,000 → exactly 300 forwarded, 10 dropped, `rate_limit` close).
**The DataChannel media leg could not run here** — see the wall,
below.

**(b) Full-stack.** The real house dedicated binary ran the real
house config (`thisissparta2.cfg` verbatim, `sparta-evilspace.pk3`,
6 bots fragging on UDP 26000). The production topology was rebuilt
locally — a path router as the door (`/rtc` → gateway, `/` →
bridge) in front of worker B's built site — and the real play page
was driven in Chromium. The engine booted in-page (846 MB fetched
and SHA-verified through the local data leg; engine started 76.7 s
after the click), the shim signaled through `/rtc`, and the
fallback carried 28 real engine datagrams to the server.
**A completed join is not claimed**: the server's replies die at
the sandbox wall (below) for WS and RTC clients alike, so the
client console ends at `Connect: failed, no reply`. The join rung
completes on the Mac (DEPLOY.md step 4).

**(c) Fallback regression — the strongest rung.** In the (b) drive
the RTC failure was genuine (sandbox-blocked ICE, below), never
simulated: the gateway logged the shim's `session_open` at `/rtc`,
`pc_state=connecting` that never progresses, closed by the shim's
own budget at 7.9 s / 8.7 s; **36 ms later** the bridge session
opened with the `sparta` subprotocol. The control run — pre-shim
`play.js` from git HEAD, everything else identical — produced
**numerically identical bridge counters (21 pkts/360 B, 7 pkts/
274 B) and byte-identical engine console transcripts** (diff
clean, 2,755 B each). Client-half unit proofs (the shim block
lifted verbatim from the built file): signaling refused → fallback
OPEN with CONNECTING-time sends flushed in order and a 1,000-byte
round-trip byte-exact; no `RTCPeerConnection` → straight to
fallback; non-house URL → a genuine WebSocket instance; silent
signaling → fallback opened at 8,004 ms by the budget.

**(d) Signaling design vs the Mac's Tailscale** — verified
read-only (above): `--set-path` exists, the current funnel config
is exactly the one mount the deploy expects to extend, and the
gateway accepts upgrades on any path with its own Origin law, so
no path rewriting is needed at the front.

## The sandbox wall (why (a)-media and (b)-join stop here)

This sandbox's egress policy returns `EPERM` ("Operation not
permitted") for **every send on an unconnected UDP socket**, from
every runtime tried (Python, Node, Go, Chromium), including in a
fresh network namespace. ICE sends its checks from unconnected
sockets — the shipped gateway's own log line is the conviction:
`failed to get server reflexive address udp4
stun:stun.l.google.com:19302: … sendto: operation not permitted`.
The same wall severs the DP server's replies (it answers from an
unconnected socket: `LHNET_Write: sendto returned error:
Operation not permitted`, 12×/20× across the drives). The bridge
works here precisely because it only ever sends on `connect()`ed
sockets — and the gateway was built the same way for its server
leg. Neither block is a defect in the shipped code; both are this
box. The Mac — the actual deployment host, whose NAT passed
`tailscale netcheck` with UDP true and endpoint-independent
mapping — is the first surface where the media path can carry a
byte. DEPLOY.md steps 3–4 are that proof, scripted
(`proof/run-proof.sh` is turnkey).

## Two interpretations, flagged

1. **Fallback is a setup-time act.** The ruling "on ANY failure
   falls back" is implemented for everything up to DataChannel
   open. A failure *after* open closes the socket instead of
   substituting a fresh WebSocket mid-game: a new socket is a new
   server-side session, and the engine would speak mid-game
   datagrams into a connection the server thinks is brand new.
   A close is the honest network-drop behavior; DP surfaces it as
   a disconnect, exactly like a real line failure.
2. **`/rtc` derivation** treats a trailing slash as the path's
   end, so the root door gets exactly `/rtc` (never `//rtc`) and
   `/door` gets `/door/rtc`. Pinned by a unit rung.

## Not proven anywhere yet (and where it becomes provable)

- DataChannel media RTT/loss numbers — Mac, DEPLOY.md step 3.
- A completed join over the DataChannel — Mac, step 4 (with the
  bridge log showing *no* session for that player as the
  counter-proof, and a gateway-killed re-join as the production
  fallback proof).
- WAN behavior (a genuinely remote player's NAT + RTT) — the
  first off-LAN player; STUN-reflexive direct UDP is the design,
  WS remains the fallback for UDP-refusing NATs (no TURN, by
  ruling).
- Safari's DataChannel behavior in this stack — Chase's play;
  the shim's fallback covers a refusal automatically.
