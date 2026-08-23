package pulumi_test

import (
	"context"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/pulumi"
)

type recordingEngine struct {
	up      pulumi.Setup
	down    pulumi.Setup
	outputs auto.OutputMap
	err     error
}

func (e *recordingEngine) Up(_ context.Context, setup pulumi.Setup, _ providerkit.Reporter) (auto.OutputMap, error) {
	e.up = setup
	return e.outputs, e.err
}

func (e *recordingEngine) Destroy(_ context.Context, setup pulumi.Setup, _ providerkit.Reporter) error {
	e.down = setup
	return e.err
}

func (e *recordingEngine) Outputs(context.Context, pulumi.Setup) (auto.OutputMap, error) {
	return e.outputs, e.err
}

type decoding struct{ program }

func (decoding) Decode(_ context.Context, _ providerkit.StackPlan, outputs auto.OutputMap) (providerkit.StackResult, error) {
	properties := make(map[string]string, len(outputs))
	for name, output := range outputs {
		properties[name], _ = output.Value.(string)
	}
	return providerkit.StackResult{Links: []providerkit.Link{{Name: "uploads", Properties: properties}}}, nil
}
