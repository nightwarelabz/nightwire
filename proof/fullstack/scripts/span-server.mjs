// span-server.mjs — verbatim rig pattern (nexuiz-research/rig/span-server.mjs):
// serves the pinned packs' raw-deflate spans from the local release zip,
// chunked exactly as functions/api/nexuiz-data.ts does in production.
import http from "node:http";
import fs from "node:fs";
const ZIP = "/home/hatch/workspace/nexuiz-research/nexuiz-252.zip";
const SPANS = { data: [23842145, 880493625], spog: [904335848, 24638] };
const CH = 16 * 1024 * 1024;
const fd = fs.openSync(ZIP, "r");
http.createServer((req, res) => {
  const u = new URL(req.url, "http://x");
  const sp = SPANS[u.searchParams.get("part")];
  if (!sp) { res.writeHead(404); res.end(); return; }
  const idx = parseInt(u.searchParams.get("chunk") || "0", 10);
  const start = sp[0] + idx * CH, len = Math.min(CH, sp[1] - idx * CH);
  if (len <= 0) { res.writeHead(404); res.end(); return; }
  const buf = Buffer.alloc(len);
  fs.readSync(fd, buf, 0, len, start);
  res.writeHead(200, { "content-type": "application/octet-stream", "content-length": len, "access-control-allow-origin": "*" });
  res.end(buf);
}).listen(18772, "127.0.0.1", () => console.log("span server on 18772"));
