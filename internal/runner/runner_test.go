package runner

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DasPechCodeWeg/panelpost/internal/license"
	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/schedule"
	"github.com/DasPechCodeWeg/panelpost/internal/secret"
	"github.com/DasPechCodeWeg/panelpost/internal/store"
	"github.com/DasPechCodeWeg/panelpost/internal/timerange"
)

func testRunner(t *testing.T, now *time.Time) (*Runner, *store.Store) {
	t.Helper()
	box, _ := secret.New([]byte("k"))
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &Runner{Store: st, License: license.NewManager(license.Options{Settings: st}), DataDir: t.TempDir(),
		Now: func() time.Time { return *now }}
	// Initialise the queue without starting workers, so jobs stay visible.
	r.queue = make(chan job, 16)
	r.wake = make(chan struct{}, 1)
	r.next = map[string]time.Time{}
	return r, st
}

func saveReport(t *testing.T, st *store.Store, name string, spec schedule.Spec) *model.Report {
	t.Helper()
	conns, _ := st.ListConnections()
	if len(conns) == 0 {
		c := &model.Connection{Name: "g", URL: "http://g", Token: "t"}
		if err := st.SaveConnection(c); err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	rep := &model.Report{Name: name, Enabled: true, ConnectionID: conns[0].ID, DashboardUID: "d",
		Time: model.TimeSpec{Preset: "yesterday"}, Schedule: spec}
	if err := st.SaveReport(rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestSchedulerFiresOnceWhenDue(t *testing.T) {
	now := time.Date(2026, 9, 24, 6, 59, 0, 0, time.UTC)
	r, st := testRunner(t, &now)
	rep := saveReport(t, st, "Daily", schedule.Spec{Kind: schedule.Daily, At: "07:00", Timezone: "UTC"})
	paused := saveReport(t, st, "Paused", schedule.Spec{Kind: schedule.Daily, At: "07:00", Timezone: "UTC"})
	paused.Enabled = false
	_ = st.SaveReport(paused)

	r.scheduleDue(nil) // first sight: only computes the next run
	if len(r.queue) != 0 {
		t.Fatal("a run fired on first sight")
	}
	if got := r.next[rep.ID]; !got.Equal(time.Date(2026, 9, 24, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("next = %s", got)
	}
	now = now.Add(30 * time.Second) // 06:59:30, not yet due
	r.scheduleDue(nil)
	if len(r.queue) != 0 {
		t.Fatal("fired early")
	}
	now = time.Date(2026, 9, 24, 7, 0, 10, 0, time.UTC)
	r.scheduleDue(nil)
	r.scheduleDue(nil) // a second tick in the same minute must not duplicate
	if len(r.queue) != 1 {
		t.Fatalf("queued = %d, want 1", len(r.queue))
	}
	j := <-r.queue
	if j.report.ID != rep.ID || j.run.Trigger != model.TriggerSchedule {
		t.Errorf("job = %+v", j.run)
	}
	if got := r.next[rep.ID]; !got.Equal(time.Date(2026, 9, 25, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("rescheduled to %s", got)
	}
	if _, ok := r.next[paused.ID]; ok {
		t.Error("paused report was scheduled")
	}
	runs, _ := st.ListRuns(rep.ID, 10)
	if len(runs) != 1 || runs[0].Status != model.RunQueued {
		t.Errorf("runs = %+v", runs)
	}
}

func TestRestartDoesNotReplayMissedRuns(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	r, st := testRunner(t, &now)
	saveReport(t, st, "Morning", schedule.Spec{Kind: schedule.Daily, At: "07:00", Timezone: "UTC"})
	r.scheduleDue(nil)
	if len(r.queue) != 0 {
		t.Fatal("missed run was replayed after restart")
	}
}

func TestCommunityLimitAppliesAfterDowngrade(t *testing.T) {
	now := time.Now()
	r, st := testRunner(t, &now)
	var reps []*model.Report
	for i := 0; i < 4; i++ {
		reps = append(reps, saveReport(t, st, "R"+string(rune('a'+i)), schedule.Spec{Kind: schedule.Manual}))
		time.Sleep(2 * time.Millisecond) // distinct creation times
	}
	lim := license.LimitsFor(license.Free)
	for i, rep := range reps {
		err := r.allowed(rep, lim)
		if i < 3 && err != nil {
			t.Errorf("report %d refused: %v", i, err)
		}
		if i == 3 && (err == nil || !strings.Contains(err.Error(), "up to 3")) {
			t.Errorf("fourth report allowed: %v", err)
		}
	}
	if err := r.allowed(reps[3], license.LimitsFor(license.Pro)); err != nil {
		t.Errorf("Pro refused: %v", err)
	}
}

func TestFileNames(t *testing.T) {
	now := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	r, _ := testRunner(t, &now)
	rng, _ := timerange.Resolve("now-1M/M", "now-1M/M", now, timerange.Options{})
	rep := &model.Report{Name: "Monthly SLA"}
	if got := r.fileName(rep, target{}, rng); got != "Monthly SLA 2026-08-01.pdf" {
		t.Errorf("default = %q", got)
	}
	if got := r.fileName(rep, target{value: "acme", label: "Acme/Corp"}, rng); got != "Monthly SLA - Acme-Corp 2026-08-01.pdf" {
		t.Errorf("burst = %q", got)
	}
	rep.FileName = "{{target}}_{{from}}_{{to}}"
	if got := r.fileName(rep, target{label: "Globex"}, rng); got != "Globex_2026-08-01_2026-08-31.pdf" {
		t.Errorf("custom = %q", got)
	}
}

func TestPathHelpers(t *testing.T) {
	if pathOf("https://example.com/grafana/") != "/grafana" || pathOf("http://g:3000") != "" {
		t.Error("pathOf")
	}
	if hostOf("https://hooks.example.com/x?y=1") != "hooks.example.com" {
		t.Error("hostOf")
	}
}
