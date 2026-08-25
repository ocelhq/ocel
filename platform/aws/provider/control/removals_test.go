package control

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	cloudflareKind edge.Kind = "cloudflare"
	cloudfrontKind edge.Kind = "cloudfront"
)

func edgeParam(t *testing.T, class, leaf string) string {
	t.Helper()
	prefix, err := bootstrap.EdgeParamPrefix(class, cloudflareKind)
	if err != nil {
		t.Fatalf("EdgeParamPrefix(%s): %v", class, err)
	}
	return prefix + leaf
}

func summary(id, kind string) cfntypes.StackResourceSummary {
	return cfntypes.StackResourceSummary{LogicalResourceId: aws.String(id), ResourceType: aws.String(kind)}
}

func removingBootstrapper(t *testing.T, class string) Bootstrapper {
	t.Helper()

	b := standingBootstrapper(t, class)
	stackName, err := bootstrap.StackNameFor(class)
	if err != nil {
		t.Fatalf("StackNameFor(%s): %v", class, err)
	}
	isrStack := bootstrap.FeatureStackName(bootstrap.FeatureISR, class)
	cfn := b.CFN.(*teardownCFN)
	cfn.present[isrStack] = bootstrap.Deployed{Present: true}
	cfn.resources = map[string][]cfntypes.StackResourceSummary{
		stackName: {
			summary("StateBucket", "AWS::S3::Bucket"),
			summary("StateTable", "AWS::DynamoDB::Table"),
			summary("AssetBucket", "AWS::S3::Bucket"),
		},
		isrStack: {summary("RevalidationQueue", "AWS::SQS::Queue")},
	}
	b.Edge = &planningEdge{removals: []edge.PlanChange{
		{Kind: "Cloudflare::Worker", Name: "ocel-deployments-store", Action: edge.PlanDelete},
		{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanDelete, Slow: true},
	}}
	b.Edges = registryOf(b.Edge)
	return b
}

func groupNamed(plan providerkit.BootstrapPlan, name string) *providerkit.ChangeGroup {
	for i, group := range plan.Groups {
		if group.Name == name {
			return &plan.Groups[i]
		}
	}
	return nil
}

