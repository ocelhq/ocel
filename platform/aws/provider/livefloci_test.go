package provider_test

import (
	"context"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/providerkit"
	provider "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

const (
	liveRegion = "us-east-1"
	liveWriter = providerkit.Writer("live-suite")
)

type account struct {
	endpoint string
	aws      aws.Config
}

func live(t *testing.T) account {
	t.Helper()
	endpoint := os.Getenv("OCEL_FLOCI_ENDPOINT")
	if endpoint == "" {
		t.Skip("no floci emulator in the environment; run under `scripts/floci.sh run <name> -- go test ./...`")
	}
	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", liveRegion)

	cfg, err := sdkconfig.Control(context.Background(), liveRegion)
	if err != nil {
		t.Fatalf("sdkconfig.Control() against %s = %v", endpoint, err)
	}
	return account{endpoint: endpoint, aws: cfg}
}

func (a account) bootstrapper(t *testing.T) providerkit.Bootstrapper {
	t.Helper()
	p, err := provider.New(context.Background(), providerkit.Options{"region": liveRegion})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	bootstrapper, err := p.Bootstrap(edges.DefaultKind)
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}
	return bootstrapper
}

func (a account) emptied(t *testing.T, classes ...providerkit.Class) providerkit.Bootstrapper {
	t.Helper()
	bootstrapper := a.bootstrapper(t)
	ctx := context.Background()
	forget := func() {
		for _, class := range classes {
			if err := bootstrapper.Remove(ctx, class, nil); err != nil {
				t.Errorf("Remove(%s) = %v, want the emulator handed back as every other test finds it", class, err)
			}
		}
	}
	forget()
	t.Cleanup(forget)
	return bootstrapper
}

func (a account) stackStatus(t *testing.T, name string) string {
	t.Helper()
	out, err := cloudformation.NewFromConfig(a.aws).DescribeStacks(context.Background(),
		&cloudformation.DescribeStacksInput{StackName: aws.String(name)})
	if err != nil {
		return ""
	}
	if len(out.Stacks) == 0 {
		return ""
	}
	return string(out.Stacks[0].StackStatus)
}

func (a account) stackOutput(t *testing.T, stack, key string) string {
	t.Helper()
	out, err := cloudformation.NewFromConfig(a.aws).DescribeStacks(context.Background(),
		&cloudformation.DescribeStacksInput{StackName: aws.String(stack)})
	if err != nil || len(out.Stacks) == 0 {
		t.Fatalf("DescribeStacks(%s) = %v", stack, err)
	}
	for _, output := range out.Stacks[0].Outputs {
		if aws.ToString(output.OutputKey) == key {
			return aws.ToString(output.OutputValue)
		}
	}
	t.Fatalf("stack %s carries no %s output", stack, key)
	return ""
}

func (a account) bucketStands(t *testing.T, name string) bool {
	t.Helper()
	_, err := s3.NewFromConfig(a.aws).HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String(name)})
	return err == nil
}

func (a account) tableStands(t *testing.T, name string) bool {
	t.Helper()
	_, err := dynamodb.NewFromConfig(a.aws).DescribeTable(context.Background(),
		&dynamodb.DescribeTableInput{TableName: aws.String(name)})
	return err == nil
}

func (a account) paramStands(t *testing.T, name string) bool {
	t.Helper()
	_, err := ssm.NewFromConfig(a.aws).GetParameter(context.Background(),
		&ssm.GetParameterInput{Name: aws.String(name), WithDecryption: aws.Bool(true)})
	return err == nil
}

func groupNamed(t *testing.T, plan providerkit.BootstrapPlan, name string) providerkit.ChangeGroup {
	t.Helper()
	for _, group := range plan.Groups {
		if group.Name == name {
			return group
		}
	}
	t.Fatalf("the plan carries no %q group, only %v", name, groupNames(plan))
	return providerkit.ChangeGroup{}
}

func groupNames(plan providerkit.BootstrapPlan) []string {
	names := make([]string, 0, len(plan.Groups))
	for _, group := range plan.Groups {
		names = append(names, group.Name)
	}
	return names
}

func changeFor(group providerkit.ChangeGroup, name string) providerkit.Change {
	for _, change := range group.Changes {
		if change.Name == name {
			return change
		}
	}
	return providerkit.Change{}
}
