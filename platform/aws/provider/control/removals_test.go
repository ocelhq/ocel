package control

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

const (
	cloudflareKind edge.Kind = "cloudflare"
	cloudfrontKind edge.Kind = "cloudfront"
)

func edgeParam(t *testing.T, tier environment.Tier, leaf string) string {
	t.Helper()
	prefix, err := defaultNamespace.EdgeParamPrefix(tier, cloudflareKind)
	if err != nil {
		t.Fatalf("EdgeParamPrefix(%s): %v", tier, err)
	}
	return prefix + leaf
}

func summary(id, kind string) cfntypes.StackResourceSummary {
	return cfntypes.StackResourceSummary{LogicalResourceId: aws.String(id), ResourceType: aws.String(kind)}
}

func removingBootstrapper(t *testing.T, tier environment.Tier) Bootstrap {
	t.Helper()

	b := installedBootstrapper(t, tier)
	stackName, err := defaultNamespace.StackNameFor(tier)
	if err != nil {
		t.Fatalf("StackNameFor(%s): %v", tier, err)
	}
	edgeStack := defaultNamespace.PartStackName(bootstrap.CloudflareEdgePart, tier)
	variablesKeyStack := defaultNamespace.PartStackName(provider.VariablesKeyPart, tier)
	cfn := b.CFN.(*teardownCFN)
	cfn.present[edgeStack] = bootstrap.Deployed{Present: true}
	cfn.present[variablesKeyStack] = bootstrap.Deployed{Present: true}
	cfn.resources = map[string][]cfntypes.StackResourceSummary{
		stackName: {
			summary("StateBucket", "AWS::S3::Bucket"),
			summary("StateTable", "AWS::DynamoDB::Table"),
			summary("AssetBucket", "AWS::S3::Bucket"),
		},
		edgeStack:         {summary("EdgeUser", "AWS::IAM::User")},
		variablesKeyStack: {summary("VariablesKey", "AWS::KMS::Key")},
	}
	b.Edge = &planningEdge{removals: []edge.PlanChange{
		{Kind: "Cloudflare::Worker", Name: "ocel-releases-store", Action: edge.PlanDelete},
		{Kind: "Cloudflare::WorkerSecret", Name: "ocel-releases-store/BOOTSTRAP_SECRET", Action: edge.PlanDelete, Slow: true},
	}}
	b.Edges = registryOf(b.Edge)
	b.Kinds = kindsOf(b.Edge)
	return b
}

func groupNamed(plan provider.Plan, name string) *provider.ChangeGroup {
	for i, group := range plan.Groups {
		if group.Name == name {
			return &plan.Groups[i]
		}
	}
	return nil
}

func changeNamed(group *provider.ChangeGroup, name string) *provider.Change {
	if group == nil {
		return nil
	}
	for i, change := range group.Changes {
		if change.Name == name {
			return &group.Changes[i]
		}
	}
	return nil
}

func groupNames(plan provider.Plan) string {
	names := make([]string, 0, len(plan.Groups))
	for _, group := range plan.Groups {
		names = append(names, group.Name)
	}
	return strings.Join(names, ", ")
}

