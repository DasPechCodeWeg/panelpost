package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/schedule"
	"github.com/DasPechCodeWeg/panelpost/internal/store"
	"github.com/DasPechCodeWeg/panelpost/internal/timerange"
)

// Timezones offered in the UI; any IANA name can be typed.
var Timezones = []string{
	"UTC", "Europe/Amsterdam", "Europe/Berlin", "Europe/Brussels", "Europe/London", "Europe/Paris", "Europe/Madrid",
	"Europe/Rome", "Europe/Zurich", "Europe/Vienna", "Europe/Stockholm", "Europe/Oslo", "Europe/Copenhagen",
	"Europe/Helsinki", "Europe/Warsaw", "Europe/Prague", "Europe/Dublin", "Europe/Lisbon", "Europe/Athens",
	"Europe/Istanbul", "Europe/Kyiv", "Africa/Johannesburg", "Africa/Lagos", "Africa/Cairo", "Asia/Dubai",
	"Asia/Kolkata", "Asia/Singapore", "Asia/Hong_Kong", "Asia/Shanghai", "Asia/Tokyo", "Asia/Seoul",
	"Asia/Jakarta", "Australia/Sydney", "Australia/Melbourne", "Australia/Perth", "Pacific/Auckland",
	"America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles", "America/Phoenix",
	"America/Toronto", "America/Vancouver", "America/Mexico_City", "America/Sao_Paulo", "America/Buenos_Aires",
	"America/Bogota", "America/Santiago",
}

type reportRow struct {
	Report   *model.Report
	Conn     string
	Next     time.Time
	Last     *model.Run
	Schedule string
}

func (s *Server) reportsList(w http.ResponseWriter, r *http.Request) {
	reports, err := s.Store.ListReports()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	conns, _ := s.Store.ListConnections()
	names := map[string]string{}
	for _, c := range conns {
		names[c.ID] = c.Name
	}
	last, _ := s.Store.LastRuns()
	var rows []reportRow
	for _, rep := range reports {
		rows = append(rows, reportRow{Report: rep, Conn: names[rep.ConnectionID], Next: s.Runner.NextRun(rep),
			Last: last[rep.ID], Schedule: rep.Schedule.Describe()})
	}
	lim := s.License.Current().Limits()
	s.render(w, r, "reports", "Reports", "reports", map[string]any{
		"Rows":        rows,
		"Connections": len(conns),
		"AtLimit":     lim.MaxReports > 0 && len(reports) >= lim.MaxReports,
		"MaxReports":  lim.MaxReports,
	})
}

type reportForm struct {
	Report      *model.Report
	IsNew       bool
	Connections []*model.Connection
	Presets     []timerange.Preset
	Timezones   []string
	Assets      []string
	Runs        []*model.Run
	Upcoming    []time.Time
	VarLines    string
	PanelLines  string
	HookLines   string
	BurstLines  string
	Weekdays    []string
	AtLimit     bool
}

func (s *Server) defaultReport() *model.Report {
	g := s.Runner.General()
	return &model.Report{
		Enabled:  true,
		Time:     model.TimeSpec{Preset: "previous_week"},
		Timezone: g.Timezone,
		Layout: model.Layout{Mode: model.LayoutDashboard, Paper: "A4", Orientation: "landscape", Theme: "light",
			CoverPage: true, ExpandRows: true, Columns: 2},
		Schedule: schedule.Spec{Kind: schedule.Weekly, At: "07:00", Weekdays: []int{1}},
	}
}

