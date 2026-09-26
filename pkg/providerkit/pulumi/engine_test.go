package pulumi_test

import (
	"context"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type recordingEngine struct {
	up        pulumi.Setup
	down      pulumi.Setup
	outputs   auto.OutputMap
	rows      []provider.Change
	previewed pulumi.Op
	err       error
}

func (e *recordingEngine) Preview(_ context.Context, _ pulumi.Setup, op pulumi.Op, _ edge.Progress) ([]provider.Change, error) {
	e.previewed = op
	return e.rows, e.err
}

func (e *recordingEngine) Up(_ context.Context, setup pulumi.Setup, _ edge.Progress) (auto.OutputMap, error) {
	e.up = setup
	return e.outputs, e.err
}

func (e *recordingEngine) Destroy(_ context.Context, setup pulumi.Setup, _ edge.Progress) error {
	e.down = setup
	return e.err
}

func (e *recordingEngine) Outputs(context.Context, pulumi.Setup) (auto.OutputMap, error) {
	return e.outputs, e.err
}

type decoding struct{ program }

func (decoding) Decode(_ context.Context, _ provider.StackSpec, outputs auto.OutputMap) (provider.StackResult, error) {
	properties := make(map[string]string, len(outputs))
	for name, output := range outputs {
		properties[name], _ = output.Value.(string)
	}
	return provider.StackResult{Bindings: []provider.Binding{{Name: "uploads", Properties: properties}}}, nil
}
