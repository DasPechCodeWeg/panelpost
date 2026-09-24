// Package grafana is a small client for the Grafana HTTP API, covering what
// Panelpost needs: identity checks, dashboard search and dashboard metadata.
package grafana

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/model"
)

// Client talks to one Grafana instance.
type Client struct {
	base    *url.URL
	orgID   int64
	token   string
	headers map[string]string
	http    *http.Client
}

// New builds a client for a connection.
func New(c *model.Connection) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(c.URL, "/"))
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.InsecureTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit user choice for self-signed Grafana
	}
	return &Client{base: u, orgID: c.OrgID, token: c.Token, headers: c.ExtraHeaders,
		http: &http.Client{Timeout: 30 * time.Second, Transport: tr}}, nil
}

// BaseURL returns the Grafana root URL without a trailing slash.
func (c *Client) BaseURL() string { return c.base.String() }

// APIError is a non-2xx answer from Grafana.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "Grafana rejected the token (401): check that the service account token is correct and not expired"
	case http.StatusForbidden:
		return "Grafana denied access (403): give the service account at least the Viewer role on this dashboard"
	case http.StatusNotFound:
		return "not found in Grafana (404)" + suffix(e.Message)
	}
	return fmt.Sprintf("Grafana returned HTTP %d%s", e.Status, suffix(e.Message))
}

