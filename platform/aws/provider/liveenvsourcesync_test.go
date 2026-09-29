package aws_test

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestLiveTheVariablesKeyMakesAnEnvSourceSyncAndRemovingTheTierRemovesIt(t *testing.T) {
	a := live(t)
	tier := environment.TierProduction
	boot := a.emptied(t, tier)
	ctx := context.Background()

	req := provider.BootstrapRequest{Tier: tier, WrittenBy: liveWriter, Features: []string{provider.FeatureVariablesKey}}
	if err := boot.Apply(ctx, req, nil); err != nil {
		t.Fatalf("Apply(%s) = %v", provider.FeatureVariablesKey, err)
	}

	name := defaultNamespace.FeatureStackName(provider.FeatureVariablesKey, tier)
	if status := a.stackStatus(t, name); status != "CREATE_COMPLETE" {
		t.Fatalf("%s is in state %q in CloudFormation, want CREATE_COMPLETE", name, status)
	}
	physical := a.stackResources(t, name)
	// TODO: assert the schedule and the sync's invoke config through their own APIs once floci's CloudFormation creates AWS::Scheduler::Schedule and AWS::Lambda::EventInvokeConfig; today it records a placeholder id for each and makes neither.
	for _, want := range []string{"EnvSourceSync", "EnvSourceSyncRole", "EnvSourceSyncLogGroup", "EnvSourceSyncInvokeConfig", "EnvSourceSyncScheduleRole", "EnvSourceSyncScheduleGroup"} {
		if physical[want] == "" {
			t.Errorf("%s has no %s, so no scheduled env source is kept current", name, want)
		}
	}
	group, err := defaultNamespace.StackNameFor(tier)
	if err != nil {
		t.Fatal(err)
	}
	if physical["EnvSourceSyncScheduleGroup"] != group {
		t.Errorf("the schedule group is %q, want %q, the name the bootstrap credential reaches it by", physical["EnvSourceSyncScheduleGroup"], group)
	}
	function := physical["EnvSourceSync"]
	if function != "" && !a.functionExists(function) {
		t.Errorf("%s names function %s, but Lambda has no such function", name, function)
	}

	if err := boot.Remove(ctx, tier, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", tier, err)
	}
	if status := a.stackStatus(t, name); status != "" && status != "DELETE_COMPLETE" {
		t.Errorf("%s is in state %q after the tier is removed, want it gone", name, status)
	}
	if function != "" && a.functionExists(function) {
		t.Errorf("the env source sync %s outlived the tier it synced", function)
	}
}

func (a account) stackResources(t *testing.T, stack string) map[string]string {
	t.Helper()
	out, err := cloudformation.NewFromConfig(a.aws).DescribeStackResources(context.Background(),
		&cloudformation.DescribeStackResourcesInput{StackName: awssdk.String(stack)})
	if err != nil {
		t.Fatalf("DescribeStackResources(%s) = %v", stack, err)
	}
	physical := map[string]string{}
	for _, resource := range out.StackResources {
		physical[awssdk.ToString(resource.LogicalResourceId)] = awssdk.ToString(resource.PhysicalResourceId)
	}
	return physical
}

func (a account) functionExists(name string) bool {
	_, err := lambda.NewFromConfig(a.aws).GetFunction(context.Background(), &lambda.GetFunctionInput{FunctionName: awssdk.String(name)})
	return err == nil
}
