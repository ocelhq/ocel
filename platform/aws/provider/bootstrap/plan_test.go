package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func planned(t *testing.T, cfn CFNAPI, class string, req Request) []providerkit.ChangeGroup {
	t.Helper()

	ctx := context.Background()
	deployed, err := CheckDeployedFor(ctx, cfn, class)
	if err != nil {
		t.Fatalf("CheckDeployedFor: %v", err)
	}
	described := providerkit.Bootstrap{Class: providerkit.Class(class), Present: deployed.Present}
	for _, stack := range deployed.Stacks {
		described.Stacks = append(described.Stacks, providerkit.BootstrapStack{
			Name:          stack.Name,
			Feature:       stack.Feature,
			Present:       stack.Present,
			Schema:        uint32(stack.Schema),
			DigestCurrent: stack.Current(),
		})
	}
	groups, err := PlanChanges(ctx, cfn, class, req, providerkit.DeriveGroups(
		NameStacks(described), Catalogue(),
		providerkit.BootstrapRequest{Class: providerkit.Class(class), Features: req.Features, Drop: req.Drop}))
	if err != nil {
		t.Fatalf("PlanChanges: %v", err)
	}
	return groups
}

func groupNamed(t *testing.T, groups []providerkit.ChangeGroup, name string) providerkit.ChangeGroup {
	t.Helper()

	for _, group := range groups {
		if group.Name == name {
			return group
		}
	}
	t.Fatalf("the plan carries no group for %s", name)
	return providerkit.ChangeGroup{}
}

func changeNamed(t *testing.T, group providerkit.ChangeGroup, name string) providerkit.Change {
	t.Helper()

	for _, change := range group.Changes {
		if change.Name == name {
			return change
		}
	}
	t.Fatalf("%s carries no change for %s; it carries %v", group.Name, name, group.Changes)
	return providerkit.Change{}
}

func (f *fakeCFN) misstamp(stackName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags[stackName] = stampTags(Stamp{Schema: RequiredSchema, Digest: "beef", WrittenBy: "1.0.0"})
}

func TestPlanOnAFreshAccountReadsEveryResourceOffTheTemplates(t *testing.T) {
	groups := planned(t, newFakeCFN(), ClassProduction, everything())

	core := groupNamed(t, groups, StackName)
	if core.Action != providerkit.ActionCreate {
		t.Errorf("the core group is %q, want it created where nothing stands", core.Action)
	}
	bucket := changeNamed(t, core, "StateBucket")
	if bucket.Kind != "AWS::S3::Bucket" || bucket.Action != providerkit.ActionCreate {
		t.Errorf("StateBucket = %+v, want the bucket its own template declares, created", bucket)
	}
	for _, group := range groups {
		if len(group.Changes) == 0 {
			t.Errorf("%s carries no resource-level detail though its template was rendered", group.Name)
		}
		for _, change := range group.Changes {
			if change.Kind == "" {
				t.Errorf("%s under %s names no AWS type", change.Name, group.Name)
			}
		}
	}
}

func TestPlanReadsAnUpdateOffAChangeSetItNeverExecutes(t *testing.T) {
	cfn, _ := standingBootstrap(t)
	stack := isrStack(ClassProduction)
	cfn.fallBehind(stack)
	cfn.plan(stack,
		change(cfntypes.ChangeActionModify, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementTrue),
		change(cfntypes.ChangeActionAdd, "Revalidator", "AWS::Lambda::Function", cfntypes.ReplacementFalse))
	before := cfn.updates

	group := groupNamed(t, planned(t, cfn, ClassProduction, everything()), stack)
	if group.Action != providerkit.ActionUpdate {
		t.Fatalf("%s is %q, want it updated where its content is behind", stack, group.Action)
	}
	queue := changeNamed(t, group, "RevalidateQueue")
	if queue.Action != providerkit.ActionReplace {
		t.Errorf("RevalidateQueue = %q, want a replacement where CloudFormation cannot change it in place", queue.Action)
	}
	if queue.Reason == "" {
		t.Error("the replacement says nothing about why the queue cannot be changed in place")
	}
	if added := changeNamed(t, group, "Revalidator"); added.Action != providerkit.ActionCreate {
		t.Errorf("Revalidator = %q, want it created", added.Action)
	}
	if cfn.updates != before {
		t.Errorf("planning executed %d change sets; a plan writes nothing", cfn.updates-before)
	}
	if left := cfn.leftBehind(); len(left) != 0 {
		t.Errorf("change sets %v outlived the plan that made them", left)
	}
}

