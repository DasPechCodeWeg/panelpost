# REST API (Pro and Business)

Create a key under *Settings → API* and send it as `Authorization: Bearer pp_...`.
Keys are stored as hashes and shown once. Each key is limited to 120
requests per minute.

| Method and path | Purpose |
|---|---|
| `GET /api/v1/reports` | List reports with their schedules and next runs |
| `POST /api/v1/reports/{id}/run` | Queue a run and deliver it (`?deliver=false` to only render) |
| `GET /api/v1/runs/{id}` | Run status, period, outputs, delivery results |
| `GET /api/v1/runs/{id}/outputs/{n}` | Download output `n` as PDF |
| `POST /api/v1/render` | Render synchronously and return the PDF |

`POST /api/v1/render` accepts either `{"report_id": "..."}` (optionally with
`"burst_value"`) or an ad hoc definition:

```json
{
  "connection_id": "…",
  "dashboard_uid": "…",
  "name": "Capacity review",
  "time": {"preset": "previous_quarter"},
  "timezone": "Europe/Amsterdam",
  "variables": [{"name": "cluster", "values": ["prod-eu"]}],
  "layout": {"mode": "panels", "cover_page": true, "orientation": "portrait"}
}
```

The response is the PDF, with `X-Panelpost-Pages` and, when relevant,
`X-Panelpost-Warnings` headers.
