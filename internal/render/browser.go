// Package render drives a headless Chromium to capture Grafana dashboards
// and to print Panelpost's report HTML to PDF.
package render

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/chromedp/chromedp"
)

// Options configure the browser.
type Options struct {
	// ExecPath is the Chromium or Chrome binary. Empty means auto-detect.
	ExecPath string
	// NoSandbox disables Chromium's sandbox, which is required in most
	// containers. The browser only ever loads your own Grafana.
	NoSandbox bool
	// MaxConcurrent limits simultaneous captures (memory bound).
	MaxConcurrent int
	Logger        *slog.Logger
}

// Browser is a lazily started, shared Chromium process.
type Browser struct {
	opts Options
	sem  chan struct{}

	mu            sync.Mutex
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
	// plainTabs is set when the browser cannot create isolated browser
	// contexts (Chrome's new headless mode answers "no browser is open").
	plainTabs bool
}

// New creates a browser; Chromium starts on first use.
func New(opts Options) *Browser {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 2
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Browser{opts: opts, sem: make(chan struct{}, opts.MaxConcurrent)}
}

// candidates are checked when no executable is configured.
var candidates = []string{
	"/headless-shell/headless-shell",
	"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome", "headless_shell",
	"/usr/bin/chromium", "/usr/bin/chromium-browser",
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
}

// FindExecutable returns the Chromium binary that will be used.
func FindExecutable(configured string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			if p, lerr := exec.LookPath(configured); lerr == nil {
				return p, nil
			}
			return "", fmt.Errorf("browser %q not found: %w", configured, err)
		}
		return configured, nil
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", errors.New("no Chromium or Chrome found; install one or set PANELPOST_CHROME_PATH")
}

func (b *Browser) start() (context.Context, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.browserCtx != nil && b.browserCtx.Err() == nil {
		return b.browserCtx, nil
	}
	b.stopLocked()
	path, err := FindExecutable(b.opts.ExecPath)
	if err != nil {
		return nil, err
	}
	flags := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		chromedp.Flag("lang", "en-US"),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("mute-audio", true),
		// Grafana's frontend crashes at boot with a POSIX locale ("en-US@posix").
		chromedp.Env("LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8", "LANGUAGE=en_US"),
		chromedp.WindowSize(1400, 1000),
	)
	if b.opts.NoSandbox {
		flags = append(flags, chromedp.NoSandbox)
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), flags...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(browserCtx); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("start browser %s: %w", path, err)
	}
	b.opts.Logger.Info("browser started", "path", path)
	b.allocCancel, b.browserCtx, b.browserCancel = allocCancel, browserCtx, browserCancel
	return browserCtx, nil
}

func (b *Browser) stopLocked() {
	if b.browserCancel != nil {
		b.browserCancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
	b.browserCtx, b.browserCancel, b.allocCancel = nil, nil, nil
}

// Close stops Chromium.
func (b *Browser) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopLocked()
}

// newTab opens an isolated tab (its own cookie jar) and reserves a slot.
func (b *Browser) newTab(ctx context.Context) (context.Context, func(), error) {
	select {
	case b.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	release := func() { <-b.sem }
	browserCtx, err := b.start()
	if err != nil {
		release()
		return nil, nil, err
	}
	b.mu.Lock()
	plain := b.plainTabs
	b.mu.Unlock()
	var tabCtx context.Context
	var cancel context.CancelFunc
	if !plain {
		// Prefer an isolated browser context: its own cookies and cache.
		tabCtx, cancel = chromedp.NewContext(browserCtx, chromedp.WithNewBrowserContext())
		if err := chromedp.Run(tabCtx); err != nil {
			cancel()
			if !strings.Contains(err.Error(), "no browser is open") {
				release()
				return nil, nil, fmt.Errorf("open browser tab: %w", err)
			}
			b.mu.Lock()
			b.plainTabs = true
			b.mu.Unlock()
			b.opts.Logger.Info("browser does not support isolated contexts; using plain tabs")
			plain = true
		}
	}
	if plain {
		tabCtx, cancel = chromedp.NewContext(browserCtx)
		if err := chromedp.Run(tabCtx); err != nil {
			cancel()
			release()
			return nil, nil, fmt.Errorf("open browser tab: %w", err)
		}
	}
	// Stop the tab when the caller's context ends.
	stop := context.AfterFunc(ctx, cancel)
	return tabCtx, func() {
		stop()
		cancel()
		release()
	}, nil
}
