package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/deliver"
	"github.com/DasPechCodeWeg/panelpost/internal/license"
	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/runner"
)

func (s *Server) settingsGeneral(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings_general", "Settings", "settings", map[string]any{
		"G": s.Runner.General(), "Timezones": Timezones, "Tab": "general",
	})
}

func (s *Server) settingsGeneralSave(w http.ResponseWriter, r *http.Request) {
	g := s.Runner.General()
	g.Timezone = strings.TrimSpace(r.FormValue("timezone"))
	if _, err := time.LoadLocation(g.Timezone); err != nil || g.Timezone == "" {
		redirectErr(w, r, "/settings", fmt.Sprintf("Unknown time zone %q.", g.Timezone))
		return
	}
	g.WeekStartsSunday = r.FormValue("week_starts_sunday") == "on"
	g.PublicURL = r.FormValue("public_url")
	g.NotifyEmail = strings.TrimSpace(r.FormValue("notify_email"))
	if g.NotifyEmail != "" {
		if _, err := mail.ParseAddress(g.NotifyEmail); err != nil {
			redirectErr(w, r, "/settings", "The alert address is not a valid email address.")
			return
		}
	}
	g.ImageFormat = r.FormValue("image_format")
	g.IncludeNotes = r.FormValue("include_notes") == "on"
	g.RenderTimeoutSeconds, _ = strconv.Atoi(r.FormValue("render_timeout"))
	if g.RenderTimeoutSeconds > 600 {
		g.RenderTimeoutSeconds = 600
	}
	g.Normalize()
	if err := s.Store.SetJSON(runner.KeyGeneral, g); err != nil {
		redirectErr(w, r, "/settings", err.Error())
		return
	}
	s.Runner.Reload()
	s.RefreshLocation()
	redirectOK(w, r, "/settings", "Settings saved.")
}

func (s *Server) settingsEmail(w http.ResponseWriter, r *http.Request) {
	smtp := s.Runner.SMTP()
	s.render(w, r, "settings_email", "Email", "settings", map[string]any{
		"S": smtp, "HasPassword": smtp.Password != "", "Tab": "email",
	})
}

func (s *Server) smtpFromForm(r *http.Request) (model.SMTP, error) {
	cur := s.Runner.SMTP()
	port, _ := strconv.Atoi(r.FormValue("port"))
	sm := model.SMTP{Host: strings.TrimSpace(r.FormValue("host")), Port: port, Username: strings.TrimSpace(r.FormValue("username")),
		From: strings.TrimSpace(r.FormValue("from")), FromName: strings.TrimSpace(r.FormValue("from_name")),
		TLS: r.FormValue("tls"), Insecure: r.FormValue("insecure") == "on", Password: cur.Password}
	if pw := r.FormValue("password"); pw != "" {
		sm.Password = pw
	}
	if r.FormValue("clear_password") == "on" {
		sm.Password = ""
	}
	if sm.Host == "" {
		return sm, fmt.Errorf("enter the SMTP server host name")
	}
	if _, err := mail.ParseAddress(sm.From); err != nil {
		return sm, fmt.Errorf("the sender address is not a valid email address")
	}
	return sm, nil
}

func (s *Server) settingsEmailSave(w http.ResponseWriter, r *http.Request) {
	sm, err := s.smtpFromForm(r)
	if err != nil {
		redirectErr(w, r, "/settings/email", err.Error())
		return
	}
	if err := s.Store.SetJSON(runner.KeySMTP, sm); err != nil {
		redirectErr(w, r, "/settings/email", err.Error())
		return
	}
	if err := s.Store.SetSecret(runner.KeySMTPPassword, sm.Password); err != nil {
		redirectErr(w, r, "/settings/email", err.Error())
		return
	}
	redirectOK(w, r, "/settings/email", "Email settings saved.")
}

