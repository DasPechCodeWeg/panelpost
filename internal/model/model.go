// Package model defines Panelpost's domain types.
package model

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/schedule"
	"github.com/DasPechCodeWeg/panelpost/internal/timerange"
)

// Connection is a Grafana instance and the service account used to read it.
type Connection struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	URL          string            `json:"url"`
	OrgID        int64             `json:"org_id"`
	Token        string            `json:"-"` // decrypted in memory only
	InsecureTLS  bool              `json:"insecure_tls"`
	ExtraHeaders map[string]string `json:"-"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// Validate checks a connection before saving.
func (c *Connection) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	c.URL = strings.TrimRight(strings.TrimSpace(c.URL), "/")
	if c.Name == "" {
		return errors.New("give the connection a name")
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("Grafana URL must start with http:// or https://")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("Grafana URL must not contain a query string")
	}
	if c.OrgID <= 0 {
		c.OrgID = 1
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("a service account token is required")
	}
	for k := range c.ExtraHeaders {
		if strings.EqualFold(k, "Authorization") {
			return errors.New("set the token field instead of an Authorization header")
		}
	}
	return nil
}

// Variable is a dashboard template variable pinned by a report.
type Variable struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// TimeSpec selects the report period.
type TimeSpec struct {
	Preset string `json:"preset,omitempty"` // a timerange preset ID, or "custom"
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// Expressions returns the from/to expressions to evaluate.
func (t TimeSpec) Expressions() (string, string, error) {
	if t.Preset != "" && t.Preset != "custom" {
		p, ok := timerange.PresetByID(t.Preset)
		if !ok {
			return "", "", fmt.Errorf("unknown time range %q", t.Preset)
		}
		return p.From, p.To, nil
	}
	if strings.TrimSpace(t.From) == "" || strings.TrimSpace(t.To) == "" {
		return "", "", errors.New("custom time ranges need a start and an end")
	}
	return t.From, t.To, nil
}

// Label describes the time spec, e.g. "Previous month".
func (t TimeSpec) Label() string {
	if p, ok := timerange.PresetByID(t.Preset); ok {
		return p.Label
	}
	return t.From + " to " + t.To
}

// Layout modes.
const (
	LayoutDashboard = "dashboard" // the dashboard as it looks in Grafana
	LayoutPanels    = "panels"    // one panel per block, in reading order
)

// Layout controls how the PDF looks.
type Layout struct {
	Mode        string   `json:"mode"`
	Columns     int      `json:"columns,omitempty"` // panels mode: 1 or 2
	Paper       string   `json:"paper"`             // A4, Letter, A3
	Orientation string   `json:"orientation"`       // landscape, portrait
	Theme       string   `json:"theme"`             // light, dark
	Width       int      `json:"width,omitempty"`   // browser width in CSS px
	ExpandRows  bool     `json:"expand_rows"`
	CoverPage   bool     `json:"cover_page"`
	Panels      []string `json:"panels,omitempty"` // panel titles to include; empty = all
}

// Normalize fills defaults and validates.
func (l *Layout) Normalize() error {
	if l.Mode == "" {
		l.Mode = LayoutDashboard
	}
	if l.Mode != LayoutDashboard && l.Mode != LayoutPanels {
		return fmt.Errorf("unknown layout %q", l.Mode)
	}
	if l.Columns != 2 {
		l.Columns = 1
	}
	switch strings.ToUpper(l.Paper) {
	case "", "A4":
		l.Paper = "A4"
	case "LETTER":
		l.Paper = "Letter"
	case "A3":
		l.Paper = "A3"
	default:
		return fmt.Errorf("unknown paper size %q", l.Paper)
	}
	if l.Orientation != "portrait" {
		l.Orientation = "landscape"
	}
	if l.Theme != "dark" {
		l.Theme = "light"
	}
	if l.Width == 0 {
		if l.Orientation == "portrait" {
			l.Width = 1100
		} else {
			l.Width = 1400
		}
	}
	if l.Width < 800 || l.Width > 3000 {
		return errors.New("browser width must be between 800 and 3000 pixels")
	}
	return nil
}

// Branding overrides the global branding for one report or burst target.
type Branding struct {
	CompanyName string `json:"company_name,omitempty"`
	LogoAsset   string `json:"logo_asset,omitempty"`
	AccentColor string `json:"accent_color,omitempty"`
	FooterText  string `json:"footer_text,omitempty"`
	Intro       string `json:"intro,omitempty"`
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Merge overlays non-empty fields of o onto b.
func (b Branding) Merge(o Branding) Branding {
	if o.CompanyName != "" {
		b.CompanyName = o.CompanyName
	}
	if o.LogoAsset != "" {
		b.LogoAsset = o.LogoAsset
	}
	if o.AccentColor != "" {
		b.AccentColor = o.AccentColor
	}
	if o.FooterText != "" {
		b.FooterText = o.FooterText
	}
	if o.Intro != "" {
		b.Intro = o.Intro
	}
	return b
}

// Validate checks colour formats.
func (b Branding) Validate() error {
	if b.AccentColor != "" && !hexColor.MatchString(b.AccentColor) {
		return errors.New("accent colour must look like #1f6feb")
	}
	if len(b.FooterText) > 200 {
		return errors.New("footer text is limited to 200 characters")
	}
	return nil
}

// Email delivery settings of a report.
type Email struct {
	To      []string `json:"to,omitempty"`
	Cc      []string `json:"cc,omitempty"`
	Bcc     []string `json:"bcc,omitempty"`
	Subject string   `json:"subject,omitempty"`
	Body    string   `json:"body,omitempty"`
}

// Webhook posts the finished PDF to an HTTP endpoint.
type Webhook struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"` // HMAC-SHA256 signing secret
}

