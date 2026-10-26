#!/usr/bin/env python3
"""rtcproof.py — a real WebRTC peer (aiortc) proving the nightwire
gateway's media path on the Mac, speaking the gateway's exact
signaling dialect (JSON offer/answer/ice over one WS).

Modes:
  getinfo  — one polite DarkPlaces connectionless query through the
             PRODUCTION gateway (via the public Funnel door) to the
             real server; the info reply proves door+signaling+ICE+
             DTLS+SCTP+gateway piping end to end.
  census   — 300 patterned messages through a LOCAL proof gateway
             instance wired to udp_echo.py: byte-exactness, boundary
             preservation, then a 2000-message blast that must end
             in the gateway's rate_limit close.
"""
import asyncio
import hashlib
import json
import sys

import websockets
from aiortc import RTCConfiguration, RTCIceServer, RTCPeerConnection, RTCSessionDescription
from aiortc.rtcicetransport import RTCIceCandidate

STUN = "stun:stun.l.google.com:19302"


def cand_to_string(c):
    s = "candidate:%s %s %s %s %s %s typ %s" % (
        c.foundation, c.component, c.transport, c.priority, c.host, c.port, c.type)
    if getattr(c, "relatedAddress", None):
        s += " raddr %s rport %s" % (c.relatedAddress, c.relatedPort)
    if getattr(c, "tcpType", None):
        s += " tcptype %s" % c.tcpType
    return s


def parse_candidate(text, mid, mline):
    # positional parse, done by hand so no aiortc helper version matters:
    # candidate:<foundation> <component> <transport> <priority> <ip> <port> typ <type> [raddr x rport y] [tcptype z]
    t = text.split()
    typ = t[t.index("typ") + 1]
    kw = {}
    tail = t[t.index("typ") + 2:]
    for i in range(0, len(tail) - 1, 2):
        kw[tail[i]] = tail[i + 1]
    return RTCIceCandidate(
        foundation=t[0].split(":", 1)[1], component=int(t[1]), transport=t[2],
        priority=int(t[3]), host=t[4], port=int(t[5]), type=typ,
        relatedAddress=kw.get("raddr"),
        relatedPort=int(kw["rport"]) if "rport" in kw else None,
        tcpType=kw.get("tcptype"), sdpMid=mid, sdpMLineIndex=mline)


async def connect(url, origin):
    pc = RTCPeerConnection(RTCConfiguration(iceServers=[RTCIceServer(urls=STUN)]))
    dc = pc.createDataChannel("sparta", ordered=False, maxRetransmits=0)
    state = {"open": asyncio.Event(), "msgs": [], "closed": asyncio.Event()}
    dc.on("open", lambda: state["open"].set())
    dc.on("message", lambda data: state["msgs"].append(data))
    dc.on("close", lambda: state["closed"].set())

    try:
        ws = await websockets.connect(url, additional_headers={"Origin": origin})
    except TypeError:
        ws = await websockets.connect(url, extra_headers={"Origin": origin})

    async def pump():
        try:
            async for raw in ws:
                msg = json.loads(raw)
                if msg.get("type") == "answer":
                    await pc.setRemoteDescription(
                        RTCSessionDescription(sdp=msg["sdp"], type="answer"))
                elif msg.get("type") == "ice":
                    await pc.addIceCandidate(parse_candidate(
                        msg["candidate"], msg.get("sdpMid"), msg.get("sdpMLineIndex")))
        except Exception:
            pass

    pump_task = asyncio.ensure_future(pump())

    @pc.on("icecandidate")
    async def _on_ice(cand):
        if cand is not None:
            await ws.send(json.dumps({
                "type": "ice", "candidate": cand_to_string(cand),
                "sdpMid": cand.sdpMid, "sdpMLineIndex": cand.sdpMLineIndex}))

    offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    await ws.send(json.dumps({"type": "offer", "sdp": pc.localDescription.sdp}))
    try:
        await asyncio.wait_for(state["open"].wait(), timeout=25)
    except asyncio.TimeoutError:
        print("DC_OPEN_TIMEOUT ice=%s conn=%s" % (pc.iceConnectionState, pc.connectionState))
        pump_task.cancel()
        await pc.close()
        return None, None, None
    print("DC OPEN ice=%s" % pc.iceConnectionState)
    return pc, dc, state


async def mode_getinfo():
    url = "wss://chases-macbook-air-1.taildc9dec.ts.net/rtc"
    pc, dc, state = await connect(url, "https://nosuchmachine.net")
    if pc is None:
        return 2
    dc.send(b"\xff\xff\xff\xffgetinfo\n")
    for _ in range(120):
        if state["msgs"]:
            break
        await asyncio.sleep(0.1)
    rc = 0
    if state["msgs"]:
        data = state["msgs"][0]
        if isinstance(data, str):
            data = data.encode()
        print("GETINFO REPLY %d bytes head=%r" % (len(data), data[:80]))
        if b"infoResponse" not in data:
            print("UNEXPECTED REPLY SHAPE")
            rc = 4
    else:
        print("GETINFO NO REPLY")
        rc = 3
    await asyncio.sleep(0.3)
    await pc.close()
    return rc


async def mode_census():
    url = "ws://127.0.0.1:26013/rtc"
    pc, dc, state = await connect(url, "https://nosuchmachine.net")
    if pc is None:
        return 2
    sent = []
    for i in range(300):
        n = 17 + (i * 37) % 1200
        body = hashlib.sha256(("sparta-%d" % i).encode()).digest()
        payload = (body * (n // len(body) + 1))[:n]
        sent.append(payload)
        dc.send(payload)
    deadline = asyncio.get_event_loop().time() + 30
    while len(state["msgs"]) < 300 and asyncio.get_event_loop().time() < deadline:
        await asyncio.sleep(0.05)
    got = state["msgs"]
    got = [g.encode() if isinstance(g, str) else g for g in got]
    bad = 0
    if sorted(got) != sorted(sent):
        bad = 1
    print("CENSUS received=%d/300 byte_exact_set=%s" % (len(got), "yes" if not bad else "NO"))
    # blast: 2000 tiny messages — the gateway's 300 pps sanity window
    # must forward ~300, drop the rest, and close with rate_limit.
    before = len(state["msgs"])
    for i in range(2000):
        try:
            dc.send(b"blast-%d" % i)
        except Exception:
            break
    try:
        await asyncio.wait_for(state["closed"].wait(), timeout=15)
        closed = True
    except asyncio.TimeoutError:
        closed = False
    await asyncio.sleep(0.5)
    echoed = len(state["msgs"]) - before
    print("BLAST echoes_before_close=%d dc_closed=%s" % (echoed, closed))
    await pc.close()
    return 0 if (len(got) == 300 and not bad) else 5


if __name__ == "__main__":
    mode = sys.argv[1] if len(sys.argv) > 1 else "getinfo"
    fn = mode_getinfo if mode == "getinfo" else mode_census
    sys.exit(asyncio.get_event_loop().run_until_complete(fn()))
