// drive-proof.mjs — rung-(a) lab proof driver for sparta-rtc.
//
// Follows the project rig pattern (~/workspace/nexuiz-research/rig/):
// spawn headless Chromium, drive it over CDP with Runtime.evaluate.
// The page (proof-page.html, loaded via file:// like the rig drives;
// its sockets carry the literal Origin "null" — the serialized opaque
// origin — which the proof instances explicitly allow) runs the SAME
// echo workload over (1) the RTCDataChannel path through sparta-rtc and
// (2) the WebSocket path through the real sparta-bridge.py, both
// fronting the same UDP echo sink. Also smokes the guards: a >300pps
// burst (rate_limit close) and foreign/absent/allowed Origin signaling
// attempts via raw sockets. Writes results.json beside this file.
//
// Note: everything is one loopback host — the RTT comparison measures
// framing/queueing overhead of the two transports, not WAN physics.
import { spawn } from "node:child_process";
import { writeFileSync, mkdirSync, existsSync, readFileSync } from "node:fs";
import net from "node:net";
import { validateResults } from "./validate-results.mjs";

const OUT = new URL(".", import.meta.url).pathname;
mkdirSync(OUT + "out", { recursive: true });
const PAGE_URL = process.env.PROOF_PAGE_URL || ("file://" + OUT + "proof-page.html");
const RTC_SIG = process.env.PROOF_RTC_SIG || "ws://127.0.0.1:26003/rtc";
const RTC_PORT = Number(new URL(RTC_SIG.replace("ws://", "http://")).port || 26003);
const BRIDGE_WS = process.env.PROOF_BRIDGE_WS || "ws://127.0.0.1:26011/";
const TOTAL = Number(process.env.PROOF_TOTAL || 300);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const t0 = Date.now();
const lap = (m) => console.log(`[+${((Date.now() - t0) / 1000).toFixed(0)}s]`, m);

function pct(sorted, p) {
  if (!sorted.length) return null;
  const i = Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1);
  return sorted[Math.max(0, i)];
}
function stats(rtts) {
  const s = [...rtts].sort((a, b) => a - b);
  const mean = s.length ? s.reduce((a, b) => a + b, 0) / s.length : null;
  const r2 = (x) => (x === null ? null : Math.round(x * 100) / 100);
  return { n: s.length, meanMs: r2(mean), p50Ms: r2(pct(s, 50)), p95Ms: r2(pct(s, 95)),
           p99Ms: r2(pct(s, 99)), maxMs: r2(s[s.length - 1]) };
}

// Raw WS-upgrade probe for the Origin law (no browser involved).
function rawUpgrade(port, path, origin) {
  return new Promise((resolve) => {
    const sock = net.connect(port, "127.0.0.1");
    let buf = "";
    const key = Buffer.from(Array.from({ length: 16 }, () => Math.floor(Math.random() * 256))).toString("base64");
    sock.on("connect", () => {
      const lines = [
        `GET ${path} HTTP/1.1`, `Host: 127.0.0.1:${port}`,
        "Upgrade: websocket", "Connection: Upgrade",
        `Sec-WebSocket-Key: ${key}`, "Sec-WebSocket-Version: 13",
      ];
      if (origin !== null) lines.push(`Origin: ${origin}`);
      sock.write(lines.join("\r\n") + "\r\n\r\n");
    });
    sock.on("data", (d) => {
      buf += d.toString("latin1");
      const status = Number((buf.split("\r\n")[0] || "").split(" ")[1]);
      if (status) { sock.destroy(); resolve(status); }
    });
    sock.on("error", () => resolve(-1));
    setTimeout(() => { sock.destroy(); resolve(-2); }, 5000);
  });
}

