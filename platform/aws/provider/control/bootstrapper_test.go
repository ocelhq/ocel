package control

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	smithy "github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func standingBootstrapper(t *testing.T, class string) Bootstrapper {
	t.Helper()

	stackName, err := bootstrap.StackNameFor(class)
	if err != nil {
		t.Fatalf("StackNameFor(%s): %v", class, err)
	}
	params, err := bootstrap.ClassParamNames(class)
	if err != nil {
		t.Fatalf("ClassParamNames(%s): %v", class, err)
	}
	userName, err := bootstrap.EdgeUserNameFor(class)
	if err != nil {
		t.Fatalf("EdgeUserNameFor(%s): %v", class, err)
	}

	stored := map[string]string{bootstrap.PassphraseParamName: "pp"}
	for _, name := range params {
		stored[name] = "{}"
	}
	deployed := bootstrap.Deployed{
		Present:        true,
		StateBucket:    "ocel-state",
		ArtifactBucket: "ocel-artifacts",
		AssetBucket:    "ocel-assets",
		StateTable:     "ocel-state-table",
		VarsTable:      "ocel-vars",
	}
	return Bootstrapper{
		CFN:     &teardownCFN{present: map[string]bootstrap.Deployed{stackName: deployed}},
		SSM:     &teardownSSM{params: stored},
		IAM:     &teardownIAM{keys: map[string][]string{userName: {"AKIAOLD"}}},
		Buckets: &teardownBuckets{},
		Edge:    &teardownEdge{},
	}
}

