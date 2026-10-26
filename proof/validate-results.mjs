// Machine-readable verdicts for the browser proof. A diagnostic RTC run may
// explicitly allow blocked media; a missing channel is never a passing check.
export function validateResults(results, { allowBlockedRTC = false } = {}) {
  const checks = [];
  const check = (name, ok) => checks.push({ name, status: ok ? "pass" : "fail" });
  const blocked = (name) => checks.push({ name, status: allowBlockedRTC ? "blocked" : "fail" });
  const datagrams = (name, r) => {
    check(name, Number.isInteger(results.total) && results.total > 0 &&
      r?.sent === results.total && r?.received === results.total &&
      Number.isInteger(r?.bytesSent) && r.bytesSent > 0 &&
      r?.bytesSent === r?.bytesReceived &&
      [r?.lost, r?.checksumFailures, r?.equalityFailures, r?.boundaryFailures, r?.unexpected].every(n => n === 0));
  };
  if (results.rtc?.dcOpened) datagrams("RTC datagrams", results.rtc);
  else blocked("RTC datagrams");
  datagrams("WebSocket datagrams", results.wsBridge);
  check("Origin policy", results.origin?.foreign === 403 && results.origin?.absent === 101 &&
    results.origin?.site === 101 && results.origin?.anyPath === 101);
  check("Session cap", results.sessionCap?.first === 101 && results.sessionCap?.second === 101 && results.sessionCap?.thirdOverCap === 503);
  check("WebSocket rate closure", results.wsBurst?.sent > 300 &&
    results.wsBurst?.closeCode >= 1000 && results.wsBurst?.closeCode <= 4999 && results.bridgeRateLimit === true);
  if (results.rtcBurst?.dcOpened === false) blocked("RTC rate closure");
  else check("RTC rate closure", results.rtcBurst?.closedHow === "dc_close" && results.gatewayRateLimit === true);
  check("Browser errors", Array.isArray(results.consoleEvents) && results.consoleEvents.length === 0);
  return checks;
}
