// Package timerange resolves Grafana-style relative time expressions
// ("now-1M/M", "now-7d", "now/w") into absolute instants in a given time
// zone, so that a report can state exactly which period it covers.
//
// The syntax follows Grafana's datemath: "now" followed by any number of
// "+N<unit>", "-N<unit>" and "/<unit>" operations. Units are s, m, h, d, w,
// M, Q (quarter, a Panelpost extension) and y. Rounding ("/unit") rounds
// down for the start of a range and up to the last millisecond of the unit
// for the end of a range, exactly like Grafana does.
package timerange

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Range is a resolved, absolute time range.
type Range struct {
	From time.Time
	To   time.Time
	// FromExpr and ToExpr keep the original expressions for display.
	FromExpr string
	ToExpr   string
}

// Preset is a named, commonly used reporting period.
type Preset struct {
	ID    string
	Label string
	From  string
	To    string
}

// Presets are offered in the UI. Calendar presets ("previous month") are the
// reason most teams want scheduled reports in the first place.
var Presets = []Preset{
	{"last_1h", "Last hour", "now-1h", "now"},
	{"last_24h", "Last 24 hours", "now-24h", "now"},
	{"today_so_far", "Today so far", "now/d", "now"},
	{"yesterday", "Yesterday", "now-1d/d", "now-1d/d"},
	{"last_7d", "Last 7 days", "now-7d", "now"},
	{"week_to_date", "This week so far", "now/w", "now"},
	{"previous_week", "Previous week", "now-1w/w", "now-1w/w"},
	{"last_30d", "Last 30 days", "now-30d", "now"},
	{"month_to_date", "This month so far", "now/M", "now"},
	{"previous_month", "Previous month", "now-1M/M", "now-1M/M"},
	{"last_90d", "Last 90 days", "now-90d", "now"},
	{"quarter_to_date", "This quarter so far", "now/Q", "now"},
	{"previous_quarter", "Previous quarter", "now-1Q/Q", "now-1Q/Q"},
	{"year_to_date", "This year so far", "now/y", "now"},
	{"previous_year", "Previous year", "now-1y/y", "now-1y/y"},
}

// PresetByID returns the preset with the given ID.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Options control how expressions are evaluated.
type Options struct {
	// Location is the time zone used for calendar rounding. Defaults to UTC.
	Location *time.Location
	// WeekStartsSunday switches "/w" rounding from Monday (ISO 8601, the
	// European default) to Sunday.
	WeekStartsSunday bool
}

func (o Options) loc() *time.Location {
	if o.Location == nil {
		return time.UTC
	}
	return o.Location
}

func (o Options) weekStart() time.Weekday {
	if o.WeekStartsSunday {
		return time.Sunday
	}
	return time.Monday
}

// ErrEmptyRange is returned when the end of a range is not after its start.
var ErrEmptyRange = errors.New("time range end must be after its start")

// Resolve evaluates a from/to pair at the instant now.
func Resolve(fromExpr, toExpr string, now time.Time, opts Options) (Range, error) {
	from, err := Parse(fromExpr, now, false, opts)
	if err != nil {
		return Range{}, fmt.Errorf("invalid start %q: %w", fromExpr, err)
	}
	to, err := Parse(toExpr, now, true, opts)
	if err != nil {
		return Range{}, fmt.Errorf("invalid end %q: %w", toExpr, err)
	}
	if !to.After(from) {
		return Range{}, ErrEmptyRange
	}
	return Range{From: from, To: to, FromExpr: fromExpr, ToExpr: toExpr}, nil
}

// Parse evaluates a single expression. roundUp selects end-of-unit rounding,
// which Grafana uses for the "to" side of a range.
func Parse(expr string, now time.Time, roundUp bool, opts Options) (time.Time, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return time.Time{}, errors.New("empty expression")
	}
	loc := opts.loc()
	if !strings.HasPrefix(expr, "now") {
		return parseAbsolute(expr, loc)
	}
	t := now.In(loc)
	rest := expr[len("now"):]
	for len(rest) > 0 {
		op := rest[0]
		rest = rest[1:]
		switch op {
		case '+', '-':
			i := 0
			for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
				i++
			}
			n := 1
			if i > 0 {
				v, err := strconv.Atoi(rest[:i])
				if err != nil {
					return time.Time{}, err
				}
				n = v
			}
			if i >= len(rest) {
				return time.Time{}, fmt.Errorf("missing unit after %c%d", op, n)
			}
			unit := rest[i]
			rest = rest[i+1:]
			if op == '-' {
				n = -n
			}
			var err error
			t, err = add(t, n, unit)
			if err != nil {
				return time.Time{}, err
			}
		case '/':
			if len(rest) == 0 {
				return time.Time{}, errors.New("missing unit after /")
			}
			unit := rest[0]
			rest = rest[1:]
			start, err := startOf(t, unit, opts.weekStart())
			if err != nil {
				return time.Time{}, err
			}
			if roundUp {
				next, err := add(start, 1, unit)
				if err != nil {
					return time.Time{}, err
				}
				t = next.Add(-time.Millisecond)
			} else {
				t = start
			}
		default:
			return time.Time{}, fmt.Errorf("unexpected %q", string(op))
		}
	}
	return t, nil
}

