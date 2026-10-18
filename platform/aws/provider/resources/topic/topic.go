package topic

import (
	"context"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

type StateTable struct {
	Name   string
	ARN    string
	Region string
}

func TableOf(at component.Stack) StateTable {
	return StateTable{Name: at.StateTable, ARN: at.StateTableARN, Region: at.Region}
}

func New() component.Component {
	return component.Component{
		Types:      []provider.BindingType{provider.BindingTopic},
		Declare:    Declare,
		Collect:    Collect,
		Credential: credential,
		Shape: func(cost component.CostTree, scope string, at component.Stack, declared []provider.Resource) error {
			Shape(cost, scope, Of(at.Project, at.Env, declared))
			return nil
		},
	}
}

func Declare(ctx *pulumi.Context, at component.Stack, declared []provider.Resource) error {
	everyTopic := Of(at.Project, at.Env, at.Resources)
	visibility := func(queue Queue) int { return VisibilitySeconds(everyTopic, queue) }
	for _, topic := range Of(at.Project, at.Env, declared) {
		if err := Register(ctx, topic, visibility, component.Tags(naming.KindTopic, "", map[string]string{component.TagResource: topic.Resource.Declared})); err != nil {
			return fmt.Errorf("declare %s: %w", topic.Resource.Declared, err)
		}
	}
	return nil
}

func Collect(_ context.Context, at component.Stack, declared provider.Resource, _ auto.OutputMap) (*variablestorev1.PublishedBinding, error) {
	binding, err := CollectBinding(TableOf(at), at.Project, at.Env, declared)
	if err != nil {
		return nil, err
	}
	return &variablestorev1.PublishedBinding{Binding: binding}, nil
}

func Shape(cost component.CostTree, scope string, topics Topics) {
	for _, topic := range topics {
		if topic.SNS != "" {
			cost.AddPlain(scope, component.TerraformSNSTopic, topic.Resource.Declared, map[string]any{"fifo_topic": topic.Spec.Ordered})
		}
		for _, queue := range topic.Queues {
			cost.AddPlain(scope, component.TerraformSQSQueue, queue.Topic+"/"+queue.Consumer.Name, map[string]any{"fifo_queue": queue.FIFO})
			cost.AddPlain(scope, component.TerraformSQSQueue, queue.Topic+"/"+queue.Consumer.Name+"/dead-letters", map[string]any{"fifo_queue": queue.FIFO})
		}
	}
}
