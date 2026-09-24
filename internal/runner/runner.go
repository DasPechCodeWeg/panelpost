// Package runner schedules and executes report runs: resolve the period,
// render each target, compose the PDF and deliver it.
package runner

import (
	"context"
	"errors"
	"fmt"
	site "github.com/DasPechCodeWeg/panelpost/internal/brand"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/compose"
	"github.com/DasPechCodeWeg/panelpost/internal/deliver"
	"github.com/DasPechCodeWeg/panelpost/internal/grafana"
	"github.com/DasPechCodeWeg/panelpost/internal/license"
	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/render"
	"github.com/DasPechCodeWeg/panelpost/internal/store"
	"github.com/DasPechCodeWeg/panelpost/internal/timerange"
)

// Settings keys shared with the web UI.
const (
	KeyGeneral      = "general"
	KeyBranding     = "branding"
	KeySMTP         = "smtp"
	KeySMTPPassword = "smtp.password"
)

// Runner owns scheduling and execution.
type Runner struct {
	Store    *store.Store
	Browser  *render.Browser
	License  *license.Manager
	DataDir  string
	Logger   *slog.Logger
	Workers  int
	HTTP     *http.Client
	Now      func() time.Time
	queue    chan job
	mu       sync.Mutex
	next     map[string]time.Time // report ID -> next scheduled run
	wake     chan struct{}
	started  bool
	inflight sync.WaitGroup
}

type job struct {
	run    *model.Run
	report *model.Report
	done   chan struct{}
}

// ReportsDir is where finished PDFs are kept.
func (r *Runner) ReportsDir() string { return filepath.Join(r.DataDir, "reports") }

// OutboxDir receives copies of PDFs for reports with folder delivery.
func (r *Runner) OutboxDir() string { return filepath.Join(r.DataDir, "outbox") }

// AssetsDir holds uploaded logos.
func (r *Runner) AssetsDir() string { return filepath.Join(r.DataDir, "assets") }

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) log() *slog.Logger {
	if r.Logger == nil {
		return slog.Default()
	}
	return r.Logger
}

// Start launches the scheduler and workers until ctx ends.
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	if r.Workers <= 0 {
		r.Workers = 1
	}
	r.queue = make(chan job, 256)
	r.wake = make(chan struct{}, 1)
	r.next = map[string]time.Time{}
	r.mu.Unlock()

	if n, err := r.Store.FailInterrupted(); err == nil && n > 0 {
		r.log().Warn("marked interrupted runs as failed", "count", n)
	}
	for i := 0; i < r.Workers; i++ {
		go r.worker(ctx)
	}
	go r.scheduleLoop(ctx)
	go r.retentionLoop(ctx)
}

// Wait blocks until running jobs finish (used at shutdown and in tests).
func (r *Runner) Wait() { r.inflight.Wait() }

// Reload recomputes schedules after reports change.
func (r *Runner) Reload() {
	r.mu.Lock()
	if r.next != nil {
		r.next = map[string]time.Time{}
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// NextRun returns when a report is next scheduled, if it is.
func (r *Runner) NextRun(rep *model.Report) time.Time {
	if !rep.Enabled {
		return time.Time{}
	}
	g := r.General()
	fallback, _ := time.LoadLocation(g.Timezone)
	next, err := rep.Schedule.Next(r.now(), fallback)
	if err != nil {
		return time.Time{}
	}
	return next
}

func (r *Runner) scheduleLoop(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		r.scheduleDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-r.wake:
		}
	}
}

func (r *Runner) scheduleDue(ctx context.Context) {
	reports, err := r.Store.ListReports()
	if err != nil {
		r.log().Error("list reports", "err", err)
		return
	}
	g := r.General()
	fallback, _ := time.LoadLocation(g.Timezone)
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, rep := range reports {
		seen[rep.ID] = true
		if !rep.Enabled {
			delete(r.next, rep.ID)
			continue
		}
		next, ok := r.next[rep.ID]
		if !ok {
			// First sight (startup or edit): schedule strictly in the future,
			// so a restart never fires a burst of missed runs.
			n, err := rep.Schedule.Next(now, fallback)
			if err == nil && !n.IsZero() {
				r.next[rep.ID] = n
			}
			continue
		}
		if !now.Before(next) {
			if _, err := r.enqueueLocked(rep, model.TriggerSchedule); err != nil {
				r.log().Error("enqueue scheduled run", "report", rep.Name, "err", err)
			}
			if n, err := rep.Schedule.Next(now, fallback); err == nil && !n.IsZero() {
				r.next[rep.ID] = n
			} else {
				delete(r.next, rep.ID)
			}
		}
	}
	for id := range r.next {
		if !seen[id] {
			delete(r.next, id)
		}
	}
}