func TestPlanRemovalReadsAsTheApplyPlanDoes(t *testing.T) {
	t.Parallel()

	b := removingBootstrapper(t, environment.TierProduction)

	plan, err := b.PlanRemove(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}

	planned := groupNamed(plan, "aws/"+defaultNamespace.PartStackName(bootstrap.CloudflareEdgePart, environment.TierProduction))
	core := groupNamed(plan, "aws/"+coreStackName)
	if planned == nil || core == nil {
		t.Fatalf("plan groups = %s, want the edge part stack and the core it depends on", groupNames(plan))
	}
	if planned.Part != bootstrap.CloudflareEdgePart {
		t.Errorf("the part group's part = %q, want it tagged with the part it provisions", planned.Part)
	}
	if plan.Groups[0].Kind != provider.StackGroupKind || plan.Groups[0].Name == core.Name {
		t.Errorf("the plan opens with %q, want the part stacks ahead of the core they depend on", plan.Groups[0].Name)
	}
	for _, group := range []*provider.ChangeGroup{planned, core} {
		if group.Kind != provider.StackGroupKind || group.Action != provider.ActionDelete {
			t.Errorf("group %+v, want a stack planned for deletion", group)
		}
		for _, change := range group.Changes {
			if change.Action != provider.ActionDelete || !strings.HasPrefix(change.Kind, "AWS::") {
				t.Errorf("row %+v, want a typed resource planned for deletion", change)
			}
		}
	}
	if got := changeNamed(core, "StateTable"); got == nil || got.Kind != "AWS::DynamoDB::Table" {
		t.Errorf("the core group's rows = %+v, want the resources the stack reports", core.Changes)
	}
	bucket := changeNamed(core, "StateBucket")
	if bucket == nil || bucket.Reason == "" || !bucket.Slow {
		t.Errorf("the state bucket row = %+v, want the note that its contents are stranded, and the pace of emptying it", bucket)
	}
	if row := changeNamed(core, "StateTable"); row != nil && row.Reason != "" {
		t.Errorf("the state table row reads %q; a row says nothing unless it records a decision", row.Reason)
	}

	params := groupNamed(plan, "aws/"+bootstrap.ParamGroupName)
	if params == nil || params.Action != provider.ActionDelete {
		t.Fatalf("plan groups = %s, want the parameters this bootstrap stores", groupNames(plan))
	}
	credentials := changeNamed(params, edgeParam(t, environment.TierProduction, "/credentials"))
	if credentials == nil || credentials.Kind != "AWS::SSM::Parameter" {
		t.Errorf("the parameters group's rows = %+v, want the stored handles typed", params.Changes)
	}
	key := changeNamed(params, edgeUserName+"/AKIAOLD")
	if key == nil || key.Kind != "AWS::IAM::AccessKey" {
		t.Errorf("the parameters group's rows = %+v, want the access key the edge signs with", params.Changes)
	}
	passphrase := changeNamed(params, passphraseParam)
	if passphrase == nil || passphrase.Action != provider.ActionDelete || passphrase.Reason == "" {
		t.Errorf("the passphrase row = %+v, want it deleted with its tier, with the reason the typed confirmation names", passphrase)
	}

	front := groupNamed(plan, string(cloudflareKind)+"/edge")
	if front == nil || front.Kind != edge.EdgeGroupKind || front.Action != provider.ActionDelete {
		t.Fatalf("plan groups = %s, want the edge under its own vendor", groupNames(plan))
	}
	if front.Part != bootstrap.CloudflareEdgePart {
		t.Errorf("the edge group's part = %q, want the one it fronts through", front.Part)
	}
	if len(front.Changes) != 2 {
		t.Fatalf("the edge group has %+v, want a row per thing the edge provisioned", front.Changes)
	}
	if cache := changeNamed(front, "ocel-releases-store/BOOTSTRAP_SECRET"); cache == nil || !cache.Slow {
		t.Errorf("the slow row = %+v, want its pace kept", cache)
	}
}

func TestPlanRemovalOfOneTierDeletesItsOwnPassphraseAndReadsNoStackOfTheOther(t *testing.T) {
	t.Parallel()

	b := removingBootstrapper(t, environment.TierPreview)
	stacks := b.CFN.(*teardownCFN)
	stacks.present[coreStackName] = bootstrap.Deployed{Present: true}

	plan, err := b.PlanRemove(context.Background(), environment.TierPreview)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	params := groupNamed(plan, "aws/"+bootstrap.ParamGroupName)
	own := changeNamed(params, defaultNamespace.PassphraseParamFor(environment.TierPreview))
	if own == nil || own.Action != provider.ActionDelete {
		t.Errorf("the preview passphrase row = %+v, want it deleted with the preview bootstrap", own)
	}
	if other := changeNamed(params, passphraseParam); other != nil {
		t.Errorf("the plan has %+v, a row for the production tier's passphrase", other)
	}
	if slices.Contains(stacks.described, coreStackName) {
		t.Errorf("PlanRemove(preview) described %v, which includes %s: the preview bootstrap credential cannot describe the production tier's stacks", stacks.described, coreStackName)
	}
}

