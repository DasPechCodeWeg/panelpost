// Package schedule turns friendly report schedules ("every Monday at 08:00")
// into cron expressions and computes their next run in the report's time zone.
package schedule

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Kinds of schedule.
const (
	Manual    = "manual"
	Daily     = "daily"
	Weekly    = "weekly"
	Monthly   = "monthly"
	Quarterly = "quarterly" // Jan, Apr, Jul and Oct: pairs with "previous quarter"
	Cron      = "cron"
)

// Spec is the user-facing schedule definition stored with a report.
type Spec struct {
	Kind     string `json:"kind"`
	At       string `json:"at,omitempty"`       // "HH:MM", 24h clock
	Weekdays []int  `json:"weekdays,omitempty"` // 0 = Sunday ... 6 = Saturday
	MonthDay int    `json:"month_day,omitempty"`
	Cron     string `json:"cron,omitempty"` // standard 5-field expression
	Timezone string `json:"timezone,omitempty"`
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Location returns the schedule's time zone, defaulting to fallback.
func (s Spec) Location(fallback *time.Location) (*time.Location, error) {
	if s.Timezone == "" {
		if fallback == nil {
			return time.UTC, nil
		}
		return fallback, nil
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", s.Timezone)
	}
	return loc, nil
}

func parseClock(at string) (int, int, error) {
	if at == "" {
		return 0, 0, errors.New("a time of day is required, for example 08:00")
	}
	parts := strings.Split(at, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("time %q must look like 08:00", at)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("time %q must look like 08:00", at)
	}
	return h, m, nil
}

// CronExpr converts the spec into a 5-field cron expression.
func (s Spec) CronExpr() (string, error) {
	switch s.Kind {
	case Manual, "":
		return "", nil
	case Cron:
		expr := strings.TrimSpace(s.Cron)
		if strings.HasPrefix(expr, "CRON_TZ=") || strings.HasPrefix(expr, "TZ=") {
			return "", errors.New("set the time zone in the time zone field, not inside the cron expression")
		}
		if _, err := parser.Parse(expr); err != nil {
			return "", fmt.Errorf("invalid cron expression: %w", err)
		}
		return expr, nil
	}
	h, m, err := parseClock(s.At)
	if err != nil {
		return "", err
	}
	switch s.Kind {
	case Daily:
		return fmt.Sprintf("%d %d * * *", m, h), nil
	case Weekly:
		if len(s.Weekdays) == 0 {
			return "", errors.New("pick at least one day of the week")
		}
		days := append([]int(nil), s.Weekdays...)
		sort.Ints(days)
		seen := map[int]bool{}
		var parts []string
		for _, d := range days {
			if d < 0 || d > 6 {
				return "", fmt.Errorf("invalid weekday %d", d)
			}
			if !seen[d] {
				seen[d] = true
				parts = append(parts, strconv.Itoa(d))
			}
		}
		return fmt.Sprintf("%d %d * * %s", m, h, strings.Join(parts, ",")), nil
	case Monthly, Quarterly:
		if s.MonthDay < 1 || s.MonthDay > 28 {
			return "", errors.New("day of the month must be between 1 and 28 so that it exists in every month")
		}
		months := "*"
		if s.Kind == Quarterly {
			months = "1,4,7,10"
		}
		return fmt.Sprintf("%d %d %d %s *", m, h, s.MonthDay, months), nil
	}
	return "", fmt.Errorf("unknown schedule kind %q", s.Kind)
}

// Validate reports whether the spec can be scheduled.
func (s Spec) Validate() error {
	if _, err := s.CronExpr(); err != nil {
		return err
	}
	_, err := s.Location(nil)
	return err
}

// Next returns the first run strictly after the given instant. It returns the
// zero time for manual schedules.
func (s Spec) Next(after time.Time, fallback *time.Location) (time.Time, error) {
	expr, err := s.CronExpr()
	if err != nil || expr == "" {
		return time.Time{}, err
	}
	loc, err := s.Location(fallback)
	if err != nil {
		return time.Time{}, err
	}
	sched, err := parser.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(after.In(loc)), nil
}

// Upcoming returns the next n run times, useful for previews in the UI.
func (s Spec) Upcoming(after time.Time, n int, fallback *time.Location) ([]time.Time, error) {
	var out []time.Time
	t := after
	for i := 0; i < n; i++ {
		next, err := s.Next(t, fallback)
		if err != nil {
			return nil, err
		}
		if next.IsZero() {
			break
		}
		out = append(out, next)
		t = next
	}
	return out, nil
}

var weekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// Describe renders a human sentence, e.g. "Every Monday at 08:00".
func (s Spec) Describe() string {
	switch s.Kind {
	case Manual, "":
		return "Manual only"
	case Daily:
		return "Every day at " + s.At
	case Weekly:
		var names []string
		days := append([]int(nil), s.Weekdays...)
		sort.Ints(days)
		for _, d := range days {
			if d >= 0 && d <= 6 {
				names = append(names, weekdayNames[d])
			}
		}
		if len(names) == 5 && days[0] == 1 && days[4] == 5 {
			return "Every weekday at " + s.At
		}
		return "Every " + joinEnglish(names) + " at " + s.At
	case Monthly:
		return fmt.Sprintf("Monthly on the %s at %s", ordinal(s.MonthDay), s.At)
	case Quarterly:
		return fmt.Sprintf("Quarterly on the %s of Jan, Apr, Jul and Oct at %s", ordinal(s.MonthDay), s.At)
	case Cron:
		return "Cron: " + s.Cron
	}
	return s.Kind
}

func joinEnglish(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}
