package web

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func (s *Server) routes() {
	m := http.NewServeMux()
	a := s.requireAuth

	m.Handle("GET /static/", staticHandler())
	m.HandleFunc("GET /healthz", s.health)
	m.HandleFunc("GET /setup", s.setupPage)
	m.HandleFunc("POST /setup", s.setupSubmit)
	m.HandleFunc("GET /login", s.loginPage)
	m.HandleFunc("POST /login", s.loginSubmit)
	m.HandleFunc("POST /logout", a(s.logout))

	m.HandleFunc("GET /{$}", a(s.reportsList))
	m.HandleFunc("GET /reports/new", a(s.reportForm))
	m.HandleFunc("GET /reports/{id}", a(s.reportForm))
	m.HandleFunc("POST /reports/save", a(s.reportSave))
	m.HandleFunc("POST /reports/{id}/run", a(s.reportRun))
	m.HandleFunc("POST /reports/{id}/preview", a(s.reportPreview))
	m.HandleFunc("POST /reports/{id}/duplicate", a(s.reportDuplicate))
	m.HandleFunc("POST /reports/{id}/delete", a(s.reportDelete))
	m.HandleFunc("POST /reports/{id}/toggle", a(s.reportToggle))

	m.HandleFunc("GET /runs", a(s.runsList))
	m.HandleFunc("GET /runs/{id}", a(s.runDetail))
	m.HandleFunc("GET /files/{run}/{index}", a(s.runFile))

	m.HandleFunc("GET /connections", a(s.connectionsList))
	m.HandleFunc("GET /connections/new", a(s.connectionForm))
	m.HandleFunc("GET /connections/{id}", a(s.connectionForm))
	m.HandleFunc("POST /connections/save", a(s.connectionSave))
	m.HandleFunc("POST /connections/{id}/delete", a(s.connectionDelete))
	m.HandleFunc("POST /connections/test", a(s.connectionTest))

	m.HandleFunc("GET /settings", a(s.settingsGeneral))
	m.HandleFunc("POST /settings/general", a(s.settingsGeneralSave))
	m.HandleFunc("GET /settings/email", a(s.settingsEmail))
	m.HandleFunc("POST /settings/email", a(s.settingsEmailSave))
	m.HandleFunc("POST /settings/email/test", a(s.settingsEmailTest))
	m.HandleFunc("GET /settings/branding", a(s.settingsBranding))
	m.HandleFunc("POST /settings/branding", a(s.settingsBrandingSave))
	m.HandleFunc("GET /settings/license", a(s.settingsLicense))
	m.HandleFunc("POST /settings/license", a(s.settingsLicenseApply))
	m.HandleFunc("POST /settings/license/remove", a(s.settingsLicenseRemove))
	m.HandleFunc("POST /settings/license/refresh", a(s.settingsLicenseRefresh))
	m.HandleFunc("GET /settings/api", a(s.settingsAPI))
	m.HandleFunc("POST /settings/api", a(s.settingsAPICreate))
	m.HandleFunc("POST /settings/api/{id}/delete", a(s.settingsAPIDelete))
	m.HandleFunc("GET /settings/account", a(s.settingsAccount))
	m.HandleFunc("POST /settings/account", a(s.settingsAccountSave))
	m.HandleFunc("GET /assets/{name}", a(s.assetFile))

	m.HandleFunc("GET /ui/dashboards", a(s.uiDashboards))
	m.HandleFunc("GET /ui/dashboard", a(s.uiDashboard))
	m.HandleFunc("GET /ui/schedule", a(s.uiSchedule))
	m.HandleFunc("GET /ui/runs/{id}", a(s.uiRun))

	s.apiRoutes(m)
	s.mux = m
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": Version})
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	if s.HasPassword() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, "setup", "Welcome", "", map[string]string{"Token": r.URL.Query().Get("token")})
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if s.HasPassword() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !s.limiter.allow("setup:" + clientIP(r)) {
		http.Error(w, "too many attempts; wait a minute", http.StatusTooManyRequests)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if s.SetupToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.SetupToken)) != 1 {
		redirectErr(w, r, "/setup", "The setup code is not correct. It is printed in Panelpost's log when it starts.")
		return
	}
	pw, confirm := r.FormValue("password"), r.FormValue("confirm")
	if pw != confirm {
		redirectErr(w, r, "/setup?token="+token, "The passwords do not match.")
		return
	}
	if err := s.SetPassword(pw); err != nil {
		redirectErr(w, r, "/setup?token="+token, err.Error())
		return
	}
	s.newSession(w, r)
	redirectOK(w, r, "/connections/new", "Welcome to Panelpost! Start by connecting your Grafana.")
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if !s.HasPassword() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", "Sign in", "", map[string]string{"Next": safeNext(r.URL.Query().Get("next"))})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("login:" + clientIP(r)) {
		http.Error(w, "too many sign-in attempts; wait a minute", http.StatusTooManyRequests)
		return
	}
	next := safeNext(r.FormValue("next"))
	if !s.checkPassword(r.FormValue("password")) {
		redirectErr(w, r, "/login?next="+next, "That password is not correct.")
		return
	}
	s.newSession(w, r)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext only allows local redirects after login.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}
