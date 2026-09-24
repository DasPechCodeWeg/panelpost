# Troubleshooting

| Symptom | Cause and fix |
|---|---|
| "Grafana showed its login page" | The token is wrong or expired, or an auth proxy sits in front of Grafana. Create a new service account token. For proxies, add the header your proxy expects under *Extra headers* on the connection. |
| "Grafana's web app failed to load in the browser" | The Grafana URL doesn't match Grafana's `root_url`, for example Grafana served under `/grafana/` without `serve_from_sub_path`. Use the exact URL you open in your browser. |
| "Grafana says the dashboard was not found" | The dashboard lives in another organisation. Set the organisation ID on the connection, or give the service account access to the folder. |
| "panels were still loading" or a timeout | A slow data source. Raise *Render timeout* under *Settings → General*, or reduce the time range. |
| A panel shows "error" in the PDF | The query failed in Grafana for that period. Open the dashboard with the same period and variables to see the message. Runs with such warnings are marked **partial**. |
| Characters show as boxes | The official image includes Noto fonts for all scripts. On custom images, install `fonts-noto-core`, `fonts-noto-cjk` and `fonts-noto-color-emoji`. |
| Emails are not delivered | Use *Settings → Email → Send test*. Port 587 needs STARTTLS and 465 needs TLS. Microsoft 365 and Google need an app password or an SMTP relay. |
| PDFs are too large for email | Choose *Balanced* image quality, fewer panels, or the *Panels* layout with *Only these panels*. |
| Self-signed Grafana certificate | Tick *Accept self-signed certificates* on the connection. |
| The browser won't start | The image includes Chromium. For the binary, install Chrome or Chromium, or set `PANELPOST_CHROME_PATH`. Containers need about 1 GB of memory. |

Run `panelpost render -h` to test a dashboard from the command line with the
same engine. Its output includes the warnings Panelpost sees.
