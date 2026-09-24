package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/license"
	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/render"
	"github.com/DasPechCodeWeg/panelpost/internal/schedule"
	"github.com/DasPechCodeWeg/panelpost/internal/secret"
	"github.com/DasPechCodeWeg/panelpost/internal/store"
)

// Run with PANELPOST_IT_GRAFANA, PANELPOST_IT_TOKEN and PANELPOST_IT_MAILPIT
// (e.g. localhost:1025|http://localhost:8025) against real services.
func TestIntegrationBurstDelivery(t *testing.T) {
	base, token, mailpit := os.Getenv("PANELPOST_IT_GRAFANA"), os.Getenv("PANELPOST_IT_TOKEN"), os.Getenv("PANELPOST_IT_MAILPIT")
	if base == "" || token == "" || mailpit == "" {
		t.Skip("set PANELPOST_IT_GRAFANA, PANELPOST_IT_TOKEN and PANELPOST_IT_MAILPIT")
	}
	smtpAddr, apiURL, _ := strings.Cut(mailpit, "|")
	host, port, _ := strings.Cut(smtpAddr, ":")
	// Start from an empty inbox.
	req, _ := http.NewRequest(http.MethodDelete, apiURL+"/api/v1/messages", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	dir := t.TempDir()
	box, _ := secret.New([]byte("k"))
	st, err := store.Open(filepath.Join(dir, "p.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pubS, privS, _ := license.GenerateKeyPair()
	pubs, _ := license.ParsePublicKeys(pubS)
	priv, _ := license.ParsePrivateKey(privS)
	lic := license.NewManager(license.Options{Settings: st, PublicKeys: pubs})
	key, _ := license.Sign(priv, license.Payload{Tier: license.Business, Name: "Northwind MSP"})
	if _, err := lic.Apply(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	_ = st.SetJSON(KeySMTP, model.SMTP{Host: host, Port: p, TLS: "none", From: "reports@northwind.example", FromName: "Northwind Reports"})
	_ = st.SetJSON(KeyBranding, model.Branding{CompanyName: "Northwind Managed IT", AccentColor: "#0f766e", FooterText: "Confidential"})
	_ = st.SetJSON(KeyGeneral, model.General{Timezone: "Europe/Amsterdam"})

	conn := &model.Connection{Name: "Grafana", URL: base, OrgID: 1, Token: token}
	if err := st.SaveConnection(conn); err != nil {
		t.Fatal(err)
	}
	rep := &model.Report{Name: "Monthly service report", Enabled: true, ConnectionID: conn.ID, DashboardUID: "proto1",
		Time: model.TimeSpec{Preset: "previous_month"}, Timezone: "Europe/Amsterdam",
		Layout:   model.Layout{Mode: model.LayoutDashboard, CoverPage: true, ExpandRows: true},
		Schedule: schedule.Spec{Kind: schedule.Manual},
		Delivery: model.Delivery{Email: model.Email{Bcc: []string{"archive@northwind.example"}}, Folder: true},
		Burst: model.Burst{Enabled: true, Variable: "client", Targets: []model.BurstTarget{
			{Value: "acme", Label: "Acme Corp", Recipients: []string{"it@acme.example"}, Branding: model.Branding{CompanyName: "Acme IT Services"}},
			{Value: "globex", Label: "Globex", Recipients: []string{"cto@globex.example", "ops@globex.example"}},
			{Value: "initech", Label: "Initech", Recipients: []string{"bill@initech.example"}},
		}},
	}
	if err := rep.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveReport(rep); err != nil {
		t.Fatal(err)
	}
	browser := render.New(render.Options{ExecPath: os.Getenv("PANELPOST_CHROME_PATH"), NoSandbox: true})
	defer browser.Close()
	r := &Runner{Store: st, Browser: browser, License: lic, DataDir: dir, Workers: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	run, err := r.Enqueue(rep, model.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r.Wait()
	run, _ = st.GetRun(run.ID)
	t.Logf("run %s in %s: %s %s", run.ID, time.Since(start).Round(time.Millisecond), run.Status, run.Error)
	if run.Status != model.RunSuccess {
		t.Fatalf("status = %s: %s", run.Status, run.Error)
	}
	if len(run.Outputs) != 3 {
		t.Fatalf("outputs = %d", len(run.Outputs))
	}
	for _, o := range run.Outputs {
		t.Logf("  %s: %d pages, %d KB, %v", o.Target, o.Pages, o.Size/1024, o.Delivered)
		if o.Pages < 2 || len(o.Delivered) != 2 {
			t.Errorf("output %+v", o)
		}
	}
	if run.Period == "" || !strings.Contains(run.Period, "Europe/Amsterdam") {
		t.Errorf("period = %q", run.Period)
	}

	resp, err := http.Get(apiURL + "/api/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var inbox struct {
		Messages []struct {
			Subject     string
			From        struct{ Name, Address string }
			To          []struct{ Address string }
			Bcc         []struct{ Address string }
			Attachments int
		}
	}
	_ = json.NewDecoder(resp.Body).Decode(&inbox)
	if len(inbox.Messages) != 3 {
		t.Fatalf("emails = %d", len(inbox.Messages))
	}
	seen := map[string]bool{}
	for _, m := range inbox.Messages {
		var to []string
		for _, a := range m.To {
			to = append(to, a.Address)
		}
		t.Logf("  mail from %q to %v: %q (%d attachment)", m.From.Name, to, m.Subject, m.Attachments)
		if m.Attachments != 1 {
			t.Errorf("attachments = %d", m.Attachments)
		}
		seen[strings.Join(to, ",")] = true
		if strings.Contains(m.Subject, "Acme") && m.From.Name != "Acme IT Services" {
			t.Errorf("white-label sender = %q", m.From.Name)
		}
	}
	for _, want := range []string{"it@acme.example", "cto@globex.example,ops@globex.example", "bill@initech.example"} {
		if !seen[want] {
			t.Errorf("no email to %s", want)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "outbox", "Monthly service report"))
	if len(entries) != 3 {
		t.Errorf("outbox files = %d", len(entries))
	}
}
