// drive.mjs — full-stack join drive (rig CDP pattern).
// Usage: node drive.mjs <tag> <playUrl> <profileDir> <maxSeconds>
// Loads the REAL play page, clicks the plate button, polls the engine
// console (#con), the page state, and screenshots. Writes:
//   ../logs/<tag>-drive.log, ../logs/<tag>-console.txt, ../shots/<tag>-*.png
import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";
const [tag, playUrl, profileDir, maxSecRaw] = process.argv.slice(2);
const MAXSEC = Number(maxSecRaw || 600);
const OUT = new URL(".", import.meta.url).pathname + "../";
// Rig pattern: Chrome 152's Local Network Access checks block an
// about:blank/direct navigation to 127.0.0.1 even with the old
// --disable-features pair (measured: ERR_BLOCKED_BY_LOCAL_NETWORK_
// ACCESS_CHECKS), but a file:// boot page that location.replace()s
// to the play URL loads fine — so the drive starts there.
const bootPath = OUT + `scripts/boot-${tag}.html`;
writeFileSync(bootPath, `<!doctype html><title>rig</title><script>location.replace(${JSON.stringify(playUrl)});</script>`);
const startUrl = "file://" + bootPath;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const t0 = Date.now();
const lap = (m) => console.log(`[+${((Date.now() - t0) / 1000).toFixed(1)}s]`, m);
const PORT = 9600 + Math.floor(Math.random() * 300);
const chrome = spawn("/opt/meta-chromium/chrome", [
  "--headless=new", "--no-sandbox", "--disable-dev-shm-usage",
  "--use-angle=swiftshader", "--enable-unsafe-swiftshader",
  "--no-proxy-server",
  "--disable-features=LocalNetworkAccessChecks,BlockInsecurePrivateNetworkRequests",
  `--remote-debugging-port=${PORT}`, "--remote-debugging-address=127.0.0.1",
  `--user-data-dir=${profileDir}`, "--window-size=1100,750", "--mute-audio",
  startUrl,
], { stdio: "ignore" });
async function waitVersion() {
  for (let i = 0; i < 80; i++) {
    try { const r = await fetch(`http://127.0.0.1:${PORT}/json/version`); if (r.ok) return await r.json(); } catch {}
    await sleep(250);
  }
  throw new Error("chromium never came up");
}
let exitCode = 0;
try {
  const version = await waitVersion();
  lap("chromium up: " + (version.Browser || ""));
  const ws = new WebSocket(version.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let nextId = 1; const pending = new Map(); const consoleLines = [];
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && pending.has(msg.id)) { const p = pending.get(msg.id); pending.delete(msg.id); msg.error ? p.rej(new Error(JSON.stringify(msg.error))) : p.res(msg.result); }
    if (msg.method === "Runtime.consoleAPICalled") consoleLines.push((msg.params.args || []).map((x) => x.value ?? x.description ?? "").join(" "));
    if (msg.method === "Runtime.exceptionThrown") consoleLines.push("EXCEPTION " + (msg.params?.exceptionDetails?.exception?.description || "").slice(0, 300));
  };
  const send = (method, params = {}, sessionId) => new Promise((res, rej) => {
    const id = nextId++; pending.set(id, { res, rej });
    ws.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }));
    setTimeout(() => { if (pending.has(id)) { pending.delete(id); rej(new Error("CDP timeout " + method)); } }, 60000);
  });
  let targetId;
  for (let i = 0; i < 60 && !targetId; i++) {
    const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
    const t = list.find((x) => x.type === "page" && x.url.includes("play.html"));
    if (t) targetId = t.id; else await sleep(300);
  }
  if (!targetId) throw new Error("play page target never appeared");
  const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
  const S = (m, p) => send(m, p, sessionId);
  await S("Runtime.enable"); await S("Page.enable");
  const ev = (expr) => S("Runtime.evaluate", { expression: expr, returnByValue: true, awaitPromise: true })
    .then((r) => { if (r.exceptionDetails) return undefined; return r.result?.value; }).catch(() => undefined);
  async function shot(name) {
    try { const { data } = await S("Page.captureScreenshot", { format: "png", fromSurface: true }); writeFileSync(OUT + "shots/" + name, Buffer.from(data, "base64")); lap("SHOT " + name); }
    catch (e) { lap("SHOT FAILED " + name + " " + String(e).slice(0, 120)); }
  }
  let btnFound = false;
  for (let i = 0; i < 60 && !btnFound; i++) { btnFound = !!(await ev("!!document.getElementById('plate-btn')")); if (!btnFound) await sleep(500); }
  if (!btnFound) {
    lap("FATAL page state: href=" + (await ev("location.href")) + " title=" + (await ev("document.title")));
    throw new Error("plate button never appeared");
  }
  lap("page loaded, clicking play");
  const tClick = Date.now();
  await ev("document.getElementById('plate-btn').click(); 'clicked'");
  let startedAt = 0, lastConLen = -1, conText = "", sawConnect = 0, shots = 0;
  const deadline = t0 + MAXSEC * 1000;
  while (Date.now() < deadline) {
    await sleep(5000);
    conText = (await ev("document.getElementById('con') ? document.getElementById('con').textContent : ''")) || conText;
    const plateHidden = await ev("document.getElementById('plate').hidden");
    const status = await ev("document.getElementById('plate-status') ? document.getElementById('plate-status').textContent : ''");
    if (plateHidden && !startedAt) { startedAt = Date.now(); lap(`ENGINE STARTED (plate hidden) at +${((startedAt - tClick) / 1000).toFixed(1)}s after click`); }
    if (conText.length !== lastConLen) {
      lastConLen = conText.length;
      const tail = conText.slice(-600).replace(/\n/g, " | ");
      lap(`con[${conText.length}] plateHidden=${plateHidden} status=${JSON.stringify(status)} tail: ${tail.slice(-420)}`);
    }
    if (/connected|join|Connection accepted|server.*info/i.test(conText) && !sawConnect) { sawConnect = Date.now(); lap("CONNECT-SHAPED LINE IN CLIENT CONSOLE"); }
    if (shots < 3 && startedAt && Date.now() - startedAt > 20000) { await shot(`${tag}-world.png`); shots++; }
    // Done conditions are judged by the caller from the logs; the drive
    // itself stops early only when the engine died.
    const err = await ev("document.getElementById('plate-status') ? document.getElementById('plate-status').className : ''");
    if (err === "err") { lap("ENGINE ERROR STATE — stopping"); break; }
  }
  await shot(`${tag}-final.png`);
  // Open the engine console for a readable screenshot: backquote key.
  await S("Input.dispatchKeyEvent", { type: "keyDown", key: "`", code: "Backquote", windowsVirtualKeyCode: 192 }).catch(() => {});
  await S("Input.dispatchKeyEvent", { type: "keyUp", key: "`", code: "Backquote", windowsVirtualKeyCode: 192 }).catch(() => {});
  await sleep(1500);
  await shot(`${tag}-console.png`);
  conText = (await ev("document.getElementById('con') ? document.getElementById('con').textContent : ''")) || conText;
  writeFileSync(OUT + `logs/${tag}-console.txt`, conText);
  writeFileSync(OUT + `logs/${tag}-browser-console.txt`, consoleLines.join("\n"));
  lap(`DONE startedAt=${startedAt ? "+" + ((startedAt - tClick) / 1000).toFixed(1) + "s" : "never"} sawConnect=${sawConnect ? "+" + ((sawConnect - tClick) / 1000).toFixed(1) + "s" : "no"} conBytes=${conText.length}`);
  ws.close();
} catch (e) {
  console.error("DRIVE FAILED", e); exitCode = 1;
} finally {
  chrome.kill("SIGKILL");
}
process.exit(exitCode);
