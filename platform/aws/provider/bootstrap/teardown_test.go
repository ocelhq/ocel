package bootstrap

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
	smithy "github.com/aws/smithy-go"
)

type teardownStack struct {
	status  cfntypes.StackStatus
	buckets map[string]string
}

type teardownCFN struct {
	stacks    map[string]teardownStack
	deleted   []string
	deleteErr error
	onDelete  func(*teardownCFN)
}

func (c *teardownCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	name := aws.ToString(in.StackName)
	stack, ok := c.stacks[name]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "ValidationError", Message: "Stack with id " + name + " does not exist"}
	}
	status := stack.status
	if status == "" {
		status = cfntypes.StackStatusCreateComplete
	}
	var outputs []cfntypes.Output
	for key, value := range stack.buckets {
		outputs = append(outputs, cfntypes.Output{OutputKey: aws.String(key), OutputValue: aws.String(value)})
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName:   in.StackName,
		StackStatus: status,
		Outputs:     outputs,
	}}}, nil
}

func (c *teardownCFN) DeleteStack(_ context.Context, in *cloudformation.DeleteStackInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error) {
	name := aws.ToString(in.StackName)
	c.deleted = append(c.deleted, name)
	if c.onDelete != nil {
		c.onDelete(c)
	}
	if c.deleteErr != nil {
		return nil, c.deleteErr
	}
	delete(c.stacks, name)
	return &cloudformation.DeleteStackOutput{}, nil
}

type teardownIAM struct {
	keys    map[string][]string
	deleted []string
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
	i.deleted = append(i.deleted, aws.ToString(in.AccessKeyId))
	return &iam.DeleteAccessKeyOutput{}, nil
}

type objectPage struct {
	keys      []string
	truncated bool
}

type teardownS3 struct {
	pages    map[string][]objectPage
	listErr  map[string]error
	refuse   []s3types.Error
	listed   []string
	deleted  []string
	markers  []string
	listCall map[string]int
}

func (b *teardownS3) ListObjectVersions(_ context.Context, in *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	bucket := aws.ToString(in.Bucket)
	b.listed = append(b.listed, bucket)
	if err := b.listErr[bucket]; err != nil {
		return nil, err
	}
	if b.listCall == nil {
		b.listCall = map[string]int{}
	}
	index := b.listCall[bucket]
	b.listCall[bucket] = index + 1
	pages := b.pages[bucket]
	if index >= len(pages) {
		return &s3.ListObjectVersionsOutput{}, nil
	}
	page := pages[index]
	out := &s3.ListObjectVersionsOutput{IsTruncated: aws.Bool(page.truncated)}
	for _, key := range page.keys {
		out.Versions = append(out.Versions, s3types.ObjectVersion{Key: aws.String(key), VersionId: aws.String("v1")})
	}
	if page.truncated {
		out.NextKeyMarker = aws.String(page.keys[len(page.keys)-1])
		out.NextVersionIdMarker = aws.String("v1")
	}
	b.markers = append(b.markers, aws.ToString(in.KeyMarker))
	return out, nil
}

func (b *teardownS3) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	for _, obj := range in.Delete.Objects {
		b.deleted = append(b.deleted, aws.ToString(obj.Key))
	}
	return &s3.DeleteObjectsOutput{Errors: b.refuse}, nil
}

func teardownFakes(t *testing.T) (TeardownAPIs, *teardownCFN, *fakeSSM, *teardownS3) {
	t.Helper()

	cfn := &teardownCFN{stacks: map[string]teardownStack{
		StackName: {buckets: map[string]string{
			outputStateBucket:    "ocel-state",
			outputArtifactBucket: "ocel-artifacts",
			outputAssetBucket:    "ocel-assets",
		}},
	}}
	ssmc := newFakeSSM()
	ssmc.params[PassphraseParamName] = "pp"
	params, err := ClassParamNames(ClassProduction)
	if err != nil {
		t.Fatalf("ClassParamNames: %v", err)
	}
	for _, name := range params {
		ssmc.params[name] = "{}"
	}
	buckets := &teardownS3{pages: map[string][]objectPage{}}
	user, err := EdgeUserNameFor(ClassProduction)
	if err != nil {
		t.Fatalf("EdgeUserNameFor: %v", err)
	}
	apis := TeardownAPIs{
		CFN:     cfn,
		SSM:     ssmc,
		IAM:     &teardownIAM{keys: map[string][]string{user: {"AKIAOLD"}}},
		Buckets: buckets,
	}
	return apis, cfn, ssmc, buckets
}

func TestTeardownEmptiesEveryPage(t *testing.T) {
	t.Parallel()

	apis, _, _, buckets := teardownFakes(t)
	buckets.pages["ocel-state"] = []objectPage{
		{keys: []string{"a"}, truncated: true},
		{keys: []string{"b"}},
	}

	if err := Teardown(context.Background(), apis, ClassProduction, nil, nil); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if !slices.Contains(buckets.deleted, "a") || !slices.Contains(buckets.deleted, "b") {
		t.Errorf("deleted objects = %v, want both pages emptied", buckets.deleted)
	}
	if !slices.Contains(buckets.markers, "a") {
		t.Errorf("list markers = %v, want the second page to resume from the first", buckets.markers)
	}
}