// Delivery lists where a report goes.
type Delivery struct {
	Email    Email     `json:"email"`
	Webhooks []Webhook `json:"webhooks,omitempty"`
	Folder   bool      `json:"folder"` // also write to the outbox folder
}

// BurstTarget is one recipient group of a burst report.
type BurstTarget struct {
	Value      string   `json:"value"`
	Label      string   `json:"label,omitempty"`
	Recipients []string `json:"recipients,omitempty"`
	Branding   Branding `json:"branding,omitempty"`
}

// Burst renders one PDF per value of a template variable.
type Burst struct {
	Enabled  bool          `json:"enabled"`
	Variable string        `json:"variable,omitempty"`
	Targets  []BurstTarget `json:"targets,omitempty"`
}

// Report is a scheduled rendering of one dashboard.
type Report struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Enabled          bool          `json:"enabled"`
	ConnectionID     string        `json:"connection_id"`
	DashboardUID     string        `json:"dashboard_uid"`
	DashboardTitle   string        `json:"dashboard_title,omitempty"`
	Time             TimeSpec      `json:"time"`
	Timezone         string        `json:"timezone,omitempty"`
	WeekStartsSunday bool          `json:"week_starts_sunday,omitempty"`
	Variables        []Variable    `json:"variables,omitempty"`
	Layout           Layout        `json:"layout"`
	Branding         Branding      `json:"branding,omitempty"`
	Schedule         schedule.Spec `json:"schedule"`
	Delivery         Delivery      `json:"delivery"`
	Burst            Burst         `json:"burst,omitempty"`
	FileName         string        `json:"file_name,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

var variableName = regexp.MustCompile(`^[A-Za-z0-9_\-.]+$`)

// Validate normalises and checks a report before saving.
func (r *Report) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return errors.New("give the report a name")
	}
	if r.ConnectionID == "" {
		return errors.New("choose a Grafana connection")
	}
	r.DashboardUID = strings.TrimSpace(r.DashboardUID)
	if r.DashboardUID == "" {
		return errors.New("choose a dashboard")
	}
	if _, _, err := r.Time.Expressions(); err != nil {
		return err
	}
	if r.Timezone != "" {
		if _, err := time.LoadLocation(r.Timezone); err != nil {
			return fmt.Errorf("unknown time zone %q", r.Timezone)
		}
	}
	if r.Time.Preset == "custom" {
		loc := time.UTC
		if r.Timezone != "" {
			loc, _ = time.LoadLocation(r.Timezone)
		}
		if _, err := timerange.Resolve(r.Time.From, r.Time.To, time.Now(), timerange.Options{Location: loc}); err != nil {
			return err
		}
	}
	for i, v := range r.Variables {
		v.Name = strings.TrimSpace(v.Name)
		if !variableName.MatchString(v.Name) {
			return fmt.Errorf("variable name %q may only contain letters, digits, _ - and .", v.Name)
		}
		r.Variables[i].Name = v.Name
	}
	if err := r.Layout.Normalize(); err != nil {
		return err
	}
	if err := r.Branding.Validate(); err != nil {
		return err
	}
	if r.Schedule.Timezone == "" {
		r.Schedule.Timezone = r.Timezone
	}
	if err := r.Schedule.Validate(); err != nil {
		return err
	}
	var err error
	if r.Delivery.Email.To, err = cleanAddresses(r.Delivery.Email.To); err != nil {
		return err
	}
	if r.Delivery.Email.Cc, err = cleanAddresses(r.Delivery.Email.Cc); err != nil {
		return err
	}
	if r.Delivery.Email.Bcc, err = cleanAddresses(r.Delivery.Email.Bcc); err != nil {
		return err
	}
	for _, w := range r.Delivery.Webhooks {
		u, perr := url.Parse(w.URL)
		if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("webhook URL %q is not valid", w.URL)
		}
	}
	if r.Burst.Enabled {
		if !variableName.MatchString(r.Burst.Variable) {
			return errors.New("choose the variable to burst on")
		}
		if len(r.Burst.Targets) == 0 {
			return errors.New("add at least one burst target")
		}
		seen := map[string]bool{}
		for i := range r.Burst.Targets {
			t := &r.Burst.Targets[i]
			t.Value = strings.TrimSpace(t.Value)
			if t.Value == "" {
				return errors.New("every burst target needs a variable value")
			}
			if seen[t.Value] {
				return fmt.Errorf("burst value %q is listed twice", t.Value)
			}
			seen[t.Value] = true
			if t.Recipients, err = cleanAddresses(t.Recipients); err != nil {
				return err
			}
			if err := t.Branding.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanAddresses(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			addr, err := mail.ParseAddress(part)
			if err != nil {
				return nil, fmt.Errorf("%q is not a valid email address", part)
			}
			key := strings.ToLower(addr.Address)
			if !seen[key] {
				seen[key] = true
				if addr.Name == "" {
					out = append(out, addr.Address)
				} else {
					out = append(out, addr.String())
				}
			}
		}
	}
	return out, nil
}

// SplitAddresses parses a comma or newline separated list, for form input.
func SplitAddresses(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []string{s}
}

// Run statuses.
const (
	RunQueued  = "queued"
	RunRunning = "running"
	RunSuccess = "success"
	RunPartial = "partial" // some burst targets or channels failed
	RunFailed  = "failed"
)

// Run triggers.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerPreview  = "preview"
	TriggerAPI      = "api"
)

// Output is one PDF produced by a run.
type Output struct {
	Target    string   `json:"target,omitempty"` // burst value, empty for normal runs
	File      string   `json:"file,omitempty"`   // path relative to the reports directory
	Size      int64    `json:"size,omitempty"`
	Pages     int      `json:"pages,omitempty"`
	Delivered []string `json:"delivered,omitempty"` // human readable destinations
	Warnings  []string `json:"warnings,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// Run is one execution of a report.
