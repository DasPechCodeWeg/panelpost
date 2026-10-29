# Panelpost demo

Try Panelpost in a couple of minutes, without your own Grafana. This starts
a Grafana 13 with a sample "Monthly service report" dashboard (fake data from
Grafana's TestData source), Panelpost already connected to it, and Mailpit
to catch the emails.

```bash
git clone https://github.com/DasPechCodeWeg/panelpost
cd panelpost/examples/demo
docker compose up -d
```

1. Open <http://localhost:8080> and sign in with the password
   `panelpost-demo`. The Grafana connection is already there.
2. *Settings → Email*: SMTP server `mailpit`, port `1025`, encryption
   *None*, sender `reports@example.com`. Save.
3. *Reports → New report*: pick the *Monthly service report* dashboard and
   "Previous month", tick *Cover page*, add any address under *Email to*,
   and click *Save & preview*.
4. On the report page, click *Run & deliver now*, then open
   <http://localhost:8025> to see the email with the PDF attached.

Grafana itself is at <http://localhost:3000> (admin / admin). The dashboard
has a `client` variable, which is what per-client PDFs use (Pro and
Business).

Stop and remove everything with `docker compose down -v`.

The `token` service creates a Viewer service account in the demo Grafana and
passes its token to Panelpost through `PANELPOST_GRAFANA_TOKEN_FILE`. With
your own Grafana you'd create the service account in Grafana's UI and paste
the token into Panelpost, or set `PANELPOST_GRAFANA_URL` and
`PANELPOST_GRAFANA_TOKEN` yourself.
