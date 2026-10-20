package task

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/cron"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"

	"slices"
)

const (
	maxScheduleExpressionLen  = 256
	maxScheduleDescriptionLen = 512
	CronMessageGroup          = "cron"
	invokePolicyName          = "invoke"
)

var schedulerWeekdays = []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}

func SchedulerExpressions(expr string) ([]string, error) {
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

type CronTarget struct {
	Task         topic.Topic
	ARN          pulumi.StringInput
	MessageGroup string
}

type SchedulerRole struct {
	Name     string
	At       naming.Coordinate
	Boundary string
	Path     string
	Tags     map[string]string
}

func Allowing(action string, resources []string) (string, error) {
	encoded, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{
		"Effect": "Allow", "Action": action, "Resource": resources,
	}}})
	return string(encoded), err
}

func DeclareCrons(ctx *pulumi.Context, role SchedulerRole, names queues.Names, group string, policy pulumi.StringInput, crons []CronTarget) error {
	roleName := component.NameOf(role.Name)
	created, granted, err := component.NewAppRole(ctx, component.AppRole{
		Name:        roleName,
		Description: component.CapDescription(role.At.Description("role EventBridge Scheduler takes to start this app's cron tasks on their workers"), component.MaxDescriptionLen),
		Principal:   iam.SchedulerPrincipal,
		Boundary:    role.Boundary,
		Path:        role.Path,
		Tags:        role.Tags,
		Policies:    map[string]pulumi.StringInput{invokePolicyName: policy},
	})
	if err != nil {
		return err
	}
	for _, scheduled := range crons {
		task := scheduled.Task.Resource.Declared
		expressions, err := SchedulerExpressions(scheduled.Task.Spec.Cron)
		if err != nil {
			return fmt.Errorf("task %s: %w", task, err)
		}
		schedules, err := names.Schedules(task, len(expressions))
		if err != nil {
			return err
		}
		target := &scheduler.ScheduleTargetArgs{
			Arn:     scheduled.ARN,
			RoleArn: created.Arn,
			Input:   pulumi.String(fmt.Sprintf(CronInput, task)),
		}
		if scheduled.MessageGroup != "" {
			target.SqsParameters = &scheduler.ScheduleTargetSqsParametersArgs{MessageGroupId: pulumi.String(scheduled.MessageGroup)}
		}
		for i, expression := range expressions {
			if _, err := scheduler.NewSchedule(ctx, schedules[i], &scheduler.ScheduleArgs{
				Name:               pulumi.String(schedules[i]),
				GroupName:          pulumi.String(group),
				Description:        component.CapDescription(role.At.Description("starts task "+task), maxScheduleDescriptionLen),
				ScheduleExpression: pulumi.String(expression),
				FlexibleTimeWindow: &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
				Target:             target,
			}, pulumi.DependsOn(granted)); err != nil {
				return err
			}
		}
	}
	return nil
}

const (
	CronInput = `{"ocelCron":%q,"scheduledTime":"<aws.scheduler.scheduled-time>"}`
)

func NeedsScheduleGroup(topics []topic.Topic) bool {
	return slices.ContainsFunc(topics, func(topic topic.Topic) bool { return topic.IsTask() && topic.Spec.Cron != "" })
}
