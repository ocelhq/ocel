package pulumi_test

import (
	"context"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
)

type recordingEngine struct {
	up        pulumi.WorkspaceSpec
	down      pulumi.WorkspaceSpec
	outputs   auto.OutputMap
	rows      []provider.Change
	previewed pulumi.Operation
	err       error
}

func (e *recordingEngine) Preview(_ context.Context, _ pulumi.WorkspaceSpec, op pulumi.Operation, _ progress.Log) ([]provider.Change, error) {
	e.previewed = op
	return e.rows, e.err
}

func (e *recordingEngine) Up(_ context.Context, setup pulumi.WorkspaceSpec, _ progress.Log) (auto.OutputMap, error) {
	e.up = setup
	return e.outputs, e.err
}

func (e *recordingEngine) Destroy(_ context.Context, setup pulumi.WorkspaceSpec, _ progress.Log) error {
	e.down = setup
	return e.err
}

func (e *recordingEngine) Outputs(context.Context, pulumi.WorkspaceSpec) (auto.OutputMap, error) {
	return e.outputs, e.err
}

func (*recordingEngine) Unlock(context.Context, pulumi.WorkspaceSpec) error { return nil }

type decoding struct{ program }

func (decoding) Decode(_ context.Context, _ provider.StackSpec, outputs auto.OutputMap) (provider.StackResult, error) {
	properties := make(map[string]string, len(outputs))
	for name, output := range outputs {
		properties[name], _ = output.Value.(string)
	}
	return provider.StackResult{Bindings: []provider.Binding{{Name: "uploads", Properties: properties}}}, nil
}

type sayings struct {
	progress.Log
	said []string
}

func (s *sayings) Say(message string) { s.said = append(s.said, message) }
