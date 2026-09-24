package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/schedule"
	"github.com/DasPechCodeWeg/panelpost/internal/secret"
)

func open(t *testing.T) *Store {
	t.Helper()
	box, _ := secret.New([]byte("test-key"))
	s, err := Open(filepath.Join(t.TempDir(), "panelpost.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestConnectionsAreEncryptedAtRest(t *testing.T) {
	s := open(t)
	c := &model.Connection{Name: "Prod", URL: "https://grafana.example", OrgID: 1, Token: "glsa_secret",
		ExtraHeaders: map[string]string{"X-Proxy": "yes"}}
	if err := s.SaveConnection(c); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT token FROM connections WHERE id = ?`, c.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "glsa_secret" || raw == "" {
		t.Fatalf("token stored as %q", raw)
	}
	got, err := s.GetConnection(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "glsa_secret" || got.ExtraHeaders["X-Proxy"] != "yes" {
		t.Errorf("round trip = %+v", got)
	}
	list, _ := s.ListConnections()
	if len(list) != 1 {
		t.Errorf("list = %d", len(list))
	}
}

func TestReportsAndRuns(t *testing.T) {
	s := open(t)
	c := &model.Connection{Name: "Prod", URL: "https://g.example", Token: "t"}
	if err := s.SaveConnection(c); err != nil {
		t.Fatal(err)
	}
	r := &model.Report{Name: "Monthly SLA", Enabled: true, ConnectionID: c.ID, DashboardUID: "abc",
		Time: model.TimeSpec{Preset: "previous_month"}, Schedule: schedule.Spec{Kind: schedule.Monthly, At: "08:00", MonthDay: 1}}
	if err := s.SaveReport(r); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnection(c.ID); err == nil {
		t.Error("deleted a connection that is in use")
	}
	got, err := s.GetReport(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Monthly SLA" || got.Schedule.MonthDay != 1 || !got.Enabled {
		t.Errorf("report = %+v", got)
	}
	n, _ := s.CountReports()
	if n != 1 {
		t.Errorf("count = %d", n)
	}

	old := &model.Run{ReportID: r.ID, ReportName: r.Name, Trigger: model.TriggerSchedule, CreatedAt: time.Now().Add(-30 * 24 * time.Hour)}
	if err := s.CreateRun(old); err != nil {
		t.Fatal(err)
	}
	run := &model.Run{ReportID: r.ID, ReportName: r.Name, Trigger: model.TriggerManual}
	if err := s.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	run.Status = model.RunSuccess
	run.StartedAt = time.Now()
	run.FinishedAt = time.Now().Add(3 * time.Second)
	run.Outputs = []model.Output{{File: "x.pdf", Pages: 4, Delivered: []string{"email: a@b.c"}}}
	if err := s.UpdateRun(run); err != nil {
		t.Fatal(err)
	}
	last, err := s.LastRuns()
	if err != nil {
		t.Fatal(err)
	}
	if last[r.ID] == nil || last[r.ID].ID != run.ID || last[r.ID].Outputs[0].Pages != 4 {
		t.Errorf("last run = %+v", last[r.ID])
	}
	olds, _ := s.RunsOlderThan(time.Now().Add(-7 * 24 * time.Hour))
	if len(olds) != 1 || olds[0].ID != old.ID {
		t.Errorf("older runs = %v", olds)
	}
	stuck := &model.Run{ReportID: r.ID, ReportName: r.Name, Trigger: model.TriggerManual, Status: model.RunRunning}
	_ = s.CreateRun(stuck)
	if n, _ := s.FailInterrupted(); n != 2 { // "old" is still queued, "stuck" was running
		t.Errorf("interrupted = %d", n)
	}
	if err := s.DeleteReport(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetReport(r.ID); err != ErrNotFound {
		t.Errorf("deleted report error = %v", err)
	}
}

func TestSettingsAndAPIKeys(t *testing.T) {
	s := open(t)
	if v, _ := s.GetSetting("missing"); v != "" {
		t.Errorf("missing setting = %q", v)
	}
	_ = s.SetSetting("a", "1")
	_ = s.SetSetting("a", "2")
	if v, _ := s.GetSetting("a"); v != "2" {
		t.Errorf("setting = %q", v)
	}
	if err := s.SetSecret("smtp.password", "hunter2"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := s.GetSetting("smtp.password"); raw == "hunter2" {
		t.Error("secret stored in plain text")
	}
	if v, _ := s.GetSecret("smtp.password"); v != "hunter2" {
		t.Errorf("secret = %q", v)
	}
	k := &model.APIKey{Name: "ci", Hash: "h1", Prefix: "pp_ab"}
	if err := s.CreateAPIKey(k); err != nil {
		t.Fatal(err)
	}
	found, err := s.FindAPIKeyByHash(context.Background(), "h1")
	if err != nil || found.ID != k.ID {
		t.Fatalf("find = %v %v", found, err)
	}
	if _, err := s.FindAPIKeyByHash(context.Background(), "nope"); err != ErrNotFound {
		t.Errorf("missing key error = %v", err)
	}
	keys, _ := s.ListAPIKeys()
	if len(keys) != 1 || keys[0].LastUsedAt.IsZero() {
		t.Errorf("keys = %+v", keys)
	}
}

func TestReopenKeepsSchema(t *testing.T) {
	dir := t.TempDir()
	box, _ := secret.New([]byte("k"))
	s, err := Open(filepath.Join(dir, "p.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetSetting("x", "y")
	s.Close()
	s2, err := Open(filepath.Join(dir, "p.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, _ := s2.GetSetting("x"); v != "y" {
		t.Errorf("after reopen = %q", v)
	}
}
