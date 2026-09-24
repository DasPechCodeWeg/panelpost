# Security

- **Least privilege.** Panelpost needs a Grafana service account with the
  Viewer role and only reads dashboards.
- **Credentials stay home.** Grafana tokens and the SMTP password are
  encrypted at rest with AES-256-GCM. The key is `/data/secret.key` or
  `PANELPOST_SECRET_KEY`. While rendering, the token is attached only to
  requests for your Grafana URL, never to third-party hosts a panel may load.
- **No data leaves your network.** Rendering happens inside the container.
  Online license checks send only the license key and an installation label.
  Offline keys make no network calls at all.
- **Web UI hardening.** Passwords are hashed with bcrypt. Sessions use
  signed, HttpOnly, SameSite cookies. Forms carry CSRF tokens and are
  origin-checked. Login and API attempts are rate-limited. A strict
  Content-Security-Policy disallows inline scripts, and uploaded SVG logos are
  sandboxed.
- **First start.** The admin password can only be set with the setup code
  printed in the log, so an instance exposed before configuration can't be
  claimed by a stranger.
- **Chromium sandbox.** In containers Chromium runs without its own sandbox.
  It only loads your Grafana. Set `PANELPOST_CHROME_SANDBOX=true` where user
  namespaces are available.

Report vulnerabilities privately through GitHub: open the repository's
**Security** tab and choose **Report a vulnerability**. Please don't open a
public issue for security problems.
