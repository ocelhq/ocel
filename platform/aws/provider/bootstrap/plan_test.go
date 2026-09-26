package bootstrap

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func planned(t *testing.T, stacks cfn.API, class string, req Request) []provider.ChangeGroup {
	t.Helper()

	ctx := context.Background()
	read, err := Read(ctx, stacks, defaultNamespace, class)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	deployed := read.Deployed
	described := provider.BootstrapDescription{Class: edge.Class(class), Present: deployed.Present}
	for _, stack := range deployed.Stacks {
		described.Stacks = append(described.Stacks, provider.BootstrapStack{
			Name:          stack.Name,
			Feature:       stack.Feature,
			Present:       stack.Present,
			Schema:        uint32(stack.Schema),
			DigestCurrent: stack.Current(),
		})
	}
	groups, err := PlanChanges(ctx, stacks, read, req, bootstrapplan.ChangeGroups(
		NameStacks(defaultNamespace, described), Catalogue(),
		provider.BootstrapRequest{Class: edge.Class(class), Features: req.Features, Remove: req.Remove}))
	if err != nil {
		t.Fatalf("PlanChanges: %v", err)
	}
	return groups
}

func groupNamed(t *testing.T, groups []provider.ChangeGroup, name string) provider.ChangeGroup {
	t.Helper()

	for _, group := range groups {
		if group.Name == name {
			return group
		}
	}
	t.Fatalf("the plan has no group for %s", name)
	return provider.ChangeGroup{}
}

func changeNamed(t *testing.T, group provider.ChangeGroup, name string) provider.Change {
	t.Helper()

	for _, change := range group.Changes {
		if change.Name == name {
			return change
		}
	}
	t.Fatalf("%s has no change for %s; it has %v", group.Name, name, group.Changes)
	return provider.Change{}
}

func (f *fakeCFN) misstamp(stackName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags[stackName] = stampTags(defaultNamespace, Stamp{Schema: RequiredSchema, Digest: "beef", WrittenBy: "1.0.0"})
}

func TestPlanOnAFreshAccountReadsEveryResourceOffTheTemplates(t *testing.T) {
	groups := planned(t, newFakeCFN(), ClassProduction, everything())

	core := groupNamed(t, groups, coreStackName)
	if core.Action != provider.ActionCreate {
		t.Errorf("the core group is %q, want it created where nothing is provisioned", core.Action)
	}
	bucket := changeNamed(t, core, "StateBucket")
	if bucket.Kind != "AWS::S3::Bucket" || bucket.Action != provider.ActionCreate {
		t.Errorf("StateBucket = %+v, want the bucket its own template declares, created", bucket)
	}
	for _, group := range groups {
		if len(group.Changes) == 0 {
			t.Errorf("%s has no resource-level detail though its template was rendered", group.Name)
		}
		for _, change := range group.Changes {
			if change.Kind == "" {
				t.Errorf("%s under %s names no AWS type", change.Name, group.Name)
			}
		}
	}
}

func TestPlanReadsAnUpdateOffAChangeSetItNeverExecutes(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := isrStack(ClassProduction)
	stacks.fallBehind(stack)
	stacks.plan(stack,
		change(cfntypes.ChangeActionModify, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementTrue),
		change(cfntypes.ChangeActionAdd, "Revalidator", "AWS::Lambda::Function", cfntypes.ReplacementFalse))
	before := stacks.updates

	group := groupNamed(t, planned(t, stacks, ClassProduction, everything()), stack)
	if group.Action != provider.ActionUpdate {
		t.Fatalf("%s is %q, want it updated where its content is behind", stack, group.Action)
	}
	queue := changeNamed(t, group, "RevalidateQueue")
	if queue.Action != provider.ActionReplace {
		t.Errorf("RevalidateQueue = %q, want a replacement where CloudFormation cannot change it in place", queue.Action)
	}
	if queue.Reason == "" {
		t.Error("the replacement says nothing about why the queue cannot be changed in place")
	}
	if added := changeNamed(t, group, "Revalidator"); added.Action != provider.ActionCreate {
		t.Errorf("Revalidator = %q, want it created", added.Action)
	}
	if stacks.updates != before {
		t.Errorf("planning executed %d change sets; a plan writes nothing", stacks.updates-before)
	}
	if left := stacks.leftBehind(); len(left) != 0 {
		t.Errorf("change sets %v outlived the plan that made them", left)
	}
}