// Enqueue queues a run of a report and returns it immediately.
func (r *Runner) Enqueue(rep *model.Report, trigger string) (*model.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enqueueLocked(rep, trigger)
}

func (r *Runner) enqueueLocked(rep *model.Report, trigger string) (*model.Run, error) {
	if r.queue == nil {
		return nil, errors.New("runner is not started")
	}
	run := &model.Run{ReportID: rep.ID, ReportName: rep.Name, Trigger: trigger, Status: model.RunQueued}
	if err := r.Store.CreateRun(run); err != nil {
		return nil, err
	}
	r.inflight.Add(1)
	select {
	case r.queue <- job{run: run, report: rep}:
	default:
		r.inflight.Done()
		run.Status, run.Error = model.RunFailed, "too many runs are queued; try again later"
		run.FinishedAt = r.now()
		_ = r.Store.UpdateRun(run)
		return run, errors.New(run.Error)
	}
	return run, nil
}

func (r *Runner) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-r.queue:
			r.execute(ctx, j.run, j.report)
			r.inflight.Done()
		}
	}
}

// General returns the installation settings with defaults applied.
func (r *Runner) General() model.General {
	var g model.General
	_, _ = r.Store.GetJSON(KeyGeneral, &g)
	g.Normalize()
	return g
}

// SMTP returns the mail settings including the decrypted password.
func (r *Runner) SMTP() model.SMTP {
	var s model.SMTP
	_, _ = r.Store.GetJSON(KeySMTP, &s)
	s.Password, _ = r.Store.GetSecret(KeySMTPPassword)
	return s
}

// Branding returns the global branding.
func (r *Runner) Branding() model.Branding {
	var b model.Branding
	_, _ = r.Store.GetJSON(KeyBranding, &b)
	return b
}

// allowed checks edition limits that can change after a report was saved,
// for example when a license expires.
func (r *Runner) allowed(rep *model.Report, lim license.Limits) error {
	if lim.MaxReports > 0 {
		reports, err := r.Store.ListReports()
		if err != nil {
			return err
		}
		sort.Slice(reports, func(i, j int) bool { return reports[i].CreatedAt.Before(reports[j].CreatedAt) })
		for i, other := range reports {
			if other.ID == rep.ID && i >= lim.MaxReports {
				return fmt.Errorf("the Community edition runs up to %d reports; activate a license to run this one", lim.MaxReports)
			}
		}
	}
	if lim.MaxConnections > 0 {
		conns, err := r.Store.ListConnections()
		if err != nil {
			return err
		}
		sort.Slice(conns, func(i, j int) bool { return conns[i].CreatedAt.Before(conns[j].CreatedAt) })
		for i, c := range conns {
			if c.ID == rep.ConnectionID && i >= lim.MaxConnections {
				return fmt.Errorf("this edition allows %d Grafana connection(s); activate a license to use more", lim.MaxConnections)
			}
		}
	}
	return nil
}

// target is one PDF to produce within a run.
type target struct {
	value      string // burst value; empty for normal runs
	label      string
	recipients []string
	branding   model.Branding
}

