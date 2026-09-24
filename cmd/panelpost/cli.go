package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/compose"
	"github.com/DasPechCodeWeg/panelpost/internal/grafana"
	"github.com/DasPechCodeWeg/panelpost/internal/license"
	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/render"
	"github.com/DasPechCodeWeg/panelpost/internal/timerange"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// renderCmd renders one dashboard to a PDF file, for scripting and for
// trying Panelpost in one minute.
func renderCmd(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	grafanaURL := fs.String("grafana", env("GRAFANA_URL", ""), "Grafana URL (or GRAFANA_URL)")
	token := fs.String("token", env("GRAFANA_TOKEN", ""), "service account token (or GRAFANA_TOKEN)")
	org := fs.Int64("org", 1, "organisation ID")
	uid := fs.String("dashboard", "", "dashboard UID")
	period := fs.String("period", "last_7d", "period preset, e.g. previous_month, previous_week, yesterday")
	from := fs.String("from", "", "custom start, e.g. now-30d/d (overrides -period)")
	to := fs.String("to", "", "custom end, e.g. now-1d/d")
	tz := fs.String("tz", env("TZ", "UTC"), "time zone, e.g. Europe/Amsterdam")
	mode := fs.String("mode", "dashboard", "layout: dashboard or panels")
	paper := fs.String("paper", "A4", "A4, Letter or A3")
	portrait := fs.Bool("portrait", false, "portrait instead of landscape")
	cover := fs.Bool("cover", false, "add a cover page")
	dark := fs.Bool("dark", false, "render with Grafana's dark theme")
	title := fs.String("title", "", "report title (default: dashboard title)")
	width := fs.Int("width", 0, "browser width in pixels")
	insecure := fs.Bool("insecure", false, "accept self-signed Grafana certificates")
	out := fs.String("out", "report.pdf", "output file")
	key := fs.String("license-key", env("PANELPOST_LICENSE_KEY", ""), "offline license key (removes the Panelpost credit)")
	var vars multiFlag
	fs.Var(&vars, "var", "template variable name=value[,value] (repeatable)")
	_ = fs.Parse(args)

	if *grafanaURL == "" || *token == "" || *uid == "" {
		return errors.New("render needs -grafana, -token and -dashboard (see panelpost render -h)")
	}
	conn := &model.Connection{Name: "cli", URL: *grafanaURL, OrgID: *org, Token: *token, InsecureTLS: *insecure}
	if err := conn.Validate(); err != nil {
		return err
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return fmt.Errorf("time zone: %w", err)
	}
	// Fail before rendering, not after, when the PDF cannot be written.
	if err := checkWritable(*out); err != nil {
		return err
	}
	fromExpr, toExpr := *from, *to
	if fromExpr == "" || toExpr == "" {
		p, ok := timerange.PresetByID(*period)
		if !ok {
			var ids []string
			for _, p := range timerange.Presets {
				ids = append(ids, p.ID)
			}
			return fmt.Errorf("unknown period %q; use one of %s", *period, strings.Join(ids, ", "))
		}
		fromExpr, toExpr = p.From, p.To
	}
	rng, err := timerange.Resolve(fromExpr, toExpr, time.Now(), timerange.Options{Location: loc})
	if err != nil {
		return err
	}
	var variables []model.Variable
	for _, v := range vars {
		name, val, ok := strings.Cut(v, "=")
		if !ok {
			return fmt.Errorf("-var %q must look like name=value", v)
		}
		variables = append(variables, model.Variable{Name: name, Values: strings.Split(val, ",")})
	}
	layout := model.Layout{Mode: *mode, Paper: *paper, Width: *width, ExpandRows: true, CoverPage: *cover}
	if *portrait {
		layout.Orientation = "portrait"
	}
	if *dark {
		layout.Theme = "dark"
	}
	if err := layout.Normalize(); err != nil {
		return err
	}

	lic := license.Community()
	if *key != "" {
		if lic, err = license.VerifyOffline(*key, license.EmbeddedPublicKeys(), time.Now()); err != nil {
			return fmt.Errorf("license key: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := grafana.New(conn)
	if err != nil {
		return err
	}
	dashPath, dashTitle := "/d/"+*uid, *uid
	if d, err := client.GetDashboard(ctx, *uid); err == nil {
		dashTitle = d.Title
		if u := d.URL; strings.HasPrefix(u, "/") {
			dashPath = u
		}
	} else {
		fmt.Fprintln(os.Stderr, "warning: could not read dashboard metadata:", err)
	}
	b := render.New(render.Options{ExecPath: os.Getenv("PANELPOST_CHROME_PATH"), NoSandbox: !strings.EqualFold(os.Getenv("PANELPOST_CHROME_SANDBOX"), "true")})
	defer b.Close()
	q := grafana.DashboardQuery(conn.OrgID, rng.From, rng.To, *tz, layout.Theme, variables)
	started := time.Now()
	page, err := b.Open(ctx, render.Request{URL: strings.TrimRight(conn.URL, "/") + dashPath + "?" + q.Encode(), Origin: conn.URL,
		Headers: map[string]string{"Authorization": "Bearer " + conn.Token}, InsecureTLS: conn.InsecureTLS,
		Width: layout.Width, Timezone: *tz, ExpandRows: true})
	if err != nil {
		return err
	}
	defer page.Close()
	for _, w := range page.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	var kvs []compose.KV
	for _, v := range variables {
		kvs = append(kvs, compose.KV{Key: v.Name, Value: strings.Join(v.Values, ", ")})
	}
	reportTitle := *title
	if reportTitle == "" {
		reportTitle = dashTitle
	}
	doc := compose.Document{Title: reportTitle, Dashboard: dashTitle, Period: rng.Label(), TimeZone: *tz, Variables: kvs,
		GeneratedAt: time.Now().In(loc), Credit: !lic.Limits().RemoveCredit, Paper: layout.Paper,
		Landscape: layout.Orientation == "landscape", Cover: layout.CoverPage, Mode: layout.Mode, Columns: 2, Theme: layout.Theme}
	res, err := compose.Build(ctx, b, page, doc, compose.Options{Format: "jpeg", Quality: 88})
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, res.PDF, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d pages, %d KB, period %s (%s), in %s\n", *out, res.Pages, len(res.PDF)/1024, rng.Label(), *tz, time.Since(started).Round(100*time.Millisecond))
	return nil
}

// licenseCmd holds the vendor tools for offline keys.
func licenseCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: panelpost license keygen | sign | verify KEY")
	}
	switch args[0] {
	case "keygen":
		pub, priv, err := license.GenerateKeyPair()
		if err != nil {
			return err
		}
		fmt.Println("Public key (embed at build time with -ldflags):")
		fmt.Println(pub)
		fmt.Println()
		fmt.Println("Private key (keep secret, never commit):")
		fmt.Println(priv)
		return nil
	case "sign":
		fs := flag.NewFlagSet("sign", flag.ExitOnError)
		keyFile := fs.String("private-key-file", "", "file containing the base64 private key")
		tier := fs.String("tier", "pro", "pro or business")
		name := fs.String("name", "", "licensee (company) name")
		email := fs.String("email", "", "licensee email")
		expires := fs.String("expires", "", "expiry date YYYY-MM-DD (empty = perpetual)")
		_ = fs.Parse(args[1:])
		raw, err := os.ReadFile(*keyFile)
		if err != nil {
			return fmt.Errorf("read private key: %w", err)
		}
		priv, err := license.ParsePrivateKey(string(raw))
		if err != nil {
			return err
		}
		t, ok := license.ParseTier(*tier)
		if !ok {
			return fmt.Errorf("unknown tier %q", *tier)
		}
		p := license.Payload{Tier: t, Name: *name, Email: *email}
		if *expires != "" {
			exp, err := time.Parse("2006-01-02", *expires)
			if err != nil {
				return fmt.Errorf("expires: %w", err)
			}
			p.ExpiresAt = exp.Add(24*time.Hour - time.Second).Unix()
		}
		key, err := license.Sign(priv, p)
		if err != nil {
			return err
		}
		fmt.Println(key)
		return nil
	case "verify":
		if len(args) < 2 {
			return errors.New("usage: panelpost license verify KEY")
		}
		lic, err := license.VerifyOffline(args[1], license.EmbeddedPublicKeys(), time.Now())
		if err != nil {
			return err
		}
		exp := "never"
		if !lic.ExpiresAt.IsZero() {
			exp = lic.ExpiresAt.Format("2006-01-02")
		}
		fmt.Printf("valid %s license %s for %s <%s>, expires %s\n", lic.Tier.Title(), lic.ID, lic.Licensee, lic.Email, exp)
		return nil
	}
	return fmt.Errorf("unknown license command %q", args[0])
}

// checkWritable reports whether a file can be created at path.
func checkWritable(path string) error {
	probe, err := os.CreateTemp(filepath.Dir(path), ".panelpost-*")
	if err != nil {
		msg := fmt.Sprintf("cannot write %s: %v", path, err)
		if errors.Is(err, os.ErrPermission) {
			msg += ` (in Docker, add --user "$(id -u):$(id -g)" so the container may write to your folder)`
		}
		return errors.New(msg)
	}
	probe.Close()
	return os.Remove(probe.Name())
}