func TestPlanNamesTheTargetThatForcesAReplacement(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := isrStack(ClassProduction)
	stacks.fallBehind(stack)
	replacing := change(cfntypes.ChangeActionModify, "RevalidateQueue", "AWS::SQS::Queue", cfntypes.ReplacementTrue)
	replacing.Details = []cfntypes.ResourceChangeDetail{{
		Target: &cfntypes.ResourceTargetDefinition{
			Attribute:          cfntypes.ResourceAttributeProperties,
			Name:               aws.String("FifoQueue"),
			RequiresRecreation: cfntypes.RequiresRecreationAlways,
		},
	}}
	stacks.plan(stack, replacing)

	group := groupNamed(t, planned(t, stacks, ClassProduction, everything()), stack)
	if reason := changeNamed(t, group, "RevalidateQueue").Reason; !strings.Contains(reason, "FifoQueue") {
		t.Errorf("the replacement reads %q, want it to name the property that forces it", reason)
	}
}

func TestPlanStillUpdatesAStackWhoseStampAloneIsBehind(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := isrStack(ClassProduction)
	stacks.misstamp(stack)

	groups := planned(t, stacks, ClassProduction, everything())
	group := groupNamed(t, groups, stack)
	if group.Action != provider.ActionUpdate {
		t.Errorf("%s is %q, want it updated where only its stamp is behind — the restamp is on the apply path", stack, group.Action)
	}
	if len(group.Changes) != 0 {
		t.Errorf("%s has %v though CloudFormation reports no resource changes", stack, group.Changes)
	}
	if group.Reason == "" {
		t.Errorf("%s says nothing about why it is updated with nothing under it", stack)
	}
	if other := groupNamed(t, groups, coreStackName); other.Action != provider.ActionKeep {
		t.Errorf("%s is %q, want the stacks nothing is stale about kept", coreStackName, other.Action)
	}
	if left := stacks.leftBehind(); len(left) != 0 {
		t.Errorf("change sets %v outlived the plan that made them", left)
	}
}

type gatedPlans struct {
	*fakeCFN
	mu       sync.Mutex
	inFlight int
	peak     int
	arrived  int
	want     int
	together chan struct{}
}

func gateOf(stacks *fakeCFN, want int) *gatedPlans {
	return &gatedPlans{fakeCFN: stacks, want: want, together: make(chan struct{})}
}

func (g *gatedPlans) CreateChangeSet(ctx context.Context, in *cloudformation.CreateChangeSetInput, opts ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	g.mu.Lock()
	g.inFlight++
	g.arrived++
	g.peak = max(g.peak, g.inFlight)
	if g.arrived == g.want {
		close(g.together)
	}
	g.mu.Unlock()

	select {
	case <-g.together:
	case <-time.After(2 * time.Second):
	}
	time.Sleep(50 * time.Millisecond)

	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
	return g.fakeCFN.CreateChangeSet(ctx, in, opts...)
}

func TestPlanReadsEveryGroupAtOnceAndHandsThemBackInOrder(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	ctx := context.Background()
	read, err := Read(ctx, stacks, defaultNamespace, ClassProduction)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var groups []provider.ChangeGroup
	for _, feature := range append([]string{""}, featureNames()...) {
		stack := coreStackName
		if feature != "" {
			stack = defaultNamespace.FeatureStackName(feature, ClassProduction)
		}
		stacks.fallBehind(stack)
		groups = append(groups, provider.ChangeGroup{
			Kind:    provider.StackGroupKind,
			Name:    stack,
			Feature: feature,
			Action:  provider.ActionUpdate,
		})
	}
	gate := gateOf(stacks, min(len(groups), planFanOut))

	plan, err := PlanChanges(ctx, gate, read, everything(), groups)
	if err != nil {
		t.Fatalf("PlanChanges: %v", err)
	}
	if len(plan) != len(groups)+1 {
		t.Fatalf("the plan has %d groups, want %d: every stack asked for, plus the runtime", len(plan), len(groups)+1)
	}
	if last := plan[len(plan)-1]; last.Name != runtimeStack(ClassProduction) {
		t.Errorf("the plan's last group is %s, want the runtime stack %s", last.Name, runtimeStack(ClassProduction))
	}
	for i, group := range plan[:len(groups)] {
		if group.Name != groups[i].Name {
			t.Errorf("group %d is %s, want %s: a plan reads back in the order it was asked for", i, group.Name, groups[i].Name)
		}
	}
	if gate.peak < 2 {
		t.Errorf("at most %d change set(s) were open at once; the groups were planned one after another", gate.peak)
	}
	if gate.peak > planFanOut {
		t.Errorf("%d change sets were open at once, over the %d CloudFormation will take", gate.peak, planFanOut)
	}
}

type unplannable struct{ *fakeCFN }

func (unplannable) CreateChangeSet(context.Context, *cloudformation.CreateChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	return nil, errors.New("this account may not plan change sets")
}