func (r *Runner) execute(ctx context.Context, run *model.Run, rep *model.Report) {
	log := r.log().With("report", rep.Name, "run", run.ID, "trigger", run.Trigger)
	run.Status = model.RunRunning
	run.StartedAt = r.now()
	_ = r.Store.UpdateRun(run)

	err := r.executeTargets(ctx, run, rep, log)
	run.FinishedAt = r.now()
	failed := 0
	for _, o := range run.Outputs {
		if o.Error != "" {
			failed++
		}
	}
	switch {
	case err != nil:
		run.Status, run.Error = model.RunFailed, err.Error()
	case failed == len(run.Outputs) && failed > 0:
		run.Status, run.Error = model.RunFailed, run.Outputs[0].Error
	case failed > 0:
		run.Status = model.RunPartial
		run.Error = fmt.Sprintf("%d of %d outputs failed", failed, len(run.Outputs))
	default:
		run.Status = model.RunSuccess
		for _, o := range run.Outputs {
			if len(o.Warnings) > 0 {
				run.Status = model.RunPartial
				run.Error = "finished with warnings"
				break
			}
		}
	}
	if uerr := r.Store.UpdateRun(run); uerr != nil {
		log.Error("store run", "err", uerr)
	}
	log.Info("run finished", "status", run.Status, "duration", run.Duration().Round(time.Millisecond), "error", run.Error)
	if run.Status == model.RunFailed && run.Trigger == model.TriggerSchedule {
		r.notifyFailure(ctx, run, rep)
	}
}

