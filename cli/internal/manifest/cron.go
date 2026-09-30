package manifest

import (
	"fmt"
	"strconv"
	"strings"
)

type cronField struct {
	name     string
	min, max int
	names    []string
}

var cronFields = []cronField{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}},
	{name: "day of week", min: 0, max: 7, names: []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}},
}

func refuseInvalidCron(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != len(cronFields) {
		return fmt.Errorf("cron %q has %d fields, and a schedule has five: minute, hour, day of month, month and day of week", expr, len(fields))
	}
	for i, field := range fields {
		if err := cronFields[i].refuseInvalid(field); err != nil {
			return fmt.Errorf("cron %q: %w", expr, err)
		}
	}
	return nil
}

func (f cronField) refuseInvalid(field string) error {
	for item := range strings.SplitSeq(field, ",") {
		if err := f.refuseInvalidItem(item); err != nil {
			return err
		}
	}
	return nil
}

func (f cronField) refuseInvalidItem(item string) error {
	span, step, stepped := strings.Cut(item, "/")
	if stepped {
		n, err := strconv.Atoi(step)
		if err != nil || n < 1 {
			return fmt.Errorf("the %s step %q is not a whole number of at least 1", f.name, step)
		}
	}
	if span == "*" {
		return nil
	}
	low, high, ranged := strings.Cut(span, "-")
	first, err := f.value(low)
	if err != nil {
		return err
	}
	if !ranged {
		return nil
	}
	last, err := f.value(high)
	if err != nil {
		return err
	}
	if first > last {
		return fmt.Errorf("the %s range %q runs backwards", f.name, span)
	}
	return nil
}

func (f cronField) value(text string) (int, error) {
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
