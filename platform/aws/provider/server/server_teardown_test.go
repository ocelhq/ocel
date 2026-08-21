package server

import (
	"context"
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
	smithy "github.com/aws/smithy-go"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestClassOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tier environmentv1.Tier
		want string
	}{
		{name: "an unspecified class is the production bootstrap", tier: environmentv1.Tier_TIER_UNSPECIFIED, want: bootstrap.ClassProduction},
		{name: "production", tier: environmentv1.Tier_TIER_PRODUCTION, want: bootstrap.ClassProduction},
		{name: "preview", tier: environmentv1.Tier_TIER_PREVIEW, want: bootstrap.ClassPreview},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := classOf(tc.tier)
			if err != nil {
				t.Fatalf("classOf(%v): %v", tc.tier, err)
			}
			if got != tc.want {
				t.Errorf("classOf(%v) = %q, want %q", tc.tier, got, tc.want)
			}
		})
	}

	t.Run("a class this build does not know is not a bootstrap", func(t *testing.T) {
		t.Parallel()

		if _, err := classOf(environmentv1.Tier(99)); err == nil {
			t.Error("a class naming no bootstrap, want a refusal")
		}
	})
}

func TestBootstrapOccupancyRefuse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		class     string
		occupancy bootstrapOccupancy
		wantOK    bool
		wants     []string
	}{
		{
			name:      "an empty bootstrap is free to go",
			class:     bootstrap.ClassProduction,
			occupancy: bootstrapOccupancy{},
			wantOK:    true,
		},
		{
			name:      "live projects are named, one command each",
			class:     bootstrap.ClassProduction,
			occupancy: bootstrapOccupancy{projects: []string{"shop", "docs"}},
			wants:     []string{"shop", "docs", "ocel destroy"},
		},
		{
			name:      "a preview wildcard is named with the command that releases it",
			class:     bootstrap.ClassPreview,
			occupancy: bootstrapOccupancy{wildcard: "preview.acme.com"},
			wants:     []string{"*.preview.acme.com", "ocel domain release --preview"},
		},
		{
			name:      "both are reported together",
			class:     bootstrap.ClassPreview,
			occupancy: bootstrapOccupancy{projects: []string{"shop"}, wildcard: "preview.acme.com"},
			wants:     []string{"shop", "ocel destroy --preview", "*.preview.acme.com", "ocel domain release --preview"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.occupancy.refuse(tc.class)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("refuse() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("refuse() = nil, want a refusal")
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal must name %q, got %q", want, err)
				}
			}
		})
	}
}

func TestReadBootstrapOccupancy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ssmc := &stateSSM{params: map[string]string{}}
	if err := bootstrap.WriteStackRecordFor(ctx, ssmc, bootstrap.ClassPreview, "shop", bootstrap.StackRecord{Edge: edge.StackState{Slug: "shop"}}); err != nil {
		t.Fatalf("WriteStackStateFor: %v", err)
	}
	if err := bootstrap.WritePreviewDomain(ctx, ssmc, bootstrap.ClassPreview, bootstrap.PreviewDomain{BaseDomain: "preview.acme.com"}); err != nil {
		t.Fatalf("WritePreviewDomain: %v", err)
	}

	deps := teardownDeps{ssm: ssmc, index: &teardownIndex{projects: []string{"shop", "docs"}}}
	got, err := readBootstrapOccupancy(ctx, deps, bootstrap.ClassPreview)
	if err != nil {
		t.Fatalf("readBootstrapOccupancy: %v", err)
	}
	if !slices.Equal(got.projects, []string{"docs", "shop"}) {
		t.Errorf("projects = %v, want the stack index and the recorded edge stacks merged", got.projects)
	}
	if got.wildcard != "preview.acme.com" {
		t.Errorf("wildcard = %q, want the recorded base domain", got.wildcard)
	}

	production, err := readBootstrapOccupancy(ctx, teardownDeps{ssm: ssmc}, bootstrap.ClassProduction)
	if err != nil {
		t.Fatalf("readBootstrapOccupancy(production): %v", err)
	}
	if len(production.projects) != 0 || production.wildcard != "" {
		t.Errorf("production occupancy = %+v, want nothing: a preview stack is not a production one", production)
	}
}