func TestPlanNamesEveryStackUnderAWSAndTheEdgeUnderItsOwnVendor(t *testing.T) {
	t.Parallel()

	front := &planningEdge{planned: []edge.PlanChange{
		{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanCreate},
		{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanKeep, Reason: "already current"},
	}}
	b := planningBootstrapper(front)

	plan, err := b.Plan(context.Background(), providerkit.BootstrapRequest{
		Class:    providerkit.ClassProduction,
		Features: []string{bootstrap.FeatureISR},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Groups) != 4 {
		t.Fatalf("plan = %v, want the core stack, the isr stack, the parameters and the edge", plan.Groups)
	}
	for _, group := range plan.Groups[:2] {
		if group.Kind != providerkit.StackGroupKind || !strings.HasPrefix(group.Name, "aws/") {
			t.Errorf("group %+v, want a stack named under the vendor that holds it", group)
		}
	}
	if got := plan.Groups[0].Name; got != "aws/"+bootstrap.StackName {
		t.Errorf("the core group is %q, want %q", got, "aws/"+bootstrap.StackName)
	}
	params := plan.Groups[2]
	if params.Kind != providerkit.ParameterGroupKind || params.Name != "aws/"+bootstrap.ParamGroupName {
		t.Errorf("the parameters group = %+v, want it named under the account that holds them", params)
	}
	edgeGroup := plan.Groups[3]
	if edgeGroup.Kind != providerkit.EdgeGroupKind || edgeGroup.Name != string(cloudflareKind)+"/edge" {
		t.Errorf("the edge group = %+v, want the edge named under its own vendor", edgeGroup)
	}
	if edgeGroup.Feature != bootstrap.FeatureCloudflareEdge {
		t.Errorf("the edge group's feature = %q, want the one the edge participates through", edgeGroup.Feature)
	}
	if edgeGroup.Action != providerkit.ActionUpdate {
		t.Errorf("the edge group is %q, want an update where one of its resources is missing", edgeGroup.Action)
	}
	if len(edgeGroup.Changes) != len(front.planned) {
		t.Errorf("the edge group carries %d changes, want the ones the edge planned", len(edgeGroup.Changes))
	}
	if front.classes[0] != edge.ClassProduction {
		t.Errorf("the edge was planned for %q, want the class the bootstrap is for", front.classes[0])
	}
}

func TestPlanShowsTheEdgeGoingWhenTheFeatureItFrontsThroughIsDropped(t *testing.T) {
	t.Parallel()

	front := &planningEdge{planned: []edge.PlanChange{
		{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: edge.PlanKeep},
		{Kind: "Cloudflare::Worker", Name: "ocel-isr-writer", Action: edge.PlanUpdate},
		{Kind: "Cloudflare::Worker", Name: "ocel-deployments", Action: edge.PlanCreate},
	}}
	b := planningBootstrapper(front)

	plan, err := b.Plan(context.Background(), providerkit.BootstrapRequest{
		Class:  providerkit.ClassProduction,
		Remove: []string{bootstrap.FeatureCloudflareEdge},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var edgeGroup providerkit.ChangeGroup
	for _, group := range plan.Groups {
		if group.Kind == providerkit.EdgeGroupKind {
			edgeGroup = group
		}
	}
	if edgeGroup.Name == "" {
		t.Fatal("the plan says nothing about the edge it is dropping")
	}
	if edgeGroup.Action != providerkit.ActionDelete {
		t.Errorf("the edge group is %q, want it deleted: the feature it fronts through is being dropped", edgeGroup.Action)
	}
	for _, change := range edgeGroup.Changes {
		if change.Action != providerkit.ActionDelete {
			t.Errorf("the drop plan carries %+v, want every edge row a delete", change)
		}
		if change.Name == "ocel-deployments" {
			t.Error("the drop plan names a worker that does not stand yet among what it deletes")
		}
	}
}

func TestPlanLeavesOutAnEdgeThatCannotPlanItsOwnBootstrap(t *testing.T) {
	t.Parallel()

	b := planningBootstrapper(&teardownEdge{})

	plan, err := b.Plan(context.Background(), providerkit.BootstrapRequest{Class: providerkit.ClassProduction})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, group := range plan.Groups {
		if group.Kind == providerkit.EdgeGroupKind {
			t.Errorf("plan carries %+v for an edge that says nothing about its own bootstrap", group)
		}
	}
}

func TestPlanCarriesTheEdgesRefusalOut(t *testing.T) {
	t.Parallel()

	b := planningBootstrapper(&planningEdge{err: errors.New("CLOUDFLARE_ACCOUNT_ID is not set")})

	_, err := b.Plan(context.Background(), providerkit.BootstrapRequest{Class: providerkit.ClassProduction})
	if err == nil || !strings.Contains(err.Error(), "CLOUDFLARE_ACCOUNT_ID") {
		t.Fatalf("Plan error = %v, want the edge's own account error carried out whole", err)
	}
}

func planningBootstrapper(front edge.Edge) Bootstrapper {
	return Bootstrapper{
		CFN:  &teardownCFN{present: map[string]bootstrap.Deployed{}},
		SSM:  &teardownSSM{params: map[string]string{}},
		IAM:  &teardownIAM{keys: map[string][]string{}},
		Edge: front,
	}
}

type planningEdge struct {
	teardownEdge

	planned []edge.PlanChange
	err     error
	classes []edge.Class
}

func (e *planningEdge) PlanBootstrap(_ context.Context, class edge.Class) ([]edge.PlanChange, error) {
	e.classes = append(e.classes, class)
	return e.planned, e.err
}

func TestRemoveTearsTheEdgeDownForTheClassThenTheAWSBootstrap(t *testing.T) {
	t.Parallel()

	b := standingBootstrapper(t, bootstrap.ClassProduction)
	cfn, buckets, iamfake := b.CFN.(*teardownCFN), b.Buckets.(*teardownBuckets), b.IAM.(*teardownIAM)

	if err := b.Remove(context.Background(), providerkit.ClassProduction, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if got := b.Edge.(*teardownEdge).torndown; !slices.Equal(got, []edge.Class{edge.ClassProduction}) {
		t.Errorf("edge teardown classes = %v, want [production]", got)
	}
	if !slices.Equal(cfn.deleted, []string{bootstrap.StackName}) {
		t.Errorf("deleted stacks = %v, want [%s]", cfn.deleted, bootstrap.StackName)
	}
	if !slices.Equal(buckets.emptied, []string{"ocel-state", "ocel-artifacts", "ocel-assets"}) {
		t.Errorf("emptied buckets = %v, want state, artifact and asset", buckets.emptied)
	}
	if !slices.Contains(buckets.deleted, "gone.json@v2") {
		t.Errorf("deleted objects = %v, want the delete markers gone too", buckets.deleted)
	}
	if !slices.Equal(iamfake.deletedKeys, []string{"AKIAOLD"}) {
		t.Errorf("deleted access keys = %v, want the edge reader's", iamfake.deletedKeys)
	}
	for _, name := range []string{
		edgeParam(t, bootstrap.ClassProduction, "/credentials"),
		edgeParam(t, bootstrap.ClassProduction, "/values"),
		bootstrap.PassphraseParamName,
	} {
		if _, still := b.SSM.(*teardownSSM).params[name]; still {
			t.Errorf("parameter %s survived the teardown", name)
		}
	}
}

func TestRemoveKeepsThePassphraseABootstrappedSiblingStillNeeds(t *testing.T) {
	t.Parallel()

	b := standingBootstrapper(t, bootstrap.ClassPreview)
	b.CFN.(*teardownCFN).present[bootstrap.StackName] = bootstrap.Deployed{Present: true}

	if err := b.Remove(context.Background(), providerkit.ClassPreview, nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, held := b.SSM.(*teardownSSM).params[bootstrap.PassphraseParamName]; !held {
		t.Error("the passphrase the production bootstrap still needs was deleted")
	}
	if _, held := b.SSM.(*teardownSSM).params[edgeParam(t, bootstrap.ClassPreview, "/credentials")]; held {
		t.Error("the preview bootstrap's own parameters must go")
	}
}

type teardownEdge struct {
	edge.Edge
	torndown []edge.Class
}

func (e *teardownEdge) Kind() edge.Kind { return cloudflareKind }

func (e *teardownEdge) Teardown(_ context.Context, class edge.Class) error {
	e.torndown = append(e.torndown, class)
	return nil
}

type teardownCFN struct {
	bootstrap.CFNAPI

	present map[string]bootstrap.Deployed
	deleted []string
}

func (c *teardownCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	name := aws.ToString(in.StackName)
	deployed, ok := c.present[name]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "ValidationError", Message: "Stack with id " + name + " does not exist"}
	}
	outputs := []cfntypes.Output{
		{OutputKey: aws.String("StateBucketName"), OutputValue: aws.String(deployed.StateBucket)},
		{OutputKey: aws.String("ArtifactBucketName"), OutputValue: aws.String(deployed.ArtifactBucket)},
		{OutputKey: aws.String("AssetBucketName"), OutputValue: aws.String(deployed.AssetBucket)},
		{OutputKey: aws.String("StateTableName"), OutputValue: aws.String(deployed.StateTable)},
		{OutputKey: aws.String("VarsTableName"), OutputValue: aws.String(deployed.VarsTable)},
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName:   in.StackName,
		StackStatus: cfntypes.StackStatusCreateComplete,
		Outputs:     outputs,
	}}}, nil
}

func (c *teardownCFN) ListStackResources(_ context.Context, _ *cloudformation.ListStackResourcesInput, _ ...func(*cloudformation.Options)) (*cloudformation.ListStackResourcesOutput, error) {
	return &cloudformation.ListStackResourcesOutput{}, nil
}

func (c *teardownCFN) DeleteStack(_ context.Context, in *cloudformation.DeleteStackInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error) {
	name := aws.ToString(in.StackName)
	c.deleted = append(c.deleted, name)
	delete(c.present, name)
	return &cloudformation.DeleteStackOutput{}, nil
}

type teardownIAM struct {
	IAMAPI

	keys        map[string][]string
	deletedKeys []string
}

func (i *teardownIAM) ListAccessKeys(_ context.Context, in *iam.ListAccessKeysInput, _ ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error) {
	held, ok := i.keys[aws.ToString(in.UserName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{}
	}
	out := &iam.ListAccessKeysOutput{}
	for _, id := range held {
		out.AccessKeyMetadata = append(out.AccessKeyMetadata, iamtypes.AccessKeyMetadata{AccessKeyId: aws.String(id)})
	}
	return out, nil
}

func (i *teardownIAM) DeleteAccessKey(_ context.Context, in *iam.DeleteAccessKeyInput, _ ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error) {
	i.deletedKeys = append(i.deletedKeys, aws.ToString(in.AccessKeyId))
	return &iam.DeleteAccessKeyOutput{}, nil
}

type teardownBuckets struct {
	emptied []string
	deleted []string
}

func (b *teardownBuckets) ListObjectVersions(_ context.Context, in *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	bucket := aws.ToString(in.Bucket)
	b.emptied = append(b.emptied, bucket)
	return &s3.ListObjectVersionsOutput{
		Versions:      []s3types.ObjectVersion{{Key: aws.String("state.json"), VersionId: aws.String("v1")}},
		DeleteMarkers: []s3types.DeleteMarkerEntry{{Key: aws.String("gone.json"), VersionId: aws.String("v2")}},
	}, nil
}

func (b *teardownBuckets) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	for _, obj := range in.Delete.Objects {
		b.deleted = append(b.deleted, aws.ToString(obj.Key)+"@"+aws.ToString(obj.VersionId))
	}
	return &s3.DeleteObjectsOutput{}, nil
}

type teardownSSM struct {
	params map[string]string
}

func (s *teardownSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	raw, ok := s.params[aws.ToString(in.Name)]
	if !ok {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(raw)}}, nil
}

func (s *teardownSSM) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	s.params[aws.ToString(in.Name)] = aws.ToString(in.Value)
	return &ssm.PutParameterOutput{}, nil
}

func (s *teardownSSM) GetParametersByPath(_ context.Context, in *ssm.GetParametersByPathInput, _ ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error) {
	out := &ssm.GetParametersByPathOutput{}
	for name := range s.params {
		if strings.HasPrefix(name, aws.ToString(in.Path)) {
			out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(name)})
		}
	}
	return out, nil
}

func (s *teardownSSM) DeleteParameter(_ context.Context, in *ssm.DeleteParameterInput, _ ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	delete(s.params, aws.ToString(in.Name))
	return &ssm.DeleteParameterOutput{}, nil
}
