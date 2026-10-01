package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Schedule struct {
	minutes, hours, days, months, weekdays uint64
	anyDay, anyWeekday                     bool
}

type field struct {
	name     string
	min, max int
	names    []string
}

var fields = [...]field{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}},
	{name: "day of week", min: 0, max: 7, names: []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}},
}

var longestMonth = [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

func Parse(expr string) (Schedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != len(fields) {
		return Schedule{}, fmt.Errorf("cron %q has %d fields, and a schedule has five: minute, hour, day of month, month and day of week", expr, len(parts))
	}
	var sets [len(fields)]uint64
	for i, part := range parts {
		set, err := fields[i].parse(part)
		if err != nil {
			return Schedule{}, fmt.Errorf("cron %q: %w", expr, err)
		}
		sets[i] = set
	}
	weekdays := sets[4]
	if weekdays&(1<<7) != 0 {
		weekdays |= 1
	}
	s := Schedule{
		minutes:    sets[0],
		hours:      sets[1],
		days:       sets[2],
		months:     sets[3],
		weekdays:   weekdays & 0x7f,
		anyDay:     strings.HasPrefix(parts[2], "*"),
		anyWeekday: strings.HasPrefix(parts[4], "*"),
	}
	if !s.isReachable() {
		return Schedule{}, fmt.Errorf("cron %q names a day of month that none of its months has, so it never fires", expr)
	}
	return s, nil
}

func (s Schedule) isReachable() bool {
	if !s.anyWeekday {
		return true
	}
	for month := 1; month <= 12; month++ {
		if s.months&(1<<month) == 0 {
			continue
		}
		for day := 1; day <= longestMonth[month]; day++ {
			if s.days&(1<<day) != 0 {
				return true
			}
		}
	}
	return false
}

func (s Schedule) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	for {
		switch {
		case s.months&(1<<int(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !s.matchesDay(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case s.hours&(1<<t.Hour()) == 0:
			t = t.Truncate(time.Hour).Add(time.Hour)
		case s.minutes&(1<<t.Minute()) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
}

func (s Schedule) matchesDay(t time.Time) bool {
	day := s.days&(1<<t.Day()) != 0
	weekday := s.weekdays&(1<<int(t.Weekday())) != 0
	if s.anyDay || s.anyWeekday {
		return day && weekday
	}
	return day || weekday
}

func (f field) parse(part string) (uint64, error) {
	var set uint64
	for item := range strings.SplitSeq(part, ",") {
		bits, err := f.parseItem(item)
		if err != nil {
			return 0, err
		}
		set |= bits
	}
	return set, nil
}

func (f field) parseItem(item string) (uint64, error) {
	span, stepText, stepped := strings.Cut(item, "/")
	step := 1
	if stepped {
		n, err := strconv.Atoi(stepText)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("the %s step %q is not a whole number of at least 1", f.name, stepText)
		}
		step = n
	}
	first, last := f.min, f.max
	if span != "*" {
		low, high, ranged := strings.Cut(span, "-")
		var err error
		if first, err = f.value(low); err != nil {
			return 0, err
		}
		last = first
		if stepped {
			last = f.max
		}
		if ranged {
			if last, err = f.value(high); err != nil {
				return 0, err
			}
			if first > last {
				return 0, fmt.Errorf("the %s range %q runs backwards", f.name, span)
			}
		}
	}
	var set uint64
	for n := first; n <= last; n += step {
		set |= 1 << n
	}
	return set, nil
}

func (f field) value(text string) (int, error) {
	for i, name := range f.names {
		if strings.EqualFold(text, name) {
			return f.min + i, nil
		}
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < f.min || n > f.max {
		return 0, fmt.Errorf("the %s %q is not between %d and %d", f.name, text, f.min, f.max)
	}
	return n, nil
}