func TestTeardownPlanItems(t *testing.T) {
	t.Parallel()

	deployed := bootstrap.Deployed{
		Present:        true,
		StateBucket:    "ocel-state",
		ArtifactBucket: "ocel-artifacts",
		AssetBucket:    "ocel-assets",
		StateTable:     "ocel-state-table",
		VarsTable:      "ocel-vars",
		Features:       bootstrap.FeatureSet{bootstrap.FeatureISR: true, bootstrap.FeatureCloudflareEdge: true},
	}

	t.Run("it lists every item the teardown touches", func(t *testing.T) {
		t.Parallel()

		items, err := teardownPlanItems(bootstrap.ClassProduction, cloudflare.Kind, deployed, false)
		if err != nil {
			t.Fatalf("teardownPlanItems: %v", err)
		}
		for _, want := range []string{
			"cloudflare",
			"ocel-state",
			"ocel-artifacts",
			"ocel-assets",
			"ocel-state-table",
			"ocel-vars",
			bootstrap.EdgeUserName,
			bootstrap.StackName,
			bootstrap.EdgeCredentialsParamName,
			bootstrap.PassphraseParamName,
		} {
			if findPlanItem(items, want) == nil {
				t.Errorf("plan is missing an item named %q; got %s", want, planNames(items))
			}
		}
		for _, item := range items {
			if item.GetKind() == "" || item.GetReason() == "" {
				t.Errorf("item %+v carries no kind or reason", item)
			}
			if item.GetAction() == contractv1.RemovalItem_ACTION_UNSPECIFIED {
				t.Errorf("item %q carries no action", item.GetName())
			}
		}
		for _, bucket := range []string{"ocel-state", "ocel-artifacts", "ocel-assets"} {
			if !findPlanItem(items, bucket).GetSlow() {
				t.Errorf("bucket %s must be flagged slow: it is emptied object by object", bucket)
			}
		}
		if got := findPlanItem(items, bootstrap.PassphraseParamName); got.GetAction() != contractv1.RemovalItem_ACTION_DELETE {
			t.Errorf("passphrase action = %v, want it deleted when no sibling bootstrap holds it", got.GetAction())
		}
		if findPlanItem(items, bootstrap.PreviewDomainParamName) != nil {
			t.Error("the production plan must not name the preview domain parameter")
		}
	})

	t.Run("a bootstrapped sibling keeps the passphrase, with the reason", func(t *testing.T) {
		t.Parallel()

		items, err := teardownPlanItems(bootstrap.ClassPreview, cloudflare.Kind, deployed, true)
		if err != nil {
			t.Fatalf("teardownPlanItems: %v", err)
		}
		kept := findPlanItem(items, bootstrap.PassphraseParamName)
		if kept.GetAction() != contractv1.RemovalItem_ACTION_KEEP {
			t.Fatalf("passphrase action = %v, want it kept", kept.GetAction())
		}
		if !strings.Contains(kept.GetReason(), bootstrap.ClassProduction) {
			t.Errorf("reason = %q, want it to name the bootstrap still holding it", kept.GetReason())
		}
		if findPlanItem(items, bootstrap.PreviewDomainParamName) == nil {
			t.Error("the preview plan must name the preview domain parameter")
		}
	})

	t.Run("no cloudflare edge is no edge reader to delete", func(t *testing.T) {
		t.Parallel()

		bare := deployed
		bare.Features = bootstrap.FeatureSet{bootstrap.FeatureISR: true}
		items, err := teardownPlanItems(bootstrap.ClassProduction, cloudflare.Kind, bare, false)
		if err != nil {
			t.Fatalf("teardownPlanItems: %v", err)
		}
		if findPlanItem(items, bootstrap.EdgeUserName) != nil {
			t.Errorf("plan names an edge reader this bootstrap never stood up; got %s", planNames(items))
		}
		if findPlanItem(items, bootstrap.FeatureStackName(bootstrap.FeatureCloudflareEdge, bootstrap.ClassProduction)) != nil {
			t.Errorf("plan names a feature stack this bootstrap does not carry; got %s", planNames(items))
		}
	})

	t.Run("a bucket the stack never reported is not planned blank", func(t *testing.T) {
		t.Parallel()

		partial := deployed
		partial.ArtifactBucket = ""
		items, err := teardownPlanItems(bootstrap.ClassProduction, cloudflare.Kind, partial, false)
		if err != nil {
			t.Fatalf("teardownPlanItems: %v", err)
		}
		for _, item := range items {
			if item.GetName() == "" {
				t.Errorf("plan carries a nameless item %+v; got %s", item, planNames(items))
			}
		}
	})

	t.Run("an absent bootstrap still plans its leftovers", func(t *testing.T) {
		t.Parallel()

		items, err := teardownPlanItems(bootstrap.ClassProduction, cloudflare.Kind, bootstrap.Deployed{}, false)
		if err != nil {
			t.Fatalf("teardownPlanItems: %v", err)
		}
		if findPlanItem(items, bootstrap.StackName) != nil {
			t.Error("no stack is deployed, so none is planned for deletion")
		}
		if findPlanItem(items, bootstrap.EdgeCredentialsParamName) == nil {
			t.Error("the parameters the bootstrap left behind are still planned")
		}
	})
}

