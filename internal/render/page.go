package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/security"
	"github.com/chromedp/chromedp"
)

// Request describes one dashboard capture.
type Request struct {
	// URL is the full dashboard URL including query parameters.
	URL string
	// Origin is the Grafana base URL; credentials are only ever sent to
	// requests below it, never to third-party hosts a panel may load.
	Origin string
	// Headers are added to Grafana requests (Authorization, proxy headers).
	Headers map[string]string
	// InsecureTLS accepts self-signed Grafana certificates.
	InsecureTLS bool
	// Width is the browser width in CSS pixels.
	Width int
	// Scale is the device pixel ratio; 2 gives print-quality images.
	Scale float64
	// Timezone overrides the browser time zone (IANA name).
	Timezone string
	// ExpandRows opens collapsed dashboard rows before capturing.
	ExpandRows bool
	// Timeout bounds the whole capture. Defaults to 90 seconds.
	Timeout time.Duration
	// MaxHeight caps very long dashboards, in CSS pixels.
	MaxHeight int
}

// Box is a rectangle in CSS pixels, relative to the top of the dashboard.
type Box struct {
	Title string  `json:"title"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Error string  `json:"error,omitempty"`
}

// Bottom returns the lower edge of the box.
func (b Box) Bottom() float64 { return b.Y + b.H }

// Geometry is the layout of a rendered dashboard.
type Geometry struct {
	Panels []Box   `json:"panels"`
	Rows   []Box   `json:"rows"`
	Bottom float64 `json:"bottom"` // lower edge of the last panel or row
	Scroll float64 `json:"scroll"` // scrollable height of the page
	View   float64 `json:"view"`   // viewport height
}

// Page is a loaded dashboard ready to be screenshotted.
type Page struct {
	ctx      context.Context
	close    func()
	Width    int
	Height   float64
	Scale    float64
	Geometry Geometry
	Warnings []string
	Elapsed  time.Duration
}

// Close releases the tab.
func (p *Page) Close() {
	if p.close != nil {
		p.close()
		p.close = nil
	}
}

// ErrLogin is returned when Grafana redirects to its login page.
var ErrLogin = errors.New("Grafana showed its login page: the service account token was not accepted for the dashboard page")

// tracker counts in-flight network requests to detect when Grafana is idle.
type tracker struct {
	mu       sync.Mutex
	inflight map[network.RequestID]time.Time
	last     time.Time
}

func newTracker() *tracker {
	return &tracker{inflight: map[network.RequestID]time.Time{}, last: time.Now()}
}

func (t *tracker) start(id network.RequestID, u string) {
	if strings.Contains(u, "/api/live/") {
		return // websocket upgrades never "finish"
	}
	t.mu.Lock()
	t.inflight[id] = time.Now()
	t.last = time.Now()
	t.mu.Unlock()
}

func (t *tracker) done(id network.RequestID) {
	t.mu.Lock()
	delete(t.inflight, id)
	t.last = time.Now()
	t.mu.Unlock()
}

// idleFor reports whether nothing (except long-hanging requests) has been in
// flight for at least d.
func (t *tracker) idleFor(d time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for id, started := range t.inflight {
		if now.Sub(started) > 45*time.Second {
			delete(t.inflight, id) // a hung request must not block the report forever
		}
	}
	return len(t.inflight) == 0 && now.Sub(t.last) >= d
}

// Open loads a dashboard and waits until it has finished rendering.
func (b *Browser) Open(parent context.Context, req Request) (*Page, error) {
	if req.Timeout <= 0 {
		req.Timeout = 90 * time.Second
	}
	if req.Width <= 0 {
		req.Width = 1400
	}
	if req.Scale <= 0 {
		req.Scale = 2
	}
	if req.MaxHeight <= 0 {
		req.MaxHeight = 20000
	}
	ctx, cancel := context.WithTimeout(parent, req.Timeout)
	tabCtx, closeTab, err := b.newTab(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	p := &Page{ctx: tabCtx, Width: req.Width, Scale: req.Scale, close: func() { closeTab(); cancel() }}
	started := time.Now()
	if err := p.load(ctx, req); err != nil {
		p.Close()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(err, ErrLogin) {
			return nil, fmt.Errorf("dashboard did not finish loading within %s: %w", req.Timeout, err)
		}
		return nil, err
	}
	p.Elapsed = time.Since(started)
	return p, nil
}

func (p *Page) load(ctx context.Context, req Request) error {
	origin := strings.TrimRight(req.Origin, "/")
	if _, err := url.Parse(origin); err != nil || origin == "" {
		return fmt.Errorf("invalid Grafana origin %q", req.Origin)
	}
	tr := newTracker()
	tabCtx := p.ctx
	chromedp.ListenTarget(tabCtx, func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			tr.start(e.RequestID, e.Request.URL)
		case *network.EventLoadingFinished:
			tr.done(e.RequestID)
		case *network.EventLoadingFailed:
			tr.done(e.RequestID)
		case *fetch.EventRequestPaused:
			go continueWithHeaders(tabCtx, e, req.Headers)
		}
	})
	loc := req.Timezone
	actions := []chromedp.Action{
		network.Enable(),
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: origin + "/*", RequestStage: fetch.RequestStageRequest}}),
		emulation.SetDeviceMetricsOverride(int64(req.Width), 1000, req.Scale, false),
		emulation.SetLocaleOverride().WithLocale("en-US"),
		emulation.SetEmulatedMedia().WithMedia("screen"),
		// Grafana mounts panels when an IntersectionObserver sees them, and
		// background tabs may never run it: keep the tab focused and in front.
		emulation.SetFocusEmulationEnabled(true),
		page.BringToFront(),
	}
	if loc != "" {
		actions = append(actions, emulation.SetTimezoneOverride(loc))
	}
	if req.InsecureTLS {
		actions = append(actions, security.SetIgnoreCertificateErrors(true))
	}
	if err := chromedp.Run(tabCtx, actions...); err != nil {
		return fmt.Errorf("prepare browser tab: %w", err)
	}
	if err := chromedp.Run(tabCtx, chromedp.Navigate(req.URL)); err != nil {
		return fmt.Errorf("open dashboard: %w", err)
	}
	if err := p.waitForDashboard(ctx); err != nil {
		return err
	}
	// Grafana only renders panels near the viewport. Grow the viewport to the
	// full dashboard height, let the new panels load (and expand rows that
	// appear), and repeat until the layout stops growing.
	height := 1000.0
	var geo Geometry
	for pass := 0; ; pass++ {
		if req.ExpandRows {
			if err := p.expandRows(ctx, tr); err != nil && pass == 0 {
				p.Warnings = append(p.Warnings, "could not expand collapsed rows: "+err.Error())
			}
		}
		if err := p.waitIdle(ctx, tr); err != nil {
			return err
		}
		var err error
		if geo, err = p.measure(); err != nil {
			return err
		}
		want := geo.Bottom + 24
		if geo.Scroll > geo.View+1 {
			// The page still scrolls: more content (possibly unrendered) below.
			want = math.Max(want, geo.Scroll)
		}
		want = math.Ceil(math.Max(want, 400))
		if want > float64(req.MaxHeight) {
			if height < float64(req.MaxHeight) {
				p.Warnings = append(p.Warnings, fmt.Sprintf("dashboard is %.0f px tall; only the first %d px are included", want, req.MaxHeight))
			}
			want = float64(req.MaxHeight)
		}
		// Stop once the viewport fits. The first pass may also shrink it, so
		// that short dashboards are captured at their natural height.
		if want == height || (want < height && pass > 0) || pass >= 8 {
			break
		}
		height = want
		// Keep the rendered surface within what Chromium can capture reliably.
		if device := height * p.Scale; device > 32000 {
			p.Scale = math.Max(1, math.Floor(32000/height*10)/10)
		}
		if err := chromedp.Run(tabCtx, emulation.SetDeviceMetricsOverride(int64(req.Width), int64(height), p.Scale, false)); err != nil {
			return err
		}
	}
	p.Height = height
	p.Geometry = geo
	for _, panel := range geo.Panels {
		if panel.Error != "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("panel %q shows an error: %s", nonEmpty(panel.Title, "(untitled)"), panel.Error))
		}
	}
	return nil
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// continueWithHeaders adds credentials to a paused Grafana request.
func continueWithHeaders(tabCtx context.Context, e *fetch.EventRequestPaused, extra map[string]string) {
	c := chromedp.FromContext(tabCtx)
	if c == nil || c.Target == nil {
		return
	}
	exec := cdp.WithExecutor(tabCtx, c.Target)
	merged := map[string]string{}
	for k, v := range e.Request.Headers {
		merged[k] = fmt.Sprint(v)
	}
	for k, v := range extra {
		for existing := range merged {
			if strings.EqualFold(existing, k) {
				delete(merged, existing)
			}
		}
		merged[k] = v
	}
	headers := make([]*fetch.HeaderEntry, 0, len(merged))
	for k, v := range merged {
		headers = append(headers, &fetch.HeaderEntry{Name: k, Value: v})
	}
	_ = fetch.ContinueRequest(e.RequestID).WithHeaders(headers).Do(exec)
}

// panelsJS returns the outermost panel containers. Grafana 11 and later use a
// section element, Grafana 10 a div with the same test id.
const panelsJS = `const panelEls = () => [...document.querySelectorAll('[data-testid^="data-testid Panel header"]')]
  .filter((e, _, all) => !all.some(o => o !== e && o.contains(e)));`

const stateJS = `(() => {
  ` + panelsJS + `
  const body = (document.body && document.body.innerText) || '';
  const s = {path: location.pathname, failed: body.indexOf('failed to load its application files') >= 0,
    booting: !!document.querySelector('[aria-label="Loading Grafana"]'),
    panels: panelEls().length,
    rows: document.querySelectorAll('[data-testid^="data-testid dashboard-row-title-"]').length,
    loading: document.querySelectorAll('[aria-label="Panel loading bar"]').length,
    text: ''};
  if (!s.panels) { s.text = body.slice(0, 400); }
  return JSON.stringify(s);
})()`

type pageState struct {
	Path    string `json:"path"`
	Failed  bool   `json:"failed"`
	Booting bool   `json:"booting"`
	Panels  int    `json:"panels"`
	Rows    int    `json:"rows"`
	Loading int    `json:"loading"`
	Text    string `json:"text"`
}

func (p *Page) state() (pageState, error) {
	var raw string
	var st pageState
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(stateJS, &raw)); err != nil {
		return st, err
	}
	err := json.Unmarshal([]byte(raw), &st)
	return st, err
}

// waitForDashboard waits for Grafana to boot and show the dashboard.
func (p *Page) waitForDashboard(ctx context.Context) error {
	var emptySince, rowsSince time.Time
	for {
		st, err := p.state()
		if err == nil {
			switch {
			case strings.HasSuffix(st.Path, "/login"):
				return ErrLogin
			case st.Failed:
				return errors.New("Grafana's web app failed to load in the browser; check the Grafana URL (root_url / sub path) and that Grafana is reachable from Panelpost")
			case st.Panels > 0:
				return nil
			case st.Rows > 0:
				// Row headers render before the panels. Only a dashboard whose
				// rows are all collapsed stays like this; give panels a moment.
				if rowsSince.IsZero() {
					rowsSince = time.Now()
				} else if time.Since(rowsSince) > 5*time.Second {
					return nil
				}
			case !st.Booting:
				lower := strings.ToLower(st.Text)
				if strings.Contains(lower, "dashboard not found") {
					return errors.New("Grafana says the dashboard was not found; check the dashboard and the organisation of the connection")
				}
				if strings.Contains(lower, "access denied") || strings.Contains(lower, "permission") && strings.Contains(lower, "dashboard") {
					return errors.New("the service account may not view this dashboard; give it Viewer access")
				}
				// An empty dashboard renders no panels at all; give it a moment.
				if emptySince.IsZero() {
					emptySince = time.Now()
				} else if time.Since(emptySince) > 8*time.Second {
					return errors.New("the dashboard has no panels to render")
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// waitIdle waits until no panel is loading and the network has been quiet.
func (p *Page) waitIdle(ctx context.Context, tr *tracker) error {
	start := time.Now()
	quiet := 0
	for {
		st, err := p.state()
		if err != nil {
			return err
		}
		if st.Loading == 0 && tr.idleFor(700*time.Millisecond) && time.Since(start) > 900*time.Millisecond {
			quiet++
			if quiet >= 2 {
				// Let canvases paint and CSS transitions settle.
				var ok bool
				_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.fonts ? document.fonts.ready.then(() => true) : true`, &ok,
					func(ep *runtime.EvaluateParams) *runtime.EvaluateParams { return ep.WithAwaitPromise(true) }))
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(400 * time.Millisecond):
				}
				return nil
			}
		} else {
			quiet = 0
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("panels were still loading (%d busy): %w", st.Loading, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (p *Page) expandRows(ctx context.Context, tr *tracker) error {
	for i := 0; i < 5; i++ {
		var clicked int
		err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
			// Grafana 13 has a toggle with aria-expanded, 11 and 12 label the title
			// button "Expand row", and 10 marks the whole row as collapsed.
			const btns = document.querySelectorAll([
				'button[data-testid^="data-testid dashboard-row-toggle-for-"][aria-expanded="false"]',
				'button[data-testid^="data-testid dashboard-row-title-"][aria-label="Expand row"]',
				'.dashboard-row--collapsed button[data-testid^="data-testid dashboard-row-title-"]',
			].join(', '));
			btns.forEach(b => b.click());
			return btns.length;
		})()`, &clicked))
		if err != nil {
			return err
		}
		if clicked == 0 {
			return nil
		}
		if err := p.waitIdle(ctx, tr); err != nil {
			return err
		}
	}
	return nil
}

const geometryJS = `(() => {
  ` + panelsJS + `
  const sy = window.scrollY, out = {panels: [], rows: [], bottom: 0};
  const prefix = 'data-testid Panel header ';
  panelEls().forEach(s => {
    const r = s.getBoundingClientRect();
    if (r.width < 2 || r.height < 2) return;
    const err = s.querySelector('[data-testid="data-testid Panel status error"]');
    let msg = '';
    if (err) {
      const m = s.querySelector('[data-testid="data-testid Panel data error message"]');
      msg = (m && m.innerText) || err.getAttribute('title') || err.getAttribute('aria-label') || 'error';
    }
    out.panels.push({title: (s.getAttribute('data-testid') || '').slice(prefix.length), x: r.left, y: r.top + sy, w: r.width, h: r.height, error: msg.trim().slice(0, 300)});
  });
  document.querySelectorAll('[data-testid^="data-testid dashboard-row-title-"]').forEach(t => {
    const h = t.closest('.dashboard-row-header') || t.parentElement;
    const r = h.getBoundingClientRect();
    out.rows.push({title: t.innerText.trim(), x: r.left, y: r.top + sy, w: r.width, h: r.height});
  });
  for (const b of out.panels.concat(out.rows)) out.bottom = Math.max(out.bottom, b.y + b.h);
  out.scroll = document.documentElement.scrollHeight;
  out.view = window.innerHeight;
  out.panels.sort((a, b) => a.y - b.y || a.x - b.x);
  out.rows.sort((a, b) => a.y - b.y);
  return JSON.stringify(out);
})()`

func (p *Page) measure() (Geometry, error) {
	var raw string
	var g Geometry
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(geometryJS, &raw)); err != nil {
		return g, fmt.Errorf("measure dashboard: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return g, err
	}
	return g, nil
}

const debugJS = `(() => {
  ` + panelsJS + `
  return JSON.stringify({
  url: location.href.slice(0, 160),
  title: document.title,
  viewport: [innerWidth, innerHeight, devicePixelRatio],
  scroll: document.documentElement.scrollHeight,
  sections: document.querySelectorAll('section').length,
  panelHeaders: panelEls().length,
  lazy: document.querySelectorAll('[class*="lazy" i], [data-testid*="lazy" i]').length,
  rows: document.querySelectorAll('[data-testid^="data-testid dashboard-row-title-"]').length,
  loading: document.querySelectorAll('[aria-label="Panel loading bar"]').length,
  visibility: document.visibilityState, focus: document.hasFocus(),
  toggles: Object.keys((window.grafanaBootData && grafanaBootData.settings && grafanaBootData.settings.featureToggles) || {})
    .filter(k => /layout|scene|v2|kubernetesDash|dashboardNew/i.test(k) && grafanaBootData.settings.featureToggles[k]),
  version: window.grafanaBootData && grafanaBootData.settings && grafanaBootData.settings.buildInfo && grafanaBootData.settings.buildInfo.version,
  testids: [...new Set([...document.querySelectorAll('[data-testid]')].map(e => e.getAttribute('data-testid').slice(0, 60)))].slice(0, 40),
  text: (document.body && document.body.innerText || '').replace(/\s+/g, ' ').slice(0, 600)
  });
})()`

// DebugState describes what the browser shows, for diagnosing failed renders.
func (p *Page) DebugState() string {
	var out string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(debugJS, &out)); err != nil {
		return "debug unavailable: " + err.Error()
	}
	return out
}

// Screenshot captures a region of the dashboard. Format is "png" or "jpeg".
func (p *Page) Screenshot(clip Box, format string, quality int) ([]byte, error) {
	if clip.W <= 0 || clip.H <= 0 {
		return nil, errors.New("empty screenshot region")
	}
	params := page.CaptureScreenshot().
		WithClip(&page.Viewport{X: clip.X, Y: clip.Y, Width: clip.W, Height: clip.H, Scale: 1}).
		WithFromSurface(true)
	if format == "jpeg" {
		if quality <= 0 || quality > 100 {
			quality = 90
		}
		params = params.WithFormat(page.CaptureScreenshotFormatJpeg).WithQuality(int64(quality))
	} else {
		params = params.WithFormat(page.CaptureScreenshotFormatPng)
	}
	var buf []byte
	err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		buf, err = params.Do(ctx)
		return err
	}))
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	return buf, nil
}