func (s *Server) settingsEmailTest(w http.ResponseWriter, r *http.Request) {
	sm, err := s.smtpFromForm(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	to := strings.TrimSpace(r.FormValue("test_to"))
	if _, err := mail.ParseAddress(to); err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": "enter the address to send the test to"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	m := &deliver.Mailer{SMTP: sm, Timeout: 30 * time.Second}
	err = m.Send(ctx, deliver.Email{To: []string{to}, Subject: "Panelpost test email",
		Text: "This is a test from Panelpost. If you can read this, scheduled reports can be delivered by email."})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "Test email sent to " + to + "."})
}

func (s *Server) settingsBranding(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings_branding", "Branding", "settings", map[string]any{
		"B": s.Runner.Branding(), "Assets": s.listAssets(), "Tab": "branding",
	})
}

var allowedLogo = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/svg+xml": ".svg", "image/webp": ".webp"}

func (s *Server) settingsBrandingSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		redirectErr(w, r, "/settings/branding", "The upload is too large; logos are limited to 1 MB.")
		return
	}
	b := model.Branding{CompanyName: strings.TrimSpace(r.FormValue("company")), LogoAsset: r.FormValue("logo"),
		AccentColor: strings.TrimSpace(r.FormValue("accent")), FooterText: strings.TrimSpace(r.FormValue("footer")),
		Intro: strings.TrimSpace(r.FormValue("intro"))}
	if f, hdr, err := r.FormFile("upload"); err == nil {
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
		if err != nil || len(data) > 1<<20 {
			redirectErr(w, r, "/settings/branding", "Logos are limited to 1 MB.")
			return
		}
		ct := http.DetectContentType(data)
		if bytes.Contains(data[:min(len(data), 1024)], []byte("<svg")) {
			ct = "image/svg+xml"
		}
		ext, ok := allowedLogo[ct]
		if !ok {
			redirectErr(w, r, "/settings/branding", "Upload a PNG, JPEG, WebP or SVG image.")
			return
		}
		name := safeAssetName(strings.TrimSuffix(hdr.Filename, filepath.Ext(hdr.Filename))) + ext
		if err := os.MkdirAll(s.Runner.AssetsDir(), 0o755); err != nil {
			redirectErr(w, r, "/settings/branding", err.Error())
			return
		}
		if err := os.WriteFile(filepath.Join(s.Runner.AssetsDir(), name), data, 0o644); err != nil {
			redirectErr(w, r, "/settings/branding", err.Error())
			return
		}
		b.LogoAsset = name
	}
	if b.LogoAsset != "" {
		b.LogoAsset = filepath.Base(b.LogoAsset)
	}
	if err := b.Validate(); err != nil {
		redirectErr(w, r, "/settings/branding", err.Error())
		return
	}
	if err := s.Store.SetJSON(runner.KeyBranding, b); err != nil {
		redirectErr(w, r, "/settings/branding", err.Error())
		return
	}
	msg := "Branding saved."
	if !s.License.Current().Limits().Branding && (b.LogoAsset != "" || b.AccentColor != "" || b.FooterText != "") {
		msg += " Your logo, colour and footer appear in reports once Pro is active."
	}
	redirectOK(w, r, "/settings/branding", msg)
}

