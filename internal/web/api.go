package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/store"
)

// The REST API is authenticated with API keys ("Authorization: Bearer pp_...")
// and is part of the Pro and Business editions.

func (s *Server) apiRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/reports", s.api(s.apiReports))
	m.HandleFunc("POST /api/v1/reports/{id}/run", s.api(s.apiRun))
	m.HandleFunc("GET /api/v1/runs/{id}", s.api(s.apiRunGet))
	m.HandleFunc("GET /api/v1/runs/{id}/outputs/{index}", s.api(s.apiRunOutput))
	m.HandleFunc("POST /api/v1/render", s.api(s.apiRender))
}

type apiError struct {
	Error string `json:"error"`
}

func (s *Server) api(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !strings.HasPrefix(key, "pp_") {
			writeJSON(w, http.StatusUnauthorized, apiError{"send an API key as: Authorization: Bearer pp_..."})
			return
		}
		ip := clientIP(r)
		if apiFailures.blocked("fail:" + ip) {
			writeJSON(w, http.StatusTooManyRequests, apiError{"too many invalid API keys; wait a minute"})
			return
		}
		if _, err := s.Store.FindAPIKeyByHash(r.Context(), hashKey(key)); err != nil {
			apiFailures.allow("fail:" + ip)
			writeJSON(w, http.StatusUnauthorized, apiError{"unknown or revoked API key"})
			return
		}
		if !apiLimiter.allow(hashKey(key)) {
			writeJSON(w, http.StatusTooManyRequests, apiError{"rate limited: 120 requests per minute"})
			return
		}
		if !s.License.Current().Limits().API {
			writeJSON(w, http.StatusPaymentRequired, apiError{"the REST API is part of Panelpost Pro and Business"})
			return
		}
		next(w, r)
	}
}

var (
	apiLimiter  = newLimiter(120, time.Minute)
	apiFailures = newLimiter(10, time.Minute)
)

type apiReport struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Dashboard string    `json:"dashboard_uid"`
	Schedule  string    `json:"schedule"`
	NextRun   time.Time `json:"next_run,omitempty"`
}

func (s *Server) apiReports(w http.ResponseWriter, r *http.Request) {
	reports, err := s.Store.ListReports()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{err.Error()})
		return
	}
	out := []apiReport{}
	for _, rep := range reports {
		out = append(out, apiReport{ID: rep.ID, Name: rep.Name, Enabled: rep.Enabled, Dashboard: rep.DashboardUID,
			Schedule: rep.Schedule.Describe(), NextRun: s.Runner.NextRun(rep)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": out})
}

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) {
	rep, err := s.Store.GetReport(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, apiError{"report not found"})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{err.Error()})
		return
	}
	trigger := model.TriggerAPI
	if r.URL.Query().Get("deliver") == "false" {
		trigger = model.TriggerPreview
	}
	run, err := s.Runner.Enqueue(rep, trigger)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) apiRunGet(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetRun(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiError{"run not found"})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) apiRunOutput(w http.ResponseWriter, r *http.Request) {
	run, err := s.Store.GetRun(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiError{"run not found"})
		return
	}
	i, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || i < 0 || i >= len(run.Outputs) || run.Outputs[i].File == "" {
		writeJSON(w, http.StatusNotFound, apiError{"output not found"})
		return
	}
	s.serveOutput(w, r, run, i)
}

// renderRequest is an ad-hoc render: either an existing report, or a
// connection and dashboard with optional overrides.
type renderRequest struct {
	ReportID     string           `json:"report_id"`
	BurstValue   string           `json:"burst_value"`
	ConnectionID string           `json:"connection_id"`
	DashboardUID string           `json:"dashboard_uid"`
	Name         string           `json:"name"`
	Time         *model.TimeSpec  `json:"time"`
	Timezone     string           `json:"timezone"`
	Variables    []model.Variable `json:"variables"`
	Layout       *model.Layout    `json:"layout"`
}

func (s *Server) apiRender(w http.ResponseWriter, r *http.Request) {
	var req renderRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{"invalid JSON: " + err.Error()})
		return
	}
	var rep *model.Report
	if req.ReportID != "" {
		var err error
		if rep, err = s.Store.GetReport(req.ReportID); err != nil {
			writeJSON(w, http.StatusNotFound, apiError{"report not found"})
			return
		}
	} else {
		rep = s.defaultReport()
		rep.Schedule.Kind = "manual"
		rep.ConnectionID, rep.DashboardUID = req.ConnectionID, req.DashboardUID
		rep.Name = nonEmpty(req.Name, "Dashboard report")
		rep.Layout.CoverPage = false
	}
	if req.Time != nil {
		rep.Time = *req.Time
	}
	if req.Timezone != "" {
		rep.Timezone = req.Timezone
	}
	if req.Variables != nil {
		rep.Variables = req.Variables
	}
	if req.Layout != nil {
		rep.Layout = *req.Layout
	}
	if err := rep.Validate(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	out, err := s.Runner.RenderNow(ctx, rep, req.BurstValue)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", out.FileName))
	w.Header().Set("X-Panelpost-Pages", strconv.Itoa(out.Pages))
	if len(out.Warnings) > 0 {
		w.Header().Set("X-Panelpost-Warnings", strconv.Itoa(len(out.Warnings)))
	}
	_, _ = w.Write(out.PDF)
}
