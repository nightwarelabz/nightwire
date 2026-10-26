#!/usr/bin/env python3
"""probe.py — two sandbox-law checks, stdlib only.
1. Unconnected UDP send (the ICE/server-reply shape) -> expect EPERM.
2. Through the real bridge: WS session (sparta subprotocol) carrying a
   DP connectionless getinfo to the real DP server; report whether any
   reply datagram comes back, plus the raw outcome.
"""
import socket, sys, time, base64, os, struct

print("== check 1: unconnected UDP send ==")
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
try:
    s.sendto(b"\xff\xff\xff\xffgetinfo", ("127.0.0.1", 26000))
    print("unconnected sendto: SUCCEEDED (no EPERM)")
except OSError as e:
    print(f"unconnected sendto: OSError errno={e.errno} {e.strerror}")
s.close()
c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
c.connect(("127.0.0.1", 26000))
try:
    c.send(b"\xff\xff\xff\xffgetinfo")
    print("connected send: SUCCEEDED")
except OSError as e:
    print(f"connected send: OSError errno={e.errno} {e.strerror}")
c.close()

print("== check 2: getinfo through bridge ws://127.0.0.1:26002 (door) ==")
# Minimal RFC6455 client.
sock = socket.create_connection(("127.0.0.1", 26002), timeout=10)
key = base64.b64encode(os.urandom(16)).decode()
req = ("GET / HTTP/1.1\r\nHost: 127.0.0.1:26002\r\nUpgrade: websocket\r\n"
       "Connection: Upgrade\r\nSec-WebSocket-Key: %s\r\n"
       "Sec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: sparta\r\n\r\n" % key)
sock.sendall(req.encode())
head = b""
while b"\r\n\r\n" not in head:
    chunk = sock.recv(4096)
    if not chunk: break
    head += chunk
print("handshake:", head.split(b"\r\n")[0].decode("latin1"),
      "| protocol:", [l for l in head.decode("latin1").split("\r\n") if "rotocol" in l])
rest = head.split(b"\r\n\r\n", 1)[1] if b"\r\n\r\n" in head else b""

def ws_send(sock, payload):
    mask = os.urandom(4)
    n = len(payload)
    hdr = bytearray([0x82])
    if n < 126: hdr.append(0x80 | n)
    elif n < 65536: hdr.append(0x80 | 126); hdr += struct.pack(">H", n)
    else: hdr.append(0x80 | 127); hdr += struct.pack(">Q", n)
    masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
    sock.sendall(bytes(hdr) + mask + masked)

def ws_recv_all(sock, seconds):
    sock.settimeout(seconds)
    got = []
    buf = rest
    end = time.time() + seconds
    while time.time() < end:
        try:
            chunk = sock.recv(65536)
            if not chunk: break
            buf += chunk
        except socket.timeout:
            break
        while len(buf) >= 2:
            op = buf[0] & 0x0F; ln = buf[1] & 0x7F; off = 2
            if ln == 126: ln = struct.unpack(">H", buf[2:4])[0]; off = 4
            elif ln == 127: ln = struct.unpack(">Q", buf[2:10])[0]; off = 10
            if len(buf) < off + ln: break
            got.append((op, buf[off:off+ln])); buf = buf[off+ln:]
    return got

ws_send(sock, b"\xff\xff\xff\xffgetinfo 0")
frames = ws_recv_all(sock, 5)
print(f"frames back in 5s: {len(frames)}")
for op, data in frames:
    print(f"  op={op} len={len(data)} head={data[:80]!r}")
sock.close()
print("probe done")
