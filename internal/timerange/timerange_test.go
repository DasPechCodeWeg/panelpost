package timerange

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

func TestPresetsResolveInAmsterdam(t *testing.T) {
	ams := mustLoc(t, "Europe/Amsterdam")
	// Thursday 24 September 2026, 10:15 local time.
	now := time.Date(2026, 9, 24, 10, 15, 0, 0, ams)
	opts := Options{Location: ams}
	cases := []struct {
		preset   string
		from, to string
	}{
		{"yesterday", "2026-09-23 00:00:00.000", "2026-09-23 23:59:59.999"},
		{"previous_week", "2026-09-14 00:00:00.000", "2026-09-20 23:59:59.999"},
		{"week_to_date", "2026-09-21 00:00:00.000", "2026-09-24 10:15:00.000"},
		{"previous_month", "2026-08-01 00:00:00.000", "2026-08-31 23:59:59.999"},
		{"month_to_date", "2026-09-01 00:00:00.000", "2026-09-24 10:15:00.000"},
		{"previous_quarter", "2026-04-01 00:00:00.000", "2026-06-30 23:59:59.999"},
		{"quarter_to_date", "2026-07-01 00:00:00.000", "2026-09-24 10:15:00.000"},
		{"previous_year", "2025-01-01 00:00:00.000", "2025-12-31 23:59:59.999"},
		{"last_7d", "2026-09-17 10:15:00.000", "2026-09-24 10:15:00.000"},
		{"last_24h", "2026-09-23 10:15:00.000", "2026-09-24 10:15:00.000"},
	}
	const layout = "2006-01-02 15:04:05.000"
	for _, c := range cases {
		p, ok := PresetByID(c.preset)
		if !ok {
			t.Fatalf("missing preset %s", c.preset)
		}
		r, err := Resolve(p.From, p.To, now, opts)
		if err != nil {
			t.Fatalf("%s: %v", c.preset, err)
		}
		if got := r.From.Format(layout); got != c.from {
			t.Errorf("%s from = %s, want %s", c.preset, got, c.from)
		}
		if got := r.To.Format(layout); got != c.to {
			t.Errorf("%s to = %s, want %s", c.preset, got, c.to)
		}
	}
}

func TestSundayWeekStart(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, ny) // Thursday
	r, err := Resolve("now-1w/w", "now-1w/w", now, Options{Location: ny, WeekStartsSunday: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.From.Format("2006-01-02 Mon"); got != "2026-09-13 Sun" {
		t.Errorf("from = %s", got)
	}
	if got := r.To.Format("2006-01-02 Mon 15:04"); got != "2026-09-19 Sat 23:59" {
		t.Errorf("to = %s", got)
	}
}

func TestMonthArithmeticClampsLikeGrafana(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	got, err := Parse("now-1M", now, false, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("now-1M on 31 March = %s, want %s", got, want)
	}
	leap, _ := Parse("now-1y", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC), false, Options{})
	if want := time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC); !leap.Equal(want) {
		t.Errorf("leap-year clamp = %s, want %s", leap, want)
	}
	jan, _ := Parse("now-2M/M", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), false, Options{})
	if want := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC); !jan.Equal(want) {
		t.Errorf("year wrap = %s, want %s", jan, want)
	}
}

func TestDaylightSavingTransitionDay(t *testing.T) {
	ams := mustLoc(t, "Europe/Amsterdam")
	// Clocks go back on Sunday 25 October 2026; that day has 25 hours.
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, ams)
	r, err := Resolve("now-1d/d", "now-1d/d", now, Options{Location: ams})
	if err != nil {
		t.Fatal(err)
	}
	if d := r.To.Sub(r.From) + time.Millisecond; d != 25*time.Hour {
		t.Errorf("DST day length = %s, want 25h", d)
	}
	if r.Label() != "25 October 2026" {
		t.Errorf("label = %q", r.Label())
	}
}

func TestAbsoluteInputs(t *testing.T) {
	ams := mustLoc(t, "Europe/Amsterdam")
	opts := Options{Location: ams}
	a, err := Parse("2026-08-01", time.Now(), false, opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.Format(time.RFC3339) != "2026-08-01T00:00:00+02:00" {
		t.Errorf("date = %s", a.Format(time.RFC3339))
	}
	b, err := Parse("1790121600000", time.Now(), false, opts)
	if err != nil {
		t.Fatal(err)
	}
	if b.UnixMilli() != 1790121600000 {
		t.Errorf("epoch = %d", b.UnixMilli())
	}
}

func TestErrors(t *testing.T) {
	now := time.Now()
	bad := []string{"", "now-", "now-7x", "now/", "now*2", "yesterday", "now-7"}
	for _, expr := range bad {
		if _, err := Parse(expr, now, false, Options{}); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", expr)
		}
	}
	if _, err := Resolve("now", "now-1d", now, Options{}); err != ErrEmptyRange {
		t.Errorf("reversed range error = %v", err)
	}
}

func TestLabels(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		from, to time.Time
		want     string
	}{
		{time.Date(2026, 8, 1, 0, 0, 0, 0, utc), time.Date(2026, 8, 31, 23, 59, 59, 999e6, utc), "1 – 31 August 2026"},
		{time.Date(2026, 6, 29, 0, 0, 0, 0, utc), time.Date(2026, 7, 5, 23, 59, 59, 999e6, utc), "29 June – 5 July 2026"},
		{time.Date(2025, 12, 29, 0, 0, 0, 0, utc), time.Date(2026, 1, 4, 23, 59, 59, 999e6, utc), "29 December 2025 – 4 January 2026"},
		{time.Date(2026, 9, 24, 8, 0, 0, 0, utc), time.Date(2026, 9, 24, 17, 30, 0, 0, utc), "24 Sep 2026, 08:00 – 17:30"},
		{time.Date(2026, 9, 23, 10, 0, 0, 0, utc), time.Date(2026, 9, 24, 10, 0, 0, 0, utc), "23 Sep 2026 10:00 – 24 Sep 2026 10:00"},
	}
	for _, c := range cases {
		if got := (Range{From: c.from, To: c.to}).Label(); got != c.want {
			t.Errorf("label = %q, want %q", got, c.want)
		}
	}
}
