package web

import (
	"context"
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DasPechCodeWeg/panelpost/internal/license"
	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/render"
	"github.com/DasPechCodeWeg/panelpost/internal/runner"
	"github.com/DasPechCodeWeg/panelpost/internal/schedule"
	"github.com/DasPechCodeWeg/panelpost/internal/secret"
	"github.com/DasPechCodeWeg/panelpost/internal/store"
)

type harness struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	client *http.Client
	store  *store.Store
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	box, _ := secret.New([]byte("k"))
	st, err := store.Open(filepath.Join(dir, "p.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pubS, privS, _ := license.GenerateKeyPair()
	pubs, _ := license.ParsePublicKeys(pubS)
	priv, _ := license.ParsePrivateKey(privS)
	lic := license.NewManager(license.Options{Settings: st, PublicKeys: pubs})
	run := &runner.Runner{Store: st, Browser: render.New(render.Options{}), License: lic, DataDir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	run.Start(ctx)
	srv, err := New(&Server{Store: st, Runner: run, License: lic, SetupToken: "setup-code"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &harness{t: t, srv: srv, ts: ts, client: client, store: st, pub: pubs[0], priv: priv}
}

func (h *harness) get(path string) (*http.Response, string) {
	h.t.Helper()
	resp, err := h.client.Get(h.ts.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (h *harness) post(path string, form url.Values) *http.Response {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.ts.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

var csrfRe = regexp.MustCompile(`name="csrf" content="([^"]+)"`)

func (h *harness) csrf() string {
	h.t.Helper()
	_, body := h.get("/settings")
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		h.t.Fatal("no CSRF token on page")
	}
	return m[1]
}

func (h *harness) setup() {
	h.t.Helper()
	resp := h.post("/setup", url.Values{"token": {"setup-code"}, "password": {"a-strong-password"}, "confirm": {"a-strong-password"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/connections/new") {
		h.t.Fatalf("setup: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestSetupRequiresTheCodeFromTheLog(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.get("/"); resp.Header.Get("Location") != "/setup" {
		t.Fatalf("fresh install should redirect to setup, got %s", resp.Header.Get("Location"))
	}
	resp := h.post("/setup", url.Values{"token": {"guess"}, "password": {"a-strong-password"}, "confirm": {"a-strong-password"}})
	if !strings.Contains(resp.Header.Get("Location"), "err=") || h.srv.HasPassword() {
		t.Fatal("setup accepted a wrong code")
	}
	h.setup()
	// Once set, setup cannot be used again to take over the instance.
	resp = h.post("/setup", url.Values{"token": {"setup-code"}, "password": {"another-password"}, "confirm": {"another-password"}})
	if resp.Header.Get("Location") != "/login" {
		t.Errorf("second setup: %s", resp.Header.Get("Location"))
	}
	if !h.srv.checkPassword("a-strong-password") {
		t.Error("password changed by second setup")
	}
}

func TestLoginAndCSRF(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.csrf()
	// A POST without the CSRF token is refused even with a valid session.
	if resp := h.post("/settings/general", url.Values{"timezone": {"Europe/Amsterdam"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("missing CSRF: %d", resp.StatusCode)
	}
	// With it, the change is saved.
	if resp := h.post("/settings/general", url.Values{"csrf": {token}, "timezone": {"Europe/Amsterdam"}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("with CSRF: %d", resp.StatusCode)
	}
	if h.srv.Runner.General().Timezone != "Europe/Amsterdam" {
		t.Error("setting not saved")
	}
	// Cross-origin form posts are refused.
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/settings/general", strings.NewReader(url.Values{"csrf": {token}, "timezone": {"UTC"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, _ := h.client.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin: %d", resp.StatusCode)
	}
	// Sign out, then pages redirect to login; a wrong password fails.
	h.post("/logout", url.Values{"csrf": {token}})
	if resp, _ := h.get("/runs"); !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("after logout: %s", resp.Header.Get("Location"))
	}
	if resp := h.post("/login", url.Values{"password": {"wrong"}}); !strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Error("wrong password accepted")
	}
	if resp := h.post("/login", url.Values{"password": {"a-strong-password"}, "next": {"//evil.example/"}}); resp.Header.Get("Location") != "/" {
		t.Errorf("open redirect after login: %s", resp.Header.Get("Location"))
	}
}

func TestEveryPageRenders(t *testing.T) {
	h := newHarness(t)
	h.setup()
	c := &model.Connection{Name: "G", URL: "http://127.0.0.1:1", Token: "t"}
	if err := h.store.SaveConnection(c); err != nil {
		t.Fatal(err)
	}
	rep := &model.Report{Name: "SLA <script>", Enabled: true, ConnectionID: c.ID, DashboardUID: "abc", Time: model.TimeSpec{Preset: "previous_month"},
		Schedule: schedule.Spec{Kind: schedule.Monthly, At: "08:00", MonthDay: 1}}
	if err := h.store.SaveReport(rep); err != nil {
		t.Fatal(err)
	}
	run := &model.Run{ReportID: rep.ID, ReportName: rep.Name, Trigger: model.TriggerManual, Status: model.RunFailed, Error: "boom"}
	_ = h.store.CreateRun(run)
	for _, path := range []string{"/", "/reports/new", "/reports/" + rep.ID, "/runs", "/runs/" + run.ID, "/connections",
		"/connections/new", "/connections/" + c.ID, "/settings", "/settings/email", "/settings/branding", "/settings/license",
		"/settings/api", "/settings/account"} {
		resp, body := h.get(path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: HTTP %d", path, resp.StatusCode)
			continue
		}
		if strings.Contains(body, "<script>") && strings.Contains(body, "SLA <script>") {
			t.Errorf("%s: report name not escaped", path)
		}
		if !strings.Contains(body, "Panelpost") {
			t.Errorf("%s: page did not render the layout", path)
		}
	}
	if resp, _ := h.get("/runs/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing run: %d", resp.StatusCode)
	}
	if resp, _ := h.get("/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("health: %d", resp.StatusCode)
	}
}

func TestCommunityLimitsAndProUnlock(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.csrf()
	c := &model.Connection{Name: "G", URL: "http://127.0.0.1:1", Token: "t"}
	_ = h.store.SaveConnection(c)
	form := func(name string) url.Values {
		return url.Values{"csrf": {token}, "name": {name}, "enabled": {"on"}, "connection_id": {c.ID}, "dashboard_uid": {"abc"},
			"time_preset": {"previous_week"}, "timezone": {"UTC"}, "mode": {"dashboard"}, "paper": {"A4"}, "orientation": {"landscape"},
			"theme": {"light"}, "schedule_kind": {"weekly"}, "at": {"07:00"}, "weekdays": {"1"}, "email_to": {"a@example.com"}}
	}
	for i := 0; i < 3; i++ {
		if resp := h.post("/reports/save", form("R"+string(rune('1'+i)))); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("report %d: %d", i, resp.StatusCode)
		}
	}
	// The fourth report is refused in the Community edition.
	if resp := h.post("/reports/save", form("R4")); resp.StatusCode == http.StatusSeeOther {
		t.Fatal("fourth report saved on Community")
	}
	if n, _ := h.store.CountReports(); n != 3 {
		t.Fatalf("reports = %d", n)
	}
	// Bursting is refused too.
	f := form("Burst")
	f.Set("burst_enabled", "on")
	f.Set("burst_variable", "client")
	f.Set("burst_targets", "acme | Acme | it@acme.example")
	reports, _ := h.store.ListReports()
	f.Set("id", reports[0].ID)
	if resp := h.post("/reports/save", f); resp.StatusCode == http.StatusSeeOther {
		t.Fatal("bursting saved on Community")
	}

	// A Business key unlocks both.
	key, _ := license.Sign(h.priv, license.Payload{Tier: license.Business, Name: "Acme MSP"})
	if resp := h.post("/settings/license", url.Values{"csrf": {token}, "key": {key}}); !strings.Contains(resp.Header.Get("Location"), "ok=") {
		t.Fatalf("activate: %s", resp.Header.Get("Location"))
	}
	if !h.srv.License.Current().Paid() {
		t.Fatal("license not active")
	}
	if resp := h.post("/reports/save", form("R4")); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("fourth report on Business: %d", resp.StatusCode)
	}
	if resp := h.post("/reports/save", f); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("burst on Business: %d", resp.StatusCode)
	}
	saved, _ := h.store.GetReport(reports[0].ID)
	if !saved.Burst.Enabled || saved.Burst.Targets[0].Label != "Acme" || saved.Burst.Targets[0].Recipients[0] != "it@acme.example" {
		t.Errorf("burst = %+v", saved.Burst)
	}
}

func TestAPIRequiresKeyAndPaidEdition(t *testing.T) {
	h := newHarness(t)
	h.setup()
	token := h.csrf()
	call := func(key string) int {
		req, _ := http.NewRequest(http.MethodGet, h.ts.URL+"/api/v1/reports", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := call(""); got != http.StatusUnauthorized {
		t.Errorf("no key: %d", got)
	}
	if got := call("pp_invalid"); got != http.StatusUnauthorized {
		t.Errorf("bad key: %d", got)
	}
	// Community cannot create keys.
	h.post("/settings/api", url.Values{"csrf": {token}, "name": {"ci"}})
	if keys, _ := h.store.ListAPIKeys(); len(keys) != 0 {
		t.Fatal("API key created on Community")
	}
	lk, _ := license.Sign(h.priv, license.Payload{Tier: license.Pro, Name: "Acme"})
	if _, err := h.srv.License.Apply(context.Background(), lk); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/settings/api", strings.NewReader(url.Values{"csrf": {token}, "name": {"ci"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.ts.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	key := regexp.MustCompile(`pp_[0-9a-f]{48}`).FindString(string(body))
	if key == "" {
		t.Fatal("new key not shown")
	}
	if got := call(key); got != http.StatusOK {
		t.Errorf("valid key: %d", got)
	}
	_ = h.srv.License.Remove()
	if got := call(key); got != http.StatusPaymentRequired {
		t.Errorf("valid key without license: %d", got)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"": "/", "/runs": "/runs", "//evil.com": "/", "https://evil.com": "/", "/\\evil": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