func parseAbsolute(expr string, loc *time.Location) (time.Time, error) {
	if ms, err := strconv.ParseInt(expr, 10, 64); err == nil {
		return time.UnixMilli(ms).In(loc), nil
	}
	layouts := []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, expr, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised time %q (use now-7d style, epoch milliseconds or 2006-01-02 15:04)", expr)
}

func add(t time.Time, n int, unit byte) (time.Time, error) {
	switch unit {
	case 's':
		return t.Add(time.Duration(n) * time.Second), nil
	case 'm':
		return t.Add(time.Duration(n) * time.Minute), nil
	case 'h':
		return t.Add(time.Duration(n) * time.Hour), nil
	case 'd':
		return t.AddDate(0, 0, n), nil
	case 'w':
		return t.AddDate(0, 0, 7*n), nil
	case 'M':
		return addMonthsClamped(t, n), nil
	case 'Q':
		return addMonthsClamped(t, 3*n), nil
	case 'y':
		return addMonthsClamped(t, 12*n), nil
	}
	return time.Time{}, fmt.Errorf("unknown unit %q", string(unit))
}

// addMonthsClamped adds months like moment.js does: 31 March minus one month
// is 28/29 February, not 3 March.
func addMonthsClamped(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	hh, mm, ss := t.Clock()
	total := int(m) - 1 + months
	ny := y + floorDiv(total, 12)
	nm := time.Month(floorMod(total, 12) + 1)
	if last := daysIn(ny, nm); d > last {
		d = last
	}
	return time.Date(ny, nm, d, hh, mm, ss, t.Nanosecond(), t.Location())
}

func startOf(t time.Time, unit byte, weekStart time.Weekday) (time.Time, error) {
	loc := t.Location()
	y, m, d := t.Date()
	switch unit {
	case 's':
		return t.Truncate(time.Second), nil
	case 'm':
		return time.Date(y, m, d, t.Hour(), t.Minute(), 0, 0, loc), nil
	case 'h':
		return time.Date(y, m, d, t.Hour(), 0, 0, 0, loc), nil
	case 'd':
		return time.Date(y, m, d, 0, 0, 0, 0, loc), nil
	case 'w':
		offset := (int(t.Weekday()) - int(weekStart) + 7) % 7
		return time.Date(y, m, d-offset, 0, 0, 0, 0, loc), nil
	case 'M':
		return time.Date(y, m, 1, 0, 0, 0, 0, loc), nil
	case 'Q':
		qm := time.Month((int(m)-1)/3*3 + 1)
		return time.Date(y, qm, 1, 0, 0, 0, 0, loc), nil
	case 'y':
		return time.Date(y, time.January, 1, 0, 0, 0, 0, loc), nil
	}
	return time.Time{}, fmt.Errorf("unknown unit %q", string(unit))
}

func daysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func floorMod(a, b int) int {
	return a - floorDiv(a, b)*b
}

// Label renders a compact human description of the range, such as
// "1 – 31 August 2026" or "24 Sep 2026 08:00 – 25 Sep 2026 08:00".
func (r Range) Label() string {
	from, to := r.From, r.To
	wholeDays := isMidnight(from) && isEndOfDay(to)
	if wholeDays {
		if sameDay(from, to) {
			return from.Format("2 January 2006")
		}
		if from.Year() == to.Year() && from.Month() == to.Month() {
			return fmt.Sprintf("%d – %s", from.Day(), to.Format("2 January 2006"))
		}
		if from.Year() == to.Year() {
			return fmt.Sprintf("%s – %s", from.Format("2 January"), to.Format("2 January 2006"))
		}
		return fmt.Sprintf("%s – %s", from.Format("2 January 2006"), to.Format("2 January 2006"))
	}
	if sameDay(from, to) {
		return fmt.Sprintf("%s, %s – %s", from.Format("2 Jan 2006"), from.Format("15:04"), to.Format("15:04"))
	}
	return fmt.Sprintf("%s – %s", from.Format("2 Jan 2006 15:04"), to.Format("2 Jan 2006 15:04"))
}

// Zone returns the time zone abbreviation or name used for display.
func (r Range) Zone() string {
	return r.From.Location().String()
}

func isMidnight(t time.Time) bool {
	h, m, s := t.Clock()
	return h == 0 && m == 0 && s == 0 && t.Nanosecond() == 0
}

func isEndOfDay(t time.Time) bool {
	h, m, s := t.Clock()
	return h == 23 && m == 59 && s == 59
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
