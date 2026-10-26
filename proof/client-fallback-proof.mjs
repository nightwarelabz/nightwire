/* client-fallback-proof — code-path proof (rung c, client half) for
 * the Nexuiz WebRTC transport shim in public/nexuiz/play.js.
 *
 * What it does: lifts the sparta-rtc-shim block VERBATIM out of the
 * built play frame (dist/nexuiz/play.js; PLAY_JS overrides the
 * path), evaluates it in a vm realm whose globals the test deals,
 * and proves three things against a local WebSocket echo server
 * (stdlib only — a minimal handshake/frame implementation below;
 * unfragmented frames, small payloads, proof server not product):
 *
 *   (i)   with a fake RTCPeerConnection present but the signaling
 *         endpoint REFUSING the /rtc upgrade, a shim socket reaches
 *         OPEN via the fallback WebSocket and round-trips bytes —
 *         including a send dealt while still CONNECTING, which the
 *         shim must queue, never drop;
 *   (ii)  with no RTCPeerConnection at all, a shim socket also
 *         falls back and round-trips bytes;
 *   (iii) a construction for any non-house URL returns a genuine
 *         WebSocket (the original constructor's own instance);
 *   (iv)  with the signaling endpoint ACCEPTING but never
 *         answering, the 8000 ms setup budget is what takes the
 *         shim to the fallback road (this rung takes ~8 s).
 *
 * Plus small unit pins: the /rtc derivation and the channel shape
 * the fake peer connection was dealt.
 *
 * The full DataChannel-open proof is NOT here: that needs a real
 * RTCPeerConnection and the sibling gateway (sparta-rtc), and
 * happens against it later. This proof covers the client's half:
 * the attempt is made in the contract's shape, and every failure
 * of it lands on the current road, byte-exact.
 *
 * Run: node client-fallback-proof.mjs        (exit 0 = all pass)
 */
import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import vm from "node:vm";

const PLAY_JS = process.env.PLAY_JS || new URL("../../../rtc-tree/dist/nexuiz/play.js", import.meta.url).pathname;

/* ------------------------------------------------ echo server */
const WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
const refusedPaths = [];
const upgraders = new Set();

function serverFrame(opcode, payload) {
  const len = payload.length;
  let head;
  if (len < 126) { head = Buffer.from([0x80 | opcode, len]); }
  else if (len < 65536) { head = Buffer.alloc(4); head[0] = 0x80 | opcode; head[1] = 126; head.writeUInt16BE(len, 2); }
  else { head = Buffer.alloc(10); head[0] = 0x80 | opcode; head[1] = 127; head.writeBigUInt64BE(BigInt(len), 2); }
  return Buffer.concat([head, payload]);
}

function acceptUpgrade(req, socket) {
  const key = req.headers["sec-websocket-key"];
  if (!key) { socket.destroy(); return false; }
  const accept = crypto.createHash("sha1").update(key + WS_GUID).digest("base64");
  const offered = String(req.headers["sec-websocket-protocol"] || "").split(",").map((s) => s.trim()).filter(Boolean);
  const proto = offered.includes("sparta") ? "sparta" : offered[0];
  let lines = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n";
  if (proto) lines += "Sec-WebSocket-Protocol: " + proto + "\r\n";
  socket.write(lines + "\r\n");
  upgraders.add(socket);
  socket.on("close", () => upgraders.delete(socket));
  socket.on("error", () => {});
  return true;
}
function readFrames(socket, head, onPayload) {
  let buf = head && head.length ? Buffer.from(head) : Buffer.alloc(0);
  socket.on("data", (chunk) => {
    buf = Buffer.concat([buf, chunk]);
    for (;;) {
      if (buf.length < 2) return;
      const opcode = buf[0] & 0x0f;
      const masked = (buf[1] & 0x80) !== 0;
      let len = buf[1] & 0x7f;
      let off = 2;
      if (len === 126) { if (buf.length < 4) return; len = buf.readUInt16BE(2); off = 4; }
      else if (len === 127) { if (buf.length < 10) return; len = Number(buf.readBigUInt64BE(2)); off = 10; }
      const maskAt = off;
      if (masked) off += 4;
      if (buf.length < off + len) return;
      let payload = Buffer.from(buf.subarray(off, off + len));
      if (masked) { const mask = buf.subarray(maskAt, maskAt + 4); for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i & 3]; }
      buf = buf.subarray(off + len);
      if (opcode === 0x8) { try { socket.write(serverFrame(0x8, Buffer.alloc(0))); } catch {} socket.end(); return; }
      if (opcode === 0x9) { socket.write(serverFrame(0xA, payload)); continue; }
      if (opcode === 0x1 || opcode === 0x2) onPayload(opcode, payload);
    }
  });
}

