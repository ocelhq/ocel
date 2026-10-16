//go:build integration

package aws_test

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/envsourcesync"
)

func TestLiveTheFirstScheduledEnvSourceAddsAnEnvSourceSyncAndRemovingTheTierRemovesIt(t *testing.T) {
	a := live(t)
	tier := environment.TierProduction
	boot := a.emptied(t, tier)
	ctx := context.Background()

	req := provider.BootstrapRequest{Tier: tier, WrittenBy: liveWriter, Features: []string{provider.FeatureVariablesKey}}
	if err := boot.Apply(ctx, req, nil); err != nil {
		t.Fatalf("Apply(%s) = %v", provider.FeatureVariablesKey, err)
	}
	stack := defaultNamespace.FeatureStackName(provider.FeatureVariablesKey, tier)
	if physical := a.stackResources(t, stack); physical["EnvSourceSync"] != "" {
		t.Errorf("%s still makes the env source sync, which the first scheduled env source adds", stack)
	}

	p, err := aws.New(ctx, provider.Settings{Options: provider.Options{"region": liveRegion}})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if err := p.Hooks().EnsureEnvSourceSync(ctx, tier, "shop"); err != nil {
		t.Fatalf("EnsureEnvSourceSync() = %v", err)
	}
	function := envsourcesync.NamesFor(string(defaultNamespace), tier).Function
	if !a.functionExists(function) {
		t.Errorf("Lambda has no %s once the first scheduled env source added it", function)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, envsourcesync.Part)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" {
		t.Errorf("part record = %+v, %v, %v, want it ready and added by shop", recorded, present, err)
	}

	functions := lambda.NewFromConfig(a.aws)
	if _, err := functions.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
		FunctionName: awssdk.String(function),
		Timeout:      awssdk.Int32(handEditedTimeout),
	}); err != nil {
		t.Fatalf("UpdateFunctionConfiguration(%s) = %v", function, err)
	}
	if err := boot.Apply(ctx, req, nil); err != nil {
		t.Fatalf("Apply(%s) again = %v", tier, err)
	}
	if config, err := functions.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: awssdk.String(function)}); err != nil || awssdk.ToInt32(config.Timeout) == handEditedTimeout {
		t.Errorf("the env source sync edited by hand still times out after %v (%v) once ocel bootstrap ran, want it made again", config, err)
	}

	if err := boot.Remove(ctx, tier, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", tier, err)
	}
	if a.functionExists(function) {
		t.Errorf("the env source sync %s outlived the tier it synced", function)
	}
}

const handEditedTimeout = 300

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