func TestPlanSaysSoWhenItCannotDiffAnUpdate(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := isrStack(ClassProduction)
	stacks.fallBehind(stack)

	group := groupNamed(t, planned(t, unplannable{stacks}, ClassProduction, everything()), stack)
	if group.Action != provider.ActionUpdate {
		t.Fatalf("%s is %q, want it still updated where the diff could not be read", stack, group.Action)
	}
	if len(group.Changes) != 0 {
		t.Errorf("%s has %v though nothing could be read off CloudFormation", stack, group.Changes)
	}
	if !strings.Contains(group.Reason, provider.DetailUnavailable) {
		t.Errorf("%s reads %q, want it to own up to the detail it could not read", stack, group.Reason)
	}
}

func TestPlanListsWhatADroppedFeatureTakesWithIt(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := isrStack(ClassProduction)

	groups := planned(t, stacks, ClassProduction, Request{
		Features: []string{FeatureImageOptimization, FeatureCloudflareEdge},
		Remove:   []string{FeatureISR},
	})
	group := groupNamed(t, groups, stack)
	if group.Action != provider.ActionDelete || group.Feature != FeatureISR {
		t.Fatalf("%s = %+v, want the dropped feature deleted", stack, group)
	}
	queue := changeNamed(t, group, "RevalidateQueue")
	if queue.Action != provider.ActionDelete || queue.Kind == "" {
		t.Errorf("RevalidateQueue = %+v, want the queue the stack contains deleted", queue)
	}
}

const leftoverTemplate = "AWSTemplateFormatVersion: '2010-09-09'\nResources:\n  LeftoverQueue:\n    Type: AWS::SQS::Queue\n"

func (f *fakeCFN) setTemplate(stackName, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.templates[stackName] = body
}

func TestPlanListsTheResourcesTheProvisionedStackContainsNotTheOnesThisBuildWouldRender(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := isrStack(ClassProduction)
	stacks.setTemplate(stack, leftoverTemplate)

	group := groupNamed(t, planned(t, stacks, ClassProduction, Request{
		Features: []string{FeatureImageOptimization, FeatureCloudflareEdge},
		Remove:   []string{FeatureISR},
	}), stack)
	leftover := changeNamed(t, group, "LeftoverQueue")
	if leftover.Kind != "AWS::SQS::Queue" || leftover.Action != provider.ActionDelete {
		t.Errorf("LeftoverQueue = %+v, want what the account contains, deleted", leftover)
	}
	for _, change := range group.Changes {
		if change.Name == "RevalidateQueue" {
			t.Error("the plan lists RevalidateQueue, which this build's template declares and the provisioned stack does not contain")
		}
	}
}

type unlistable struct{ *fakeCFN }

func (unlistable) ListStackResources(context.Context, *cloudformation.ListStackResourcesInput, ...func(*cloudformation.Options)) (*cloudformation.ListStackResourcesOutput, error) {
	return nil, errors.New("this account may not list stack resources")
}

func TestPlanFallsBackToTheTemplateWhenItCannotReadTheProvisionedStack(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := isrStack(ClassProduction)

	group := groupNamed(t, planned(t, unlistable{stacks}, ClassProduction, Request{
		Features: []string{FeatureImageOptimization, FeatureCloudflareEdge},
		Remove:   []string{FeatureISR},
	}), stack)
	if queue := changeNamed(t, group, "RevalidateQueue"); queue.Action != provider.ActionDelete {
		t.Errorf("RevalidateQueue = %+v, want the template's best guess at what goes", queue)
	}
	if !strings.Contains(group.Reason, provider.DetailUnavailable) {
		t.Errorf("%s reads %q, want it to own up to reading the listing off a template rather than the account", stack, group.Reason)
	}
}

func TestRemovalTakesNoKeyFromAnAccountThatBroughtItsOwn(t *testing.T) {
	ctx := context.Background()
	stacks := newFakeCFN()
	frontedBy(t, &fakeEdge{kind: "cloudflare"})
	apis := apisOf(stacks, newFakeSSM(), &fakeIAM{}, preloadedStore())
	req := Request{Features: []string{FeatureVarsKey}, VarsKey: broughtKeyARN}
	if err := Run(ctx, apis, defaultNamespace, ClassProduction, req, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	read, err := Read(ctx, stacks, defaultNamespace, ClassProduction)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	groups, err := PlanRemove(ctx, unlistable{stacks}, read)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	group := groupNamed(t, groups, defaultNamespace.FeatureStackName(FeatureVarsKey, ClassProduction))
	for _, change := range group.Changes {
		if change.Name == "VarsKey" || change.Name == "VarsKeyAlias" {
			t.Errorf("the removal plan takes %s from a stack that owns no key, and a destroy must not claim to take a key or an alias this account brought", change.Name)
		}
	}
}
