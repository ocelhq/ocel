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

func TestReclaimTargets(t *testing.T) {
	t.Parallel()

	t.Run("derives stack and prefixes per record", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("reclaims the build's edge prefix", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("fingerprinted identity keys the stack not the prefixes", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("build a surviving deployment shares keeps its storage", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("last deployment of a build still reclaims its storage", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("a survivor on another pointer keeps only the envless prefixes", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("removing a pointer reclaims its cache but not the shared prefixes", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("empty input yields nil", func(t *testing.T) {
		t.Parallel()
		got, err := ReclaimTargets("proj1", "prod", nil, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		if got != nil {
			t.Errorf("ReclaimTargets = %v, want nil", got)
		}
	})

	t.Run("malformed key errors", func(t *testing.T) {
		t.Parallel()
		for _, key := range []string{"no-slash", "record:/build-1", "record:web/"} {
			if _, err := ReclaimTargets("proj1", "prod", []string{key}, nil, nil); err == nil {
				t.Errorf("ReclaimTargets(%q) err = nil, want an error for a malformed key", key)
			}
		}
	})
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

func TestDeletePrefix(t *testing.T) {
	t.Run("deletes every object across pages", func(t *testing.T) {
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
	})

	noops := []struct {
		name       string
		nilDeleter bool
		pages      [][]string
		bucket     string
		prefix     string
		wantList   int
	}{
		{name: "nil deleter is a no-op", nilDeleter: true, bucket: "bucket", prefix: "prefix"},
		{name: "empty bucket is a no-op", prefix: "prefix"},
		{name: "empty prefix is a no-op", pages: [][]string{{"prod/proj1/web/build-1/cache/a"}}, bucket: "bucket"},
		{name: "no matches is a no-op", pages: [][]string{{}}, bucket: "bucket", prefix: "prefix", wantList: 1},
	}
	for _, tc := range noops {
		t.Run(tc.name, func(t *testing.T) {
			var deleter PrefixDeleter
			var fake *fakePrefixDeleter
			if !tc.nilDeleter {
				fake = &fakePrefixDeleter{pages: tc.pages}
				deleter = fake
			}

			if err := deletePrefix(context.Background(), deleter, tc.bucket, tc.prefix); err != nil {
				t.Fatalf("deletePrefix: %v", err)
			}
			if fake == nil {
				return
			}
			if fake.call != tc.wantList {
				t.Errorf("ListObjectsV2 calls = %d, want %d", fake.call, tc.wantList)
			}
			if fake.deleted != nil {
				t.Errorf("deleted = %v, want none", fake.deleted)
			}
		})
	}

	t.Run("list error propagates", func(t *testing.T) {
		fake := &fakePrefixDeleter{listErr: errors.New("list failed")}
		if err := deletePrefix(context.Background(), fake, "bucket", "prefix"); err == nil {
			t.Error("deletePrefix err = nil, want the list error propagated")
		}
	})

	t.Run("delete error propagates", func(t *testing.T) {
		fake := &fakePrefixDeleter{
			pages:     [][]string{{"k1"}},
			deleteErr: errors.New("delete failed"),
		}
		if err := deletePrefix(context.Background(), fake, "bucket", "prefix"); err == nil {
			t.Error("deletePrefix err = nil, want the delete error propagated")
		}
	})
}

func TestAsPrefixDeleter(t *testing.T) {
	t.Run("narrow uploader yields nil", func(t *testing.T) {
		var up ArtifactUploader = &fakeUploader{}
		if d := asPrefixDeleter(up); d != nil {
			t.Errorf("asPrefixDeleter = %v, want nil for an uploader with no delete capability", d)
		}
	})

	t.Run("wider uploader is recovered", func(t *testing.T) {
		fake := &fakeUploaderWithDelete{}
		var up ArtifactUploader = fake
		if d := asPrefixDeleter(up); d == nil {
			t.Error("asPrefixDeleter = nil, want the PrefixDeleter capability recovered")
		}
	})
}

type fakeUploaderWithDelete struct{ fakePrefixDeleter }

func (f *fakeUploaderWithDelete) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeUploaderWithDelete) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return nil, errors.New("not implemented")
}
