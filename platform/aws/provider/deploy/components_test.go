package deploy

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/task"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
)

func registeredKV(t *testing.T, project string) *componenttest.Recorder {
	t.Helper()
	store := kv.New(kv.Tokens{Parameters: newFakeParameters(), Root: "/ocel/kv"})
	at := component.Stack{
		Project:    project,
		Env:        "prod",
		Network:    component.Network{VPCID: "vpc-1", VPCCIDR: "10.0.0.0/16", SubnetIDs: []string{"subnet-1", "subnet-2"}},
		Transforms: (*transformPatches)(nil),
	}
	return componenttest.Record(t, func(ctx *pulumi.Context) error {
		return store.Declare(ctx, at, []provider.Resource{{Name: "kv--cache", Type: provider.BindingKV, KV: &provider.KVSpec{}}})
	})
}

func declareTopicsAndTasks(pctx *pulumi.Context, r *release, project, env string, resources []provider.Resource) error {
	at := component.Stack{Project: project, Env: env, Region: r.cfg.Region, StateTable: r.cfg.StateTable, StateTableARN: r.cfg.StateTableARN, Resources: resources}
	for _, declaring := range []component.Component{task.New(), topic.New()} {
		if err := declaring.Declare(pctx, at, declaring.Declared(resources)); err != nil {
			return err
		}
	}
	return nil
}
