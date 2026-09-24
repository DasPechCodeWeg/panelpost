package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/grafana"
	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/render"
	"github.com/DasPechCodeWeg/panelpost/internal/timerange"
)

// Run with PANELPOST_IT_GRAFANA=http://localhost:3000 PANELPOST_IT_TOKEN=... go test -run Integration ./internal/compose/
func TestIntegrationRenderPDF(t *testing.T) {
	base, token := os.Getenv("PANELPOST_IT_GRAFANA"), os.Getenv("PANELPOST_IT_TOKEN")
	if base == "" || token == "" {
		t.Skip("set PANELPOST_IT_GRAFANA and PANELPOST_IT_TOKEN to run against a real Grafana")
	}
	uid := os.Getenv("PANELPOST_IT_DASHBOARD")
	if uid == "" {
		uid = "proto1"
	}
	b := render.New(render.Options{ExecPath: os.Getenv("PANELPOST_CHROME_PATH"), NoSandbox: true})
	defer b.Close()
	ctx := context.Background()
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	rng, err := timerange.Resolve("now-7d/d", "now-1d/d", time.Now(), timerange.Options{Location: ams})
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("PANELPOST_IT_OUT")
	if out == "" {
		out = t.TempDir()
	}
	for _, mode := range []string{"dashboard", "panels"} {
		for _, cover := range []bool{true, false} {
			start := time.Now()
			q := grafana.DashboardQuery(1, rng.From, rng.To, "Europe/Amsterdam", "light", []model.Variable{{Name: "client", Values: []string{"globex"}}})
			page, err := b.Open(ctx, render.Request{
				URL:     strings.TrimRight(base, "/") + "/d/" + uid + "?" + q.Encode(),
				Origin:  base,
				Headers: map[string]string{"Authorization": "Bearer " + token},
				Width:   1400, Timezone: "Europe/Amsterdam", ExpandRows: true,
			})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			doc := Document{
				Title: "Weekly infrastructure report", Dashboard: "Proto Infra Overview", Period: rng.Label(), TimeZone: "Europe/Amsterdam",
				Subtitle: "Prepared for Globex", Variables: []KV{{"Client", "globex"}}, GeneratedAt: time.Now().In(ams),
				Branding: Branding{CompanyName: "Northwind Managed IT", Accent: "#0f766e", FooterText: "Confidential · Northwind Managed IT",
					Intro: "This report summarises the availability and performance of your infrastructure.\nQuestions? support@northwind.example"},
				Credit: true, Paper: "A4", Landscape: true, Cover: cover, Mode: mode, Columns: 2,
			}
			// 20 visible panels plus two inside the collapsed "Archive" row.
			if n := len(page.Geometry.Panels); n < 22 && uid == "proto1" {
				t.Errorf("found %d panels, want 22 (collapsed rows expanded); browser state: %s", n, page.DebugState())
			}
			res, err := Build(ctx, b, page, doc, Options{Format: "jpeg", Quality: 90})
			page.Close()
			if err != nil {
				t.Fatalf("build %s: %v", mode, err)
			}
			name := filepath.Join(out, mode+map[bool]string{true: "-cover", false: ""}[cover]+".pdf")
			if err := os.WriteFile(name, res.PDF, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: %d pages, %d KB, %s, warnings=%v", name, res.Pages, len(res.PDF)/1024, time.Since(start).Round(time.Millisecond), page.Warnings)
			if res.Pages < 2 {
				t.Errorf("%s: only %d pages", name, res.Pages)
			}
		}
	}
}
