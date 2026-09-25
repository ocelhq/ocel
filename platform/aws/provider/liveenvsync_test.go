package provider_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func TestLiveTheVarsKeyStandsAnEnvSyncerAndRemovingTheClassTakesIt(t *testing.T) {
	a := live(t)
	class := providerkit.ClassProduction
	bootstrapper := a.emptied(t, class)
	ctx := context.Background()

	req := providerkit.BootstrapRequest{Class: class, Writer: liveWriter, Features: []string{bootstrap.FeatureVarsKey}}
	if err := bootstrapper.Apply(ctx, req, nil); err != nil {
		t.Fatalf("Apply(%s) = %v", bootstrap.FeatureVarsKey, err)
	}

	name := defaultNamespace.FeatureStackName(bootstrap.FeatureVarsKey, string(class))
	if status := a.stackStatus(t, name); status != "CREATE_COMPLETE" {
		t.Fatalf("%s stands at %q in CloudFormation, want CREATE_COMPLETE", name, status)
	}
	physical := a.stackResources(t, name)
	// TODO: assert EnvSyncSchedule stands once floci's CloudFormation creates AWS::Scheduler::Schedule; today it records a placeholder id and no schedule.
	for _, want := range []string{"EnvSync", "EnvSyncRole", "EnvSyncLogGroup", "EnvSyncScheduleRole"} {
		if physical[want] == "" {
			t.Errorf("%s stands without %s, so no standing env source is kept in step", name, want)
		}
	}
	function := physical["EnvSync"]
	if function != "" && !a.functionStands(function) {
		t.Errorf("%s names function %s, but Lambda holds no such function", name, function)
	}

	if err := bootstrapper.Remove(ctx, class, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", class, err)
	}
	if status := a.stackStatus(t, name); status != "" && status != "DELETE_COMPLETE" {
		t.Errorf("%s stands at %q after the class is removed, want it gone", name, status)
	}
	if function != "" && a.functionStands(function) {
		t.Errorf("the env syncer %s outlived the class it synced", function)
	}
}

func (a account) stackResources(t *testing.T, stack string) map[string]string {
	t.Helper()
	out, err := cloudformation.NewFromConfig(a.aws).DescribeStackResources(context.Background(),
		&cloudformation.DescribeStackResourcesInput{StackName: aws.String(stack)})
	if err != nil {
		t.Fatalf("DescribeStackResources(%s) = %v", stack, err)
	}
	physical := map[string]string{}
	for _, resource := range out.StackResources {
		physical[aws.ToString(resource.LogicalResourceId)] = aws.ToString(resource.PhysicalResourceId)
	}
	return physical
}

func (a account) functionStands(name string) bool {
	_, err := lambda.NewFromConfig(a.aws).GetFunction(context.Background(), &lambda.GetFunctionInput{FunctionName: aws.String(name)})
	return err == nil
}
