package task

import (
	scheduler "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
)

func New() component.Component {
	return component.Component{
		Types:      []provider.BindingType{provider.BindingTask},
		Declare:    declare,
		Collect:    topic.Collect,
		Credential: credential,
		Shape: func(cost component.CostTree, scope string, at component.Stack, declared []provider.Resource) error {
			topic.Shape(cost, scope, topic.Of(at.Project, at.Env, declared))
			return nil
		},
	}
}

func declare(ctx *pulumi.Context, at component.Stack, declared []provider.Resource) error {
	if NeedsScheduleGroup(topic.Of(at.Project, at.Env, declared)) {
		if _, err := scheduler.NewScheduleGroup(ctx, naming.ResourceID(naming.KindWorker, "cron"), &scheduler.ScheduleGroupArgs{
			Name: pulumi.String(queues.ScheduleGroupName(at.Project, at.Env)),
		}); err != nil {
			return err
		}
	}
	return topic.Declare(ctx, at, declared)
}
