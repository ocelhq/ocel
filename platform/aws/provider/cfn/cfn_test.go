package cfn

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

type failedStack struct {
	API
	status cfntypes.StackStatus
	events []cfntypes.StackEvent
	nested map[string][]cfntypes.StackEvent
	pages  int
}

func (f *failedStack) CreateStack(context.Context, *cloudformation.CreateStackInput, ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error) {
	return &cloudformation.CreateStackOutput{}, nil
}

func (f *failedStack) UpdateStack(context.Context, *cloudformation.UpdateStackInput, ...func(*cloudformation.Options)) (*cloudformation.UpdateStackOutput, error) {
	return &cloudformation.UpdateStackOutput{}, nil
}

func (f *failedStack) CreateChangeSet(_ context.Context, in *cloudformation.CreateChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	return &cloudformation.CreateChangeSetOutput{Id: aws.String(aws.ToString(in.StackName) + "/changes")}, nil
}

func (f *failedStack) DescribeChangeSet(_ context.Context, in *cloudformation.DescribeChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error) {
	return &cloudformation.DescribeChangeSetOutput{
		ChangeSetId: in.ChangeSetName,
		Status:      cfntypes.ChangeSetStatusCreateComplete,
		Changes: []cfntypes.Change{{ResourceChange: &cfntypes.ResourceChange{
			Action:            cfntypes.ChangeActionModify,
			LogicalResourceId: aws.String("EdgeFront"),
		}}},
	}, nil
}

func (f *failedStack) ExecuteChangeSet(context.Context, *cloudformation.ExecuteChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error) {
	return &cloudformation.ExecuteChangeSetOutput{}, nil
}

type namer struct{}

func (namer) ChangeSetNameFor(stackName string) string { return stackName + "-changes" }

const updateRollbackCleanup = cfntypes.ResourceStatus("UPDATE_ROLLBACK_COMPLETE_CLEANUP_IN_PROGRESS")

func (f *failedStack) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName:   in.StackName,
		StackStatus: f.status,
	}}}, nil
}

func (f *failedStack) DescribeStackEvents(_ context.Context, in *cloudformation.DescribeStackEventsInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStackEventsOutput, error) {
	f.pages++
	if held, nested := f.nested[aws.ToString(in.StackName)]; nested {
		return &cloudformation.DescribeStackEventsOutput{StackEvents: held}, nil
	}
	return &cloudformation.DescribeStackEventsOutput{StackEvents: f.events}, nil
}

func event(logical, kind string, status cfntypes.ResourceStatus, reason string) cfntypes.StackEvent {
	held := cfntypes.StackEvent{
		LogicalResourceId: aws.String(logical),
		ResourceType:      aws.String(kind),
		ResourceStatus:    status,
	}
	if reason != "" {
		held.ResourceStatusReason = aws.String(reason)
	}
	return held
}

const stackType = "AWS::CloudFormation::Stack"

func stackEvent(stackID, logical string, status cfntypes.ResourceStatus, reason string) cfntypes.StackEvent {
	held := event(logical, stackType, status, reason)
	held.StackId = aws.String(stackID)
	held.PhysicalResourceId = aws.String(stackID)
	return held
}

func embeddedEvent(parentID, childID, logical string, status cfntypes.ResourceStatus, reason string) cfntypes.StackEvent {
	held := event(logical, stackType, status, reason)
	held.StackId = aws.String(parentID)
	held.PhysicalResourceId = aws.String(childID)
	return held
}

const thisStack = "arn:aws:cloudformation:eu-west-1:111122223333:stack/ocel-bootstrap/now"

