package deploy

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/cron"
)

const maxScheduleExpressionLen = 256

var schedulerWeekdays = []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}

func schedulerExpressions(expr string) ([]string, error) {
	schedule, err := cron.Parse(expr)
	if err != nil {
		return nil, err
	}
	f := schedule.Fields()
	minutes, hours, months := listOf(f.Minutes, 60), listOf(f.Hours, 24), listOf(f.Months, 12)
	days, weekdays := listOf(f.Days, 31), weekdayList(f.Weekdays)
	var fields [][2]string
	switch {
	case f.AnyDay && f.AnyWeekday:
		fields = [][2]string{{days, "?"}}
	case f.AnyWeekday:
		fields = [][2]string{{days, "?"}}
	case f.AnyDay:
		fields = [][2]string{{"?", weekdays}}
	default:
		fields = [][2]string{{days, "?"}, {"?", weekdays}}
	}
	var expressions []string
	for _, day := range fields {
		rendered := fmt.Sprintf("cron(%s %s %s %s %s *)", minutes, hours, day[0], months, day[1])
		if len(rendered) > maxScheduleExpressionLen {
			return nil, fmt.Errorf("cron %q names more single values than EventBridge Scheduler takes in one expression of %d characters: name ranges or steps instead", expr, maxScheduleExpressionLen)
		}
		expressions = append(expressions, rendered)
	}
	return expressions, nil
}

func listOf(values []int, every int) string {
	if len(values) == every {
		return "*"
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}

func weekdayList(values []int) string {
	if len(values) == len(schedulerWeekdays) {
		return "*"
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = schedulerWeekdays[value]
	}
	return strings.Join(parts, ",")
}
