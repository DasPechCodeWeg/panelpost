# Creating reports

## Periods

Pick a preset such as **Previous month** or **Previous week**, or enter a
custom range in Grafana syntax:

| Expression | Meaning |
|---|---|
| `now-7d` to `now` | the last seven days up to the moment the report runs |
| `now-1d/d` to `now-1d/d` | yesterday, midnight to midnight |
| `now-1M/M` to `now-1M/M` | the previous calendar month |
| `now-1Q/Q` to `now-1Q/Q` | the previous calendar quarter (Panelpost extension) |
| `2026-08-01` to `2026-08-31 23:59` | fixed dates |

Rounding (`/d`, `/w`, `/M`, `/Q`, `/y`) happens in the report's time zone.
Weeks start on Monday unless you choose Sunday. Daylight saving time and
month ends are handled: a day can have 23 or 25 hours, and 31 March minus one
month is 28 February. Panelpost sends Grafana exact timestamps, so the charts
and the period on the cover always agree.

## Template variables

Write one variable per line: `name = value1, value2`. Use `All` for Grafana's
"All" option. The editor reads the variables of the dashboard you pick and
fills in their current values for you.

## Layouts

- **As on screen** prints the dashboard exactly as Grafana shows it. Page
  breaks fall between panels, and a slightly taller band is scaled down
  rather than leaving half a page empty.
- **Panels with row headings** prints panels in reading order, never splits
  a panel, and turns dashboard rows into section headings. *One panel per
  line* enlarges small panels for readability. *Only these panels* limits
  the report to the titles you list.

Other options: A4, Letter or A3 in portrait or landscape; the light or dark
Grafana theme; a cover page; expanding collapsed rows; and the browser width
(narrower means larger text).

## Schedules

Every day, selected weekdays, a day of the month (1 to 28), or any
five-field cron expression, always in the report's time zone. If Panelpost
was down at the scheduled moment, the run is skipped rather than replayed
later, so recipients never get a burst of stale reports. Set an alert
address under *Settings → General* to hear about failures.

## One PDF per client (bursting, Pro)

Turn on *Enable per-client PDFs*, name the variable (for example `client`) and
list one target per line:

```
acme    | Acme Corp | it@acme.example
globex  | Globex    | cto@globex.example, ops@globex.example
```

Each target gets its own PDF, rendered with that variable value, titled
"Prepared for <label>", and emailed only to its recipients. Cc and Bcc on the
report still apply, which is handy for an internal archive copy. With
Business, each target can carry its own company name for white-labelled
senders.

## Email

The subject and message accept placeholders: `{{report}}`, `{{period}}`,
`{{dashboard}}`, `{{target}}`, `{{company}}`, `{{date}}` and `{{pages}}`.
The PDF is attached. Under 10 MB is safe for most mail systems; choose
*Balanced* image quality to keep files small.

## Webhooks and the drop folder

Webhooks (Pro) receive the PDF as `multipart/form-data`, signed with
`X-Panelpost-Signature: sha256=<HMAC>`. The drop folder writes
`/data/outbox/<report>/<file>.pdf`. Mount that path to a network share, and
your document management system picks the reports up.