func safeAssetName(s string) string {
	s = deliver.SafeFileName(strings.ToLower(s))
	s = strings.ReplaceAll(s, " ", "-")
	if s == "" || s == "report" {
		s = "logo"
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return s + "-" + hex.EncodeToString(b)
}

func (s *Server) assetFile(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	path := filepath.Join(s.Runner.AssetsDir(), name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".svg") {
		// Never let an uploaded SVG run script in our origin.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	http.ServeFile(w, r, path)
}

// ---------------------------------------------------------------- license

type tierInfo struct {
	Tier   license.Tier
	Price  string
	Limits license.Limits
}

func (s *Server) settingsLicense(w http.ResponseWriter, r *http.Request) {
	tiers := []tierInfo{
		{license.Free, "Free", license.LimitsFor(license.Free)},
		{license.Pro, "€29 / month or €290 / year", license.LimitsFor(license.Pro)},
		{license.Business, "€79 / month or €790 / year", license.LimitsFor(license.Business)},
	}
	s.render(w, r, "settings_license", "License", "settings", map[string]any{
		"L": s.License.Current(), "Masked": s.License.MaskedKey(), "Tiers": tiers, "Tab": "license",
	})
}

func (s *Server) settingsLicenseApply(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	lic, err := s.License.Apply(ctx, r.FormValue("key"))
	if err != nil {
		redirectErr(w, r, "/settings/license", err.Error())
		return
	}
	s.Runner.Reload()
	redirectOK(w, r, "/settings/license", fmt.Sprintf("Thank you! Panelpost %s is active for %s.", lic.Tier.Title(), nonEmpty(lic.Licensee, lic.Email)))
}

func (s *Server) settingsLicenseRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.License.Remove(); err != nil {
		redirectErr(w, r, "/settings/license", err.Error())
		return
	}
	redirectOK(w, r, "/settings/license", "License removed. This installation now runs the Community edition.")
}

func (s *Server) settingsLicenseRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	lic := s.License.Refresh(ctx, true)
	if lic.Problem != "" {
		redirectErr(w, r, "/settings/license", lic.Problem)
		return
	}
	redirectOK(w, r, "/settings/license", "License checked: "+lic.Tier.Title()+" is active.")
}

// ---------------------------------------------------------------- API keys

func (s *Server) settingsAPI(w http.ResponseWriter, r *http.Request) {
	keys, _ := s.Store.ListAPIKeys()
	s.render(w, r, "settings_api", "API", "settings", map[string]any{
		"Keys": keys, "New": r.URL.Query().Get("key"), "Tab": "api", "Allowed": s.License.Current().Limits().API,
	})
}

func hashKey(k string) string {
	sum := sha256.Sum256([]byte(k))
	return hex.EncodeToString(sum[:])
}

func (s *Server) settingsAPICreate(w http.ResponseWriter, r *http.Request) {
	if !s.License.Current().Limits().API {
		redirectErr(w, r, "/settings/api", "The REST API is part of Pro and Business.")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "API key"
	}
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	key := "pp_" + hex.EncodeToString(raw)
	k := &model.APIKey{Name: name, Hash: hashKey(key), Prefix: key[:9]}
	if err := s.Store.CreateAPIKey(k); err != nil {
		redirectErr(w, r, "/settings/api", err.Error())
		return
	}
	// The key is shown once; only its hash is stored.
	s.render(w, r, "settings_api", "API", "settings", map[string]any{
		"Keys": mustKeys(s), "New": key, "Tab": "api", "Allowed": true,
	})
}

func mustKeys(s *Server) []*model.APIKey {
	keys, _ := s.Store.ListAPIKeys()
	return keys
}

func (s *Server) settingsAPIDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteAPIKey(r.PathValue("id")); err != nil {
		redirectErr(w, r, "/settings/api", err.Error())
		return
	}
	redirectOK(w, r, "/settings/api", "API key revoked.")
}

// ---------------------------------------------------------------- account

func (s *Server) settingsAccount(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings_account", "Account", "settings", map[string]any{"Tab": "account"})
}

func (s *Server) settingsAccountSave(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("password:" + clientIP(r)) {
		redirectErr(w, r, "/settings/account", "Too many attempts; wait a minute.")
		return
	}
	if !s.checkPassword(r.FormValue("current")) {
		redirectErr(w, r, "/settings/account", "The current password is not correct.")
		return
	}
	if r.FormValue("password") != r.FormValue("confirm") {
		redirectErr(w, r, "/settings/account", "The new passwords do not match.")
		return
	}
	if err := s.SetPassword(r.FormValue("password")); err != nil {
		redirectErr(w, r, "/settings/account", err.Error())
		return
	}
	redirectOK(w, r, "/settings/account", "Password changed.")
}
