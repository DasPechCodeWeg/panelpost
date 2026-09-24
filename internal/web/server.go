// Package web serves Panelpost's admin UI and REST API.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/DasPechCodeWeg/panelpost/internal/brand"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/DasPechCodeWeg/panelpost/internal/license"
	"github.com/DasPechCodeWeg/panelpost/internal/runner"
	"github.com/DasPechCodeWeg/panelpost/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Version is set at build time.
var Version = "dev"

// PricingURL is where the upgrade buttons point.
var PricingURL = brand.URL("#editions")

const (
	keyPasswordHash = "auth.password_hash"
	keySessionKey   = "auth.session_key"
	cookieName      = "panelpost_session"
	sessionTTL      = 12 * time.Hour
)

// Server is the HTTP application.
type Server struct {
	Store   *store.Store
	Runner  *runner.Runner
	License *license.Manager
	Logger  *slog.Logger
	// SetupToken must be presented to create the admin password on a fresh
	// installation, so that nobody else can claim an exposed instance.
	SetupToken string

	tmpl       map[string]*template.Template
	sessionKey []byte
	loc        atomic.Pointer[time.Location]
	limiter    *limiter
	mux        *http.ServeMux
}

// New prepares the server.
func New(s *Server) (*Server, error) {
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	key, err := s.Store.GetSetting(keySessionKey)
	if err != nil {
		return nil, err
	}
	if key == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		key = base64.StdEncoding.EncodeToString(b)
		if err := s.Store.SetSetting(keySessionKey, key); err != nil {
			return nil, err
		}
	}
	s.sessionKey, _ = base64.StdEncoding.DecodeString(key)
	s.limiter = newLimiter(8, time.Minute)
	s.RefreshLocation()
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

// RefreshLocation reloads the time zone used to display times.
func (s *Server) RefreshLocation() {
	loc, err := time.LoadLocation(s.Runner.General().Timezone)
	if err != nil {
		loc = time.UTC
	}
	s.loc.Store(loc)
}

func (s *Server) displayLoc() *time.Location {
	if l := s.loc.Load(); l != nil {
		return l
	}
	return time.UTC
}

// HasPassword reports whether initial setup is complete.
func (s *Server) HasPassword() bool {
	h, _ := s.Store.GetSetting(keyPasswordHash)
	return h != ""
}

// SetPassword stores a new admin password.
func (s *Server) SetPassword(pw string) error {
	if len(pw) < 10 {
		return errors.New("use a password of at least 10 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.Store.SetSetting(keyPasswordHash, string(hash))
}

func (s *Server) checkPassword(pw string) bool {
	h, _ := s.Store.GetSetting(keyPasswordHash)
	return h != "" && bcrypt.CompareHashAndPassword([]byte(h), []byte(pw)) == nil
}

func (s *Server) parseTemplates() error {
	funcs := template.FuncMap{
		"since":    since,
		"dt":       func(t time.Time) string { return formatTime(t, s.displayLoc()) },
		"dur":      func(d time.Duration) string { return d.Round(100 * time.Millisecond).String() },
		"join":     strings.Join,
		"lower":    strings.ToLower,
		"contains": func(list []string, v string) bool { return contains(list, v) },
		"containsInt": func(list []int, v int) bool {
			for _, x := range list {
				if x == v {
					return true
				}
			}
			return false
		},
		"kb":         func(n int64) string { return humanSize(n) },
		"pricingURL": func() string { return PricingURL },
		"version":    func() string { return Version },
		"add":        func(a, b int) int { return a + b },
		"json": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"lines": func(list []string) string { return strings.Join(list, "\n") },
		"list":  func(items ...string) []string { return items },
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
	}
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	s.tmpl = map[string]*template.Template{}
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/"), ".html")
		if name == "layout" || strings.HasPrefix(name, "_") {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/_partials.html", p)
		if err != nil {
			return fmt.Errorf("template %s: %w", name, err)
		}
		s.tmpl[name] = t
	}
	return nil
}

// ServeHTTP implements http.Handler with security headers.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'self'")
	s.mux.ServeHTTP(w, r)
}

// ---------------------------------------------------------------- session

type session struct {
	Expires int64  `json:"e"`
	ID      string `json:"i"`
}

func (s *Server) sign(b []byte) string {
	m := hmac.New(sha256.New, s.sessionKey)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) newSession(w http.ResponseWriter, r *http.Request) {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	sess := session{Expires: time.Now().Add(sessionTTL).Unix(), ID: hex.EncodeToString(id)}
	raw, _ := json.Marshal(sess)
	val := base64.RawURLEncoding.EncodeToString(raw) + "." + s.sign(raw)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: val, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: int(sessionTTL.Seconds())})
}

