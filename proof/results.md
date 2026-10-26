# sparta-rtc — rung-(a) lab proof results

All on one loopback host (the sandbox VM). The RTT comparison therefore measures framing/queueing overhead of the two transports, **not WAN physics**.

## The sandbox blocks WebRTC media — measured, not assumed

This sandbox's egress policy returns `EPERM` ("Operation not permitted") for **every send on an unconnected UDP socket**, from every runtime tried (Python, Node, Go/pion, Chromium). Only connect()ed UDP sockets may send (which is why the bridge, a connected-socket design, works here). ICE sends its STUN connectivity checks from unconnected sockets, so no WebRTC media path — pion↔pion, pion↔Chromium, UDP or ICE-TCP (whose non-loopback dials are additionally redirected to the egress proxy and reset) — can be established inside this sandbox. Verbatim from the shipped gateway binary's own log during the browser drive (94 EPERM lines in total; ICE lines shown):

```
ice WARNING: 2026/10/26 04:37:03 failed to get server reflexive address udp4 stun:stun.l.google.com:19302: write udp4 0.0.0.0:26158->198.18.2.11:19302: sendto: operation not permitted
ice WARNING: 2026/10/26 04:37:21 failed to get server reflexive address udp4 stun:stun.l.google.com:19302: write udp4 0.0.0.0:26125->198.18.2.11:19302: sendto: operation not permitted
```

Consequence: the DataChannel workload, its RTT percentiles, and the gateway's end-to-end `rate_limit` close are proven on the first machine without this filter (the Mac), not here. Everything that does not need ICE media is proven below, in a real headless Chromium against the real binaries. The gateway's rate-window arithmetic and registry cap are additionally pinned by Go unit tests (`gateway_test.go`, run output in `out/go-test.log`).

## RTC path — signaling in real Chromium (gateway on :26003)

- answer received by the page: **True**
- ICE candidates: page gathered 2, gateway trickled 2
- DataChannel opened: **False** (ice=checking, conn=connecting — expected in this sandbox, per above)
- gateway session_open lines: 8

```
2026-10-26T04:17:09.278Z session_open session=1 peer=127.0.0.1:58762 origin=null path=/rtc subprotocol=-
2026-10-26T04:27:39.202Z session_open session=1 peer=127.0.0.1:56912 origin=null path=/rtc subprotocol=-
```

## WS path — same workload through the real sparta-bridge.py (:26011)

- messages: sent 300, received 300, lost 0
- bytes: sent 795876, received 795876
- checksum failures: 0, byte-equality failures: 0, boundary failures: 0, unexpected: 0
- RTT ms: mean 0.85, p50 0.5, p95 2.5, p99 3.6, max 7.1 (n=300)
- workload end: complete

Payload sizes cycled 1, 16, 64, 200, 850, 1200, 1400, 4000, 16384 bytes (self-describing seq+len+crc32 for >=12-byte payloads; smaller ones verified against the sender's retained copy). Boundary = each echo is exactly one sent payload.

## Guards

- Origin law (raw upgrades vs the gateway): foreign Origin -> **403** (want 403); absent Origin -> 101 (want 101); site Origin -> 101 (want 101); site Origin on an arbitrary path -> 101 (want 101, any-path acceptance for the /rtc Funnel mount)
- Session cap (gateway instance with --max-sessions 2): upgrades 1/2 -> 101/101 (want 101/101), third -> **503** (want 503)
- Rate limit, house semantics end-to-end on the bridge: blast 2000 msgs -> the bridge forwarded exactly 300 (the 1 s window), dropped the next 10, and closed the session `rate_limit` (its log below is ground truth; it sends close 1008 — the page observed 1006, the abnormal-closure code, because the socket is torn down with the client's remaining blast still unread). Echoes the page received before the close: 197. Bridge log:

```
2026-10-26T04:28:54.266Z session_close session=2 peer=127.0.0.1:59146 reason=rate_limit seconds=0.0 to_server_pkts=300 to_server_bytes=9600 to_client_pkts=262 to_client_bytes=8384 rate_drops=10 udp_errors=0
2026-10-26T04:33:51.508Z session_close session=2 peer=127.0.0.1:53558 reason=rate_limit seconds=0.0 to_server_pkts=300 to_server_bytes=9600 to_client_pkts=202 to_client_bytes=6464 rate_drops=10 udp_errors=0
2026-10-26T04:37:21.868Z session_close session=2 peer=127.0.0.1:50574 reason=rate_limit seconds=0.0 to_server_pkts=300 to_server_bytes=9600 to_client_pkts=197 to_client_bytes=6304 rate_drops=10 udp_errors=0
```
- Rate limit on the gateway itself: not runnable in this sandbox (DataChannel cannot open; rtcBurst dcOpened=False). The gateway's window/drop/close logic is the bridge's semantics ported line-for-line and pinned by `TestRateWindow` / `TestRegistryCap` / `TestOriginLawAndCap` in gateway_test.go:

```
2026-10-26T04:39:55.990Z session_open session=1 peer=127.0.0.1:49412 origin=- path=/rtc subprotocol=-
2026-10-26T04:39:55.990Z reject_cap peer=127.0.0.1:49422 sessions=1
2026-10-26T04:39:55.990Z session_close session=1 peer=127.0.0.1:49412 reason=eof seconds=0.0 to_server_pkts=0 to_server_bytes=0 to_client_pkts=0 to_client_bytes=0 rate_drops=0 udp_errors=0
--- PASS: TestOriginLawAndCap (0.00s)
PASS
ok  	sparta-rtc	0.011s
```

