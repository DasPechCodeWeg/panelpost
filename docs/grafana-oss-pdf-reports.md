# Scheduled PDF reports from self-hosted Grafana: every option (2026)

Grafana OSS can't email a dashboard as a PDF on a schedule. This page lists
every way to get there, including the free ones, with what each costs and
what it takes to run. It was checked against the vendors' own documentation
on 29 October 2026.

Disclosure: Panelpost, one of the options, is this project. The other options
are described as fairly as we can; corrections are welcome as an issue.

## What Grafana OSS does by itself

- **Export as PDF** is "only available on Grafana Enterprise"
  ([docs](https://grafana.com/docs/grafana/latest/visualizations/dashboards/share-dashboards-panels/)).
- **Scheduled reports** are part of Grafana Enterprise and Grafana Cloud.
- **Grafana 13 removed the image renderer plugin.** Grafana's own
  screenshots and reports now need the separate
  [renderer service](https://grafana.com/docs/grafana/latest/setup-grafana/image-rendering/),
  which Grafana suggests giving at least 16 GiB of memory and 4 CPU cores
  ([what's new in 13.0](https://grafana.com/docs/grafana/latest/whatsnew/whats-new-in-v13/whats-new-in-v13-0/)).

## The options

| Option | Cost | Schedule and email | Runs where | Best for |
|---|---|---|---|---|
| Print from the browser | Free | No | Your browser | A one-off PDF |
| Grafana Cloud | Reporting included from the Free tier | ✓ | Grafana's cloud | Anyone whose dashboards may move to the cloud |
| Grafana Enterprise | Enterprise contract | ✓ | Your servers, plus the renderer service | Organisations that already buy Enterprise |
| Dashboard Reporter plugin + cron | Free (Apache-2.0) | You script it | Your servers, plus the renderer and a Chromium | Technical teams happy to maintain a script |
| Your own headless-browser script | Free | You script it | Your servers | Full control, full maintenance |
| Skedler, DataViRe | Roughly $1,500 to $2,400+ a year, or by quote | ✓ | Your servers | Teams that also need Excel/CSV or many channels |
| Panelpost | Free for 3 reports; Pro €390 once or €290/year | ✓ | Your servers, one container | Self-hosted Grafana, branded reports, one PDF per client |

### Grafana Cloud

If your data and dashboards are allowed to live in Grafana's cloud, this is
the simplest route: reporting is listed for the Free, Pro and contracted
plans ([plans](https://grafana.com/docs/grafana-cloud/cost-management-and-billing/understand-grafana-cloud-features/)).
The usual reasons not to are data residency, air-gapped networks, or a large
existing self-hosted setup.

### Grafana Enterprise

The same reporting on your own servers, with a commercial contract. Since
Grafana 13 it needs the renderer service next to Grafana.

### The free Dashboard Reporter plugin, plus cron

[grafana-dashboard-reporter-app](https://github.com/mahendrapaipuri/grafana-dashboard-reporter-app)
(Apache-2.0, maintained) makes a PDF of a dashboard on request, through an
API endpoint. It has no schedule or email of its own, so you add those. Know
before you start:

- It isn't in Grafana's plugin catalog. You install it as an unsigned plugin
  and allow it with `allow_loading_unsigned_plugins`.
- It uses the image renderer for panel images and a Chromium (local or
  remote) to build the PDF.
- Its documentation lists version-specific settings for Grafana 10.3 to 11.4
  and a known issue on Grafana 12.

A minimal monthly job, as a sketch to adapt (GNU `date` and `mailutils`):

```bash
#!/bin/sh
# Last month's dashboard as a PDF, emailed on the 1st at 07:00 via cron:
#   0 7 1 * *  /usr/local/bin/monthly-report.sh
set -eu
first_of_month=$(date +%Y-%m-01)
from=$(date -d "$first_of_month -1 month" +%s)000
to=$(( $(date -d "$first_of_month" +%s) * 1000 - 1 ))
curl -sf -o /tmp/report.pdf -H "Authorization: Bearer $GRAFANA_TOKEN" \
  "$GRAFANA_URL/api/plugins/mahendrapaipuri-dashboardreporter-app/resources/report?dashUid=$DASH_UID&from=$from&to=$to"
echo "Last month's report is attached." | mail -s "Monthly report" -A /tmp/report.pdf team@example.com
```

What a script like this leaves to you: the time zone the month is computed
in, retries and an alert when a run fails, a loop with its own recipients for
each client, and branding.

### Your own script

Headless Chromium (Puppeteer, Playwright or chromedp) can print any
dashboard. Expect to handle Grafana's lazy loading of panels below the fold,
page breaks that cut panels in half, and login. It's a weekend project that
needs attention on every Grafana upgrade.

### Commercial add-ons

[Skedler](https://www.skedler.com/pricing) and
[DataViRe](https://www.datavire.com/) are established products with more
formats (Excel, CSV) and channels (Slack, Teams). Their prices are by quote
or on third-party listings, roughly $1,500 to $2,400 a year and up.

### Panelpost

One Docker container next to your Grafana with a Viewer service account.
Chromium is inside the container, so it needs neither the renderer plugin nor
the renderer service. It turns a dashboard into a branded PDF with a cover
page and page numbers, for calendar periods such as "previous month" in your
time zone, and emails it on a schedule. From one dashboard with a client
variable it can make a separate PDF for each client, each sent to that
client's recipients.

It's young: automated tests render reports on Grafana 10.4, 11.6, 12.3, 13.2
and Enterprise 13.2, but few real-world dashboards have been through it.
It's source-available under the Elastic License 2.0, not open source. The
Community edition is free for three reports.
[Quick start](../README.md#quick-start).

## Which one to pick

- Dashboards may move to the cloud: **Grafana Cloud**.
- You already pay for Enterprise: **Grafana Enterprise**.
- One or two reports, a technical team, no budget: **the free plugin and a
  cron job**.
- Self-hosted or air-gapped Grafana, reports going to customers or
  management, or one report per client: **Panelpost**, or a commercial
  add-on if you need Excel or chat channels.