func TestPlanNamesTheTargetThatForcesAReplacement(t *testing.T) {
	cfn, _ := standingBootstrap(t)
	stack := isrStack(ClassProduction)
	cfn.fallBehind(stack)
	replacing := change(cfntypes.ChangeActionModify, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementTrue)
	replacing.Details = []cfntypes.ResourceChangeDetail{{
		Target: &cfntypes.ResourceTargetDefinition{
			Attribute:          cfntypes.ResourceAttributeProperties,
			Name:               aws.String("FifoQueue"),
			RequiresRecreation: cfntypes.RequiresRecreationAlways,
		},
	}}
	cfn.plan(stack, replacing)

	group := groupNamed(t, planned(t, cfn, ClassProduction, everything()), stack)
	if reason := changeNamed(t, group, "RevalidateQueue").Reason; !strings.Contains(reason, "FifoQueue") {
		t.Errorf("the replacement reads %q, want it to name the property that forces it", reason)
	}
}

func TestPlanKeepsAStackCloudFormationReportsNoChangesFor(t *testing.T) {
	cfn, _ := standingBootstrap(t)
	stack := isrStack(ClassProduction)
	cfn.misstamp(stack)

	group := groupNamed(t, planned(t, cfn, ClassProduction, everything()), stack)
	if group.Action != providerkit.ActionKeep {
		t.Errorf("%s is %q, want it kept where CloudFormation says the stack holds the content already", stack, group.Action)
	}
	if left := cfn.leftBehind(); len(left) != 0 {
		t.Errorf("change sets %v outlived the plan that made them", left)
	}
}

type unplannable struct{ *fakeCFN }

func (unplannable) CreateChangeSet(context.Context, *cloudformation.CreateChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	return nil, errors.New("this account may not plan change sets")
}

func TestPlanSaysSoWhenItCannotDiffAnUpdate(t *testing.T) {
	cfn, _ := standingBootstrap(t)
	stack := isrStack(ClassProduction)
	cfn.fallBehind(stack)

	group := groupNamed(t, planned(t, unplannable{cfn}, ClassProduction, everything()), stack)
	if group.Action != providerkit.ActionUpdate {
		t.Fatalf("%s is %q, want it still updated where the diff could not be read", stack, group.Action)
	}
	if len(group.Changes) != 0 {
		t.Errorf("%s carries %v though nothing could be read off CloudFormation", stack, group.Changes)
	}
	if !strings.Contains(group.Reason, providerkit.DetailUnavailable) {
		t.Errorf("%s reads %q, want it to own up to the detail it could not read", stack, group.Reason)
	}
}

func TestPlanListsWhatADroppedFeatureTakesWithIt(t *testing.T) {
	cfn, _ := standingBootstrap(t)
	stack := isrStack(ClassProduction)

	groups := planned(t, cfn, ClassProduction, Request{
		Features: []string{FeatureImageOptimization, FeatureCloudflareEdge},
		Drop:     []string{FeatureISR},
	})
	group := groupNamed(t, groups, stack)
	if group.Action != providerkit.ActionDelete || group.Feature != FeatureISR {
		t.Fatalf("%s = %+v, want the dropped feature deleted", stack, group)
	}
	queue := changeNamed(t, group, "RevalidateQueue")
	if queue.Action != providerkit.ActionDelete || queue.Kind == "" {
		t.Errorf("RevalidateQueue = %+v, want the queue the stack holds deleted", queue)
	}
}
