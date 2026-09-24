#!/usr/bin/env python3
"""Walk through the README's first-run steps in a browser, as a new user would.

Usage: install-test.py
Environment:
  PANELPOST_URL   Panelpost web UI (default http://localhost:8080)
  SETUP_CODE      one-time code from the Panelpost log
  GRAFANA_URL     Grafana as Panelpost sees it
  GRAFANA_TOKEN   Viewer service account token
  SMTP_HOST, SMTP_PORT  a mail server as Panelpost sees it (no TLS)
  MAILPIT_API     Mailpit HTTP API, to check that the email arrived
  CHROMIUM_PATH   optional browser for the test itself

Needs: pip install playwright && python -m playwright install --with-deps chromium
"""
import json
import os
import sys
import time
import urllib.request

from playwright.sync_api import sync_playwright

BASE = os.environ.get("PANELPOST_URL", "http://localhost:8080")
env = os.environ


def step(msg):
    print(f"--> {msg}", flush=True)


with sync_playwright() as p:
    browser = p.chromium.launch(executable_path=env.get("CHROMIUM_PATH") or None)
    page = browser.new_page(viewport={"width": 1360, "height": 900})
    page.set_default_timeout(30000)

    step("setup: enter the code from the log and choose a password")
    page.goto(BASE + "/")
    page.fill("#token", env["SETUP_CODE"])
    page.fill("#password", "install-test-password")
    page.fill("#confirm", "install-test-password")
    page.click("button[type=submit]")
    page.wait_for_url("**/connections/new**")

    step("connect Grafana with a Viewer token")
    page.fill("#name", "Grafana")
    page.fill("#url", env["GRAFANA_URL"])
    page.fill("#token", env["GRAFANA_TOKEN"])
    page.click("form[data-connection-form] button[type=submit]")
    page.wait_for_url("**/reports/new**")

    step("configure email under Settings → Email")
    page.goto(BASE + "/settings/email")
    page.fill("#host", env["SMTP_HOST"])
    page.fill("#port", env.get("SMTP_PORT", "25"))
    page.fill("#from", "reports@example.com")
    page.select_option("select[name=tls]", "none")
    page.click("form[data-email-form] .formbar button[type=submit]")
    page.wait_for_url("**ok=**")

    step("create a weekly report for the previous week")
    page.goto(BASE + "/reports/new")
    page.wait_for_function("document.querySelector('[data-dashboard-select]').options.length > 1")
    page.select_option("[data-dashboard-select]", "proto1")
    page.fill("#name", "Install test report")
    page.select_option("#time_preset", "previous_week")
    page.select_option("#schedule_kind", "weekly")
    page.fill("#email_to", "someone@example.com")
    page.click("button:has-text('Save report')")
    page.wait_for_url("**ok=**")

    step("run it now and wait for the result")
    page.click("button:has-text('Run & deliver now')")
    page.wait_for_url("**/runs/**")
    deadline = time.time() + 180
    while time.time() < deadline:
        text = page.inner_text("main")
        if "success" in text or "failed" in text:
            break
        page.wait_for_timeout(2000)
        page.reload()
    print(page.inner_text("main"))
    if "success" not in page.inner_text("main"):
        sys.exit("FAIL: the run did not succeed")

    step("download the PDF")
    href = page.get_attribute("a:has-text('Download')", "href")
    pdf = page.request.get(BASE + href).body()
    if not pdf.startswith(b"%PDF") or len(pdf) < 50_000:
        sys.exit(f"FAIL: download is not a real PDF ({len(pdf)} bytes)")
    print(f"PDF: {len(pdf) // 1024} KB")
    browser.close()

step("check that the email arrived with the PDF attached")
with urllib.request.urlopen(env["MAILPIT_API"] + "/api/v1/messages") as resp:
    messages = json.load(resp)["messages"]
if not messages or messages[0].get("Attachments", 0) < 1:
    sys.exit(f"FAIL: no email with an attachment: {messages}")
print(f"email: {messages[0]['Subject']!r} to {[t['Address'] for t in messages[0]['To']]}")
print("PASS: a new user can install Panelpost and receive a report")