func findPlanItem(items []*contractv1.RemovalItem, name string) *contractv1.RemovalItem {
	for _, item := range items {
		if item.GetName() == name {
			return item
		}
	}
	return nil
}

func planNames(items []*contractv1.RemovalItem) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.GetName())
	}
	return names
}

func TestPlanTeardown(t *testing.T) {
	t.Parallel()

	t.Run("it refuses while a project is still deployed", func(t *testing.T) {
		t.Parallel()

		deps := newTeardownFakes(t, bootstrap.ClassProduction)
		if err := bootstrap.WriteStackRecordFor(context.Background(), deps.ssm, bootstrap.ClassProduction, "shop", bootstrap.StackRecord{}); err != nil {
			t.Fatalf("WriteStackStateFor: %v", err)
		}

		_, err := planTeardown(context.Background(), deps, bootstrap.ClassProduction)
		if err == nil {
			t.Fatal("planTeardown = nil error, want the live-stack refusal")
		}
		if !strings.Contains(err.Error(), "shop") {
			t.Errorf("err = %v, want it to name the live project", err)
		}
	})

	t.Run("it refuses on a project only the stack index knows about", func(t *testing.T) {
		t.Parallel()

		deps := newTeardownFakes(t, bootstrap.ClassProduction)
		deps.index = &teardownIndex{projects: []string{"shop"}}

		_, err := planTeardown(context.Background(), deps, bootstrap.ClassProduction)
		if err == nil {
			t.Fatal("planTeardown = nil error, want the refusal: a failed `ocel destroy` leaves the index populated and the rootstack parameter gone")
		}
		for _, want := range []string{"shop", "ocel destroy"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("it reports the edge fronting the bootstrap", func(t *testing.T) {
		t.Parallel()

		deps := newTeardownFakes(t, bootstrap.ClassProduction)
		plan, err := planTeardown(context.Background(), deps, bootstrap.ClassProduction)
		if err != nil {
			t.Fatalf("planTeardown: %v", err)
		}
		if plan.GetEdgeKind() != string(cloudflare.Kind) {
			t.Errorf("edge kind = %q, want cloudflare", plan.GetEdgeKind())
		}
		if findPlanItem(plan.GetItems(), bootstrap.StackName) == nil {
			t.Errorf("plan is missing the bootstrap stack; got %s", planNames(plan.GetItems()))
		}
	})
}

func TestRunTeardown(t *testing.T) {
	t.Parallel()

	t.Run("it refuses while the preview wildcard is still in use", func(t *testing.T) {
		t.Parallel()

		deps := newTeardownFakes(t, bootstrap.ClassPreview)
		if err := bootstrap.WritePreviewDomain(context.Background(), deps.ssm, bootstrap.ClassPreview, bootstrap.PreviewDomain{BaseDomain: "preview.acme.com"}); err != nil {
			t.Fatalf("WritePreviewDomain: %v", err)
		}

		err := runTeardown(context.Background(), deps, bootstrap.ClassPreview, func(string) {}, func(string) {})
		if err == nil {
			t.Fatal("runTeardown = nil, want the wildcard refusal")
		}
		if !strings.Contains(err.Error(), "*.preview.acme.com") {
			t.Errorf("err = %v, want it to name the wildcard", err)
		}
		if edgeOf(t, deps).torndown != nil {
			t.Error("a refused teardown must not reach the edge")
		}
	})

	t.Run("it refuses while the stack index still lists a project", func(t *testing.T) {
		t.Parallel()

		deps := newTeardownFakes(t, bootstrap.ClassProduction)
		deps.index = &teardownIndex{projects: []string{"shop"}}

		err := runTeardown(context.Background(), deps, bootstrap.ClassProduction, func(string) {}, func(string) {})
		if err == nil {
			t.Fatal("runTeardown = nil, want the refusal: its live Pulumi stacks become unmanageable once the state bucket goes")
		}
		if !strings.Contains(err.Error(), "shop") {
			t.Errorf("err = %v, want it to name the indexed project", err)
		}
		if edgeOf(t, deps).torndown != nil {
			t.Error("a refused teardown must not reach the edge")
		}
		if len(cfnOf(t, deps).deleted) != 0 {
			t.Error("a refused teardown must not delete the bootstrap stack")
		}
	})

	t.Run("it tears the edge down for the class, then the AWS bootstrap", func(t *testing.T) {
		t.Parallel()

		deps := newTeardownFakes(t, bootstrap.ClassProduction)
		cfn, s3fake, iamfake := cfnOf(t, deps), bucketsOf(t, deps), iamOf(t, deps)

		if err := runTeardown(context.Background(), deps, bootstrap.ClassProduction, func(string) {}, func(string) {}); err != nil {
			t.Fatalf("runTeardown: %v", err)
		}

		if got := edgeOf(t, deps).torndown; !slices.Equal(got, []edge.Class{edge.ClassProduction}) {
			t.Errorf("edge teardown classes = %v, want [production]", got)
		}
		if !slices.Equal(cfn.deleted, []string{bootstrap.StackName}) {
			t.Errorf("deleted stacks = %v, want [%s]", cfn.deleted, bootstrap.StackName)
		}
		if !slices.Equal(s3fake.emptied, []string{"ocel-state", "ocel-artifacts", "ocel-assets"}) {
			t.Errorf("emptied buckets = %v, want state, artifact and asset", s3fake.emptied)
		}
		if !slices.Contains(s3fake.deleted, "gone.json@v2") {
			t.Errorf("deleted objects = %v, want the delete markers gone too", s3fake.deleted)
		}
		if !slices.Equal(iamfake.deletedKeys, []string{"AKIAOLD"}) {
			t.Errorf("deleted access keys = %v, want the edge reader's", iamfake.deletedKeys)
		}
		for _, name := range []string{bootstrap.EdgeCredentialsParamName, bootstrap.EdgeValuesParamName, bootstrap.PassphraseParamName} {
			if _, still := deps.ssm.(*stateSSM).params[name]; still {
				t.Errorf("parameter %s survived the teardown", name)
			}
		}
	})

	t.Run("a bootstrapped sibling keeps the shared passphrase", func(t *testing.T) {
		t.Parallel()

		deps := newTeardownFakes(t, bootstrap.ClassPreview)
		cfnOf(t, deps).present[bootstrap.StackName] = bootstrap.Deployed{Present: true}

		if err := runTeardown(context.Background(), deps, bootstrap.ClassPreview, func(string) {}, func(string) {}); err != nil {
			t.Fatalf("runTeardown: %v", err)
		}
		if _, held := deps.ssm.(*stateSSM).params[bootstrap.PassphraseParamName]; !held {
			t.Error("the passphrase the production bootstrap still needs was deleted")
		}
		if _, held := deps.ssm.(*stateSSM).params[bootstrap.EdgeCredentialsPreviewParamName]; held {
			t.Error("the preview bootstrap's own parameters must go")
		}
	})
}

func newTeardownFakes(t *testing.T, class string) teardownDeps {
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
	return teardownDeps{
		edge:     &teardownEdge{},
		cfn:      &teardownCFN{present: map[string]bootstrap.Deployed{stackName: deployed}},
		ssm:      &stateSSM{params: stored},
		iam:      &teardownIAM{keys: map[string][]string{userName: {"AKIAOLD"}}},
		buckets:  &teardownBuckets{},
		deployed: deployed,
		index:    &teardownIndex{},
	}
}

type teardownIndex struct {
	projects []string
	err      error
}

func (i *teardownIndex) Projects(context.Context) ([]string, error) { return i.projects, i.err }

func edgeOf(t *testing.T, deps teardownDeps) *teardownEdge {
	t.Helper()
	return deps.edge.(*teardownEdge)
}

func cfnOf(t *testing.T, deps teardownDeps) *teardownCFN {
	t.Helper()
	return deps.cfn.(*teardownCFN)
}

func iamOf(t *testing.T, deps teardownDeps) *teardownIAM {
	t.Helper()
	return deps.iam.(*teardownIAM)
}

func bucketsOf(t *testing.T, deps teardownDeps) *teardownBuckets {
	t.Helper()
	return deps.buckets.(*teardownBuckets)
}

type teardownEdge struct {
	edge.Edge
	torndown []edge.Class
}

func (e *teardownEdge) Kind() edge.Kind { return cloudflare.Kind }

func (e *teardownEdge) Teardown(_ context.Context, class edge.Class) error {
	e.torndown = append(e.torndown, class)
	return nil
}

type teardownCFN struct {
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

func (c *teardownCFN) DeleteStack(_ context.Context, in *cloudformation.DeleteStackInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error) {
	name := aws.ToString(in.StackName)
	c.deleted = append(c.deleted, name)
	delete(c.present, name)
	return &cloudformation.DeleteStackOutput{}, nil
}

type teardownIAM struct {
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
