package connector

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

type installedConnector struct {
	cfn.API
	deleted bool
}

func (c *installedConnector) DescribeStacks(context.Context, *cloudformation.DescribeStacksInput,
	...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	status := cfntypes.StackStatusCreateComplete
	if c.deleted {
		status = cfntypes.StackStatusDeleteComplete
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName:   aws.String(StackName(defaultNamespace)),
		StackStatus: status,
		Outputs:     []cfntypes.Output{{OutputKey: aws.String(outputBucket), OutputValue: aws.String("ocel-connector-code")}},
	}}}, nil
}

func (c *installedConnector) DeleteStack(context.Context, *cloudformation.DeleteStackInput,
	...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error) {
	c.deleted = true
	return &cloudformation.DeleteStackOutput{}, nil
}

type emptyBuckets struct{}

func (emptyBuckets) ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	return &s3.ListObjectVersionsOutput{}, nil
}

func (emptyBuckets) DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	return &s3.DeleteObjectsOutput{}, nil
}

type absentConnector struct{ cfn.API }

func (absentConnector) DescribeStacks(context.Context, *cloudformation.DescribeStacksInput,
	...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	return &cloudformation.DescribeStacksOutput{}, nil
}

func TestRemovingTheConnectorNamesTheBucketStackAndParameterItDeleted(t *testing.T) {
	t.Parallel()

	var progress fake.Log
	apis := APIs{CFN: &installedConnector{}, Buckets: emptyBuckets{}, SSM: &keyStore{}}
	if err := Remove(context.Background(), apis, defaultNamespace, &progress); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	want := []string{
		"INFO Emptied bucket ocel-connector-code, where the connector's code was staged",
		"INFO Deleted stack " + StackName(defaultNamespace),
		"INFO Deleted parameter " + KeyParameter(defaultNamespace) + ", the connector's key",
	}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("Remove() said %q, want %q", got, want)
	}
}

func TestRemovingAConnectorThatIsNotInstalledSaysThereIsNothingToRemove(t *testing.T) {
	t.Parallel()

	var progress fake.Log
	if err := Remove(context.Background(), APIs{CFN: absentConnector{}}, defaultNamespace, &progress); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	want := []string{"INFO Nothing to remove: stack " + StackName(defaultNamespace) + " does not exist"}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("Remove() said %q, want %q", got, want)
	}
}

func TestAConnectorUpgradeThatReplacesItsUrlAndPermissionsIsWrittenAndSaysSo(t *testing.T) {
	t.Parallel()

	var progress fake.Log
	err := reviewing(&progress)(StackName(defaultNamespace), []cfntypes.ResourceChange{
		{LogicalResourceId: aws.String("ConnectorUrl"), ResourceType: aws.String("AWS::Lambda::Url"), Action: cfntypes.ChangeActionModify, Replacement: cfntypes.ReplacementConditional},
		{LogicalResourceId: aws.String("ConnectorHeartbeatInvokes"), ResourceType: aws.String("AWS::Lambda::Permission"), Action: cfntypes.ChangeActionModify, Replacement: cfntypes.ReplacementTrue},
	})
	if err != nil {
		t.Fatalf("reviewing() = %v, want an upgrade that replaces the function's url and permissions written: neither holds state, and connector add asks nothing", err)
	}
	if want := []string{"INFO Replacing ConnectorHeartbeatInvokes, which holds nothing but what this install writes again"}; !slices.Equal(progress.Lines(), want) {
		t.Errorf("reviewing() said %q, want %q", progress.Lines(), want)
	}
}