func TestCreateCarriesTheReasonCloudFormationGaveThroughTheRollbackThatFollowedIt(t *testing.T) {
	const reason = "Resource handler returned message: \"Limit exceeded for resource of type 'AWS::CloudFront::CachePolicy'. Reason: Account's Cache Policies limit reached.\""
	api := &failedStack{
		status: cfntypes.StackStatusRollbackComplete,
		events: []cfntypes.StackEvent{
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusRollbackComplete, ""),
			event("EdgeResolver", "AWS::CloudFront::Function", cfntypes.ResourceStatusDeleteComplete, ""),
			event("EdgeCachePolicy", "AWS::CloudFront::CachePolicy", cfntypes.ResourceStatusDeleteComplete, ""),
			event("EdgeResolver", "AWS::CloudFront::Function", cfntypes.ResourceStatusDeleteInProgress, ""),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusRollbackInProgress, "The following resource(s) failed to create: [EdgeCachePolicy]. Rollback requested by user."),
			event("EdgeResolver", "AWS::CloudFront::Function", cfntypes.ResourceStatusCreateFailed, "Resource creation cancelled"),
			event("EdgeCachePolicy", "AWS::CloudFront::CachePolicy", cfntypes.ResourceStatusCreateFailed, reason),
			event("EdgeCachePolicy", "AWS::CloudFront::CachePolicy", cfntypes.ResourceStatusCreateInProgress, "Resource creation Initiated"),
			event("EdgeCachePolicy", "AWS::CloudFront::CachePolicy", cfntypes.ResourceStatusCreateInProgress, ""),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusCreateInProgress, "User Initiated"),
		},
	}

	err := Create(context.Background(), api, "ocel-bootstrap-cloudfront-edge", "{}", nil, nil, nil)
	if err == nil {
		t.Fatal("Create over a stack that failed to create = nil, want the waiter failure")
	}
	if !strings.Contains(err.Error(), reason) {
		t.Errorf("Create = %v, want the reason CloudFormation gave", err)
	}
	if !strings.Contains(err.Error(), "EdgeCachePolicy") {
		t.Errorf("Create = %v, want the resource that failed named", err)
	}
	if strings.Contains(err.Error(), "cancelled") {
		t.Errorf("Create = %v, want the resource that failed rather than one cancelled behind it", err)
	}
}

func TestUpdateCarriesTheReasonThisRunFailedOnRatherThanAnOlderOne(t *testing.T) {
	const ancient = "Resource handler returned message: Account's Cache Policies limit reached."
	const now = "Resource handler returned message: the VPC origin is not available."
	api := &failedStack{
		status: cfntypes.StackStatusUpdateRollbackComplete,
		events: []cfntypes.StackEvent{
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusUpdateRollbackComplete, ""),
			stackEvent(thisStack, "ocel-bootstrap", updateRollbackCleanup, ""),
			event("EdgeFront", "AWS::CloudFront::Distribution", cfntypes.ResourceStatusUpdateComplete, ""),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusUpdateRollbackInProgress, "The following resource(s) failed to update: [EdgeFront]."),
			event("EdgeFront", "AWS::CloudFront::Distribution", cfntypes.ResourceStatusUpdateFailed, now),
			event("EdgeFront", "AWS::CloudFront::Distribution", cfntypes.ResourceStatusUpdateInProgress, ""),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusUpdateInProgress, "User Initiated"),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusUpdateRollbackComplete, ""),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusUpdateRollbackInProgress, "The following resource(s) failed to update: [EdgeCachePolicy]."),
			event("EdgeCachePolicy", "AWS::CloudFront::CachePolicy", cfntypes.ResourceStatusUpdateFailed, ancient),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusUpdateInProgress, "User Initiated"),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusCreateComplete, ""),
		},
	}

	err := Update(context.Background(), api, namer{}, "ocel-bootstrap", "{}", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("Update over a stack that rolled the update back = nil, want the waiter failure")
	}
	if !strings.Contains(err.Error(), now) {
		t.Errorf("Update = %v, want the reason this run failed on", err)
	}
	if strings.Contains(err.Error(), ancient) {
		t.Errorf("Update = %v, want this run's failure rather than one the stack carried from an earlier operation", err)
	}
}

func TestRestampCarriesTheReasonCloudFormationGave(t *testing.T) {
	const reason = "Resource handler returned message: the distribution is being deployed."
	api := &failedStack{
		status: cfntypes.StackStatusUpdateRollbackComplete,
		events: []cfntypes.StackEvent{
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusUpdateRollbackComplete, ""),
			stackEvent(thisStack, "ocel-bootstrap", updateRollbackCleanup, ""),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusUpdateRollbackInProgress, "The following resource(s) failed to update: [EdgeFront]."),
			event("EdgeFront", "AWS::CloudFront::Distribution", cfntypes.ResourceStatusUpdateFailed, reason),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusUpdateInProgress, "User Initiated"),
		},
	}

	err := Restamp(context.Background(), api, "ocel-bootstrap", nil, nil, []cfntypes.Tag{{Key: aws.String(TagNamespace), Value: aws.String("dev")}})
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Errorf("Restamp = %v, want the reason CloudFormation gave", err)
	}
}

