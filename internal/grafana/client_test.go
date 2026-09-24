package grafana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/model"
)

func fakeGrafana(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"invalid API key"}`))
			return
		}
		if r.Header.Get("X-Grafana-Org-Id") != "2" {
			t.Errorf("org header = %q", r.Header.Get("X-Grafana-Org-Id"))
		}
		switch r.URL.Path {
		case "/grafana/api/org":
			_, _ = w.Write([]byte(`{"id":2,"name":"Customers"}`))
		case "/grafana/api/user":
			_, _ = w.Write([]byte(`{"login":"sa-reporter"}`))
		case "/grafana/api/frontend/settings":
			_, _ = w.Write([]byte(`{"buildInfo":{"version":"13.2.2","edition":"Open Source"}}`))
		case "/grafana/api/search":
			if r.URL.Query().Get("type") != "dash-db" {
				t.Errorf("search type = %q", r.URL.Query().Get("type"))
			}
			_, _ = w.Write([]byte(`[{"uid":"abc","title":"SLA","url":"/grafana/d/abc/sla","folderTitle":"Clients"}]`))
		case "/grafana/api/dashboards/uid/abc":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{"url": "/grafana/d/abc/sla"},
				"dashboard": map[string]any{"uid": "abc", "title": "SLA",
					"templating": map[string]any{"list": []any{
						map[string]any{"name": "client", "type": "custom", "multi": true, "includeAll": true,
							"current": map[string]any{"value": []any{"acme", "globex"}},
							"options": []any{map[string]any{"value": "$__all"}, map[string]any{"value": "acme"}, map[string]any{"value": "globex"}}},
						map[string]any{"name": "env", "type": "custom", "query": "Production : prod, staging", "current": map[string]any{"value": "prod"}},
						map[string]any{"name": "filters", "type": "adhoc"},
					}},
					"panels": []any{
						map[string]any{"title": "Uptime", "type": "stat"},
						map[string]any{"title": "Details", "type": "row", "panels": []any{map[string]any{"title": "Latency", "type": "timeseries"}}},
					}},
			})
		case "/grafana/api/dashboards/uid/v2only":
			w.WriteHeader(http.StatusNotAcceptable)
		case "/grafana/apis/dashboard.grafana.app/v2/namespaces/org-2/dashboards/v2only":
			_, _ = w.Write([]byte(`{"spec":{"title":"New layouts","variables":[{"kind":"CustomVariable","spec":{"name":"site","current":{"value":"ams"},"options":[{"value":"ams"},{"value":"fra"}]}}],"elements":{"panel-1":{"spec":{"title":"Power"}}}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestClient(t *testing.T) {
	srv := fakeGrafana(t)
	defer srv.Close()
	c, err := New(&model.Connection{URL: srv.URL + "/grafana/", OrgID: 2, Token: "good"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	info, err := c.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "13.2.2" || info.OrgName != "Customers" || info.Login != "sa-reporter" {
		t.Errorf("info = %+v", info)
	}
	hits, err := c.SearchDashboards(ctx, "", 0)
	if err != nil || len(hits) != 1 || hits[0].FolderTitle != "Clients" {
		t.Fatalf("hits = %+v %v", hits, err)
	}
	d, err := c.GetDashboard(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if d.URL != "/grafana/d/abc/sla" || len(d.Variables) != 2 {
		t.Fatalf("dashboard = %+v", d)
	}
	client := d.Variables[0]
	if !client.Multi || !client.IncludeAll || strings.Join(client.Current, ",") != "acme,globex" || strings.Join(client.Options, ",") != "acme,globex" {
		t.Errorf("client variable = %+v", client)
	}
	if env := d.Variables[1]; strings.Join(env.Options, ",") != "prod,staging" || env.Current[0] != "prod" {
		t.Errorf("env variable = %+v", env)
	}
	if strings.Join(d.Panels, ",") != "Uptime,Latency" {
		t.Errorf("panels = %v", d.Panels)
	}
	v2, err := c.GetDashboard(ctx, "v2only")
	if err != nil {
		t.Fatal(err)
	}
	if v2.Title != "New layouts" || v2.Variables[0].Name != "site" || v2.Panels[0] != "Power" {
		t.Errorf("v2 dashboard = %+v", v2)
	}
}

func TestClientErrors(t *testing.T) {
	srv := fakeGrafana(t)
	defer srv.Close()
	c, _ := New(&model.Connection{URL: srv.URL + "/grafana", OrgID: 2, Token: "bad"})
	_, err := c.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rejected the token") {
		t.Errorf("error = %v", err)
	}
	gone, _ := New(&model.Connection{URL: "http://127.0.0.1:1", Token: "x"})
	if _, err := gone.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "could not reach") {
		t.Errorf("unreachable error = %v", err)
	}
}

func TestDashboardQuery(t *testing.T) {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 31, 23, 59, 59, 999e6, time.UTC)
	q := DashboardQuery(0, from, to, "Europe/Amsterdam", "light", []model.Variable{
		{Name: "client", Values: []string{"acme", "globex"}},
		{Name: "env", Values: []string{"All"}},
	})
	if q.Get("orgId") != "1" || q.Get("from") != "1785542400000" || q.Get("to") != "1788220799999" {
		t.Errorf("range query = %v", q)
	}
	if got := q["var-client"]; len(got) != 2 || got[1] != "globex" {
		t.Errorf("multi value = %v", got)
	}
	if q.Get("var-env") != AllValue {
		t.Errorf("all value = %q", q.Get("var-env"))
	}
	for _, k := range []string{"kiosk", "_dash.hideTimePicker", "_dash.hideVariables"} {
		if _, ok := q[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
}
