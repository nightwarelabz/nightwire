// door.mjs — the local "Funnel mount": one WS door port.
//   path /rtc  -> sparta-rtc gateway signaling (127.0.0.1:26003)
//   everything -> sparta-bridge (127.0.0.1:26001), request line as-is
// Raw upgrade proxy: parse the request line, pick the backend, replay
// the buffered head, then pipe both ways. Logs one line per upgrade.
import net from "node:net";
const DOOR = Number(process.env.DOOR_PORT || 26002);
const log = (m) => console.log(new Date().toISOString(), m);
net.createServer((client) => {
  client.once("data", (head) => {
    const line = head.toString("latin1").split("\r\n")[0] || "";
    const path = (line.split(" ")[1] || "/").split("?")[0];
    const backend = path === "/rtc" ? 26003 : 26001;
    log(`upgrade ${line} -> backend ${backend}`);
    const up = net.connect(backend, "127.0.0.1", () => {
      up.write(head);
      client.pipe(up); up.pipe(client);
    });
    up.on("error", (e) => { log("backend error " + e.message); client.destroy(); });
    client.on("error", () => up.destroy());
  });
}).listen(DOOR, "127.0.0.1", () => log(`door on ${DOOR}: /rtc->26003, else->26001`));
