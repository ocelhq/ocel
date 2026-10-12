package deploy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/cron"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const (
	maxScheduleExpressionLen = 256
	cronMessageGroup         = "cron"
)

var schedulerWeekdays = []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}

func schedulerExpressions(expr string) ([]string, error) {
	schedule, err := cron.Parse(expr)
	if err != nil {
		return nil, err
	}
	f := schedule.Fields()
	minutes, hours, months := cronField(f.Minutes, 60), cronField(f.Hours, 24), cronField(f.Months, 12)
	days, weekdays := cronField(f.Days, 31), weekdayField(f.Weekdays)
	var fields [][2]string
	switch {
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

func cronField(values []int, possible int) string {
	if len(values) == possible {
		return "*"
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}

func weekdayField(values []int) string {
	if len(values) == len(schedulerWeekdays) {
		return "*"
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = schedulerWeekdays[value]
	}
	return strings.Join(parts, ",")
}

type cronTarget struct {
	task         deployedTopic
	arn          pulumi.StringInput
	messageGroup string
}

type schedulerRole struct {
	at       naming.Coordinate
	boundary string
	path     string
	tags     map[string]string
}

func allowing(action string, resources []string) (string, error) {
	encoded, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{
		"Effect": "Allow", "Action": action, "Resource": resources,
	}}})
	return string(encoded), err
}

func declareCrons(ctx *pulumi.Context, role schedulerRole, group string, policy pulumi.StringInput, crons []cronTarget) error {
	created, err := iam.NewRole(ctx, naming.ResourceID(naming.KindRole, "scheduler"), &iam.RoleArgs{
		NamePrefix:          pulumi.String(rolePrefix(role.at)),
		Description:         capDescription(role.at.Description("role EventBridge Scheduler takes to start this app's cron tasks on their workers"), maxDescriptionLen),
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(schedulerServicePrincipal)),
		PermissionsBoundary: pulumi.String(role.boundary),
		Path:                pulumi.String(role.path),
		Tags:                resourceTags(role.at.Kind, "", role.tags),
	})
	if err != nil {
		return err
	}
	invoking, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, "scheduler", "invoke"), &iam.RolePolicyArgs{Role: created.Name, Policy: policy})
	if err != nil {
		return err
	}
	for _, scheduled := range crons {
		task := scheduled.task.resource.Declared
		expressions, err := schedulerExpressions(scheduled.task.declared.Cron)
		if err != nil {
			return fmt.Errorf("task %s: %w", task, err)
		}
		target := &scheduler.ScheduleTargetArgs{
			Arn:     scheduled.arn,
			RoleArn: created.Arn,
			Input:   pulumi.String(fmt.Sprintf(cronInput, task)),
		}
		if scheduled.messageGroup != "" {
			target.SqsParameters = &scheduler.ScheduleTargetSqsParametersArgs{MessageGroupId: pulumi.String(scheduled.messageGroup)}
		}
		for i, expression := range expressions {
			name := queues.CronScheduleName(task)
			if len(expressions) > 1 {
				name = queues.CronScheduleName(task + "-" + strconv.Itoa(i+1))
			}
			if _, err := scheduler.NewSchedule(ctx, naming.ResourceID(naming.KindWorker, "cron", name), &scheduler.ScheduleArgs{
				Name:               pulumi.String(name),
				GroupName:          pulumi.String(group),
				ScheduleExpression: pulumi.String(expression),
				FlexibleTimeWindow: &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
				Target:             target,
			}, pulumi.DependsOn([]pulumi.Resource{invoking})); err != nil {
				return err
			}
		}
	}
	return nil
}
