package deploy

import (
	"slices"
	"testing"
)

func TestACronIsWrittenAsTheSchedulerExpressionsThatFireAtTheSameMinutes(t *testing.T) {
	t.Parallel()

	for expr, want := range map[string][]string{
		"* * * * *":        {"cron(* * * * ? *)"},
		"*/15 9-17 * * *":  {"cron(0,15,30,45 9,10,11,12,13,14,15,16,17 * * ? *)"},
		"0 3 1 * *":        {"cron(0 3 1 * ? *)"},
		"30 6 * * 1-5":     {"cron(30 6 ? * MON,TUE,WED,THU,FRI *)"},
		"0 0 1,15 * 0":     {"cron(0 0 1,15 * ? *)", "cron(0 0 ? * SUN *)"},
		"0 12 * JAN,JUL 7": {"cron(0 12 ? 1,7 SUN *)"},
	} {
		got, err := schedulerExpressions(expr)
		if err != nil {
			t.Errorf("schedulerExpressions(%q): %v", expr, err)
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("schedulerExpressions(%q) = %v, want %v", expr, got, want)
		}
	}
}
