# Installation

Panelpost is one container. It needs network access to your Grafana and to your
SMTP server, and about 1 GB of memory while it renders.

## Docker

```bash
docker run -d --name panelpost --restart unless-stopped \
  -p 8080:8080 -v panelpost-data:/data -e TZ=Europe/Amsterdam \
  ghcr.io/daspechcodeweg/panelpost:latest
docker logs panelpost   # shows the one-time setup code
```

Open `http://<host>:8080`, enter the setup code and choose the admin password.
You can also set `PANELPOST_ADMIN_PASSWORD` to skip that step, or later to
reset a forgotten password.

## Docker Compose next to Grafana

```yaml
services:
  grafana:
    image: grafana/grafana:13.2.2
    ports: ["3000:3000"]
  panelpost:
    image: ghcr.io/daspechcodeweg/panelpost:latest
    ports: ["8080:8080"]
    volumes: ["panelpost-data:/data"]
    environment:
      TZ: Europe/Amsterdam
    restart: unless-stopped
volumes:
  panelpost-data:
```

In Panelpost, connect to `http://grafana:3000`, the address as seen from the
Panelpost container.

## Kubernetes

Run it as a `Deployment` with one replica and a `PersistentVolumeClaim` on
`/data`. Probe `GET /healthz`. Don't scale it beyond one replica: the
scheduler runs inside the process. Give it `requests: {memory: 512Mi}` and
`limits: {memory: 1.5Gi}`.

## Binary

Download a binary from the releases page and run `panelpost serve`. It needs
Chrome, Chromium or Edge installed. Set `PANELPOST_CHROME_PATH` if it can't
find one.

## HTTPS and reverse proxies

Put Panelpost behind your existing proxy (Traefik, Caddy, nginx or an
ingress). Forward `Host` and `X-Forwarded-Proto` so that secure cookies and
the origin check work.

```nginx
location / {
  proxy_pass http://127.0.0.1:8080;
  proxy_set_header Host $host;
  proxy_set_header X-Forwarded-Proto $scheme;
  proxy_read_timeout 300s;
}
```

## Upgrades and backups

- Upgrading is a pull and restart. The database migrates itself.
- Back up the `/data` volume. It holds `panelpost.db`, `secret.key`, logos
  and the kept PDFs. Without `secret.key`, stored tokens can't be decrypted,
  unless you set `PANELPOST_SECRET_KEY`.