type Run struct {
	ID         string    `json:"id"`
	ReportID   string    `json:"report_id"`
	ReportName string    `json:"report_name"`
	Trigger    string    `json:"trigger"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	RangeFrom  time.Time `json:"range_from,omitempty"`
	RangeTo    time.Time `json:"range_to,omitempty"`
	Period     string    `json:"period,omitempty"` // human label in the report's time zone
	Outputs    []Output  `json:"outputs,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Duration of a finished run.
func (r Run) Duration() time.Duration {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return 0
	}
	return r.FinishedAt.Sub(r.StartedAt)
}

// APIKey authenticates REST API calls.
type APIKey struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Hash       string    `json:"-"`
	Prefix     string    `json:"prefix"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

// SMTP is the outgoing mail server.
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"-"`
	From     string `json:"from"`
	FromName string `json:"from_name,omitempty"`
	TLS      string `json:"tls"` // starttls, tls, none
	Insecure bool   `json:"insecure,omitempty"`
}

// Configured reports whether mail can be sent.
func (s SMTP) Configured() bool { return s.Host != "" && s.From != "" }

// General holds installation-wide preferences.
type General struct {
	// Timezone is the default for new reports and schedules.
	Timezone string `json:"timezone"`
	// WeekStartsSunday switches calendar weeks from Monday to Sunday.
	WeekStartsSunday bool `json:"week_starts_sunday,omitempty"`
	// PublicURL is where users reach Panelpost, used for links in emails.
	PublicURL string `json:"public_url,omitempty"`
	// NotifyEmail receives an alert when a scheduled report fails.
	NotifyEmail string `json:"notify_email,omitempty"`
	// ImageFormat is "jpeg" (smaller PDFs) or "png" (sharpest).
	ImageFormat string `json:"image_format,omitempty"`
	// IncludeNotes prints rendering problems at the end of the PDF.
	IncludeNotes bool `json:"include_notes,omitempty"`
	// RenderTimeoutSeconds bounds one dashboard capture.
	RenderTimeoutSeconds int `json:"render_timeout_seconds,omitempty"`
}

// Normalize fills defaults.
func (g *General) Normalize() {
	if g.Timezone == "" {
		g.Timezone = "UTC"
		if tz := os.Getenv("TZ"); tz != "" {
			if _, err := time.LoadLocation(tz); err == nil {
				g.Timezone = tz
			}
		}
	}
	if g.ImageFormat != "png" {
		g.ImageFormat = "jpeg"
	}
	if g.RenderTimeoutSeconds <= 0 {
		g.RenderTimeoutSeconds = 120
	}
	g.PublicURL = strings.TrimRight(strings.TrimSpace(g.PublicURL), "/")
}
