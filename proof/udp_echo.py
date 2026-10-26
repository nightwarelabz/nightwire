#!/usr/bin/env python3
"""udp_echo.py — UDP echo sink standing in for the DarkPlaces server
during the rung-(a) lab proof. Every datagram is sent back to its
sender verbatim, one datagram in == one datagram out.

Sandbox quirk (measured, see results.md): this box's egress policy
returns EPERM for every send on an *unconnected* UDP socket; only
connect()ed sockets may send. So the sink keeps one socket per
client: bound (SO_REUSEADDR) to the listen port so replies carry the
listen port as their source, and connect()ed to the client so sends
are legal. The listener itself only ever receives first-contact
datagrams; a client's later datagrams arrive on its connected socket
(the kernel prefers the exact match), so the loop selects over all.
"""
import select
import socket
import sys

port = int(sys.argv[1]) if len(sys.argv) > 1 else 26099
def bigbuf(s):
    s.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 4 * 1024 * 1024)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_SNDBUF, 4 * 1024 * 1024)

listener = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
if hasattr(socket, "SO_REUSEPORT"):
    # Darwin requires REUSEPORT on every socket sharing the port;
    # Linux's REUSEADDR alone sufficed, which hid this in the lab.
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEPORT, 1)
bigbuf(listener)
listener.bind(("127.0.0.1", port))
listener.setblocking(False)
clients = {}  # sender addr -> connected socket bound to `port`
print(f"udp_echo listening on 127.0.0.1:{port}", flush=True)
n = 0
while True:
    socks = [listener] + [c for c in clients.values() if c is not None]
    readable, _, _ = select.select(socks, [], [], 5)
    for s in readable:
        # Drain everything queued on this socket before moving on.
        while True:
            try:
                if s is listener:
                    data, addr = s.recvfrom(65535)
                    if addr not in clients:
                        try:
                            c = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                            c.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                            if hasattr(socket, "SO_REUSEPORT"):
                                c.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEPORT, 1)
                            bigbuf(c)
                            c.bind(("127.0.0.1", port))
                            c.connect(addr)
                            c.setblocking(False)
                            clients[addr] = c
                        except OSError:
                            # Darwin still refuses the shared bind on
                            # some builds; there unconnected sends are
                            # legal, so answer from the listener itself
                            # (None marks listener-direct clients).
                            clients[addr] = None
                    reply = clients[addr]
                else:
                    data = s.recv(65535)
                    reply = s
            except BlockingIOError:
                break
            if not data:
                break
            n += 1
            if reply is None:
                listener.sendto(data, addr)
            else:
                reply.send(data)
            if n % 500 == 0:
                print(f"udp_echo echoed {n}", flush=True)
