#!/usr/bin/env python3
"""Lab only: a canary listener for the Secure VM fence tests.

It listens on every --tcp and --udp address given, and writes one JSON line per
arrival to --log: the time, the protocol, the local address, the peer and the
first bytes received. The probe in the guest sends a random token, so a line
that carries the step's token is proof that the guest reached this address.
Nothing else counts as an arrival.

Usage:
  canary.py --log arrivals.jsonl --tcp 0.0.0.0:47001 --tcp 127.0.0.1:47002 \
            --udp 0.0.0.0:47001 --tcp '[::]:47003'

Run it with -I so it never imports from the current directory.
"""
import argparse
import json
import selectors
import socket
import sys
import time


def parse(addr):
    host, _, port = addr.rpartition(":")
    host = host.strip("[]")
    return host, int(port)


def family(host):
    return socket.AF_INET6 if ":" in host else socket.AF_INET


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--log", required=True)
    ap.add_argument("--tcp", action="append", default=[])
    ap.add_argument("--udp", action="append", default=[])
    a = ap.parse_args()

    sel = selectors.DefaultSelector()
    for spec in a.tcp:
        host, port = parse(spec)
        s = socket.socket(family(host), socket.SOCK_STREAM)
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        if family(host) == socket.AF_INET6:
            s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
        s.bind((host, port))
        s.listen(64)
        s.setblocking(False)
        sel.register(s, selectors.EVENT_READ, ("tcp-listen", spec))
    for spec in a.udp:
        host, port = parse(spec)
        s = socket.socket(family(host), socket.SOCK_DGRAM)
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        if family(host) == socket.AF_INET6:
            s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
        s.bind((host, port))
        s.setblocking(False)
        sel.register(s, selectors.EVENT_READ, ("udp", spec))

    log = open(a.log, "a", buffering=1)

    def write(proto, spec, peer, data):
        log.write(json.dumps({
            "ts": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "proto": proto, "bind": spec, "peer": str(peer[0]),
            "data": data[:96].decode("utf-8", "replace").strip(),
        }) + "\n")

    # A TCP token can arrive in pieces, so each connection collects bytes until a
    # newline, EOF, 256 bytes or IDLE seconds, then logs what it has. At most
    # MAX_CONN connections are open; the rest are logged and closed at once.
    IDLE, MAX_CONN = 3.0, 256
    conns = {}

    def finish(c):
        spec, peer, _t0, buf = conns.pop(c)
        write("tcp", spec, peer, bytes(buf) or b"<connected, no data>")
        sel.unregister(c)
        c.close()

    print("canary up:", a.tcp, a.udp, file=sys.stderr, flush=True)
    while True:
        for key, _ in sel.select(timeout=1.0):
            kind, spec = key.data
            if kind == "tcp-listen":
                try:
                    c, peer = key.fileobj.accept()
                except OSError:
                    continue
                if len(conns) >= MAX_CONN:
                    write("tcp", spec, peer, b"<over the connection cap, closed>")
                    c.close()
                    continue
                c.setblocking(False)
                conns[c] = (spec, peer, time.time(), bytearray())
                sel.register(c, selectors.EVENT_READ, ("tcp-conn", spec))
            elif kind == "tcp-conn":
                c = key.fileobj
                try:
                    data = c.recv(256)
                except OSError:
                    data = b""
                buf = conns[c][3]
                buf += data
                if not data or b"\n" in buf or len(buf) >= 256:
                    finish(c)
            else:
                try:
                    data, peer = key.fileobj.recvfrom(512)
                except OSError:
                    continue
                write("udp", spec, peer, data)
        now = time.time()
        for c in [c for c, v in conns.items() if now - v[2] > IDLE]:
            finish(c)


if __name__ == "__main__":
    main()