func suffix(m string) string {
	if m == "" {
		return ""
	}
	return ": " + m
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	if q != nil {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if c.orgID > 0 {
		req.Header.Set("X-Grafana-Org-Id", strconv.FormatInt(c.orgID, 10))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Grafana at %s: %w", c.base.Host, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &e)
		return &APIError{Status: resp.StatusCode, Message: e.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		if strings.Contains(strings.ToLower(string(body[:min(len(body), 200)])), "<html") {
			return errors.New("Grafana answered with an HTML page instead of JSON; is the URL correct and not behind a login proxy?")
		}
		return fmt.Errorf("unexpected Grafana response: %w", err)
	}
	return nil
}

// Info describes the Grafana server and the identity of the token.
type Info struct {
	Version string
	Edition string
	OrgName string
	Login   string
}

// Check verifies the connection and returns server information.
func (c *Client) Check(ctx context.Context) (Info, error) {
	var info Info
	var org struct {
		Name string `json:"name"`
	}
	if err := c.get(ctx, "/api/org", nil, &org); err != nil {
		return info, err
	}
	info.OrgName = org.Name
	var user struct {
		Login string `json:"login"`
	}
	if err := c.get(ctx, "/api/user", nil, &user); err == nil {
		info.Login = user.Login
	}
	var fs struct {
		BuildInfo struct {
			Version string `json:"version"`
			Edition string `json:"edition"`
		} `json:"buildInfo"`
	}
	if err := c.get(ctx, "/api/frontend/settings", nil, &fs); err == nil {
		info.Version, info.Edition = fs.BuildInfo.Version, fs.BuildInfo.Edition
	}
	return info, nil
}

// DashboardHit is a search result.
type DashboardHit struct {
	UID         string   `json:"uid"`
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	FolderTitle string   `json:"folderTitle"`
	Tags        []string `json:"tags"`
}

// SearchDashboards finds dashboards by title.
func (c *Client) SearchDashboards(ctx context.Context, query string, limit int) ([]DashboardHit, error) {
	if limit <= 0 {
		limit = 100
	}
	q := url.Values{"type": {"dash-db"}, "query": {query}, "limit": {strconv.Itoa(limit)}}
	var hits []DashboardHit
	if err := c.get(ctx, "/api/search", q, &hits); err != nil {
		return nil, err
	}
	return hits, nil
}

// VariableInfo describes a dashboard template variable.
type VariableInfo struct {
	Name       string   `json:"name"`
	Label      string   `json:"label"`
	Type       string   `json:"type"`
	Multi      bool     `json:"multi"`
	IncludeAll bool     `json:"include_all"`
	Current    []string `json:"current"`
	Options    []string `json:"options"`
}

// Dashboard is the metadata Panelpost needs about a dashboard.
type Dashboard struct {
	UID       string
	Title     string
	URL       string // path such as /d/uid/slug
	Variables []VariableInfo
	Panels    []string // panel titles, in layout order
}

type v1Dashboard struct {
	Meta struct {
		URL string `json:"url"`
	} `json:"meta"`
	Dashboard struct {
		UID        string `json:"uid"`
		Title      string `json:"title"`
		Templating struct {
			List []struct {
				Name       string          `json:"name"`
				Label      string          `json:"label"`
				Type       string          `json:"type"`
				Multi      bool            `json:"multi"`
				IncludeAll bool            `json:"includeAll"`
				Current    json.RawMessage `json:"current"`
				Options    []struct {
					Text  any `json:"text"`
					Value any `json:"value"`
				} `json:"options"`
				Query any  `json:"query"`
				Hide  int  `json:"hide"`
				Skip  bool `json:"skipUrlSync"`
			} `json:"list"`
		} `json:"templating"`
		Panels []v1Panel `json:"panels"`
	} `json:"dashboard"`
}

type v1Panel struct {
	Title  string    `json:"title"`
	Type   string    `json:"type"`
	Panels []v1Panel `json:"panels"`
}

// GetDashboard loads dashboard metadata by UID.
func (c *Client) GetDashboard(ctx context.Context, uid string) (*Dashboard, error) {
	var raw v1Dashboard
	err := c.get(ctx, "/api/dashboards/uid/"+url.PathEscape(uid), nil, &raw)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotAcceptable {
			return c.getDashboardV2(ctx, uid)
		}
		return nil, err
	}
	d := &Dashboard{UID: raw.Dashboard.UID, Title: raw.Dashboard.Title, URL: raw.Meta.URL}
	if d.URL == "" {
		d.URL = "/d/" + uid
	}
	for _, v := range raw.Dashboard.Templating.List {
		if v.Type == "adhoc" || v.Type == "groupby" {
			continue
		}
		info := VariableInfo{Name: v.Name, Label: v.Label, Type: v.Type, Multi: v.Multi, IncludeAll: v.IncludeAll}
		info.Current = currentValues(v.Current)
		for _, o := range v.Options {
			if s := stringify(o.Value); s != "" && s != "$__all" {
				info.Options = append(info.Options, s)
			}
		}
		if len(info.Options) == 0 && v.Type == "custom" {
			if q, ok := v.Query.(string); ok {
				for _, part := range strings.Split(q, ",") {
					part = strings.TrimSpace(part)
					if i := strings.Index(part, " : "); i >= 0 {
						part = strings.TrimSpace(part[i+3:])
					}
					if part != "" {
						info.Options = append(info.Options, part)
					}
				}
			}
		}
		d.Variables = append(d.Variables, info)
	}
	var walk func([]v1Panel)
	walk = func(ps []v1Panel) {
		for _, p := range ps {
			if p.Type != "row" && p.Title != "" {
				d.Panels = append(d.Panels, p.Title)
			}
			walk(p.Panels)
		}
	}
	walk(raw.Dashboard.Panels)
	return d, nil
}

// getDashboardV2 reads dashboards stored in the v2 schema (Grafana 12+).
func (c *Client) getDashboardV2(ctx context.Context, uid string) (*Dashboard, error) {
	ns := "default"
	if c.orgID > 1 {
		ns = "org-" + strconv.FormatInt(c.orgID, 10)
	}
	var raw struct {
		Spec struct {
			Title     string `json:"title"`
			Variables []struct {
				Kind string `json:"kind"`
				Spec struct {
					Name       string          `json:"name"`
					Label      string          `json:"label"`
					Multi      bool            `json:"multi"`
					IncludeAll bool            `json:"includeAll"`
					Current    json.RawMessage `json:"current"`
					Options    []struct {
						Value any `json:"value"`
					} `json:"options"`
				} `json:"spec"`
			} `json:"variables"`
			Elements map[string]struct {
				Spec struct {
					Title string `json:"title"`
				} `json:"spec"`
			} `json:"elements"`
		} `json:"spec"`
	}
	var err error
	for _, version := range []string{"v2", "v2beta1"} {
		err = c.get(ctx, "/apis/dashboard.grafana.app/"+version+"/namespaces/"+ns+"/dashboards/"+url.PathEscape(uid), nil, &raw)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	d := &Dashboard{UID: uid, Title: raw.Spec.Title, URL: "/d/" + uid}
	for _, v := range raw.Spec.Variables {
		if strings.Contains(strings.ToLower(v.Kind), "adhoc") || strings.Contains(strings.ToLower(v.Kind), "groupby") {
			continue
		}
		info := VariableInfo{Name: v.Spec.Name, Label: v.Spec.Label, Type: strings.TrimSuffix(strings.ToLower(v.Kind), "variable"),
			Multi: v.Spec.Multi, IncludeAll: v.Spec.IncludeAll, Current: currentValues(v.Spec.Current)}
		for _, o := range v.Spec.Options {
			if s := stringify(o.Value); s != "" && s != "$__all" {
				info.Options = append(info.Options, s)
			}
		}
		d.Variables = append(d.Variables, info)
	}
	for _, e := range raw.Spec.Elements {
		if e.Spec.Title != "" {
			d.Panels = append(d.Panels, e.Spec.Title)
		}
	}
	return d, nil
}

func currentValues(raw json.RawMessage) []string {
	var cur struct {
		Value any `json:"value"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &cur) != nil {
		return nil
	}
	switch v := cur.Value.(type) {
	case []any:
		var out []string
		for _, x := range v {
			if s := stringify(x); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		if s := stringify(v); s != "" {
			return []string{s}
		}
	}
	return nil
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// AllValue is Grafana's URL token for "All" in a template variable.
const AllValue = "$__all"

// DashboardQuery builds the URL query that renders a dashboard for a report.
func DashboardQuery(orgID int64, from, to time.Time, timezone, theme string, vars []model.Variable) url.Values {
	q := url.Values{}
	q.Set("orgId", strconv.FormatInt(max(orgID, 1), 10))
	q.Set("from", strconv.FormatInt(from.UnixMilli(), 10))
	q.Set("to", strconv.FormatInt(to.UnixMilli(), 10))
	if timezone != "" {
		q.Set("timezone", timezone)
	}
	q.Set("theme", theme)
	q.Set("kiosk", "true")
	q.Set("_dash.hideTimePicker", "true")
	q.Set("_dash.hideVariables", "true")
	q.Set("_dash.hideLinks", "true")
	for _, v := range vars {
		for _, val := range v.Values {
			if strings.EqualFold(val, "all") || val == "*" {
				val = AllValue
			}
			q.Add("var-"+v.Name, val)
		}
	}
	return q
}