func TestTeardownReportsRefusedObjects(t *testing.T) {
	t.Parallel()

	apis, cfn, ssmc, buckets := teardownFakes(t)
	buckets.pages["ocel-state"] = []objectPage{{keys: []string{"locked.json"}}}
	buckets.refuse = []s3types.Error{{
		Key:     aws.String("locked.json"),
		Code:    aws.String("AccessDenied"),
		Message: aws.String("Object is under legal hold"),
	}}

	err := Teardown(context.Background(), apis, ClassProduction, nil, nil)
	if err == nil {
		t.Fatal("Teardown = nil, want the objects S3 refused to delete reported")
	}
	for _, want := range []string{"ocel-state", "locked.json", "legal hold"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
	if len(cfn.deleted) != 0 {
		t.Errorf("deleted stacks = %v, want none: the bucket was never emptied", cfn.deleted)
	}
	if _, held := ssmc.params[PassphraseParamName]; !held {
		t.Error("the passphrase must survive a teardown that never deleted the stack")
	}
}

func TestTeardownRetriesAfterAPartialStackDelete(t *testing.T) {
	t.Parallel()

	apis, _, _, buckets := teardownFakes(t)
	buckets.listErr = map[string]error{
		"ocel-artifacts": &s3types.NoSuchBucket{},
	}
	buckets.pages["ocel-state"] = []objectPage{{keys: []string{"state.json"}}}

	if err := Teardown(context.Background(), apis, ClassProduction, nil, nil); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if !slices.Contains(buckets.listed, "ocel-assets") {
		t.Errorf("listed buckets = %v, want a bucket CloudFormation already deleted to be skipped, not fatal", buckets.listed)
	}
}

func TestTeardownKeepsEverythingWhenTheStackDeleteFails(t *testing.T) {
	t.Parallel()

	apis, cfn, ssmc, _ := teardownFakes(t)
	cfn.deleteErr = errors.New("DeleteStack refused")

	before := len(ssmc.params)
	if err := Teardown(context.Background(), apis, ClassProduction, nil, nil); err == nil {
		t.Fatal("Teardown = nil, want the failed stack delete surfaced")
	}
	if len(ssmc.params) != before {
		t.Errorf("parameters = %v, want every one kept while the stack is still there", ssmc.params)
	}
	if _, held := ssmc.params[PassphraseParamName]; !held {
		t.Error("the passphrase must outlive a stack that was not deleted")
	}
}

func TestTeardownRereadsTheSiblingBeforeDroppingThePassphrase(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		sibling teardownStack
	}{
		{name: "a sibling bootstrapped mid-teardown", sibling: teardownStack{status: cfntypes.StackStatusCreateComplete}},
		{name: "a sibling still creating", sibling: teardownStack{status: cfntypes.StackStatusCreateInProgress}},
		{name: "a sibling that failed to delete", sibling: teardownStack{status: cfntypes.StackStatusDeleteFailed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			apis, cfn, ssmc, _ := teardownFakes(t)
			cfn.onDelete = func(c *teardownCFN) { c.stacks[PreviewStackName] = tc.sibling }

			if err := Teardown(context.Background(), apis, ClassProduction, nil, nil); err != nil {
				t.Fatalf("Teardown: %v", err)
			}
			if _, held := ssmc.params[PassphraseParamName]; !held {
				t.Error("the preview substrate landed mid-teardown; its Pulumi state is encrypted under the passphrase that was deleted")
			}
			if _, held := ssmc.params[EdgeCredentialsParamName]; held {
				t.Error("the torn-down substrate's own parameters must still go")
			}
		})
	}
}

func TestTeardownReclaimsTheClassOriginSecret(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		class string
		stack string
		mine  string
		other string
	}{
		{ClassProduction, StackName, OriginSecretParamName, OriginSecretPreviewParamName},
		{ClassPreview, PreviewStackName, OriginSecretPreviewParamName, OriginSecretParamName},
	} {
		t.Run(tc.class, func(t *testing.T) {
			t.Parallel()

			cfn := &teardownCFN{stacks: map[string]teardownStack{tc.stack: {}}}
			ssmc := newFakeSSM()
			names, err := ClassParamNames(tc.class)
			if err != nil {
				t.Fatalf("ClassParamNames: %v", err)
			}
			for _, name := range names {
				ssmc.params[name] = "{}"
			}
			ssmc.params[tc.other] = "the other substrate's secret"
			user, err := EdgeUserNameFor(tc.class)
			if err != nil {
				t.Fatalf("EdgeUserNameFor: %v", err)
			}
			apis := TeardownAPIs{
				CFN:     cfn,
				SSM:     ssmc,
				IAM:     &teardownIAM{keys: map[string][]string{user: {"AKIAOLD"}}},
				Buckets: &teardownS3{pages: map[string][]objectPage{}},
			}

			if err := Teardown(context.Background(), apis, tc.class, nil, nil); err != nil {
				t.Fatalf("Teardown: %v", err)
			}
			if _, held := ssmc.params[tc.mine]; held {
				t.Errorf("%s outlived its substrate; the next bootstrap adopts a secret no release demands", tc.mine)
			}
			if _, held := ssmc.params[tc.other]; !held {
				t.Errorf("tearing down %s took %s with it, stranding every release the other substrate still serves", tc.class, tc.other)
			}
		})
	}
}

func TestTeardownDropsThePassphraseWithNoSibling(t *testing.T) {
	t.Parallel()

	apis, _, ssmc, _ := teardownFakes(t)

	if err := Teardown(context.Background(), apis, ClassProduction, nil, nil); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if _, held := ssmc.params[PassphraseParamName]; held {
		t.Error("nothing else is bootstrapped, so the passphrase must go with the substrate")
	}
}
