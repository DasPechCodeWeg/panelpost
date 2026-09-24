# syntax=docker/dockerfile:1.7

# ---- build ------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/panelpost ./cmd/panelpost

# ---- runtime ----------------------------------------------------------------
# Chromium's headless shell, maintained by the chromedp project.
FROM chromedp/headless-shell:151.0.7922.109
ARG SKIP_APT=""
RUN if [ -z "$SKIP_APT" ]; then \
      apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates tzdata tini fonts-noto-core fonts-noto-cjk fonts-noto-color-emoji fonts-liberation \
      && rm -rf /var/lib/apt/lists/*; \
    fi \
 && useradd --system --uid 10001 --user-group --home-dir /data --shell /usr/sbin/nologin panelpost \
 && mkdir -p /data && chown panelpost:panelpost /data
COPY --from=build /out/panelpost /usr/local/bin/panelpost
ENV PANELPOST_DATA=/data \
    PANELPOST_ADDR=:8080 \
    PANELPOST_CHROME_PATH=/headless-shell/headless-shell \
    LANG=en_US.UTF-8
USER panelpost
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s CMD ["/usr/local/bin/panelpost", "healthcheck"]
# tini reaps Chromium's child processes; the entrypoint falls back when absent.
ENTRYPOINT ["/bin/sh", "-c", "if command -v tini >/dev/null; then exec tini -- /usr/local/bin/panelpost \"$@\"; else exec /usr/local/bin/panelpost \"$@\"; fi", "--"]
CMD ["serve"]
