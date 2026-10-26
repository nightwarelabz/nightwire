import test from "node:test";
import assert from "node:assert/strict";
import { validateResults } from "./validate-results.mjs";

function passing() {
  const data = { sent: 300, received: 300, bytesSent: 1000, bytesReceived: 1000,
    lost: 0, checksumFailures: 0, equalityFailures: 0, boundaryFailures: 0, unexpected: 0 };
  return { total: 300, rtc: { dcOpened: true, ...data }, wsBridge: { ...data },
    origin: { foreign: 403, absent: 101, site: 101, anyPath: 101 },
    sessionCap: { first: 101, second: 101, thirdOverCap: 503 },
    wsBurst: { sent: 2000, closeCode: 1006 }, rtcBurst: { closedHow: "dc_close" },
    gatewayRateLimit: true, bridgeRateLimit: true, consoleEvents: [] };
}

test("complete proof passes", () => {
  assert.ok(validateResults(passing()).every(c => c.status === "pass"));
});

test("RTC failures require an explicit diagnostic opt-in", () => {
  const r = passing(); r.rtc = { dcOpened: false }; r.rtcBurst = { dcOpened: false };
  assert.equal(validateResults(r).filter(c => c.status === "fail").length, 2);
  const checks = validateResults(r, { allowBlockedRTC: true });
  assert.equal(checks.filter(c => c.status === "blocked").length, 2);
  assert.ok(checks.every(c => c.status !== "fail"));
});

test("loss, missing metrics, wrong guards, and absent rate closure fail", () => {
  for (const mutate of [
    r => { r.rtc.received--; }, r => { delete r.wsBridge.bytesReceived; },
    r => { delete r.wsBridge.bytesSent; delete r.wsBridge.bytesReceived; },
    r => { r.origin.foreign = 101; }, r => { r.sessionCap.thirdOverCap = 101; },
    r => { r.rtcBurst.closedHow = "timeout"; }, r => { r.gatewayRateLimit = false; },
    r => { r.wsBurst.closeCode = -1; }, r => { r.bridgeRateLimit = false; }, r => { r.consoleEvents.push("error"); },
  ]) {
    const r = passing(); mutate(r);
    assert.ok(validateResults(r).some(c => c.status === "fail"));
  }
});
