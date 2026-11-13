package task

import (
	scheduler "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
)

func New() component.Component {
	return component.Component{
		Types:      []provider.BindingType{provider.BindingTask},
		Declare:    declare,
		Collect:    topic.Collect,
		Credential: credential,
		Inventory: func(tree component.InventoryTree, scope string, at component.Stack, declared []provider.Resource) error {
			topics, err := topic.Of(topic.NamesAt(at), declared)
			if err != nil {
				return err
			}
			topic.Inventory(tree, scope, topics)
			return nil
		},
	}
}

func declare(ctx *pulumi.Context, at component.Stack, declared []provider.Resource) error {
	topics, err := topic.Of(topic.NamesAt(at), declared)
	if err != nil {
		return err
	}
	if NeedsScheduleGroup(topics) {
		group, err := topic.NamesAt(at).ScheduleGroup()
		if err != nil {
			return err
		}
		if _, err := scheduler.NewScheduleGroup(ctx, group, &scheduler.ScheduleGroupArgs{
			Name: pulumi.String(group),
		}); err != nil {
			return err
		}
	}
	return topic.Declare(ctx, at, declared)
}
