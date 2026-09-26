package aws_test

import (
	"context"
	"os"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	liveRegion = "us-east-1"
	liveWriter = provider.WrittenBy("live-suite")
)

type account struct {
	endpoint string
	aws      awssdk.Config
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

func (a account) boot(t *testing.T) provider.Bootstrap {
	t.Helper()
	p, err := aws.New(context.Background(), provider.Settings{Options: provider.Options{"region": liveRegion}})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	boot, err := p.Bootstrap(edges.DefaultKind)
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}
	return boot
}

func (a account) emptied(t *testing.T, classes ...edge.Class) provider.Bootstrap {
	t.Helper()
	boot := a.boot(t)
	ctx := context.Background()
	forget := func() {
		for _, class := range classes {
			if err := boot.Remove(ctx, class, nil); err != nil {
				t.Errorf("Remove(%s) = %v, want the emulator handed back as every other test finds it", class, err)
			}
		}
	}
	forget()
	t.Cleanup(forget)
	return boot
}

func (a account) stackStatus(t *testing.T, name string) string {
	t.Helper()
	out, err := cloudformation.NewFromConfig(a.aws).DescribeStacks(context.Background(),
		&cloudformation.DescribeStacksInput{StackName: awssdk.String(name)})
	if err != nil {
		return ""
	}
	if len(out.Stacks) == 0 {
		return ""
	}
	return string(out.Stacks[0].StackStatus)
}

func (a account) bucketExists(t *testing.T, name string) bool {
	t.Helper()
	_, err := s3.NewFromConfig(a.aws).HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: awssdk.String(name)})
	return err == nil
}

func (a account) tableExists(t *testing.T, name string) bool {
	t.Helper()
	_, err := dynamodb.NewFromConfig(a.aws).DescribeTable(context.Background(),
		&dynamodb.DescribeTableInput{TableName: awssdk.String(name)})
	return err == nil
}

func (a account) paramExists(t *testing.T, name string) bool {
	t.Helper()
	_, err := ssm.NewFromConfig(a.aws).GetParameter(context.Background(),
		&ssm.GetParameterInput{Name: awssdk.String(name), WithDecryption: awssdk.Bool(true)})
	return err == nil
}

func groupNamed(t *testing.T, plan provider.Plan, name string) provider.ChangeGroup {
	t.Helper()
	for _, group := range plan.Groups {
		if group.Name == name {
			return group
		}
	}
	t.Fatalf("the plan has no %q group, only %v", name, groupNames(plan))
	return provider.ChangeGroup{}
}

func groupNames(plan provider.Plan) []string {
	names := make([]string, 0, len(plan.Groups))
	for _, group := range plan.Groups {
		names = append(names, group.Name)
	}
	return names
}

func changeFor(group provider.ChangeGroup, name string) provider.Change {
	for _, change := range group.Changes {
		if change.Name == name {
			return change
		}
	}
	return provider.Change{}
}
