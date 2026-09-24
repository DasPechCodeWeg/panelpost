#!/usr/bin/env python3
"""Load the "Monthly service report" demo dashboard into a Grafana instance.

Usage: demo-setup.py http://localhost:3000 [admin:password]
Uses Grafana's built-in TestData source, so no real data is needed.
"""
import base64
import json
import sys
import urllib.error
import urllib.request

GRAFANA = sys.argv[1].rstrip("/") if len(sys.argv) > 1 else "http://localhost:3000"
CREDS = (sys.argv[2] if len(sys.argv) > 2 else "admin:admin").encode()
AUTH = "Basic " + base64.b64encode(CREDS).decode()


def call(method, path, body=None):
    req = urllib.request.Request(GRAFANA + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json", "Authorization": AUTH})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


try:
    call("POST", "/api/datasources", {"name": "TestData", "type": "grafana-testdata-datasource", "access": "proxy"})
except urllib.error.HTTPError as e:
    if e.code != 409:
        raise
uid = [d for d in call("GET", "/api/datasources") if d["type"] == "grafana-testdata-datasource"][0]["uid"]
DS = {"type": "grafana-testdata-datasource", "uid": uid}

dashboard = json.load(open(__file__.replace("scripts/demo-setup.py", "examples/demo-dashboard.json")))


def fix(panels):
    for p in panels:
        if p.get("type") != "row":
            p["datasource"] = DS
            for t in p.get("targets", []):
                t["datasource"] = DS
        fix(p.get("panels", []))


fix(dashboard["panels"])
res = call("POST", "/api/dashboards/db", {"dashboard": dashboard, "overwrite": True})
print(res["url"])