const server = http.createServer((req, res) => { res.writeHead(426); res.end("upgrade required"); });
server.on("upgrade", (req, socket, head) => {
  const path = new URL(req.url, "http://local").pathname;
  if (path === "/rtc") { refusedPaths.push(path); socket.destroy(); return; }
  if (!acceptUpgrade(req, socket)) return;
  readFrames(socket, head, (opcode, payload) => socket.write(serverFrame(opcode, payload)));
});

/* The second door, for rung (iv): signaling is accepted and then
 * met with silence; the game door echoes. */
const silentPaths = [];
const server2 = http.createServer((req, res) => { res.writeHead(426); res.end("upgrade required"); });
server2.on("upgrade", (req, socket, head) => {
  const path = new URL(req.url, "http://local").pathname;
  silentPaths.push(path);
  if (!acceptUpgrade(req, socket)) return;
  readFrames(socket, head, (opcode, payload) => {
    if (path !== "/rtc") socket.write(serverFrame(opcode, payload));
  });
});

/* ------------------------------------------------ shim lift */
function liftShim() {
  const src = fs.readFileSync(PLAY_JS, "utf8");
  const a = src.indexOf("sparta-rtc-shim:start");
  const b = src.indexOf("sparta-rtc-shim:end");
  assert.ok(a >= 0 && b > a, "shim markers must exist in the built play.js");
  const blockStart = src.lastIndexOf("/*", a);
  const blockEnd = src.indexOf("*/", b) + 2;
  const block = src.slice(blockStart, blockEnd);
  for (const needle of ["class NxSpartaSocket", "nxInstallSpartaWebSocket", "maxRetransmits", "stun:stun.l.google.com:19302"]) {
    assert.ok(block.includes(needle), `lifted block must carry: ${needle}`);
  }
  return block;
}

const OriginalWebSocket = globalThis.WebSocket;
assert.equal(typeof OriginalWebSocket, "function", "this Node must deal a global WebSocket client");

function makeRealm(block, rtcCtor) {
  const sandbox = {
    WebSocket: OriginalWebSocket,
    RTCPeerConnection: rtcCtor,
    URL,
    Blob,
    setTimeout,
    clearTimeout,
    console,
  };
  sandbox.globalThis = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(block + "\n;globalThis.__nx = { NxSpartaSocket, nxInstallSpartaWebSocket, nxSpartaSignalUrl, NX_RTC_BUDGET_MS };", sandbox);
  return sandbox;
}

/* A peer connection that negotiates on paper and never opens a
 * channel: enough for the shim to build the channel, write the
 * offer, and then discover the signaling door is shut. */
class FakePC {
  constructor(config) {
    this.config = config;
    this.iceConnectionState = "new";
    this.connectionState = "new";
    FakePC.instances.push(this);
  }
  createDataChannel(label, opts) {
    this.dcLabel = label;
    this.dcOpts = opts;
    this.dc = { readyState: "connecting", bufferedAmount: 0, binaryType: "arraybuffer", onopen: null, onmessage: null, onclose: null, onerror: null, send() {}, close() { this.readyState = "closed"; } };
    return this.dc;
  }
  async createOffer() { return { type: "offer", sdp: "fake-offer-sdp" }; }
  async setLocalDescription(d) { this.localDescription = { type: d.type, sdp: d.sdp }; }
  async setRemoteDescription(d) { this.remoteDescription = d; }
  async addIceCandidate() {}
  close() { this.closed = true; }
}
FakePC.instances = [];

