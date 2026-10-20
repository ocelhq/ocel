package deploy

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

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
		Namespace:  testNamespace,
		Project:    project,
		Env:        "prod",
		Network:    component.Network{VPCID: "vpc-1", SubnetIDs: []string{"subnet-1", "subnet-2"}, Clients: "sg-vpc-clients"},
		Transforms: (*transformPatches)(nil),
	}
	return componenttest.Record(t, func(ctx *pulumi.Context) error {
		return store.Declare(ctx, at, []provider.Resource{{Name: "kv--cache", Type: provider.BindingKV, KV: &provider.KVSpec{}}})
	})
}

func declareTopicsAndTasks(pctx *pulumi.Context, r *release, project, env string, resources []provider.Resource) error {
	at := component.Stack{Namespace: r.cfg.Namespace, Project: project, Env: env, Region: r.cfg.Region, StateTable: r.cfg.StateTable, StateTableARN: r.cfg.StateTableARN, Resources: resources}
	for _, declaring := range []component.Component{task.New(), topic.New()} {
		if err := declaring.Declare(pctx, at, declaring.Declared(resources)); err != nil {
			return err
		}
	}
	return nil
}

type defaultVPCNetwork struct{}

func (defaultVPCNetwork) DescribeVpcs(context.Context, *awsec2.DescribeVpcsInput, ...func(*awsec2.Options)) (*awsec2.DescribeVpcsOutput, error) {
	return &awsec2.DescribeVpcsOutput{Vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-default"), CidrBlock: aws.String("172.31.0.0/16")}}}, nil
}

func (defaultVPCNetwork) DescribeAvailabilityZones(context.Context, *awsec2.DescribeAvailabilityZonesInput, ...func(*awsec2.Options)) (*awsec2.DescribeAvailabilityZonesOutput, error) {
	return &awsec2.DescribeAvailabilityZonesOutput{AvailabilityZones: []ec2types.AvailabilityZone{{ZoneId: aws.String("use1-az1")}, {ZoneId: aws.String("use1-az2")}}}, nil
}

func (defaultVPCNetwork) DescribeSubnets(context.Context, *awsec2.DescribeSubnetsInput, ...func(*awsec2.Options)) (*awsec2.DescribeSubnetsOutput, error) {
	return &awsec2.DescribeSubnetsOutput{}, nil
}
