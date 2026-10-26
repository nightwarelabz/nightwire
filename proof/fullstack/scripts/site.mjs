// site.mjs — static dist server (rig serve.mjs pattern) + data pipe.
//   /api/nexuiz-data* -> span server on 18772 (local zip spans)
//   everything else   -> DIST (argv[2]) with COOP/COEP like the rig.
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
const DIST = process.argv[2];
const PORT = Number(process.env.SITE_PORT || 18770);
const MIME = { ".html": "text/html", ".js": "text/javascript", ".wasm": "application/wasm", ".css": "text/css", ".txt": "text/plain", ".cfg": "text/plain", ".json": "application/json", ".svg": "image/svg+xml", ".png": "image/png", ".ico": "image/x-icon", ".pk3": "application/octet-stream", ".webmanifest": "application/manifest+json", ".webp": "image/webp" };
http.createServer((req, res) => {
  const url = new URL(req.url, "http://127.0.0.1");
  if (url.pathname.startsWith("/api/nexuiz-data")) {
    const preq = http.request({ host: "127.0.0.1", port: 18772, path: url.pathname + url.search, method: "GET" }, (pres) => {
      res.writeHead(pres.statusCode || 502, pres.headers); pres.pipe(res);
    });
    preq.on("error", (e) => { res.writeHead(502); res.end("span proxy: " + e.message); });
    preq.end(); return;
  }
  let p = decodeURIComponent(url.pathname);
  if (p.endsWith("/")) p += "index.html";
  const file = path.join(DIST, p);
  if (!file.startsWith(DIST) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) {
    res.writeHead(404); res.end("not found: " + p); return;
  }
  res.writeHead(200, { "content-type": MIME[path.extname(file)] || "application/octet-stream", "cross-origin-embedder-policy": "require-corp", "cross-origin-opener-policy": "same-origin" });
  fs.createReadStream(file).pipe(res);
}).listen(PORT, "127.0.0.1", () => console.log(`site ${DIST} on ${PORT}`));
