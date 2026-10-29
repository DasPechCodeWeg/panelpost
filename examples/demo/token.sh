#!/bin/sh
# Create (or reuse) a Viewer service account and write a fresh token for it.
set -eu
G=http://grafana:3000
AUTH=admin:admin
json='Content-Type: application/json'
id=$(curl -sf -u "$AUTH" "$G/api/serviceaccounts/search?query=panelpost" | sed -n 's/.*"serviceAccounts":\[{"id":\([0-9]*\).*/\1/p')
if [ -z "$id" ]; then
  id=$(curl -sf -u "$AUTH" -H "$json" -d '{"name":"panelpost","role":"Viewer"}' "$G/api/serviceaccounts" | sed -n 's/^{"id":\([0-9]*\).*/\1/p')
fi
key=$(curl -sf -u "$AUTH" -H "$json" -d "{\"name\":\"demo-$(date +%s)\"}" "$G/api/serviceaccounts/$id/tokens" | sed -n 's/.*"key":"\([^"]*\)".*/\1/p')
[ -n "$key" ] || { echo "could not create a service account token" >&2; exit 1; }
printf '%s' "$key" > /token/grafana
echo "Service account token written for Panelpost."
