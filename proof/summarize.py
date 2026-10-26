#!/usr/bin/env python3
"""Render this run's measurements without assuming why a check failed."""
import json
import pathlib

here = pathlib.Path(__file__).parent
res = json.loads((here / "results.json").read_text())
checks = res.get("validation", [])
verdict = "FAIL" if not checks or any(c["status"] == "fail" for c in checks) else (
    "INCOMPLETE" if any(c["status"] == "blocked" for c in checks) else "PASS")
out = ["# sparta-rtc browser proof", "", f"Verdict: **{verdict}**", "",
       f"Run: {res.get('runAt', 'not recorded')}", "",
       "Measurements are loopback transport overhead, not WAN latency.", "",
       "| Check | Status |", "| --- | --- |"]
out += [f"| {c['name']} | {c['status']} |" for c in checks]
if not checks:
    out.append("| Machine-readable validation | missing |")

for title, key in (("RTC workload", "rtc"), ("WebSocket workload", "wsBridge")):
    r = res.get(key, {})
    out += ["", f"## {title}", "", "```json", json.dumps(r, indent=2), "```"]

out += ["", "## Guard measurements", "", "```json", json.dumps({
    k: res.get(k) for k in ("origin", "sessionCap", "rtcBurst", "wsBurst", "gatewayRateLimit", "bridgeRateLimit")
}, indent=2), "```"]

def log_lines(name):
    p = here / "out" / name
    return p.read_text(errors="replace").splitlines() if p.exists() else []

errors = [line for line in log_lines("gateway-stdout.log") if "operation not permitted" in line.lower()]
if errors:
    out += ["", "## Captured permission errors", "",
            "These errors were observed in this run. Inspect the logs to determine which operations were blocked.",
            "", "```", *errors[:5], "```"]
out += ["", "## Go tests", "", "```", *(log_lines("go-test.log")[-12:] or ["Output missing; not a passing check."]), "```", ""]
(here / "results.md").write_text("\n".join(out))
print("wrote results.md:", verdict)
