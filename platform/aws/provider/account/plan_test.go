package account

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

func planned(t *testing.T, stacks cfn.API, tier environment.Tier, req Request) []provider.ChangeGroup {
	t.Helper()

	ctx := context.Background()
	read, err := Read(ctx, stacks, defaultNamespace, tier)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	deployed := read.Deployed
	described := provider.BootstrapDescription{Tier: tier, Present: deployed.Present}
	for _, stack := range deployed.Stacks {
		described.Stacks = append(described.Stacks, provider.BootstrapStack{
			Name:          stack.Name,
			Part:          stack.Part,
			Present:       stack.Present,
			DigestCurrent: stack.Current(),
		})
	}
	groups, err := PlanChanges(ctx, stacks, newFakeLayers(), read, req, bootstrapplan.ChangeGroups(
		WithDefaultStackNames(defaultNamespace, described), Parts(),
		provider.BootstrapRequest{Tier: tier, Parts: req.Parts, Remove: req.Remove}))
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
	f.tags[stackName] = stampTags(defaultNamespace, environment.TierProduction, Stamp{Digest: "beef", WrittenBy: "1.0.0"})
}

func TestPlanOnAFreshAccountReadsEveryResourceOffTheTemplates(t *testing.T) {
	groups := planned(t, newFakeCFN(), environment.TierProduction, everything())

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
	stack := keyStack(environment.TierProduction)
	stacks.fallBehind(stack)
	stacks.plan(stack,
		change(cfntypes.ChangeActionModify, "VariablesKey", "AWS::KMS::Key", cfntypes.ReplacementTrue),
		change(cfntypes.ChangeActionAdd, "VariablesKeyAlias", "AWS::KMS::Alias", cfntypes.ReplacementFalse))
	before := stacks.updates

	group := groupNamed(t, planned(t, stacks, environment.TierProduction, everything()), stack)
	if group.Action != provider.ActionUpdate {
		t.Fatalf("%s is %q, want it updated where its content is behind", stack, group.Action)
	}
	key := changeNamed(t, group, "VariablesKey")
	if key.Action != provider.ActionReplace {
		t.Errorf("VariablesKey = %q, want a replacement where CloudFormation cannot change it in place", key.Action)
	}
	if key.Reason == "" {
		t.Error("the replacement says nothing about why the key cannot be changed in place")
	}
	if added := changeNamed(t, group, "VariablesKeyAlias"); added.Action != provider.ActionCreate {
		t.Errorf("VariablesKeyAlias = %q, want it created", added.Action)
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
	stack := keyStack(environment.TierProduction)
	stacks.fallBehind(stack)
	replacing := change(cfntypes.ChangeActionModify, "VariablesKey", "AWS::KMS::Key", cfntypes.ReplacementTrue)
	replacing.Details = []cfntypes.ResourceChangeDetail{{
		Target: &cfntypes.ResourceTargetDefinition{
			Attribute:          cfntypes.ResourceAttributeProperties,
			Name:               aws.String("KeySpec"),
			RequiresRecreation: cfntypes.RequiresRecreationAlways,
		},
	}}
	stacks.plan(stack, replacing)

	group := groupNamed(t, planned(t, stacks, environment.TierProduction, everything()), stack)
	if reason := changeNamed(t, group, "VariablesKey").Reason; !strings.Contains(reason, "KeySpec") {
		t.Errorf("the replacement reads %q, want it to name the property that forces it", reason)
	}
}

func TestPlanStillUpdatesAStackWhoseStampAloneIsBehind(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := keyStack(environment.TierProduction)
	stacks.misstamp(stack)

	groups := planned(t, stacks, environment.TierProduction, everything())
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
	read, err := Read(ctx, stacks, defaultNamespace, environment.TierProduction)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var groups []provider.ChangeGroup
	for _, part := range append([]string{""}, partNames()...) {
		stack := coreStackName
		if part != "" {
			stack = defaultNamespace.PartStackName(part, environment.TierProduction)
		}
		stacks.fallBehind(stack)
		groups = append(groups, provider.ChangeGroup{
			Kind:   provider.StackGroupKind,
			Name:   stack,
			Part:   part,
			Action: provider.ActionUpdate,
		})
	}
	gate := gateOf(stacks, min(len(groups), planFanOut))

	plan, err := PlanChanges(ctx, gate, newFakeLayers(), read, everything(), groups)
	if err != nil {
		t.Fatalf("PlanChanges: %v", err)
	}
	if len(plan) != len(groups)+1 {
		t.Fatalf("the plan has %d groups, want %d: every stack asked for, plus the runtime", len(plan), len(groups)+1)
	}
	if last := plan[len(plan)-1]; last.Kind != runtimeLayerGroupKind {
		t.Errorf("the plan's last group is %s %s, want the runtime layers", last.Kind, last.Name)
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
	stack := keyStack(environment.TierProduction)
	stacks.fallBehind(stack)

	group := groupNamed(t, planned(t, unplannable{stacks}, environment.TierProduction, everything()), stack)
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

func TestPlanListsWhatADroppedPartTakesWithIt(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := edgeStack(environment.TierProduction)

	groups := planned(t, stacks, environment.TierProduction, Request{
		Parts:  []string{provider.VariablesKeyPart},
		Remove: []string{CloudflareEdgePart},
	})
	group := groupNamed(t, groups, stack)
	if group.Action != provider.ActionDelete || group.Part != CloudflareEdgePart {
		t.Fatalf("%s = %+v, want the dropped part deleted", stack, group)
	}
	user := changeNamed(t, group, "EdgeUser")
	if user.Action != provider.ActionDelete || user.Kind == "" {
		t.Errorf("EdgeUser = %+v, want the user the stack contains deleted", user)
	}
}

const leftoverTemplate = `{"AWSTemplateFormatVersion": "2010-09-09", "Resources": {"LeftoverQueue": {"Type": "AWS::SQS::Queue"}}}`

func (f *fakeCFN) setTemplate(stackName, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.templates[stackName] = body
}

func TestPlanListsTheResourcesTheProvisionedStackContainsNotTheOnesThisBuildWouldRender(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := edgeStack(environment.TierProduction)
	stacks.setTemplate(stack, leftoverTemplate)

	group := groupNamed(t, planned(t, stacks, environment.TierProduction, Request{
		Parts:  []string{provider.VariablesKeyPart},
		Remove: []string{CloudflareEdgePart},
	}), stack)
	leftover := changeNamed(t, group, "LeftoverQueue")
	if leftover.Kind != "AWS::SQS::Queue" || leftover.Action != provider.ActionDelete {
		t.Errorf("LeftoverQueue = %+v, want what the account contains, deleted", leftover)
	}
	for _, change := range group.Changes {
		if change.Name == "EdgeUser" {
			t.Error("the plan lists EdgeUser, which this build's template declares and the provisioned stack does not contain")
		}
	}
}

type unlistable struct{ *fakeCFN }

func (unlistable) ListStackResources(context.Context, *cloudformation.ListStackResourcesInput, ...func(*cloudformation.Options)) (*cloudformation.ListStackResourcesOutput, error) {
	return nil, errors.New("this account may not list stack resources")
}

func TestPlanFallsBackToTheTemplateWhenItCannotReadTheProvisionedStack(t *testing.T) {
	stacks, _ := installedBootstrap(t)
	stack := edgeStack(environment.TierProduction)

	group := groupNamed(t, planned(t, unlistable{stacks}, environment.TierProduction, Request{
		Parts:  []string{provider.VariablesKeyPart},
		Remove: []string{CloudflareEdgePart},
	}), stack)
	if user := changeNamed(t, group, "EdgeUser"); user.Action != provider.ActionDelete {
		t.Errorf("EdgeUser = %+v, want the template's best guess at what goes", user)
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
	req := Request{Parts: []string{provider.VariablesKeyPart}, VariablesKey: broughtKeyARN}
	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, req, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	read, err := Read(ctx, stacks, defaultNamespace, environment.TierProduction)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	groups, err := PlanRemove(ctx, unlistable{stacks}, newFakeLayers(), newFakeRecords().in, read)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	group := groupNamed(t, groups, defaultNamespace.PartStackName(provider.VariablesKeyPart, environment.TierProduction))
	for _, change := range group.Changes {
		if change.Name == "VariablesKey" || change.Name == "VariablesKeyAlias" {
			t.Errorf("the removal plan takes %s from a stack that owns no key, and a destroy must not claim to take a key or an alias this account brought", change.Name)
		}
	}
}

func TestRemovalPlansEveryStackLeftBesideACoreThatRolledBack(t *testing.T) {
	ctx := context.Background()
	stacks := newFakeCFN()
	core := defaultNamespace.CoreStackName()
	part := defaultNamespace.PartStackName(provider.VariablesKeyPart, environment.TierProduction)
	for _, name := range []string{core, part} {
		stacks.templates[name] = "Resources: {}"
	}
	stacks.statuses[core] = cfntypes.StackStatusRollbackComplete
	stacks.statuses[part] = cfntypes.StackStatusCreateComplete

	read, err := Read(ctx, stacks, defaultNamespace, environment.TierProduction)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	groups, err := PlanRemove(ctx, stacks, newFakeLayers(), newFakeRecords().in, read)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	var names []string
	for _, group := range groups {
		if group.Action != provider.ActionDelete {
			t.Errorf("%s is planned as %q, want it deleted", group.Name, group.Action)
		}
		names = append(names, group.Name)
	}
	if got := groupNamed(t, groups, part).Part; got != provider.VariablesKeyPart {
		t.Errorf("%s is labelled part %q, want %q", part, got, provider.VariablesKeyPart)
	}
	if want := []string{part, core}; !slices.Equal(names, want) {
		t.Errorf("PlanRemove() plans %v, want %v: a destroy that plans nothing never runs the teardown that removes them", names, want)
	}
}

func TestRemovalPlansNothingWhereNoStackStands(t *testing.T) {
	ctx := context.Background()

	read, err := Read(ctx, newFakeCFN(), defaultNamespace, environment.TierProduction)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	groups, err := PlanRemove(ctx, newFakeCFN(), newFakeLayers(), newFakeRecords().in, read)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("PlanRemove() plans %v on an account with no bootstrap stack, want nothing", groups)
	}
}