func changeNamed(group *providerkit.ChangeGroup, name string) *providerkit.Change {
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

func groupNames(plan providerkit.BootstrapPlan) string {
	names := make([]string, 0, len(plan.Groups))
	for _, group := range plan.Groups {
		names = append(names, group.Name)
	}
	return strings.Join(names, ", ")
}

func TestPlanRemovalReadsAsTheApplyPlanDoes(t *testing.T) {
	t.Parallel()

	b := removingBootstrapper(t, bootstrap.ClassProduction)

	plan, err := b.PlanRemoval(context.Background(), providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("PlanRemoval: %v", err)
	}

	isr := groupNamed(plan, "aws/"+bootstrap.FeatureStackName(bootstrap.FeatureISR, bootstrap.ClassProduction))
	core := groupNamed(plan, "aws/"+bootstrap.StackName)
	if isr == nil || core == nil {
		t.Fatalf("plan groups = %s, want the isr stack and the core it stands on", groupNames(plan))
	}
	if isr.Feature != bootstrap.FeatureISR {
		t.Errorf("the isr group's feature = %q, want it tagged with the feature it carries", isr.Feature)
	}
	if plan.Groups[0].Name != isr.Name {
		t.Errorf("the plan opens with %q, want the feature stacks ahead of the core they depend on", plan.Groups[0].Name)
	}
	for _, group := range []*providerkit.ChangeGroup{isr, core} {
		if group.Kind != providerkit.StackGroupKind || group.Action != providerkit.ActionDelete {
			t.Errorf("group %+v, want a stack planned for deletion", group)
		}
		for _, change := range group.Changes {
			if change.Action != providerkit.ActionDelete || !strings.HasPrefix(change.Kind, "AWS::") {
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
		t.Errorf("the state table row carries %q; a row says nothing unless it carries a decision", row.Reason)
	}

	params := groupNamed(plan, "aws/"+bootstrap.ParamGroupName)
	if params == nil || params.Action != providerkit.ActionDelete {
		t.Fatalf("plan groups = %s, want the parameters this bootstrap holds", groupNames(plan))
	}
	credentials := changeNamed(params, edgeParam(t, bootstrap.ClassProduction, "/credentials"))
	if credentials == nil || credentials.Kind != "AWS::SSM::Parameter" {
		t.Errorf("the parameters group's rows = %+v, want the stored handles typed", params.Changes)
	}
	key := changeNamed(params, bootstrap.EdgeUserName+"/AKIAOLD")
	if key == nil || key.Kind != "AWS::IAM::AccessKey" {
		t.Errorf("the parameters group's rows = %+v, want the access key the edge signs with", params.Changes)
	}
	passphrase := changeNamed(params, bootstrap.PassphraseParamName)
	if passphrase == nil || passphrase.Action != providerkit.ActionDelete {
		t.Errorf("the passphrase row = %+v, want it deleted when no sibling bootstrap holds it", passphrase)
	}

	front := groupNamed(plan, string(cloudflareKind)+"/edge")
	if front == nil || front.Kind != providerkit.EdgeGroupKind || front.Action != providerkit.ActionDelete {
		t.Fatalf("plan groups = %s, want the edge under its own vendor", groupNames(plan))
	}
	if front.Feature != bootstrap.FeatureCloudflareEdge {
		t.Errorf("the edge group's feature = %q, want the one it fronts through", front.Feature)
	}
	if len(front.Changes) != 2 {
		t.Fatalf("the edge group carries %+v, want a row per thing the edge stood up", front.Changes)
	}
	if cache := changeNamed(front, "ocel-edge-cache"); cache == nil || !cache.Slow {
		t.Errorf("the R2 bucket row = %+v, want the pace of emptying it kept", cache)
	}
}

func TestPlanRemovalKeepsThePassphraseABootstrappedSiblingHolds(t *testing.T) {
	t.Parallel()

	b := removingBootstrapper(t, bootstrap.ClassPreview)
	b.CFN.(*teardownCFN).present[bootstrap.StackName] = bootstrap.Deployed{Present: true}

	plan, err := b.PlanRemoval(context.Background(), providerkit.ClassPreview)
	if err != nil {
		t.Fatalf("PlanRemoval: %v", err)
	}
	params := groupNamed(plan, "aws/"+bootstrap.ParamGroupName)
	kept := changeNamed(params, bootstrap.PassphraseParamName)
	if kept == nil || kept.Action != providerkit.ActionKeep {
		t.Fatalf("the passphrase row = %+v, want it kept", kept)
	}
	if !strings.Contains(kept.Reason, bootstrap.ClassProduction) {
		t.Errorf("reason = %q, want it to name the bootstrap still holding it", kept.Reason)
	}
	if params.Action != providerkit.ActionDelete {
		t.Errorf("the parameters group = %q, want it still a deletion: only the passphrase stays", params.Action)
	}
}

func TestPlanRemovalOfAnAbsentBootstrapStillPlansWhatItLeftBehind(t *testing.T) {
	t.Parallel()

	b := removingBootstrapper(t, bootstrap.ClassProduction)
	delete(b.CFN.(*teardownCFN).present, bootstrap.StackName)

	plan, err := b.PlanRemoval(context.Background(), providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("PlanRemoval: %v", err)
	}
	if groupNamed(plan, "aws/"+bootstrap.StackName) != nil {
		t.Errorf("plan groups = %s, want no stack planned where none stands", groupNames(plan))
	}
	params := groupNamed(plan, "aws/"+bootstrap.ParamGroupName)
	if changeNamed(params, edgeParam(t, bootstrap.ClassProduction, "/credentials")) == nil {
		t.Errorf("plan groups = %s, want the parameters the bootstrap left behind still planned", groupNames(plan))
	}
}

func TestPlanRemovalLeavesOutAnEdgeThatSaysNothingAboutItsOwnRemoval(t *testing.T) {
	t.Parallel()

	b := removingBootstrapper(t, bootstrap.ClassProduction)
	b.Edge = &teardownEdge{}

	plan, err := b.PlanRemoval(context.Background(), providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("PlanRemoval: %v", err)
	}
	for _, group := range plan.Groups {
		if group.Kind == providerkit.EdgeGroupKind {
			t.Errorf("plan carries %+v for an edge that says nothing about its own removal", group)
		}
	}
}

func frontedBootstrapper(t *testing.T, class string) (Bootstrapper, *planningEdge, *planningEdge) {
	t.Helper()

	b := removingBootstrapper(t, class)
	standing := b.Edge.(*planningEdge)
	selected := &planningEdge{
		teardownEdge: teardownEdge{kind: cloudfrontKind},
		removals: []edge.PlanChange{
			{Kind: "AWS::CloudFront::KeyValueStore", Name: "ocel-routes", Action: edge.PlanDelete},
		},
	}
	b.Edge = selected
	b.Edges = registryOf(selected, standing)
	return b, selected, standing
}

func TestPlanRemovalNamesEveryStandingEdgeByItsOwnKind(t *testing.T) {
	t.Parallel()

	b, _, _ := frontedBootstrapper(t, bootstrap.ClassProduction)

	plan, err := b.PlanRemoval(context.Background(), providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("PlanRemoval: %v", err)
	}

	front := groupNamed(plan, string(cloudfrontKind)+"/edge")
	if front == nil || front.Feature != bootstrap.FeatureCloudFrontEdge {
		t.Fatalf("plan groups = %s, want the selected edge under its own kind and feature", groupNames(plan))
	}
	if changeNamed(front, "ocel-routes") == nil {
		t.Errorf("the %s group carries %+v, want the rows that edge planned", front.Name, front.Changes)
	}

	other := groupNamed(plan, string(cloudflareKind)+"/edge")
	if other == nil || other.Feature != bootstrap.FeatureCloudflareEdge {
		t.Fatalf("plan groups = %s, want the standing edge too, under its own kind and feature", groupNames(plan))
	}
	if changeNamed(other, "ocel-edge-cache") == nil {
		t.Errorf("the %s group carries %+v, want the rows that edge planned", other.Name, other.Changes)
	}
	for _, group := range []*providerkit.ChangeGroup{front, other} {
		for _, change := range group.Changes {
			if strings.HasPrefix(change.Kind, "Cloudflare::") != (group.Name == string(cloudflareKind)+"/edge") {
				t.Errorf("group %q carries %+v, want only the rows of the edge it names", group.Name, change)
			}
		}
	}
}

func TestPlanRemovalLeavesOutAnEdgeThisAccountHoldsNothingFor(t *testing.T) {
	t.Parallel()

	b, selected, standing := frontedBootstrapper(t, bootstrap.ClassProduction)
	unused := &planningEdge{
		teardownEdge: teardownEdge{kind: "relay"},
		removals:     []edge.PlanChange{{Kind: "Relay::Worker", Name: "ocel-relay", Action: edge.PlanDelete}},
	}
	b.Edges = registryOf(selected, standing, unused)

	plan, err := b.PlanRemoval(context.Background(), providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("PlanRemoval: %v", err)
	}
	if group := groupNamed(plan, "relay/edge"); group != nil {
		t.Errorf("plan carries %+v for an edge this account holds no parameters for", group)
	}
}

func TestPlanRemovalStillSeesAnEdgeWhoseParametersAreAlreadyGone(t *testing.T) {
	t.Parallel()

	b, _, _ := frontedBootstrapper(t, bootstrap.ClassProduction)
	severed(t, b, bootstrap.ClassProduction)

	plan, err := b.PlanRemoval(context.Background(), providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("PlanRemoval: %v", err)
	}
	if groupNamed(plan, string(cloudflareKind)+"/edge") == nil {
		t.Errorf("plan groups = %s, want the edge whose feature stack still stands: its externals are live and nothing else would take them", groupNames(plan))
	}
}

func TestRemoveTearsDownAnEdgeWhoseParametersAreAlreadyGone(t *testing.T) {
	t.Parallel()

	b, _, standing := frontedBootstrapper(t, bootstrap.ClassProduction)
	severed(t, b, bootstrap.ClassProduction)

	if err := b.Remove(context.Background(), providerkit.ClassProduction, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(standing.torndown) == 0 {
		t.Error("the cloudflare edge was left standing, so a run that severed its parameters and stopped orphans them for good")
	}
}

func severed(t *testing.T, b Bootstrapper, class string) {
	t.Helper()

	b.CFN.(*teardownCFN).present[bootstrap.FeatureStackName(bootstrap.FeatureCloudflareEdge, class)] =
		bootstrap.Deployed{Present: true}
	prefix, err := bootstrap.EdgeParamPrefix(class, cloudflareKind)
	if err != nil {
		t.Fatalf("EdgeParamPrefix(%s): %v", class, err)
	}
	held := b.SSM.(*teardownSSM).params
	for name := range held {
		if strings.HasPrefix(name, prefix+"/") {
			delete(held, name)
		}
	}
}

func TestRemoveTearsDownEveryEdgeThePlanShowed(t *testing.T) {
	t.Parallel()

	b, selected, standing := frontedBootstrapper(t, bootstrap.ClassProduction)

	if err := b.Remove(context.Background(), providerkit.ClassProduction, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, front := range []*planningEdge{selected, standing} {
		if !slices.Equal(front.torndown, []edge.Class{edge.ClassProduction}) {
			t.Errorf("the %s edge saw teardowns %v, want the class the plan showed it under", front.Kind(), front.torndown)
		}
	}
}

func TestRemoveLeavesAloneAnEdgeThisAccountHoldsNothingFor(t *testing.T) {
	t.Parallel()

	b, selected, standing := frontedBootstrapper(t, bootstrap.ClassProduction)
	unused := &planningEdge{teardownEdge: teardownEdge{kind: "relay"}}
	b.Edges = registryOf(selected, standing, unused)

	if err := b.Remove(context.Background(), providerkit.ClassProduction, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(unused.torndown) != 0 {
		t.Errorf("the relay edge saw teardowns %v, want none: this account holds nothing for it", unused.torndown)
	}
}