func TestPlanRemovalOfAnAbsentBootstrapStillPlansWhatItLeftBehind(t *testing.T) {
	t.Parallel()

	b := removingBootstrapper(t, environment.TierProduction)
	delete(b.CFN.(*teardownCFN).present, coreStackName)

	plan, err := b.PlanRemove(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	if groupNamed(plan, "aws/"+coreStackName) != nil {
		t.Errorf("plan groups = %s, want no stack planned where none exists", groupNames(plan))
	}
	params := groupNamed(plan, "aws/"+bootstrap.ParamGroupName)
	if changeNamed(params, edgeParam(t, environment.TierProduction, "/credentials")) == nil {
		t.Errorf("plan groups = %s, want the parameters the bootstrap left behind still planned", groupNames(plan))
	}
}

func TestPlanRemovalLeavesOutAnEdgeThatSaysNothingAboutItsOwnRemoval(t *testing.T) {
	t.Parallel()

	b := removingBootstrapper(t, environment.TierProduction)
	b.Edge = &teardownEdge{}

	plan, err := b.PlanRemove(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	for _, group := range plan.Groups {
		if group.Kind == edge.EdgeGroupKind {
			t.Errorf("plan includes %+v for an edge that says nothing about its own removal", group)
		}
	}
}

func frontedBootstrapper(t *testing.T, tier environment.Tier) (Bootstrap, *planningEdge, *planningEdge) {
	t.Helper()

	b := removingBootstrapper(t, tier)
	installed := b.Edge.(*planningEdge)
	selected := &planningEdge{
		teardownEdge: teardownEdge{kind: cloudfrontKind},
		removals: []edge.PlanChange{
			{Kind: "AWS::CloudFront::KeyValueStore", Name: "ocel-routes", Action: edge.PlanDelete},
		},
	}
	b.Edge = selected
	b.Edges = registryOf(selected, installed)
	b.Kinds = kindsOf(selected, installed)
	return b, selected, installed
}

func TestPlanRemovalNamesEveryInstalledEdgeByItsOwnKind(t *testing.T) {
	t.Parallel()

	b, _, _ := frontedBootstrapper(t, environment.TierProduction)

	plan, err := b.PlanRemove(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}

	front := groupNamed(plan, string(cloudfrontKind)+"/edge")
	if front == nil || front.Part != "" {
		t.Fatalf("plan groups = %s, want the selected edge under its own kind, with no part of its own", groupNames(plan))
	}
	if changeNamed(front, "ocel-routes") == nil {
		t.Errorf("the %s group has %+v, want the rows that edge planned", front.Name, front.Changes)
	}

	other := groupNamed(plan, string(cloudflareKind)+"/edge")
	if other == nil || other.Part != bootstrap.CloudflareEdgePart {
		t.Fatalf("plan groups = %s, want the installed edge too, under its own kind and part", groupNames(plan))
	}
	if changeNamed(other, "ocel-releases-store/BOOTSTRAP_SECRET") == nil {
		t.Errorf("the %s group has %+v, want the rows that edge planned", other.Name, other.Changes)
	}
	for _, group := range []*provider.ChangeGroup{front, other} {
		for _, change := range group.Changes {
			if strings.HasPrefix(change.Kind, "Cloudflare::") != (group.Name == string(cloudflareKind)+"/edge") {
				t.Errorf("group %q has %+v, want only the rows of the edge it names", group.Name, change)
			}
		}
	}
}

func TestPlanRemovalLeavesOutAnEdgeThisAccountStoresNothingFor(t *testing.T) {
	t.Parallel()

	b, selected, installed := frontedBootstrapper(t, environment.TierProduction)
	unused := &planningEdge{
		teardownEdge: teardownEdge{kind: "relay"},
		removals:     []edge.PlanChange{{Kind: "Relay::Worker", Name: "ocel-relay", Action: edge.PlanDelete}},
	}
	b.Edges = registryOf(selected, installed, unused)
	b.Kinds = kindsOf(selected, installed, unused)

	plan, err := b.PlanRemove(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	if group := groupNamed(plan, "relay/edge"); group != nil {
		t.Errorf("plan includes %+v for an edge this account stores no parameters for", group)
	}
}

func TestPlanRemovalStillSeesAnEdgeWhoseParametersAreAlreadyGone(t *testing.T) {
	t.Parallel()

	b, _, _ := frontedBootstrapper(t, environment.TierProduction)
	severed(t, b, environment.TierProduction)

	plan, err := b.PlanRemove(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	if groupNamed(plan, string(cloudflareKind)+"/edge") == nil {
		t.Errorf("plan groups = %s, want the edge whose part stack is still provisioned: its externals are live and nothing else would take them", groupNames(plan))
	}
}

func TestRemoveTearsDownAnEdgeWhoseParametersAreAlreadyGone(t *testing.T) {
	t.Parallel()

	b, _, installed := frontedBootstrapper(t, environment.TierProduction)
	severed(t, b, environment.TierProduction)

	if err := b.Remove(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(installed.torndown) == 0 {
		t.Error("the cloudflare edge was left installed, so a run that severed its parameters and stopped orphans them for good")
	}
}

func severed(t *testing.T, b Bootstrap, tier environment.Tier) {
	t.Helper()

	b.CFN.(*teardownCFN).present[defaultNamespace.PartStackName(bootstrap.CloudflareEdgePart, tier)] =
		bootstrap.Deployed{Present: true}
	prefix, err := defaultNamespace.EdgeParamPrefix(tier, cloudflareKind)
	if err != nil {
		t.Fatalf("EdgeParamPrefix(%s): %v", tier, err)
	}
	params := b.SSM.(*teardownSSM).params
	for name := range params {
		if strings.HasPrefix(name, prefix+"/") {
			delete(params, name)
		}
	}
}

func TestRemoveTearsDownEveryEdgeThePlanShowed(t *testing.T) {
	t.Parallel()

	b, selected, installed := frontedBootstrapper(t, environment.TierProduction)

	if err := b.Remove(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, front := range []*planningEdge{selected, installed} {
		if !slices.Equal(front.torndown, []environment.Tier{environment.TierProduction}) {
			t.Errorf("the %s edge saw teardowns %v, want the tier the plan showed it under", front.Kind(), front.torndown)
		}
	}
}

func TestRemoveLeavesAloneAnEdgeThisAccountStoresNothingFor(t *testing.T) {
	t.Parallel()

	b, selected, installed := frontedBootstrapper(t, environment.TierProduction)
	unused := &planningEdge{teardownEdge: teardownEdge{kind: "relay"}}
	b.Edges = registryOf(selected, installed, unused)
	b.Kinds = kindsOf(selected, installed, unused)

	if err := b.Remove(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(unused.torndown) != 0 {
		t.Errorf("the relay edge saw teardowns %v, want none: this account stores nothing for it", unused.torndown)
	}
}

func TestPlanRemovalSaysWhatDroppingTheVariablesKeyStrands(t *testing.T) {
	t.Parallel()

	b := removingBootstrapper(t, environment.TierProduction)

	plan, err := b.PlanRemove(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}

	group := groupNamed(plan, "aws/"+defaultNamespace.PartStackName(provider.VariablesKeyPart, environment.TierProduction))
	if group == nil {
		t.Fatalf("plan groups = %s, want the stack the variables key is provisioned in", groupNames(plan))
	}
	key := changeNamed(group, "VariablesKey")
	if key == nil || key.Reason == "" {
		t.Errorf("the variables key row = %+v, want the note that every value sealed under it is stranded", key)
	}
}

func TestRemoveTakesDownThePartsDeploysAddedBeforeTheCore(t *testing.T) {
	t.Parallel()

	b := installedBootstrapper(t, environment.TierPreview)
	var removed []environment.Tier
	b.RemoveParts = func(_ context.Context, tier environment.Tier) error {
		removed = append(removed, tier)
		return nil
	}

	if err := b.Remove(context.Background(), environment.TierPreview, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if !slices.Equal(removed, []environment.Tier{environment.TierPreview}) {
		t.Errorf("parts removed for %v, want the preview tier's once", removed)
	}
}

func TestPlanRemovalListsThePartsDeploysAddedAheadOfTheCore(t *testing.T) {
	t.Parallel()

	b := removingBootstrapper(t, environment.TierProduction)
	part := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: "bootstrap part image-optimizer", Action: provider.ActionDelete}
	b.PlanPartRemoval = func(context.Context, environment.Tier) ([]provider.ChangeGroup, error) {
		return []provider.ChangeGroup{part}, nil
	}

	plan, err := b.PlanRemove(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}

	if len(plan.Groups) == 0 || plan.Groups[0].Name != "aws/"+part.Name {
		t.Errorf("plan groups = %s, want the part first, named under the vendor that hosts it", groupNames(plan))
	}
}
