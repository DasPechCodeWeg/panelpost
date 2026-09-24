package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/grafana"
	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/schedule"
)

func (s *Server) connectionsList(w http.ResponseWriter, r *http.Request) {
	conns, err := s.Store.ListConnections()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	lim := s.License.Current().Limits()
	s.render(w, r, "connections", "Grafana connections", "connections", map[string]any{
		"Connections": conns,
		"AtLimit":     lim.MaxConnections > 0 && len(conns) >= lim.MaxConnections,
		"Max":         lim.MaxConnections,
	})
}

func (s *Server) connectionForm(w http.ResponseWriter, r *http.Request) {
	c := &model.Connection{OrgID: 1}
	isNew := r.PathValue("id") == ""
	if !isNew {
		var err error
		if c, err = s.Store.GetConnection(r.PathValue("id")); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	var headers []string
	for k, v := range c.ExtraHeaders {
		headers = append(headers, k+": "+v)
	}
	title := "Connect Grafana"
	if !isNew {
		title = c.Name
	}
	s.render(w, r, "connection_form", title, "connections", map[string]any{
		"Conn": c, "IsNew": isNew, "Headers": strings.Join(headers, "\n"), "HasToken": c.Token != "",
	})
}

func parseHeaders(text string) (map[string]string, error) {
	out := map[string]string{}
	for _, l := range lines(text) {
		k, v, ok := strings.Cut(l, ":")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("header line %q must look like Name: value", l)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (s *Server) connectionFromForm(r *http.Request) (*model.Connection, bool, error) {
	id := r.FormValue("id")
	c := &model.Connection{}
	isNew := id == ""
	if !isNew {
		existing, err := s.Store.GetConnection(id)
		if err != nil {
			return nil, false, fmt.Errorf("connection not found")
		}
		c = existing
	}
	c.Name = r.FormValue("name")
	c.URL = r.FormValue("url")
	org, _ := strconv.ParseInt(r.FormValue("org_id"), 10, 64)
	c.OrgID = org
	if tok := strings.TrimSpace(r.FormValue("token")); tok != "" {
		c.Token = tok
	}
	c.InsecureTLS = r.FormValue("insecure_tls") == "on"
	headers, err := parseHeaders(r.FormValue("headers"))
	if err != nil {
		return c, isNew, err
	}
	c.ExtraHeaders = headers
	return c, isNew, c.Validate()
}

func (s *Server) connectionSave(w http.ResponseWriter, r *http.Request) {
	c, isNew, err := s.connectionFromForm(r)
	back := "/connections/new"
	if !isNew && c != nil {
		back = "/connections/" + c.ID
	}
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	lim := s.License.Current().Limits()
	if isNew && lim.MaxConnections > 0 {
		if conns, _ := s.Store.ListConnections(); len(conns) >= lim.MaxConnections {
			redirectErr(w, r, "/connections", fmt.Sprintf("This edition includes %d Grafana connection(s). Upgrade to connect more.", lim.MaxConnections))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	client, err := grafana.New(c)
	if err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	info, checkErr := client.Check(ctx)
	if err := s.Store.SaveConnection(c); err != nil {
		redirectErr(w, r, back, err.Error())
		return
	}
	if checkErr != nil {
		redirectErr(w, r, "/connections/"+c.ID, "Saved, but the connection test failed: "+checkErr.Error())
		return
	}
	msg := fmt.Sprintf("Connected to %s (Grafana %s, %s).", info.OrgName, info.Version, info.Edition)
	if isNew {
		if n, _ := s.Store.CountReports(); n == 0 {
			redirectOK(w, r, "/reports/new?connection="+c.ID, msg+" Now create your first report.")
			return
		}
	}
	redirectOK(w, r, "/connections", msg)
}

func (s *Server) connectionTest(w http.ResponseWriter, r *http.Request) {
	c, _, err := s.connectionFromForm(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	client, err := grafana.New(c)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	info, err := client.Check(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": fmt.Sprintf("Connected to organisation %q as %s · Grafana %s (%s)", info.OrgName, nonEmpty(info.Login, "service account"), info.Version, info.Edition)})
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func (s *Server) connectionDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteConnection(r.PathValue("id")); err != nil {
		redirectErr(w, r, "/connections", err.Error())
		return
	}
	redirectOK(w, r, "/connections", "Connection removed.")
}

// ------------------------------------------------------- UI JSON helpers

func (s *Server) uiClient(w http.ResponseWriter, r *http.Request) (*grafana.Client, bool) {
	c, err := s.Store.GetConnection(r.URL.Query().Get("connection"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "choose a connection first"})
		return nil, false
	}
	client, err := grafana.New(c)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, false
	}
	return client, true
}

func (s *Server) uiDashboards(w http.ResponseWriter, r *http.Request) {
	client, ok := s.uiClient(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	hits, err := client.SearchDashboards(ctx, r.URL.Query().Get("q"), 200)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dashboards": hits})
}

func (s *Server) uiDashboard(w http.ResponseWriter, r *http.Request) {
	client, ok := s.uiClient(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	d, err := client.GetDashboard(ctx, r.URL.Query().Get("uid"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"title": d.Title, "variables": d.Variables, "panels": d.Panels})
}

func (s *Server) uiSchedule(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	day, _ := strconv.Atoi(q.Get("month_day"))
	var weekdays []int
	for _, v := range q["weekdays"] {
		if d, err := strconv.Atoi(v); err == nil {
			weekdays = append(weekdays, d)
		}
	}
	spec := schedule.Spec{Kind: q.Get("kind"), At: q.Get("at"), Weekdays: weekdays, MonthDay: day, Cron: q.Get("cron"), Timezone: q.Get("tz")}
	if err := spec.Validate(); err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	loc, _ := spec.Location(time.UTC)
	runs, _ := spec.Upcoming(time.Now(), 3, loc)
	var out []string
	for _, t := range runs {
		out = append(out, t.In(loc).Format("Mon 2 Jan 2006 15:04 MST"))
	}
	writeJSON(w, http.StatusOK, map[string]any{"describe": spec.Describe(), "next": out})
}