func (r *Runner) executeTargets(ctx context.Context, run *model.Run, rep *model.Report, log *slog.Logger) error {
	lic := r.License.Current()
	lim := lic.Limits()
	if err := r.allowed(rep, lim); err != nil {
		return err
	}
	conn, err := r.Store.GetConnection(rep.ConnectionID)
	if err != nil {
		return fmt.Errorf("load Grafana connection: %w", err)
	}
	g := r.General()
	rng, err := r.resolveRange(rep, g)
	if err != nil {
		return err
	}
	run.RangeFrom, run.RangeTo = rng.From, rng.To
	run.Period = rng.Label() + " · " + rng.Zone()
	_ = r.Store.UpdateRun(run)

	targets := []target{{recipients: nil}}
	if rep.Burst.Enabled {
		if !lim.Bursting {
			return errors.New("per-recipient bursting needs a Pro or Business license")
		}
		targets = targets[:0]
		for i, t := range rep.Burst.Targets {
			if lim.MaxBurstTargets > 0 && i >= lim.MaxBurstTargets {
				log.Warn("burst targets beyond the edition limit were skipped", "limit", lim.MaxBurstTargets)
				break
			}
			label := t.Label
			if label == "" {
				label = t.Value
			}
			targets = append(targets, target{value: t.Value, label: label, recipients: t.Recipients, branding: t.Branding})
		}
	}
	dashPath, title := r.dashboardPath(ctx, conn, rep)
	runDir := filepath.Join(r.ReportsDir(), run.ID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	for _, t := range targets {
		out := r.renderTarget(ctx, run, rep, conn, dashPath, title, rng, t, lic, g, runDir)
		run.Outputs = append(run.Outputs, out)
		_ = r.Store.UpdateRun(run)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

func pathOf(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return ""
	}
	rest := raw[i+3:]
	if j := strings.Index(rest, "/"); j >= 0 {
		return strings.TrimRight(rest[j:], "/")
	}
	return ""
}

// produced is a rendered PDF and what happened while rendering it.
type produced struct {
	result   *compose.Result
	warnings []string
	brand    model.Branding
}

// produce renders one target to PDF without saving or delivering it.
func (r *Runner) produce(ctx context.Context, rep *model.Report, conn *model.Connection, dashPath, title string,
	rng timerange.Range, t target, lic license.License, g model.General) (*produced, error) {
	lim := lic.Limits()
	vars := make([]model.Variable, 0, len(rep.Variables)+1)
	for _, v := range rep.Variables {
		if t.value != "" && v.Name == rep.Burst.Variable {
			continue
		}
		vars = append(vars, v)
	}
	if t.value != "" {
		vars = append(vars, model.Variable{Name: rep.Burst.Variable, Values: []string{t.value}})
	}
	tzName := rep.Timezone
	if tzName == "" {
		tzName = g.Timezone
	}
	q := grafana.DashboardQuery(conn.OrgID, rng.From, rng.To, tzName, rep.Layout.Theme, vars)
	headers := map[string]string{"Authorization": "Bearer " + conn.Token}
	for k, v := range conn.ExtraHeaders {
		headers[k] = v
	}
	page, err := r.Browser.Open(ctx, render.Request{
		URL:         strings.TrimRight(conn.URL, "/") + dashPath + "?" + q.Encode(),
		Origin:      conn.URL,
		Headers:     headers,
		InsecureTLS: conn.InsecureTLS,
		Width:       rep.Layout.Width,
		Timezone:    tzName,
		ExpandRows:  rep.Layout.ExpandRows,
		Timeout:     time.Duration(g.RenderTimeoutSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	defer page.Close()

	brand := r.Branding()
	if lim.Branding {
		brand = brand.Merge(rep.Branding)
		if lim.WhiteLabel {
			brand = brand.Merge(t.branding)
		}
	} else {
		// Community edition: keep the company name, not the visual identity.
		merged := brand.Merge(rep.Branding)
		brand = model.Branding{CompanyName: merged.CompanyName, Intro: merged.Intro}
	}
	var logo []byte
	if brand.LogoAsset != "" {
		logo, _ = os.ReadFile(filepath.Join(r.AssetsDir(), filepath.Base(brand.LogoAsset)))
	}
	var kvs []compose.KV
	for _, v := range vars {
		if t.value != "" && v.Name == rep.Burst.Variable {
			continue // the target label already says who it is for
		}
		kvs = append(kvs, compose.KV{Key: v.Name, Value: strings.Join(v.Values, ", ")})
	}
	doc := compose.Document{
		Title:       rep.Name,
		Dashboard:   title,
		Period:      rng.Label(),
		TimeZone:    tzName,
		Variables:   kvs,
		GeneratedAt: r.now().In(rng.From.Location()),
		Branding: compose.Branding{CompanyName: brand.CompanyName, Logo: logo, Accent: brand.AccentColor,
			FooterText: brand.FooterText, Intro: brand.Intro},
		Credit:    !lim.RemoveCredit,
		Paper:     rep.Layout.Paper,
		Landscape: rep.Layout.Orientation != "portrait",
		Cover:     rep.Layout.CoverPage,
		Mode:      rep.Layout.Mode,
		Columns:   rep.Layout.Columns,
		Theme:     rep.Layout.Theme,
	}
	if t.label != "" {
		doc.Subtitle = "Prepared for " + t.label
	}
	if g.IncludeNotes {
		doc.Notes = page.Warnings
	}
	res, err := compose.Build(ctx, r.Browser, page, doc, compose.Options{Format: g.ImageFormat, Quality: 88, Panels: rep.Layout.Panels})
	if err != nil {
		return nil, err
	}
	return &produced{result: res, warnings: page.Warnings, brand: brand}, nil
}

func (r *Runner) renderTarget(ctx context.Context, run *model.Run, rep *model.Report, conn *model.Connection, dashPath, title string,
	rng timerange.Range, t target, lic license.License, g model.General, runDir string) model.Output {
	out := model.Output{Target: t.label}
	p, err := r.produce(ctx, rep, conn, dashPath, title, rng, t, lic, g)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Warnings = append(out.Warnings, p.warnings...)
	out.Pages = p.result.Pages
	out.Size = int64(len(p.result.PDF))
	name := r.fileName(rep, t, rng)
	path := filepath.Join(runDir, name)
	if err := os.WriteFile(path, p.result.PDF, 0o644); err != nil {
		out.Error = "save PDF: " + err.Error()
		return out
	}
	out.File = filepath.Join(run.ID, name)
	if run.Trigger == model.TriggerPreview {
		return out
	}
	r.deliver(ctx, run, rep, t, rng, title, p.brand, lic.Limits(), p.result, name, &out)
	return out
}

// Rendered is the result of an on-demand render.
type Rendered struct {
	PDF      []byte
	Pages    int
	FileName string
	Warnings []string
	From, To time.Time
}

// RenderNow renders a report (saved or ad hoc) synchronously without
// delivering it. burstValue selects one burst target; empty renders the
// report as configured without bursting.
func (r *Runner) RenderNow(ctx context.Context, rep *model.Report, burstValue string) (*Rendered, error) {
	lic := r.License.Current()
	conn, err := r.Store.GetConnection(rep.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("load Grafana connection: %w", err)
	}
	if rep.ID != "" {
		if err := r.allowed(rep, lic.Limits()); err != nil {
			return nil, err
		}
	}
	g := r.General()
	rng, err := r.resolveRange(rep, g)
	if err != nil {
		return nil, err
	}
	t := target{}
	if burstValue != "" {
		for _, bt := range rep.Burst.Targets {
			if bt.Value == burstValue {
				label := bt.Label
				if label == "" {
					label = bt.Value
				}
				t = target{value: bt.Value, label: label, recipients: bt.Recipients, branding: bt.Branding}
			}
		}
		if t.value == "" {
			return nil, fmt.Errorf("no burst target with value %q", burstValue)
		}
	}
	dashPath, title := r.dashboardPath(ctx, conn, rep)
	p, err := r.produce(ctx, rep, conn, dashPath, title, rng, t, lic, g)
	if err != nil {
		return nil, err
	}
	return &Rendered{PDF: p.result.PDF, Pages: p.result.Pages, FileName: r.fileName(rep, t, rng), Warnings: p.warnings, From: rng.From, To: rng.To}, nil
}

func (r *Runner) resolveRange(rep *model.Report, g model.General) (timerange.Range, error) {
	tzName := rep.Timezone
	if tzName == "" {
		tzName = g.Timezone
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return timerange.Range{}, fmt.Errorf("time zone %q: %w", tzName, err)
	}
	fromExpr, toExpr, err := rep.Time.Expressions()
	if err != nil {
		return timerange.Range{}, err
	}
	return timerange.Resolve(fromExpr, toExpr, r.now(), timerange.Options{Location: loc, WeekStartsSunday: rep.WeekStartsSunday || g.WeekStartsSunday})
}

// dashboardPath finds the dashboard's URL path below the Grafana root and its title.
func (r *Runner) dashboardPath(ctx context.Context, conn *model.Connection, rep *model.Report) (string, string) {
	title := rep.DashboardTitle
	dashPath := "/d/" + rep.DashboardUID
	client, err := grafana.New(conn)
	if err != nil {
		return dashPath, nonEmpty(title, rep.Name)
	}
	d, err := client.GetDashboard(ctx, rep.DashboardUID)
	if err != nil {
		r.log().Warn("could not read dashboard metadata; rendering anyway", "report", rep.Name, "err", err)
		return dashPath, nonEmpty(title, rep.Name)
	}
	if d.URL != "" && strings.HasPrefix(d.URL, "/") {
		dashPath = d.URL
		// meta.url includes Grafana's sub path; strip it to avoid doubling.
		if p := pathOf(strings.TrimRight(conn.URL, "/")); p != "" && strings.HasPrefix(d.URL, p+"/") {
			dashPath = strings.TrimPrefix(d.URL, p)
		}
	}
	return dashPath, nonEmpty(d.Title, nonEmpty(title, rep.Name))
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func (r *Runner) fileName(rep *model.Report, t target, rng timerange.Range) string {
	tmpl := rep.FileName
	if tmpl == "" {
		tmpl = "{{report}}{{ - target}} {{from}}"
	}
	repl := strings.NewReplacer(
		"{{report}}", rep.Name,
		"{{ - target}}", map[bool]string{true: " - " + t.label, false: ""}[t.label != ""],
		"{{target}}", t.label,
		"{{from}}", rng.From.Format("2006-01-02"),
		"{{to}}", rng.To.Format("2006-01-02"),
		"{{date}}", r.now().Format("2006-01-02"),
	)
	name := deliver.SafeFileName(repl.Replace(tmpl))
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name += ".pdf"
	}
	return name
}

func (r *Runner) deliver(ctx context.Context, run *model.Run, rep *model.Report, t target, rng timerange.Range, title string,
	brand model.Branding, lim license.Limits, res *compose.Result, name string, out *model.Output) {
	ph := deliver.Placeholders{Report: rep.Name, Dashboard: title, Period: rng.Label(), Target: t.label,
		Company: brand.CompanyName, Date: r.now().Format("2 January 2006"), Pages: res.Pages}
	var errs []string

	to := rep.Delivery.Email.To
	if t.value != "" {
		to = t.recipients
	}
	if len(to)+len(rep.Delivery.Email.Cc)+len(rep.Delivery.Email.Bcc) > 0 {
		subject := rep.Delivery.Email.Subject
		if subject == "" {
			subject = deliver.DefaultSubject
			if t.label != "" {
				subject = "{{report}} · {{target}} · {{period}}"
			}
		}
		body := rep.Delivery.Email.Body
		if body == "" {
			body = deliver.DefaultBody
		}
		if brand.CompanyName != "" && rep.Delivery.Email.Body == "" {
			body += "\n\n— " + brand.CompanyName
		}
		if !lim.RemoveCredit {
			body += "\n\nSent with Panelpost · " + site.URL("")
		}
		fromName := ""
		if lim.WhiteLabel && t.branding.CompanyName != "" {
			fromName = t.branding.CompanyName
		}
		m := &deliver.Mailer{SMTP: r.SMTP()}
		err := m.Send(ctx, deliver.Email{To: to, Cc: rep.Delivery.Email.Cc, Bcc: rep.Delivery.Email.Bcc,
			Subject: ph.Expand(subject), Text: ph.Expand(body), FromName: fromName,
			Attachments: []deliver.Attachment{{Name: name, Data: res.PDF, ContentType: "application/pdf"}}})
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			all := append(append(append([]string{}, to...), rep.Delivery.Email.Cc...), rep.Delivery.Email.Bcc...)
			out.Delivered = append(out.Delivered, "email: "+strings.Join(all, ", "))
		}
	}
	if len(rep.Delivery.Webhooks) > 0 {
		if !lim.Webhooks {
			errs = append(errs, "webhook delivery needs a Pro or Business license")
		} else {
			for _, w := range rep.Delivery.Webhooks {
				meta := deliver.WebhookPayload{Event: "report.delivered", Report: rep.Name, ReportID: rep.ID, RunID: run.ID,
					Dashboard: title, Target: t.label, From: rng.From, To: rng.To, Pages: res.Pages, FileName: name}
				if err := deliver.PostWebhook(ctx, r.HTTP, w.URL, w.Secret, meta, res.PDF); err != nil {
					errs = append(errs, "webhook "+hostOf(w.URL)+": "+err.Error())
				} else {
					out.Delivered = append(out.Delivered, "webhook: "+hostOf(w.URL))
				}
			}
		}
	}
	if rep.Delivery.Folder {
		if path, err := deliver.WriteToFolder(r.OutboxDir(), rep.Name, name, res.PDF); err != nil {
			errs = append(errs, "folder: "+err.Error())
		} else {
			out.Delivered = append(out.Delivered, "folder: "+path)
		}
	}
	if len(errs) > 0 {
		out.Error = strings.Join(errs, "; ")
	}
}

func hostOf(u string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	return s
}

func (r *Runner) notifyFailure(ctx context.Context, run *model.Run, rep *model.Report) {
	g := r.General()
	if g.NotifyEmail == "" {
		return
	}
	smtp := r.SMTP()
	if !smtp.Configured() {
		return
	}
	link := ""
	if g.PublicURL != "" {
		link = "\n\nDetails: " + g.PublicURL + "/runs/" + run.ID
	}
	m := &deliver.Mailer{SMTP: smtp}
	err := m.Send(ctx, deliver.Email{To: []string{g.NotifyEmail},
		Subject: "Panelpost: report \"" + rep.Name + "\" failed",
		Text:    "The scheduled report \"" + rep.Name + "\" could not be produced.\n\nError: " + run.Error + link})
	if err != nil {
		r.log().Warn("could not send failure notification", "err", err)
	}
}

func (r *Runner) retentionLoop(ctx context.Context) {
	for {
		r.cleanup()
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

// cleanup deletes runs and their files beyond the edition's history.
func (r *Runner) cleanup() {
	days := r.License.Current().Limits().HistoryDays
	if days <= 0 {
		return
	}
	old, err := r.Store.RunsOlderThan(r.now().Add(-time.Duration(days) * 24 * time.Hour))
	if err != nil {
		r.log().Error("retention", "err", err)
		return
	}
	for _, run := range old {
		_ = os.RemoveAll(filepath.Join(r.ReportsDir(), filepath.Base(run.ID)))
		_ = r.Store.DeleteRun(run.ID)
	}
	if len(old) > 0 {
		r.log().Info("removed old runs", "count", len(old), "older_than_days", days)
	}
}
