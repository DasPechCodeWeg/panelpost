package model

import (
	"strings"
	"testing"

	"github.com/DasPechCodeWeg/panelpost/internal/schedule"
)

func validReport() *Report {
	return &Report{Name: " SLA ", ConnectionID: "c", DashboardUID: "abc", Time: TimeSpec{Preset: "previous_month"},
		Schedule: schedule.Spec{Kind: schedule.Monthly, At: "08:00", MonthDay: 1}}
}

func TestReportValidateNormalises(t *testing.T) {
	r := validReport()
	r.Delivery.Email.To = []string{"a@example.com, A@Example.com; Bob <bob@example.com>\nc@example.com"}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if r.Name != "SLA" || r.Layout.Mode != LayoutDashboard || r.Layout.Paper != "A4" || r.Layout.Width != 1400 {
		t.Errorf("defaults not applied: %+v", r.Layout)
	}
	got := strings.Join(r.Delivery.Email.To, "|")
	if got != `a@example.com|"Bob" <bob@example.com>|c@example.com` {
		t.Errorf("addresses = %s", got)
	}
}

func TestReportValidateRejects(t *testing.T) {
	cases := map[string]func(*Report){
		"name":        func(r *Report) { r.Name = "" },
		"dashboard":   func(r *Report) { r.DashboardUID = "" },
		"preset":      func(r *Report) { r.Time.Preset = "fortnight" },
		"custom":      func(r *Report) { r.Time = TimeSpec{Preset: "custom", From: "now", To: "now-1d"} },
		"timezone":    func(r *Report) { r.Timezone = "Mars/Base" },
		"variable":    func(r *Report) { r.Variables = []Variable{{Name: "bad name!"}} },
		"email":       func(r *Report) { r.Delivery.Email.To = []string{"not-an-email"} },
		"webhook":     func(r *Report) { r.Delivery.Webhooks = []Webhook{{URL: "ftp://x"}} },
		"accent":      func(r *Report) { r.Branding.AccentColor = "red" },
		"burst empty": func(r *Report) { r.Burst = Burst{Enabled: true, Variable: "client"} },
		"burst dup": func(r *Report) {
			r.Burst = Burst{Enabled: true, Variable: "client", Targets: []BurstTarget{{Value: "a"}, {Value: "a"}}}
		},
		"schedule": func(r *Report) { r.Schedule.MonthDay = 31 },
		"width":    func(r *Report) { r.Layout.Width = 100 },
	}
	for name, mutate := range cases {
		r := validReport()
		mutate(r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
}

func TestConnectionValidate(t *testing.T) {
	c := &Connection{Name: "x", URL: "https://grafana.example.com/", Token: "t"}
	if err := c.Validate(); err != nil || c.URL != "https://grafana.example.com" || c.OrgID != 1 {
		t.Errorf("valid connection: %v %+v", err, c)
	}
	bad := []*Connection{
		{Name: "", URL: "https://g", Token: "t"},
		{Name: "x", URL: "grafana.example.com", Token: "t"},
		{Name: "x", URL: "https://g?x=1", Token: "t"},
		{Name: "x", URL: "https://g", Token: ""},
		{Name: "x", URL: "https://g", Token: "t", ExtraHeaders: map[string]string{"authorization": "Basic x"}},
	}
	for _, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%+v validated", c)
		}
	}
}

func TestBrandingMerge(t *testing.T) {
	base := Branding{CompanyName: "MSP", AccentColor: "#111111", FooterText: "f"}
	got := base.Merge(Branding{CompanyName: "Client", LogoAsset: "logo.png"})
	if got.CompanyName != "Client" || got.AccentColor != "#111111" || got.LogoAsset != "logo.png" || got.FooterText != "f" {
		t.Errorf("merge = %+v", got)
	}
}
