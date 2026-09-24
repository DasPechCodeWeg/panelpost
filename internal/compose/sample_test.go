package compose

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/grafana"
	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/render"
	"github.com/DasPechCodeWeg/panelpost/internal/timerange"
)

// TestSampleReport regenerates the marketing sample from the demo dashboard
// (scripts/demo-setup.py). Run with PANELPOST_SAMPLE_OUT=path.pdf plus the
// PANELPOST_IT_* variables.
func TestSampleReport(t *testing.T) {
	out, base, token := os.Getenv("PANELPOST_SAMPLE_OUT"), os.Getenv("PANELPOST_IT_GRAFANA"), os.Getenv("PANELPOST_IT_TOKEN")
	if out == "" || base == "" || token == "" {
		t.Skip("set PANELPOST_SAMPLE_OUT, PANELPOST_IT_GRAFANA and PANELPOST_IT_TOKEN")
	}
	logo, err := os.ReadFile("../../examples/northwind-logo.svg")
	if err != nil {
		t.Fatal(err)
	}
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	rng, _ := timerange.Resolve("now-1M/M", "now-1M/M", time.Now(), timerange.Options{Location: ams})
	b := render.New(render.Options{ExecPath: os.Getenv("PANELPOST_CHROME_PATH"), NoSandbox: true})
	defer b.Close()
	q := grafana.DashboardQuery(1, rng.From, rng.To, "Europe/Amsterdam", "light", []model.Variable{{Name: "client", Values: []string{"acme"}}})
	page, err := b.Open(context.Background(), render.Request{URL: strings.TrimRight(base, "/") + "/d/service-report?" + q.Encode(),
		Origin: base, Headers: map[string]string{"Authorization": "Bearer " + token}, Width: 1400, Timezone: "Europe/Amsterdam", ExpandRows: true})
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close()
	doc := Document{
		Title: "Monthly service report", Dashboard: "Availability and performance", Period: rng.Label(), TimeZone: "Europe/Amsterdam",
		Subtitle: "Prepared for Acme Corp", GeneratedAt: time.Now().In(ams),
		Branding: Branding{CompanyName: "Northwind Managed IT", Logo: logo, Accent: "#0f766e", FooterText: "Confidential · Northwind Managed IT · support@northwind.example",
			Intro: "This report summarises the availability, performance and capacity of the services Northwind manages for Acme Corp.\nYour service manager: Sanne de Vries, +31 20 555 0142."},
		Paper: "A4", Landscape: true, Cover: true, Mode: "dashboard",
	}
	res, err := Build(context.Background(), b, page, doc, Options{Format: "jpeg", Quality: 88})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, res.PDF, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s: %d pages, %d KB", out, res.Pages, len(res.PDF)/1024)
}
