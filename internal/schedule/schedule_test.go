package schedule

import (
	"testing"
	"time"
)

func TestCronExpressions(t *testing.T) {
	cases := []struct {
		spec Spec
		want string
	}{
		{Spec{Kind: Daily, At: "08:30"}, "30 8 * * *"},
		{Spec{Kind: Weekly, At: "07:00", Weekdays: []int{5, 1, 1}}, "0 7 * * 1,5"},
		{Spec{Kind: Monthly, At: "06:15", MonthDay: 1}, "15 6 1 * *"},
		{Spec{Kind: Quarterly, At: "07:00", MonthDay: 2}, "0 7 2 1,4,7,10 *"},
		{Spec{Kind: Cron, Cron: "0 9 * * 1-5"}, "0 9 * * 1-5"},
		{Spec{Kind: Manual}, ""},
	}
	for _, c := range cases {
		got, err := c.spec.CronExpr()
		if err != nil {
			t.Fatalf("%+v: %v", c.spec, err)
		}
		if got != c.want {
			t.Errorf("%+v = %q, want %q", c.spec, got, c.want)
		}
	}
}

func TestInvalidSpecs(t *testing.T) {
	bad := []Spec{
		{Kind: Daily},
		{Kind: Daily, At: "25:00"},
		{Kind: Daily, At: "8"},
		{Kind: Weekly, At: "08:00"},
		{Kind: Weekly, At: "08:00", Weekdays: []int{7}},
		{Kind: Monthly, At: "08:00", MonthDay: 31},
		{Kind: Cron, Cron: "not cron"},
		{Kind: Cron, Cron: "CRON_TZ=UTC 0 8 * * *"},
		{Kind: "hourly", At: "08:00"},
		{Kind: Daily, At: "08:00", Timezone: "Mars/Olympus"},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v validated, want error", s)
		}
	}
}

func TestNextRespectsTimeZoneAndDST(t *testing.T) {
	spec := Spec{Kind: Monthly, At: "08:00", MonthDay: 1, Timezone: "Europe/Amsterdam"}
	after := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	next, err := spec.Next(after, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 1 October 2026 08:00 CEST is 06:00 UTC.
	if want := time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next = %s, want %s", next.UTC(), want)
	}
	runs, err := spec.Upcoming(after, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 1 November is after the switch to CET, so 08:00 local is 07:00 UTC.
	if want := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC); !runs[1].Equal(want) {
		t.Errorf("second run = %s, want %s", runs[1].UTC(), want)
	}
}

func TestManualHasNoNextRun(t *testing.T) {
	next, err := Spec{Kind: Manual}.Next(time.Now(), nil)
	if err != nil || !next.IsZero() {
		t.Errorf("manual next = %v, %v", next, err)
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string]Spec{
		"Every day at 08:00":                                     {Kind: Daily, At: "08:00"},
		"Every weekday at 07:30":                                 {Kind: Weekly, At: "07:30", Weekdays: []int{1, 2, 3, 4, 5}},
		"Every Monday and Thursday at 09:00":                     {Kind: Weekly, At: "09:00", Weekdays: []int{4, 1}},
		"Monthly on the 1st at 06:00":                            {Kind: Monthly, At: "06:00", MonthDay: 1},
		"Monthly on the 22nd at 06:00":                           {Kind: Monthly, At: "06:00", MonthDay: 22},
		"Monthly on the 13th at 06:00":                           {Kind: Monthly, At: "06:00", MonthDay: 13},
		"Manual only":                                            {Kind: Manual},
		"Quarterly on the 2nd of Jan, Apr, Jul and Oct at 07:00": {Kind: Quarterly, At: "07:00", MonthDay: 2},
	}
	for want, spec := range cases {
		if got := spec.Describe(); got != want {
			t.Errorf("Describe(%+v) = %q, want %q", spec, got, want)
		}
	}
}

func TestQuarterlyRunsAfterEachQuarter(t *testing.T) {
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	spec := Spec{Kind: Quarterly, At: "07:00", MonthDay: 1, Timezone: "Europe/Amsterdam"}
	got, err := spec.Upcoming(time.Date(2026, 9, 24, 12, 0, 0, 0, ams), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-10-01 07:00 CEST", "2027-01-01 07:00 CET", "2027-04-01 07:00 CEST"}
	for i, w := range want {
		if s := got[i].Format("2006-01-02 15:04 MST"); s != w {
			t.Errorf("run %d = %s, want %s", i, s, w)
		}
	}
}