// ---------------------------------------------------------------- chromium
const PORT = 9500 + Math.floor(Math.random() * 400);
const chromeBin = process.env.CHROME_BIN || [
  "/opt/meta-chromium/chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser",
  "/usr/bin/google-chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
].find(existsSync);
if (!chromeBin) throw new Error("Chromium not found; set CHROME_BIN to its executable path");
const chrome = spawn(chromeBin, [
  "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
  "--no-proxy-server",
  "--disable-features=LocalNetworkAccessChecks,BlockInsecurePrivateNetworkRequests",
  `--remote-debugging-port=${PORT}`, "--remote-debugging-address=127.0.0.1",
  `--user-data-dir=/tmp/sparta-proof-profile-${Date.now()}`,
  "--window-size=900,700", "--mute-audio", "about:blank",
], { stdio: "ignore" });

async function waitVersion() {
  for (let i = 0; i < 60; i++) {
    try { const r = await fetch(`http://127.0.0.1:${PORT}/json/version`); if (r.ok) return await r.json(); } catch {}
    await sleep(250);
  }
  throw new Error("chromium never came up");
}

const results = { total: TOTAL, runAt: new Date().toISOString(), note: "loopback-only comparison: framing/queueing overhead, not WAN physics" };
try {
  const version = await waitVersion();
  const ws = new WebSocket(version.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let nextId = 1; const pending = new Map();
  const consoleEvents = [];
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && pending.has(msg.id)) { const p = pending.get(msg.id); pending.delete(msg.id); msg.error ? p.rej(new Error(JSON.stringify(msg.error))) : p.res(msg.result); }
    if (msg.method === "Runtime.exceptionThrown") consoleEvents.push("EXC " + (msg.params?.exceptionDetails?.exception?.description || "").slice(0, 200));
    if (msg.method === "Runtime.consoleAPICalled" && msg.params?.type === "error") consoleEvents.push("ERR " + JSON.stringify(msg.params.args?.map(a => a.value || a.description || "")).slice(0, 200));
  };
  const send = (method, params = {}, sessionId) => new Promise((res, rej) => {
    const id = nextId++; pending.set(id, { res, rej });
    ws.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }));
    setTimeout(() => { if (pending.has(id)) { pending.delete(id); rej(new Error("CDP timeout: " + method)); } }, 150000);
  });
  let targetId;
  for (let tries = 0; tries < 40 && !targetId; tries++) {
    const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
    const t = list.find((x) => x.type === "page");
    if (t) targetId = t.id; else await sleep(300);
  }
  const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
  const S = (m, p) => send(m, p, sessionId);
  await S("Page.enable"); await S("Runtime.enable");
  await S("Page.navigate", { url: PAGE_URL });
  const evalJs = async (expr) => {
    const r = await S("Runtime.evaluate", { expression: expr, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error("eval: " + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
    return r.result.value;
  };
  let ready = false;
  for (let i = 0; i < 40 && !ready; i++) { ready = await evalJs(`!!window.__proof`).catch(() => false); if (!ready) await sleep(300); }
  if (!ready) throw new Error("proof page never became ready (url=" + (await evalJs(`location.href`).catch(() => "?")) + ")");
  lap("page ready");

  lap("RTC signaling + workload attempt…");
  const rtc = await evalJs(`window.__proof.rtcRun(${JSON.stringify(RTC_SIG)}, ${TOTAL})`);
  if (rtc.rtts) { rtc.rtt = stats(rtc.rtts); delete rtc.rtts; }
  results.rtc = rtc;
  lap(`RTC: ${JSON.stringify(rtc).slice(0, 300)}`);

  lap("WS (bridge) workload…");
  const wsp = await evalJs(`window.__proof.wsRun(${JSON.stringify(BRIDGE_WS)}, ${TOTAL})`);
  wsp.rtt = stats(wsp.rtts || []); delete wsp.rtts;
  results.wsBridge = wsp;
  lap(`WS done: recv=${wsp.received}/${wsp.sent} p50=${wsp.rtt.p50Ms}ms p95=${wsp.rtt.p95Ms}ms`);

  lap("burst guard (bridge, house rate_limit semantics)…");
  results.wsBurst = await evalJs(`window.__proof.wsBurst(${JSON.stringify(BRIDGE_WS)}, 2000)`);
  lap(`wsBurst: ${JSON.stringify(results.wsBurst)}`);

  lap("burst guard (gateway RTC)…");
  results.rtcBurst = await evalJs(`window.__proof.rtcBurst(${JSON.stringify(RTC_SIG)}, 2000)`);
  lap(`rtcBurst: ${JSON.stringify(results.rtcBurst)}`);

  results.consoleEvents = consoleEvents;
  ws.close();
} finally {
  chrome.kill("SIGKILL");
}

lap("origin law (raw sockets)…");
results.origin = {
  foreign: await rawUpgrade(RTC_PORT, "/rtc", "https://evil.example"),
  absent: await rawUpgrade(RTC_PORT, "/rtc", null),
  site: await rawUpgrade(RTC_PORT, "/rtc", "https://nosuchmachine.net"),
  anyPath: await rawUpgrade(RTC_PORT, "/some/other/path", "https://nosuchmachine.net"),
};
lap(`origin: ${JSON.stringify(results.origin)}`);

// Session cap, end-to-end at the signaling layer: a second gateway
// instance runs with --max-sessions 2 (port 26013, see run-proof.sh).
// Hold two upgrades open; the third must be refused 503.
function rawUpgradeHold(port, path) {
  return new Promise((resolve) => {
    const sock = net.connect(port, "127.0.0.1");
    let buf = "";
    const key = Buffer.from(Array.from({ length: 16 }, () => Math.floor(Math.random() * 256))).toString("base64");
    sock.on("connect", () => {
      sock.write([`GET ${path} HTTP/1.1`, `Host: 127.0.0.1:${port}`,
        "Upgrade: websocket", "Connection: Upgrade",
        `Sec-WebSocket-Key: ${key}`, "Sec-WebSocket-Version: 13",
        "Origin: https://nosuchmachine.net"].join("\r\n") + "\r\n\r\n");
    });
    sock.on("data", (d) => {
      buf += d.toString("latin1");
      const status = Number((buf.split("\r\n")[0] || "").split(" ")[1]);
      if (status) resolve({ status, sock });
    });
    sock.on("error", () => resolve({ status: -1, sock }));
    setTimeout(() => resolve({ status: -2, sock }), 5000);
  });
}
lap("session cap (raw sockets, --max-sessions 2 instance)…");
const h1 = await rawUpgradeHold(26013, "/rtc");
const h2 = await rawUpgradeHold(26013, "/rtc");
const h3 = await rawUpgradeHold(26013, "/rtc");
results.sessionCap = { first: h1.status, second: h2.status, thirdOverCap: h3.status };
h1.sock.destroy(); h2.sock.destroy(); h3.sock.destroy();
lap(`cap: ${JSON.stringify(results.sessionCap)}`);

const gatewayLog = OUT + "out/gateway.log";
results.gatewayRateLimit = existsSync(gatewayLog) && readFileSync(gatewayLog, "utf8").split("\n")
  .some(line => line.includes(" session_close ") && line.includes("reason=rate_limit ") && line.includes("rate_drops=10"));
const bridgeLog = OUT + "out/bridge.log";
results.bridgeRateLimit = existsSync(bridgeLog) && readFileSync(bridgeLog, "utf8").split("\n")
  .some(line => line.includes(" session_close ") && line.includes("reason=rate_limit ") && line.includes("rate_drops=10"));
results.validation = validateResults(results, { allowBlockedRTC: process.env.PROOF_ALLOW_BLOCKED_RTC === "1" });
writeFileSync(OUT + "results.json", JSON.stringify(results, null, 2));
console.log(JSON.stringify(results, null, 2));
if (results.validation.some(c => c.status === "fail")) process.exitCode = 1;
lap("wrote results.json");