function opened(sock, ms = 6000) {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("socket did not open in time")), ms);
    const prev = sock.onopen;
    sock.onopen = (ev) => { clearTimeout(t); if (typeof prev === "function") prev(ev); resolve(); };
  });
}
function nextMessage(sock, ms = 6000) {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("no message in time")), ms);
    const prev = sock.onmessage;
    sock.onmessage = (ev) => { clearTimeout(t); if (typeof prev === "function") prev(ev); resolve(ev.data); };
  });
}
const bytes = (u8) => [...new Uint8Array(u8 instanceof ArrayBuffer ? u8 : u8.buffer.slice(u8.byteOffset, u8.byteOffset + u8.byteLength))];

await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
await new Promise((resolve) => server2.listen(0, "127.0.0.1", resolve));
const port = server.address().port;
const bridge = `ws://127.0.0.1:${port}`;
const silentBridge = `ws://127.0.0.1:${server2.address().port}`;
const block = liftShim();
const results = [];
try {
  /* ---- unit pins: derivation + budget */
  {
    const realm = makeRealm(block, undefined);
    const nx = realm.__nx;
    assert.equal(nx.nxSpartaSignalUrl("wss://host"), "wss://host/rtc");
    assert.equal(nx.nxSpartaSignalUrl("wss://host/door"), "wss://host/door/rtc");
    assert.equal(nx.nxSpartaSignalUrl("wss://host/door/"), "wss://host/door/rtc");
    assert.equal(nx.NX_RTC_BUDGET_MS, 8000);
    results.push("unit: /rtc derivation (root, path, trailing slash) + 8000 ms budget — pass");
  }

  /* ---- (i) signaling refused -> fallback, bytes round-trip */
  {
    FakePC.instances = [];
    const realm = makeRealm(block, FakePC);
    const nx = realm.__nx;
    nx.nxInstallSpartaWebSocket(bridge);
    const HouseWS = realm.WebSocket;
    assert.notEqual(HouseWS, OriginalWebSocket, "installer must replace the realm's WebSocket");
    const sock = new HouseWS(bridge, ["sparta"]);
    assert.ok(sock instanceof nx.NxSpartaSocket, "house construction must return the shim socket");
    assert.equal(sock.readyState, sock.CONNECTING, "shim must stay CONNECTING until a transport is truly open");
    assert.equal(sock.CONNECTING, 0); assert.equal(sock.OPEN, 1); assert.equal(sock.CLOSING, 2); assert.equal(sock.CLOSED, 3);
    assert.equal(sock.protocol, "sparta");
    const early = new Uint8Array([0, 1, 2, 253, 254, 255]);
    sock.send(early.buffer); /* dealt while CONNECTING: queued by the shim, never dropped */
    const openP = opened(sock);
    const echoP = nextMessage(sock);
    await openP;
    assert.equal(sock.readyState, sock.OPEN);
    assert.deepEqual(bytes(await echoP), [...early], "the CONNECTING-time send must flush, in order, on the fallback");
    const payload = new Uint8Array(1000);
    for (let i = 0; i < payload.length; i++) payload[i] = (i * 7) & 0xff;
    const echo2P = nextMessage(sock);
    sock.send(payload); /* a view, as SOCKFS sends */
    const back = await echo2P;
    assert.ok(back instanceof ArrayBuffer, "onmessage data must be an ArrayBuffer");
    assert.deepEqual(bytes(back), [...payload], "1000-byte round-trip must be byte-exact");
    sock.close();
    assert.equal(sock.readyState, sock.CLOSED);
    /* the attempt really was made, in the contract's shape */
    assert.equal(FakePC.instances.length, 1, "exactly one RTC attempt");
    const pc = FakePC.instances[0];
    assert.equal(pc.dcLabel, "sparta");
    assert.equal(pc.dcOpts.ordered, false);
    assert.equal(pc.dcOpts.maxRetransmits, 0);
    assert.ok(JSON.stringify(pc.config).includes("stun:stun.l.google.com:19302"), "STUN server must be the house one");
    assert.equal(pc.closed, true, "the failed RTC attempt must be torn down at the switch");
    assert.ok(refusedPaths.includes("/rtc"), "the signaling upgrade must have been refused at /rtc");
    results.push("(i) signaling refused -> OPEN via fallback, CONNECTING-time send flushed, 1000-byte view round-trip byte-exact — pass");
  }

  /* ---- (ii) no RTCPeerConnection -> straight to fallback */
  {
    FakePC.instances = [];
    const realm = makeRealm(block, undefined);
    const nx = realm.__nx;
    nx.nxInstallSpartaWebSocket(bridge);
    const sock = new realm.WebSocket(bridge, ["sparta"]);
    assert.ok(sock instanceof nx.NxSpartaSocket);
    const openP = opened(sock);
    await openP;
    const msg = new Uint8Array([9, 8, 7, 6, 5]);
    const echoP = nextMessage(sock);
    sock.send(msg.buffer);
    assert.deepEqual(bytes(await echoP), [...msg]);
    sock.close();
    assert.equal(FakePC.instances.length, 0, "no RTC attempt may be made without RTCPeerConnection");
    results.push("(ii) RTCPeerConnection undefined -> OPEN via fallback, bytes round-trip — pass");
  }

  /* ---- (iii) non-house URL -> a genuine WebSocket */
  {
    const realm = makeRealm(block, FakePC);
    const nx = realm.__nx;
    nx.nxInstallSpartaWebSocket(bridge);
    const other = `${bridge}/other`;
    const sock = new realm.WebSocket(other, ["sparta"]);
    assert.ok(sock instanceof OriginalWebSocket, "non-house construction must be a genuine WebSocket instance");
    assert.ok(!(sock instanceof nx.NxSpartaSocket), "non-house construction must not be the shim");
    assert.equal(Object.getPrototypeOf(sock), OriginalWebSocket.prototype);
    sock.binaryType = "arraybuffer";
    const openP = opened(sock);
    await openP;
    const msg = new Uint8Array([42, 42, 42]);
    const echoP = nextMessage(sock);
    sock.send(msg.buffer);
    assert.deepEqual(bytes(await echoP), [...msg], "the genuine socket must behave untouched (echo round-trip)");
    sock.close();
    results.push("(iii) non-house URL -> genuine WebSocket instance (original prototype), untouched echo — pass");
  }
  /* ---- (iv) signaling silent -> the budget takes the other road */
  {
    FakePC.instances = [];
    silentPaths.length = 0;
    const realm = makeRealm(block, FakePC);
    const nx = realm.__nx;
    nx.nxInstallSpartaWebSocket(silentBridge);
    const t0 = Date.now();
    const sock = new realm.WebSocket(silentBridge, ["sparta"]);
    assert.ok(sock instanceof nx.NxSpartaSocket);
    await opened(sock, 12000);
    const elapsed = Date.now() - t0;
    assert.ok(elapsed >= 7900 && elapsed < 11000, `the budget, not a signal, must open this socket — got ${elapsed} ms`);
    assert.deepEqual(silentPaths, ["/rtc", "/"], "signaling first, the fallback second, in that order");
    const msg = new Uint8Array([1, 2, 3]);
    const echoP = nextMessage(sock);
    sock.send(msg.buffer);
    assert.deepEqual(bytes(await echoP), [...msg]);
    sock.close();
    results.push(`(iv) signaling accepted but silent -> budget expiry at ${elapsed} ms -> OPEN via fallback, bytes round-trip — pass`);
  }
} finally {
  for (const s of upgraders) { try { s.destroy(); } catch {} }
  await new Promise((resolve) => server.close(resolve));
  await new Promise((resolve) => server2.close(resolve));
}
console.log(`client-fallback-proof — play frame: ${PLAY_JS}`);
for (const line of results) console.log("  " + line);
console.log("client-fallback-proof: ALL PASS");
