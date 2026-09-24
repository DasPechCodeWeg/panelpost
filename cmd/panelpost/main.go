// Command panelpost schedules and delivers PDF reports of Grafana dashboards.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // IANA time zones even on hosts without tzdata

	_ "golang.org/x/crypto/x509roots/fallback" // CA roots for minimal containers

	"github.com/DasPechCodeWeg/panelpost/internal/license"
	"github.com/DasPechCodeWeg/panelpost/internal/render"
	"github.com/DasPechCodeWeg/panelpost/internal/runner"
	"github.com/DasPechCodeWeg/panelpost/internal/secret"
	"github.com/DasPechCodeWeg/panelpost/internal/store"
	"github.com/DasPechCodeWeg/panelpost/internal/web"
)

// version is set with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	web.Version = version
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "render":
		err = renderCmd(args)
	case "license":
		err = licenseCmd(args)
	case "healthcheck":
		err = healthcheck()
	case "version", "--version":
		fmt.Println("panelpost", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`Panelpost: scheduled PDF reports for Grafana.

Usage:
  panelpost [serve]            start the web UI and scheduler (default)
  panelpost render [flags]     render one dashboard to a PDF file
  panelpost license verify KEY show what a license key contains
  panelpost healthcheck        exit 0 when the local server is healthy
  panelpost version

Configuration (environment):
  PANELPOST_ADDR               listen address (default :8080)
  PANELPOST_DATA               data directory (default ./data, /data in Docker)
  PANELPOST_ADMIN_PASSWORD     set or reset the admin password at start-up
  PANELPOST_SECRET_KEY         key used to encrypt stored tokens (default: generated key file)
  PANELPOST_CHROME_PATH        Chromium/Chrome binary (default: auto-detect)
  PANELPOST_CHROME_SANDBOX     set to true to keep Chromium's sandbox enabled
  PANELPOST_WORKERS            reports rendered in parallel (default 1)
  PANELPOST_LOG_FORMAT         text or json (default text)
`)
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func logger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if env("PANELPOST_LOG_LEVEL", "") == "debug" {
		opts.Level = slog.LevelDebug
	}
	if env("PANELPOST_LOG_FORMAT", "text") == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func defaultDataDir() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "/data"
	}
	return "./data"
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", env("PANELPOST_ADDR", ":8080"), "listen address")
	dataDir := fs.String("data", env("PANELPOST_DATA", defaultDataDir()), "data directory")
	_ = fs.Parse(args)

	log := logger()
	slog.SetDefault(log)
	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	box, err := secret.LoadOrCreate(os.Getenv("PANELPOST_SECRET_KEY"), *dataDir)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(*dataDir, "panelpost.db"), box)
	if err != nil {
		return err
	}
	defer st.Close()

	hostname, _ := os.Hostname()
	lic := license.NewManager(license.Options{
		Settings:      st,
		Providers:     license.DefaultProviders(),
		PublicKeys:    license.EmbeddedPublicKeys(),
		InstanceLabel: "panelpost@" + hostname,
		Logger:        log,
	})
	workers, _ := strconv.Atoi(env("PANELPOST_WORKERS", "1"))
	browser := render.New(render.Options{
		ExecPath:      os.Getenv("PANELPOST_CHROME_PATH"),
		NoSandbox:     !strings.EqualFold(os.Getenv("PANELPOST_CHROME_SANDBOX"), "true"),
		MaxConcurrent: max(2, workers+1),
		Logger:        log,
	})
	defer browser.Close()
	if path, err := render.FindExecutable(os.Getenv("PANELPOST_CHROME_PATH")); err != nil {
		log.Warn("no browser found; rendering will fail until one is installed", "err", err)
	} else {
		log.Info("using browser", "path", path)
	}

	run := &runner.Runner{Store: st, Browser: browser, License: lic, DataDir: *dataDir, Logger: log, Workers: max(1, workers)}
	srv, err := web.New(&web.Server{Store: st, Runner: run, License: lic, Logger: log})
	if err != nil {
		return err
	}
	if pw := os.Getenv("PANELPOST_ADMIN_PASSWORD"); pw != "" {
		if err := srv.SetPassword(pw); err != nil {
			return fmt.Errorf("PANELPOST_ADMIN_PASSWORD: %w", err)
		}
		log.Info("admin password set from PANELPOST_ADMIN_PASSWORD")
	}
	if !srv.HasPassword() {
		srv.SetupToken = setupCode()
		log.Info("first start: open the web UI and enter this setup code to create the admin password",
			"setup_code", srv.SetupToken, "url", "http://localhost"+portOf(*addr)+"/setup?token="+srv.SetupToken)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go lic.Run(ctx)
	runCtx, cancelRuns := context.WithCancel(context.Background())
	defer cancelRuns()
	run.Start(runCtx)

	httpSrv := &http.Server{Addr: *addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		log.Info("Panelpost is running", "version", version, "addr", *addr, "data", *dataDir, "edition", lic.Current().Tier.Title())
		errCh <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down; waiting up to 60s for running reports")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = httpSrv.Shutdown(shutdownCtx)
		cancel()
		done := make(chan struct{})
		go func() { run.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			log.Warn("reports still running at shutdown were cancelled")
		}
	}
	return nil
}

func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":8080"
}

func setupCode() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

func healthcheck() error {
	port := portOf(env("PANELPOST_ADDR", ":8080"))
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}
