package deploy

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestReclaimTargets_DerivesStackAndPrefixesPerRecord(t *testing.T) {
	got, err := ReclaimTargets("proj1", "prod", []string{"record:web/build-1", "record:api/build-2"}, nil, nil)
	if err != nil {
		t.Fatalf("ReclaimTargets: %v", err)
	}

	want := []PruneTarget{
		{
			App:            "web",
			Identity:       buildOnly("build-1"),
			Stack:          AppDeployStackName("proj1", "web", buildOnly("build-1")),
			AssetPrefix:    appAssetR2Prefix("proj1", "web", "build-1"),
			ImageConfigKey: imageConfigKey("proj1", "web", "build-1"),
			CachePrefix:    appAssetPrefixFor("prod", "proj1", "web", "build-1"),
			EdgePrefix:     appEdgeR2Prefix("proj1", "web", "build-1"),
		},
		{
			App:            "api",
			Identity:       buildOnly("build-2"),
			Stack:          AppDeployStackName("proj1", "api", buildOnly("build-2")),
			AssetPrefix:    appAssetR2Prefix("proj1", "api", "build-2"),
			ImageConfigKey: imageConfigKey("proj1", "api", "build-2"),
			CachePrefix:    appAssetPrefixFor("prod", "proj1", "api", "build-2"),
			EdgePrefix:     appEdgeR2Prefix("proj1", "api", "build-2"),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReclaimTargets = %+v, want %+v", got, want)
	}
}

func TestReclaimTargets_ReclaimsTheBuildsEdgePrefix(t *testing.T) {
	got, err := ReclaimTargets("proj1", "prod", []string{"record:web/build-1"}, nil, nil)
	if err != nil {
		t.Fatalf("ReclaimTargets: %v", err)
	}
	if want := "edge/proj1/web/build-1"; got[0].EdgePrefix != want {
		t.Errorf("EdgePrefix = %q, want %q", got[0].EdgePrefix, want)
	}
	other, err := ReclaimTargets("proj1", "prod", []string{"record:web/build-2"}, nil, nil)
	if err != nil {
		t.Fatalf("ReclaimTargets: %v", err)
	}
	if other[0].EdgePrefix == got[0].EdgePrefix {
		t.Error("two builds of one app resolved the same edge prefix; pruning one would take the other's bundle")
	}
}

func TestReclaimTargets_FingerprintedIdentityKeysTheStackNotThePrefixes(t *testing.T) {
	id := fingerprinted("build-1", "fp1")
	got, err := ReclaimTargets("proj1", "prod", []string{"record:web/" + id.String()}, nil, nil)
	if err != nil {
		t.Fatalf("ReclaimTargets: %v", err)
	}
	want := PruneTarget{
		App:            "web",
		Identity:       id,
		Stack:          AppDeployStackName("proj1", "web", id),
		AssetPrefix:    appAssetR2Prefix("proj1", "web", "build-1"),
		ImageConfigKey: imageConfigKey("proj1", "web", "build-1"),
		CachePrefix:    appAssetPrefixFor("prod", "proj1", "web", "build-1"),
		EdgePrefix:     appEdgeR2Prefix("proj1", "web", "build-1"),
	}
	if !reflect.DeepEqual(got, []PruneTarget{want}) {
		t.Errorf("ReclaimTargets = %+v, want %+v", got, want)
	}
	plain, err := ReclaimTargets("proj1", "prod", []string{"record:web/build-1"}, nil, nil)
	if err != nil {
		t.Fatalf("ReclaimTargets: %v", err)
	}
	if plain[0].Stack == got[0].Stack {
		t.Error("two Deployments of one build resolved the same app-deploy stack")
	}
}

func TestReclaimTargets_BuildASurvivingDeploymentSharesKeepsItsStorage(t *testing.T) {
	rotated := fingerprinted("build-1", "fp2")
	got, err := ReclaimTargets("proj1", "prod",
		[]string{"record:web/build-1"},
		[]string{"record:web/" + rotated.String()},
		[]string{"record:web/" + rotated.String()})
	if err != nil {
		t.Fatalf("ReclaimTargets: %v", err)
	}
	want := []PruneTarget{{
		App:      "web",
		Identity: buildOnly("build-1"),
		Stack:    AppDeployStackName("proj1", "web", buildOnly("build-1")),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReclaimTargets = %+v, want the stack alone %+v", got, want)
	}
}

func TestReclaimTargets_LastDeploymentOfABuildStillReclaimsItsStorage(t *testing.T) {
	got, err := ReclaimTargets("proj1", "prod",
		[]string{"record:web/build-1"},
		[]string{"record:web/build-2", "record:api/build-1"},
		[]string{"record:web/build-2", "record:api/build-1"})
	if err != nil {
		t.Fatalf("ReclaimTargets: %v", err)
	}
	want := []PruneTarget{{
		App:            "web",
		Identity:       buildOnly("build-1"),
		Stack:          AppDeployStackName("proj1", "web", buildOnly("build-1")),
		AssetPrefix:    appAssetR2Prefix("proj1", "web", "build-1"),
		ImageConfigKey: imageConfigKey("proj1", "web", "build-1"),
		CachePrefix:    appAssetPrefixFor("prod", "proj1", "web", "build-1"),
		EdgePrefix:     appEdgeR2Prefix("proj1", "web", "build-1"),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReclaimTargets = %+v, want %+v", got, want)
	}
}

func TestReclaimTargets_ASurvivorOnAnotherPointerKeepsOnlyTheEnvlessPrefixes(t *testing.T) {
	pruned := fingerprinted("B1", "fpP")
	preview := fingerprinted("B1", "fpV")
	got, err := ReclaimTargets("proj1", "prod",
		[]string{"record:web/" + pruned.String()},
		[]string{"record:web/" + preview.String()},
		nil)
	if err != nil {
		t.Fatalf("ReclaimTargets: %v", err)
	}
	want := []PruneTarget{{
		App:         "web",
		Identity:    pruned,
		Stack:       AppDeployStackName("proj1", "web", pruned),
		CachePrefix: appAssetPrefixFor("prod", "proj1", "web", "B1"),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReclaimTargets = %+v, want %+v", got, want)
	}
}

func TestPreviewReclaimTargets_RemovingAPointerReclaimsItsCacheButNotTheSharedPrefixes(t *testing.T) {
	pruned := fingerprinted("B1", "fpV")
	got, err := PreviewReclaimTargets("proj1", "pr-7", "preview-pr-7",
		[]string{"record:web/" + pruned.String()},
		[]string{"record:web/" + fingerprinted("B1", "fpP").String()},
		nil)
	if err != nil {
		t.Fatalf("PreviewReclaimTargets: %v", err)
	}
	want := []PruneTarget{{
		App:         "web",
		Identity:    pruned,
		Stack:       PreviewAppDeployStackName("proj1", "pr-7", "web", pruned),
		CachePrefix: appAssetPrefixFor("preview-pr-7", "proj1", "web", "B1"),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PreviewReclaimTargets = %+v, want %+v", got, want)
	}
}

func TestReclaimTargets_EmptyInputYieldsNil(t *testing.T) {
	got, err := ReclaimTargets("proj1", "prod", nil, nil, nil)
	if err != nil {
		t.Fatalf("ReclaimTargets: %v", err)
	}
	if got != nil {
		t.Errorf("ReclaimTargets = %v, want nil", got)
	}
}

func TestReclaimTargets_MalformedKeyErrors(t *testing.T) {
	for _, key := range []string{"no-slash", "record:/build-1", "record:web/"} {
		if _, err := ReclaimTargets("proj1", "prod", []string{key}, nil, nil); err == nil {
			t.Errorf("ReclaimTargets(%q) err = nil, want an error for a malformed key", key)
		}
	}
}

type fakePrefixDeleter struct {
	pages     [][]string
	call      int
	deleted   []string
	listErr   error
	deleteErr error
}

func (f *fakePrefixDeleter) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.call >= len(f.pages) {
		return &s3.ListObjectsV2Output{}, nil
	}
	page := f.pages[f.call]
	f.call++
	var contents []s3types.Object
	for _, k := range page {
		key := k
		contents = append(contents, s3types.Object{Key: &key})
	}
	return &s3.ListObjectsV2Output{
		Contents:    contents,
		IsTruncated: aws.Bool(f.call < len(f.pages)),
	}, nil
}

func (f *fakePrefixDeleter) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	for _, obj := range in.Delete.Objects {
		f.deleted = append(f.deleted, aws.ToString(obj.Key))
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func TestDeletePrefix_DeletesEveryObjectAcrossPages(t *testing.T) {
	fake := &fakePrefixDeleter{
		pages: [][]string{
			{"prod/proj1/web/build-1/cache/a", "prod/proj1/web/build-1/cache/b"},
			{"prod/proj1/web/build-1/fetch-cache/c"},
		},
	}

	if err := deletePrefix(context.Background(), fake, "bucket", "prod/proj1/web/build-1/"); err != nil {
		t.Fatalf("deletePrefix: %v", err)
	}

	want := []string{
		"prod/proj1/web/build-1/cache/a", "prod/proj1/web/build-1/cache/b",
		"prod/proj1/web/build-1/fetch-cache/c",
	}
	if !reflect.DeepEqual(fake.deleted, want) {
		t.Errorf("deleted = %v, want %v", fake.deleted, want)
	}
}

func TestDeletePrefix_EmptyBucketOrNilDeleterIsNoOp(t *testing.T) {
	if err := deletePrefix(context.Background(), nil, "bucket", "prefix"); err != nil {
		t.Errorf("deletePrefix with nil deleter: %v", err)
	}
	fake := &fakePrefixDeleter{}
	if err := deletePrefix(context.Background(), fake, "", "prefix"); err != nil {
		t.Errorf("deletePrefix with empty bucket: %v", err)
	}
	if fake.call != 0 {
		t.Errorf("expected no ListObjectsV2 call for an empty bucket, got %d", fake.call)
	}
}

func TestDeletePrefix_EmptyPrefixIsANoOp(t *testing.T) {
	fake := &fakePrefixDeleter{pages: [][]string{{"prod/proj1/web/build-1/cache/a"}}}
	if err := deletePrefix(context.Background(), fake, "bucket", ""); err != nil {
		t.Fatalf("deletePrefix: %v", err)
	}
	if fake.call != 0 || fake.deleted != nil {
		t.Errorf("deleted %v in %d list calls, want the whole bucket left alone", fake.deleted, fake.call)
	}
}

func TestDeletePrefix_NoMatchesIsANoOp(t *testing.T) {
	fake := &fakePrefixDeleter{pages: [][]string{{}}}
	if err := deletePrefix(context.Background(), fake, "bucket", "prefix"); err != nil {
		t.Errorf("deletePrefix: %v", err)
	}
	if fake.deleted != nil {
		t.Errorf("deleted = %v, want none", fake.deleted)
	}
}

func TestDeletePrefix_ListErrorPropagates(t *testing.T) {
	fake := &fakePrefixDeleter{listErr: errors.New("list failed")}
	if err := deletePrefix(context.Background(), fake, "bucket", "prefix"); err == nil {
		t.Error("deletePrefix err = nil, want the list error propagated")
	}
}

func TestDeletePrefix_DeleteErrorPropagates(t *testing.T) {
	fake := &fakePrefixDeleter{
		pages:     [][]string{{"k1"}},
		deleteErr: errors.New("delete failed"),
	}
	if err := deletePrefix(context.Background(), fake, "bucket", "prefix"); err == nil {
		t.Error("deletePrefix err = nil, want the delete error propagated")
	}
}

func TestAsPrefixDeleter_NarrowUploaderYieldsNil(t *testing.T) {
	var up ArtifactUploader = &fakeUploader{}
	if d := asPrefixDeleter(up); d != nil {
		t.Errorf("asPrefixDeleter = %v, want nil for an uploader with no delete capability", d)
	}
}

func TestAsPrefixDeleter_WiderUploaderIsRecovered(t *testing.T) {
	fake := &fakeUploaderWithDelete{}
	var up ArtifactUploader = fake
	if d := asPrefixDeleter(up); d == nil {
		t.Error("asPrefixDeleter = nil, want the PrefixDeleter capability recovered")
	}
}

type fakeUploaderWithDelete struct{ fakePrefixDeleter }

func (f *fakeUploaderWithDelete) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeUploaderWithDelete) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return nil, errors.New("not implemented")
}