func TestCreateKeepsAReasonThatMerelySaysCancelled(t *testing.T) {
	const reason = "Received response status [FAILED] from custom resource: the upstream request was cancelled by the origin."
	api := &failedStack{
		status: cfntypes.StackStatusRollbackComplete,
		events: []cfntypes.StackEvent{
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusRollbackComplete, ""),
			event("Warmer", "AWS::CloudFormation::CustomResource", cfntypes.ResourceStatusDeleteComplete, ""),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusRollbackInProgress, "The following resource(s) failed to create: [Warmer]. Rollback requested by user."),
			event("Warmer", "AWS::CloudFormation::CustomResource", cfntypes.ResourceStatusCreateFailed, reason),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusCreateInProgress, "User Initiated"),
		},
	}

	err := Create(context.Background(), api, "ocel-bootstrap", "{}", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Errorf("Create = %v, want the reason CloudFormation gave rather than nothing because it holds the word cancelled", err)
	}
}

func TestCreateFollowsAnEmbeddedStackToTheResourceThatActuallyFailed(t *testing.T) {
	const child = "arn:aws:cloudformation:eu-west-1:111122223333:stack/ocel-bootstrap-EdgeSet/now"
	const reason = "Resource handler returned message: \"Limit exceeded for resource of type 'AWS::CloudFront::CachePolicy'.\""
	api := &failedStack{
		status: cfntypes.StackStatusRollbackComplete,
		events: []cfntypes.StackEvent{
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusRollbackComplete, ""),
			embeddedEvent(thisStack, child, "EdgeSet", cfntypes.ResourceStatusDeleteComplete, ""),
			embeddedEvent(thisStack, child, "EdgeSet", cfntypes.ResourceStatusDeleteInProgress, ""),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusRollbackInProgress, "The following resource(s) failed to create: [EdgeSet]. Rollback requested by user."),
			embeddedEvent(thisStack, child, "EdgeSet", cfntypes.ResourceStatusCreateFailed, "Embedded stack "+child+" was not successfully created: The following resource(s) failed to create: [EdgeCachePolicy]."),
			embeddedEvent(thisStack, child, "EdgeSet", cfntypes.ResourceStatusCreateInProgress, "Resource creation Initiated"),
			stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusCreateInProgress, "User Initiated"),
		},
		nested: map[string][]cfntypes.StackEvent{child: {
			stackEvent(child, "ocel-bootstrap-EdgeSet", cfntypes.ResourceStatusDeleteComplete, ""),
			event("EdgeCachePolicy", "AWS::CloudFront::CachePolicy", cfntypes.ResourceStatusDeleteComplete, ""),
			stackEvent(child, "ocel-bootstrap-EdgeSet", cfntypes.ResourceStatusDeleteInProgress, ""),
			stackEvent(child, "ocel-bootstrap-EdgeSet", cfntypes.ResourceStatusRollbackComplete, ""),
			event("EdgeCachePolicy", "AWS::CloudFront::CachePolicy", cfntypes.ResourceStatusDeleteComplete, ""),
			stackEvent(child, "ocel-bootstrap-EdgeSet", cfntypes.ResourceStatusRollbackInProgress, "The following resource(s) failed to create: [EdgeCachePolicy]. Rollback requested by user."),
			event("EdgeCachePolicy", "AWS::CloudFront::CachePolicy", cfntypes.ResourceStatusCreateFailed, reason),
			event("EdgeCachePolicy", "AWS::CloudFront::CachePolicy", cfntypes.ResourceStatusCreateInProgress, ""),
			stackEvent(child, "ocel-bootstrap-EdgeSet", cfntypes.ResourceStatusCreateInProgress, "User Initiated"),
		}},
	}

	err := Create(context.Background(), api, "ocel-bootstrap", "{}", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Errorf("Create = %v, want the reason the embedded stack gave rather than only that it was not successfully created", err)
	}
}

func TestCreateSaysWhatItCanWhenNoEventNamesAReason(t *testing.T) {
	api := &failedStack{
		status: cfntypes.StackStatusRollbackComplete,
		events: []cfntypes.StackEvent{stackEvent(thisStack, "ocel-bootstrap", cfntypes.ResourceStatusCreateInProgress, "User Initiated")},
	}

	err := Create(context.Background(), api, "ocel-bootstrap", "{}", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "ocel-bootstrap") {
		t.Fatalf("Create = %v, want the waiter failure naming the stack", err)
	}
}