func (s *Server) reportForm(w http.ResponseWriter, r *http.Request) {
	var rep *model.Report
	isNew := r.PathValue("id") == ""
	if isNew {
		rep = s.defaultReport()
		if c := r.URL.Query().Get("connection"); c != "" {
			rep.ConnectionID = c
		}
	} else {
		var err error
		rep, err = s.Store.GetReport(r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.renderReportForm(w, r, rep, isNew)
}

func (s *Server) renderReportForm(w http.ResponseWriter, r *http.Request, rep *model.Report, isNew bool) {
	conns, _ := s.Store.ListConnections()
	f := reportForm{Report: rep, IsNew: isNew, Connections: conns, Presets: timerange.Presets, Timezones: Timezones,
		Weekdays: []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}}
	f.Assets = s.listAssets()
	if !isNew {
		f.Runs, _ = s.Store.ListRuns(rep.ID, 10)
		loc, _ := time.LoadLocation(s.Runner.General().Timezone)
		f.Upcoming, _ = rep.Schedule.Upcoming(time.Now(), 3, loc)
	} else {
		lim := s.License.Current().Limits()
		n, _ := s.Store.CountReports()
		f.AtLimit = lim.MaxReports > 0 && n >= lim.MaxReports
	}
	var vl []string
	for _, v := range rep.Variables {
		vl = append(vl, v.Name+" = "+strings.Join(v.Values, ", "))
	}
	f.VarLines = strings.Join(vl, "\n")
	f.PanelLines = strings.Join(rep.Layout.Panels, "\n")
	var hl []string
	for _, h := range rep.Delivery.Webhooks {
		line := h.URL
		if h.Secret != "" {
			line += " " + h.Secret
		}
		hl = append(hl, line)
	}
	f.HookLines = strings.Join(hl, "\n")
	var bl []string
	for _, t := range rep.Burst.Targets {
		bl = append(bl, strings.Join([]string{t.Value, t.Label, strings.Join(t.Recipients, ", ")}, " | "))
	}
	f.BurstLines = strings.Join(bl, "\n")
	title := "New report"
	if !isNew {
		title = rep.Name
	}
	s.render(w, r, "report_form", title, "reports", f)
}

func (s *Server) listAssets() []string {
	entries, err := os.ReadDir(s.Runner.AssetsDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseReportForm reads the posted report form into rep.
func parseReportForm(r *http.Request, rep *model.Report) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	f := r.PostForm
	rep.Name = f.Get("name")
	rep.Enabled = f.Get("enabled") == "on"
	rep.ConnectionID = f.Get("connection_id")
	rep.DashboardUID = strings.TrimSpace(f.Get("dashboard_uid"))
	rep.DashboardTitle = f.Get("dashboard_title")
	rep.Time = model.TimeSpec{Preset: f.Get("time_preset"), From: strings.TrimSpace(f.Get("time_from")), To: strings.TrimSpace(f.Get("time_to"))}
	rep.Timezone = strings.TrimSpace(f.Get("timezone"))
	rep.WeekStartsSunday = f.Get("week_starts_sunday") == "on"

	rep.Variables = nil
	for _, l := range lines(f.Get("variables")) {
		name, vals, ok := strings.Cut(l, "=")
		if !ok {
			return fmt.Errorf("variable line %q must look like name = value1, value2", l)
		}
		rep.Variables = append(rep.Variables, model.Variable{Name: strings.TrimSpace(name), Values: splitList(vals)})
	}

	width, _ := strconv.Atoi(f.Get("width"))
	cols, _ := strconv.Atoi(f.Get("columns"))
	rep.Layout = model.Layout{Mode: f.Get("mode"), Columns: cols, Paper: f.Get("paper"), Orientation: f.Get("orientation"),
		Theme: f.Get("theme"), Width: width, ExpandRows: f.Get("expand_rows") == "on", CoverPage: f.Get("cover_page") == "on",
		Panels: lines(f.Get("panels"))}

	rep.Branding = model.Branding{CompanyName: strings.TrimSpace(f.Get("brand_company")), LogoAsset: f.Get("brand_logo"),
		AccentColor: strings.TrimSpace(f.Get("brand_accent")), FooterText: strings.TrimSpace(f.Get("brand_footer")),
		Intro: strings.TrimSpace(f.Get("brand_intro"))}
	if f.Get("brand_accent_default") == "on" {
		rep.Branding.AccentColor = ""
	}

	day, _ := strconv.Atoi(f.Get("month_day"))
	var weekdays []int
	for _, v := range f["weekdays"] {
		if d, err := strconv.Atoi(v); err == nil {
			weekdays = append(weekdays, d)
		}
	}
	rep.Schedule = schedule.Spec{Kind: f.Get("schedule_kind"), At: strings.TrimSpace(f.Get("at")), Weekdays: weekdays,
		MonthDay: day, Cron: strings.TrimSpace(f.Get("cron")), Timezone: rep.Timezone}
	switch rep.Schedule.Kind { // keep only the fields of the chosen kind
	case schedule.Daily:
		rep.Schedule.Weekdays, rep.Schedule.MonthDay, rep.Schedule.Cron = nil, 0, ""
	case schedule.Weekly:
		rep.Schedule.MonthDay, rep.Schedule.Cron = 0, ""
	case schedule.Monthly, schedule.Quarterly:
		rep.Schedule.Weekdays, rep.Schedule.Cron = nil, ""
	case schedule.Cron:
		rep.Schedule.Weekdays, rep.Schedule.MonthDay, rep.Schedule.At = nil, 0, ""
	case schedule.Manual:
		rep.Schedule = schedule.Spec{Kind: schedule.Manual, Timezone: rep.Timezone}
	}

	rep.Delivery = model.Delivery{
		Email: model.Email{To: splitList(f.Get("email_to")), Cc: splitList(f.Get("email_cc")), Bcc: splitList(f.Get("email_bcc")),
			Subject: strings.TrimSpace(f.Get("email_subject")), Body: strings.TrimSpace(f.Get("email_body"))},
		Folder: f.Get("folder") == "on",
	}
	for _, l := range lines(f.Get("webhooks")) {
		parts := strings.Fields(l)
		hook := model.Webhook{URL: parts[0]}
		if len(parts) > 1 {
			hook.Secret = parts[1]
		}
		rep.Delivery.Webhooks = append(rep.Delivery.Webhooks, hook)
	}

	rep.Burst = model.Burst{Enabled: f.Get("burst_enabled") == "on", Variable: strings.TrimSpace(f.Get("burst_variable"))}
	for _, l := range lines(f.Get("burst_targets")) {
		parts := strings.Split(l, "|")
		if len(parts) == 1 {
			parts = strings.Split(l, "\t")
		}
		t := model.BurstTarget{Value: strings.TrimSpace(parts[0])}
		if len(parts) > 1 {
			t.Label = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			t.Recipients = splitList(strings.Join(parts[2:], ","))
		}
		rep.Burst.Targets = append(rep.Burst.Targets, t)
	}
	if !rep.Burst.Enabled && len(rep.Burst.Targets) == 0 {
		rep.Burst = model.Burst{}
	}
	rep.FileName = strings.TrimSpace(f.Get("file_name"))
	return nil
}

func (s *Server) reportSave(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	rep := s.defaultReport()
	isNew := id == ""
	if !isNew {
		existing, err := s.Store.GetReport(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		rep = existing
	}
	fail := func(msg string) {
		// Re-render with the user's input so nothing typed is lost.
		w.Header().Set("X-Form-Error", "1")
		r.URL.RawQuery = "err=" + urlQuery(msg)
		s.renderReportForm(w, r, rep, isNew)
	}
	if err := parseReportForm(r, rep); err != nil {
		fail(err.Error())
		return
	}
	lim := s.License.Current().Limits()
	if isNew && lim.MaxReports > 0 {
		if n, _ := s.Store.CountReports(); n >= lim.MaxReports {
			fail(fmt.Sprintf("The Community edition includes %d reports. Upgrade to Pro for unlimited reports.", lim.MaxReports))
			return
		}
	}
	if rep.Burst.Enabled && !lim.Bursting {
		fail("Sending one PDF per client (bursting) is part of Pro and Business. Turn it off or activate a license.")
		return
	}
	if len(rep.Delivery.Webhooks) > 0 && !lim.Webhooks {
		fail("Webhook delivery is part of Pro and Business.")
		return
	}
	if rep.Burst.Enabled && lim.MaxBurstTargets > 0 && len(rep.Burst.Targets) > lim.MaxBurstTargets {
		fail(fmt.Sprintf("Pro includes up to %d burst targets per report; Business is unlimited.", lim.MaxBurstTargets))
		return
	}
	if err := rep.Validate(); err != nil {
		fail(err.Error())
		return
	}
	if _, err := s.Store.GetConnection(rep.ConnectionID); err != nil {
		fail("Choose one of your Grafana connections.")
		return
	}
	if err := s.Store.SaveReport(rep); err != nil {
		fail(err.Error())
		return
	}
	s.Runner.Reload()
	msg := "Report saved."
	if !lim.Branding && (rep.Branding.LogoAsset != "" || rep.Branding.AccentColor != "" || rep.Branding.FooterText != "") {
		msg += " Custom logo, colour and footer are used once you activate Pro."
	}
	if r.FormValue("then") == "preview" {
		run, err := s.Runner.Enqueue(rep, model.TriggerPreview)
		if err != nil {
			redirectErr(w, r, "/reports/"+rep.ID, err.Error())
			return
		}
		http.Redirect(w, r, "/runs/"+run.ID, http.StatusSeeOther)
		return
	}
	redirectOK(w, r, "/reports/"+rep.ID, msg)
}

func (s *Server) loadReport(w http.ResponseWriter, r *http.Request) (*model.Report, bool) {
	rep, err := s.Store.GetReport(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	return rep, true
}

func (s *Server) reportRun(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.loadReport(w, r)
	if !ok {
		return
	}
	run, err := s.Runner.Enqueue(rep, model.TriggerManual)
	if err != nil {
		redirectErr(w, r, "/", err.Error())
		return
	}
	http.Redirect(w, r, "/runs/"+run.ID, http.StatusSeeOther)
}

func (s *Server) reportPreview(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.loadReport(w, r)
	if !ok {
		return
	}
	run, err := s.Runner.Enqueue(rep, model.TriggerPreview)
	if err != nil {
		redirectErr(w, r, "/reports/"+rep.ID, err.Error())
		return
	}
	http.Redirect(w, r, "/runs/"+run.ID, http.StatusSeeOther)
}

func (s *Server) reportDuplicate(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.loadReport(w, r)
	if !ok {
		return
	}
	lim := s.License.Current().Limits()
	if n, _ := s.Store.CountReports(); lim.MaxReports > 0 && n >= lim.MaxReports {
		redirectErr(w, r, "/", fmt.Sprintf("The Community edition includes %d reports. Upgrade to Pro for unlimited reports.", lim.MaxReports))
		return
	}
	cp := *rep
	cp.ID, cp.Name, cp.Enabled = "", rep.Name+" (copy)", false
	if err := s.Store.SaveReport(&cp); err != nil {
		redirectErr(w, r, "/", err.Error())
		return
	}
	redirectOK(w, r, "/reports/"+cp.ID, "Copy created. It is paused until you enable it.")
}

func (s *Server) reportDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteReport(r.PathValue("id")); err != nil {
		redirectErr(w, r, "/", err.Error())
		return
	}
	s.Runner.Reload()
	redirectOK(w, r, "/", "Report deleted. Its past runs remain under Runs.")
}

func (s *Server) reportToggle(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.loadReport(w, r)
	if !ok {
		return
	}
	rep.Enabled = !rep.Enabled
	if err := s.Store.SaveReport(rep); err != nil {
		redirectErr(w, r, "/", err.Error())
		return
	}
	s.Runner.Reload()
	state := "paused"
	if rep.Enabled {
		state = "enabled"
	}
	redirectOK(w, r, "/", fmt.Sprintf("%q is %s.", rep.Name, state))
}

// ------------------------------------------------------------------ runs

func (s *Server) runsList(w http.ResponseWriter, r *http.Request) {
	runs, err := s.Store.ListRuns(r.URL.Query().Get("report"), 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "runs", "Runs", "runs", map[string]any{"Runs": runs})
}

func (s *Server) runDetail(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetRun(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "run", "Run of "+run.ReportName, "runs", map[string]any{"Run": run})
}

func (s *Server) runFile(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetRun(r.PathValue("run"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	i, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || i < 0 || i >= len(run.Outputs) || run.Outputs[i].File == "" {
		http.NotFound(w, r)
		return
	}
	s.serveOutput(w, r, run, i)
}

func (s *Server) serveOutput(w http.ResponseWriter, r *http.Request, run *model.Run, i int) {
	rel := filepath.Clean(run.Outputs[i].File)
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.Runner.ReportsDir(), rel)
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "the PDF is no longer available (removed by the retention policy)", http.StatusGone)
		return
	}
	defer f.Close()
	name := filepath.Base(path)
	disp := "inline"
	if r.URL.Query().Get("download") == "1" {
		disp = "attachment"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disp, name))
	info, _ := f.Stat()
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *Server) uiRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetRun(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func urlQuery(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "+", "%2B", " ", "+").Replace(s)
}
