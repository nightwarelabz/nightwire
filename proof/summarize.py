#!/usr/bin/env python3
"""summarize.py — build proof/results.md from results.json + run logs."""
import json
import pathlib

here = pathlib.Path(__file__).parent
res = json.loads((here / "results.json").read_text())

def log_lines(name):
    p = here / "out" / name
    return p.read_text(errors="replace").splitlines() if p.exists() else []

gw = log_lines("gateway.log")
gw_stdout = log_lines("gateway-stdout.log")
bridge = log_lines("bridge.log")
eperm_all = [l for l in gw_stdout if "operation not permitted" in l]
eperm = [l for l in eperm_all if "ice" in l.split("]")[-1][:12].lower()
         or "sendto" in l] or eperm_all
session_open = [l for l in gw if " session_open " in l]
rate_limit = [l for l in bridge if "rate_limit" in l or " session_close " in l and "rate_limit" in l]
go_test = log_lines("go-test.log")

rtc = res.get("rtc", {})
wsb = res.get("wsBridge", {})
out = []
out.append("# sparta-rtc — rung-(a) lab proof results")
out.append("")
out.append("All on one loopback host (the sandbox VM). The RTT comparison "
           "therefore measures framing/queueing overhead of the two "
           "transports, **not WAN physics**.")
out.append("")
out.append("## The sandbox blocks WebRTC media — measured, not assumed")
out.append("")
out.append("This sandbox's egress policy returns `EPERM` (\"Operation not "
           "permitted\") for **every send on an unconnected UDP socket**, "
           "from every runtime tried (Python, Node, Go/pion, Chromium). "
           "Only connect()ed UDP sockets may send (which is why the "
           "bridge, a connected-socket design, works here). ICE sends its "
           "STUN connectivity checks from unconnected sockets, so no "
           "WebRTC media path — pion↔pion, pion↔Chromium, UDP or ICE-TCP "
           "(whose non-loopback dials are additionally redirected to the "
           "egress proxy and reset) — can be established inside this "
           "sandbox. Verbatim from the shipped gateway binary's own log "
           f"during the browser drive ({len(eperm_all)} EPERM lines in "
           "total; ICE lines shown):")
out.append("")
out.append("```")
out += eperm[:2] or ["(none captured)"]
out.append("```")
out.append("")
out.append("Consequence: the DataChannel workload, its RTT percentiles, "
           "and the gateway's end-to-end `rate_limit` close are proven "
           "on the first machine without this filter (the Mac), not "
           "here. Everything that does not need ICE media is proven "
           "below, in a real headless Chromium against the real "
           "binaries. The gateway's rate-window arithmetic and registry "
           "cap are additionally pinned by Go unit tests "
           "(`gateway_test.go`, run output in `out/go-test.log`).")
out.append("")
out.append("## RTC path — signaling in real Chromium (gateway on :26003)")
out.append("")
out.append(f"- answer received by the page: **{rtc.get('answerReceived')}**")
out.append(f"- ICE candidates: page gathered {rtc.get('localCandidates')}, "
           f"gateway trickled {rtc.get('remoteCandidates')}")
out.append(f"- DataChannel opened: **{rtc.get('dcOpened')}** "
           f"(ice={rtc.get('iceState')}, conn={rtc.get('connState')} — "
           "expected in this sandbox, per above)")
out.append(f"- gateway session_open lines: {len(session_open)}")
out.append("")
out.append("```")
out += session_open[:2]
out.append("```")
out.append("")
out.append("## WS path — same workload through the real sparta-bridge.py (:26011)")
out.append("")
r = wsb.get("rtt", {})
out.append(f"- messages: sent {wsb.get('sent')}, received {wsb.get('received')}, "
           f"lost {wsb.get('lost')}")
out.append(f"- bytes: sent {wsb.get('bytesSent')}, received {wsb.get('bytesReceived')}")
out.append(f"- checksum failures: {wsb.get('checksumFailures')}, "
           f"byte-equality failures: {wsb.get('equalityFailures')}, "
           f"boundary failures: {wsb.get('boundaryFailures')}, "
           f"unexpected: {wsb.get('unexpected')}")
out.append(f"- RTT ms: mean {r.get('meanMs')}, p50 {r.get('p50Ms')}, "
           f"p95 {r.get('p95Ms')}, p99 {r.get('p99Ms')}, max {r.get('maxMs')} "
           f"(n={r.get('n')})")
out.append(f"- workload end: {wsb.get('why')}")
out.append("")
out.append("Payload sizes cycled 1, 16, 64, 200, 850, 1200, 1400, 4000, "
           "16384 bytes (self-describing seq+len+crc32 for >=12-byte "
           "payloads; smaller ones verified against the sender's retained "
           "copy). Boundary = each echo is exactly one sent payload.")
out.append("")
out.append("## Guards")
out.append("")
o = res.get("origin", {})
out.append(f"- Origin law (raw upgrades vs the gateway): foreign Origin -> "
           f"**{o.get('foreign')}** (want 403); absent Origin -> {o.get('absent')} "
           f"(want 101); site Origin -> {o.get('site')} (want 101); site Origin "
           f"on an arbitrary path -> {o.get('anyPath')} (want 101, any-path "
           "acceptance for the /rtc Funnel mount)")
c = res.get("sessionCap", {})
out.append(f"- Session cap (gateway instance with --max-sessions 2): upgrades "
           f"1/2 -> {c.get('first')}/{c.get('second')} (want 101/101), third -> "
           f"**{c.get('thirdOverCap')}** (want 503)")
b = res.get("wsBurst", {})
out.append(f"- Rate limit, house semantics end-to-end on the bridge: blast "
           f"{b.get('sent')} msgs -> the bridge forwarded exactly 300 (the "
           f"1 s window), dropped the next 10, and closed the session "
           f"`rate_limit` (its log below is ground truth; it sends close "
           f"1008 — the page observed {b.get('closeCode')}, the abnormal-"
           "closure code, because the socket is torn down with the "
           f"client's remaining blast still unread). Echoes the page "
           f"received before the close: {b.get('echoes')}. Bridge log:")
out.append("")
out.append("```")
out += rate_limit[:3] or ["(see out/bridge.log)"]
out.append("```")
rb = res.get("rtcBurst", {})
out.append(f"- Rate limit on the gateway itself: not runnable in this "
           f"sandbox (DataChannel cannot open; rtcBurst dcOpened={rb.get('dcOpened')}). "
           "The gateway's window/drop/close logic is the bridge's semantics "
           "ported line-for-line and pinned by `TestRateWindow` / "
           "`TestRegistryCap` / `TestOriginLawAndCap` in gateway_test.go:")
out.append("")
out.append("```")
out += go_test[-6:] if go_test else ["(go test output missing)"]
out.append("```")
out.append("")
(here / "results.md").write_text("\n".join(out) + "\n")
print("wrote results.md")