func (s *Server) currentSession(r *http.Request) (*session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(s.sign(raw)), []byte(parts[1])) != 1 {
		return nil, false
	}
	var sess session
	if json.Unmarshal(raw, &sess) != nil || time.Now().Unix() > sess.Expires {
		return nil, false
	}
	return &sess, true
}

func (s *Server) csrfToken(sess *session) string {
	if sess == nil {
		return ""
	}
	return s.sign([]byte("csrf:" + sess.ID))[:32]
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// requireAuth wraps UI handlers: login required and CSRF-checked POSTs.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.HasPassword() {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		sess, ok := s.currentSession(r)
		if !ok {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodDelete || r.Method == http.MethodPut {
			if !sameOrigin(r) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
			token := r.Header.Get("X-CSRF-Token")
			if token == "" {
				token = r.FormValue("csrf")
			}
			if subtle.ConstantTimeCompare([]byte(token), []byte(s.csrfToken(sess))) != 1 {
				http.Error(w, "your session expired; reload the page and try again", http.StatusForbidden)
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
	}
}

type sessionKey struct{}

func sessionFrom(r *http.Request) *session {
	s, _ := r.Context().Value(sessionKey{}).(*session)
	return s
}

// sameOrigin rejects cross-site form posts when the browser tells us.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return strings.EqualFold(u.Host, host)
}

// ---------------------------------------------------------------- limiter

type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.max {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	return true
}

// blocked reports whether key has used up its budget, without counting a hit.
func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, t := range l.hits[key] {
		if time.Since(t) < l.window {
			n++
		}
	}
	return n >= l.max
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------------------------------------------------------- render

// page is the data passed to every template.
type page struct {
	Title    string
	Nav      string
	CSRF     string
	License  license.License
	Flash    string
	FlashErr string
	Data     any
	Version  string
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title, nav string, data any) {
	t, ok := s.tmpl[name]
	if !ok {
		http.Error(w, "missing template "+name, http.StatusInternalServerError)
		return
	}
	p := page{Title: title, Nav: nav, CSRF: s.csrfToken(sessionFrom(r)), License: s.License.Current(), Data: data, Version: Version}
	p.Flash = r.URL.Query().Get("ok")
	p.FlashErr = r.URL.Query().Get("err")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout.html", p); err != nil {
		s.Logger.Error("render template", "template", name, "err", err)
	}
}

func redirectOK(w http.ResponseWriter, r *http.Request, to, msg string) {
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	http.Redirect(w, r, to+sep+"ok="+url.QueryEscape(msg), http.StatusSeeOther)
}

func redirectErr(w http.ResponseWriter, r *http.Request, to, msg string) {
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	http.Redirect(w, r, to+sep+"err="+url.QueryEscape(msg), http.StatusSeeOther)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func since(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	future := d < 0
	if future {
		d = -d
	}
	var s string
	switch {
	case d < time.Minute:
		s = "moments"
	case d < time.Hour:
		s = strconv.Itoa(int(d.Minutes())) + " min"
	case d < 48*time.Hour:
		s = strconv.Itoa(int(d.Hours())) + " h"
	default:
		s = strconv.Itoa(int(d.Hours()/24)) + " days"
	}
	if future {
		return "in " + s
	}
	return s + " ago"
}

func formatTime(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(loc).Format("2 Jan 2006 15:04 MST")
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	fsrv := http.FileServer(http.FS(sub))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fsrv.ServeHTTP(w, r)
	}))
}
