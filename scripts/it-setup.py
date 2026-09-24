#!/usr/bin/env python3
"""Prepare a Grafana instance for Panelpost's integration tests.

Creates a TestData data source, a Viewer service account token and demo
dashboards, then prints the token. Usage: it-setup.py http://localhost:3000
"""
import base64
import json
import sys
import time
import urllib.error
import urllib.request

GRAFANA = sys.argv[1].rstrip("/") if len(sys.argv) > 1 else "http://localhost:3000"
AUTH = "Basic " + base64.b64encode(b"admin:admin").decode()


def call(method, path, body=None):
    req = urllib.request.Request(GRAFANA + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json", "Authorization": AUTH})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


for _ in range(60):
    try:
        if call("GET", "/api/health").get("database") == "ok":
            break
    except (urllib.error.URLError, ConnectionError):
        pass
    time.sleep(2)

try:
    call("POST", "/api/datasources", {"name": "TestData", "type": "grafana-testdata-datasource", "access": "proxy", "isDefault": True})
except urllib.error.HTTPError as e:
    if e.code != 409:
        raise
uid = [d for d in call("GET", "/api/datasources") if d["type"] == "grafana-testdata-datasource"][0]["uid"]
ds = {"type": "grafana-testdata-datasource", "uid": uid}


def panel(i, title, typ, x, y, w, h):
    return {"id": i, "title": title, "type": typ, "gridPos": {"x": x, "y": y, "w": w, "h": h}, "datasource": ds,
            "targets": [{"refId": "A", "scenarioId": "random_walk", "seriesCount": 3, "datasource": ds}]}


panels = [panel(1, "CPU usage", "timeseries", 0, 0, 12, 8), panel(2, "Memory", "timeseries", 12, 0, 12, 8),
          panel(3, "Requests", "stat", 0, 8, 6, 4), panel(4, "Errors", "gauge", 6, 8, 6, 4),
          panel(5, "Latency table", "table", 12, 8, 12, 8),
          {"id": 6, "type": "row", "title": "Details", "gridPos": {"x": 0, "y": 16, "w": 24, "h": 1}, "collapsed": False, "panels": []},
          panel(7, "Disk IO", "timeseries", 0, 17, 24, 9), panel(8, "Network", "barchart", 0, 26, 12, 8),
          panel(9, "Uptime", "stat", 12, 26, 12, 8)]
for i in range(10, 22):
    panels.append(panel(i, f"Service {i - 9}", "timeseries", (i % 2) * 12, 34 + ((i - 10) // 2) * 8, 12, 8))
# A collapsed row: the renderer must expand it, giving 22 panels in total.
panels.append({"id": 30, "type": "row", "title": "Archive", "gridPos": {"x": 0, "y": 82, "w": 24, "h": 1}, "collapsed": True,
               "panels": [panel(31, "Archived CPU", "timeseries", 0, 83, 12, 8), panel(32, "Archived memory", "stat", 12, 83, 12, 8)]})
clients = ["acme", "globex", "initech"]
call("POST", "/api/dashboards/db", {"overwrite": True, "dashboard": {
    "uid": "proto1", "title": "Proto Infra Overview", "panels": panels, "schemaVersion": 39,
    "time": {"from": "now-24h", "to": "now"},
    "templating": {"list": [{"name": "client", "type": "custom", "query": ",".join(clients),
                             "current": {"text": "acme", "value": "acme"},
                             "options": [{"text": c, "value": c, "selected": c == "acme"} for c in clients]}]}}})

stamp = "%d" % (time.time() * 1000)
sa = call("POST", "/api/serviceaccounts", {"name": "panelpost-it-" + stamp, "role": "Viewer"})
token = call("POST", "/api/serviceaccounts/%d/tokens" % sa["id"], {"name": "it-" + stamp})["key"]
print(token)
