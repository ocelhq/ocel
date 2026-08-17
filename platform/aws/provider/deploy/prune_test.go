package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestReclaimDeclaresOneChildStagePerTarget(t *testing.T) {
	t.Parallel()

	targets := []PruneTarget{
		reclaimedFor(t, "prod", "shop", "web", deployedAs("build-1")),
		reclaimedFor(t, "prod", "shop", "api", deployedAs("build-2")),
	}
	ft := &fakeTracer{}
	parent := NewRootStage("Reclaiming deployments")

	if err := Reclaim(context.Background(), Config{Tracer: ft}, targets, parent, nil); err == nil {
		t.Fatal("Reclaim err = nil, want the stack-less Destroy to fail fast")
	}

	if len(ft.declared) != 1 || !ft.final[0] {
		t.Fatalf("declared = %+v final = %v, want exactly one final declaration", ft.declared, ft.final)
	}
	children := ft.declared[0]
	if len(children) != len(targets) {
		t.Fatalf("declared %d children, want one per reclaimed deployment (%d)", len(children), len(targets))
	}
	for i, child := range children {
		if child.ParentID != parent.ID {
			t.Errorf("child[%d].ParentID = %v, want the Reclaim stage %v", i, child.ParentID, parent.ID)
		}
		if child.Title != reclaimTitle(targets[i]) {
			t.Errorf("child[%d].Title = %q, want %q", i, child.Title, reclaimTitle(targets[i]))
		}
	}

	if len(ft.spans) != len(targets)+1 {
		t.Fatalf("recorded %d spans, want one per target plus the Reclaim parent", len(ft.spans))
	}
	wantIDs := make(map[StageID]bool, len(children))
	for _, child := range children {
		wantIDs[child.ID] = true
	}
	var sawParent bool
	for _, span := range ft.spans {
		if span.id == parent.ID {
			sawParent = true
		} else if !wantIDs[span.id] {
			t.Errorf("span id %v does not match any declared child stage or the Reclaim parent", span.id)
		}
		if span.err == nil {
			t.Errorf("span for %v has err = nil, want the stack-less Destroy failure reported on its own stage", span.id)
		}
	}
	if !sawParent {
		t.Error("no span was recorded for the Reclaim parent stage")
	}
}

func recordKeyFor(app string, id Identity) string {
	return removedRecordKeyPrefix + app + "/" + id.String()
}

func reclaimedFor(t *testing.T, env, slug, app string, id Identity) PruneTarget {
	t.Helper()
	coord := storageCoordinate(env, slug, app, releaseOf(id))
	return PruneTarget{
		App:            app,
		Identity:       id,
		Stack:          appStack(t, env, app, id),
		AssetPrefix:    appAssetPrefix(coord),
		ImageConfigKey: coord.ImageConfigKey(),
		CachePrefix:    isrPrefixOf(coord),
		EdgePrefix:     appEdgePrefix(coord),
		FunctionPrefix: functionArtifactPrefix(coord),
	}
}

func TestReclaimTargets(t *testing.T) {
	t.Parallel()

	t.Run("derives stack and prefixes per record", func(t *testing.T) {
		t.Parallel()
		got, err := ReclaimTargets("proj1", "prod", []string{recordKeyFor("web", deployedAs("build-1")), recordKeyFor("api", deployedAs("build-2"))}, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}

		release := releaseTokenFor("build-1")
		want := []PruneTarget{
			{
				App:            "web",
				Identity:       deployedAs("build-1"),
				Stack:          appStack(t, "prod", "web", deployedAs("build-1")),
				AssetPrefix:    "prod/proj1/web/" + release + "/assets",
				ImageConfigKey: "prod/proj1/web/" + release + "/image-config.json",
				CachePrefix:    "prod/proj1/web/" + release + "/isr",
				EdgePrefix:     "prod/proj1/web/" + release + "/edge",
				FunctionPrefix: "prod/proj1/web/" + release + "/fn",
			},
			{
				App:            "api",
				Identity:       deployedAs("build-2"),
				Stack:          appStack(t, "prod", "api", deployedAs("build-2")),
				AssetPrefix:    "prod/proj1/api/" + releaseTokenFor("build-2") + "/assets",
				ImageConfigKey: "prod/proj1/api/" + releaseTokenFor("build-2") + "/image-config.json",
				CachePrefix:    "prod/proj1/api/" + releaseTokenFor("build-2") + "/isr",
				EdgePrefix:     "prod/proj1/api/" + releaseTokenFor("build-2") + "/edge",
				FunctionPrefix: "prod/proj1/api/" + releaseTokenFor("build-2") + "/fn",
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ReclaimTargets = %+v, want %+v", got, want)
		}
	})

	t.Run("every prefix of one record shares the release prefix", func(t *testing.T) {
		t.Parallel()
		got, err := ReclaimTargets("proj1", "prod", []string{recordKeyFor("web", deployedAs("build-1"))}, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		root := "prod/proj1/web/" + releaseTokenFor("build-1") + "/"
		for name, prefix := range map[string]string{
			"AssetPrefix":    got[0].AssetPrefix,
			"ImageConfigKey": got[0].ImageConfigKey,
			"CachePrefix":    got[0].CachePrefix,
			"EdgePrefix":     got[0].EdgePrefix,
			"FunctionPrefix": got[0].FunctionPrefix,
		} {
			if !strings.HasPrefix(prefix, root) {
				t.Errorf("%s = %q, want it under the one release prefix %q", name, prefix, root)
			}
		}
	})

	t.Run("another release of one app is a different prefix", func(t *testing.T) {
		t.Parallel()
		got, err := ReclaimTargets("proj1", "prod", []string{recordKeyFor("web", deployedAs("build-1"))}, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		other, err := ReclaimTargets("proj1", "prod", []string{recordKeyFor("web", deployedAs("build-2"))}, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		if other[0].EdgePrefix == got[0].EdgePrefix {
			t.Error("two releases of one app resolved the same edge prefix; pruning one would take the other's bundle")
		}
		if other[0].FunctionPrefix == got[0].FunctionPrefix {
			t.Error("two releases of one app resolved the same function prefix; pruning one would take the other's packages")
		}
	})

	t.Run("a rotated value fingerprint is its own release and its own storage", func(t *testing.T) {
		t.Parallel()
		id := fingerprinted("build-1", "fp1")
		got, err := ReclaimTargets("proj1", "prod", []string{recordKeyFor("web", id)}, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		if !reflect.DeepEqual(got, []PruneTarget{reclaimedFor(t, "prod", "proj1", "web", id)}) {
			t.Errorf("ReclaimTargets = %+v, want %+v", got, reclaimedFor(t, "prod", "proj1", "web", id))
		}
		plain, err := ReclaimTargets("proj1", "prod", []string{recordKeyFor("web", deployedAs("build-1"))}, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		if plain[0].Stack == got[0].Stack || plain[0].AssetPrefix == got[0].AssetPrefix {
			t.Error("two deployments of one build with different values resolved the same stack or storage")
		}
	})

	t.Run("a surviving deployment of the same release keeps its storage", func(t *testing.T) {
		t.Parallel()
		id := fingerprinted("build-1", "fp1")
		got, err := ReclaimTargets("proj1", "prod",
			[]string{recordKeyFor("web", id)},
			[]string{recordKeyFor("web", id)},
			[]string{recordKeyFor("web", id)})
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		want := []PruneTarget{{
			App:      "web",
			Identity: id,
			Stack:    appStack(t, "prod", "web", id),
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ReclaimTargets = %+v, want the stack alone %+v", got, want)
		}
	})

	t.Run("another app or another release does not shield this one", func(t *testing.T) {
		t.Parallel()
		got, err := ReclaimTargets("proj1", "prod",
			[]string{recordKeyFor("web", deployedAs("build-1"))},
			[]string{recordKeyFor("web", deployedAs("build-2")), recordKeyFor("api", deployedAs("build-1"))},
			[]string{recordKeyFor("web", deployedAs("build-2")), recordKeyFor("api", deployedAs("build-1"))})
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		if !reflect.DeepEqual(got, []PruneTarget{reclaimedFor(t, "prod", "proj1", "web", deployedAs("build-1"))}) {
			t.Errorf("ReclaimTargets = %+v, want every prefix reclaimed", got)
		}
	})

	t.Run("a survivor on another pointer keeps the shared prefixes but not the cache", func(t *testing.T) {
		t.Parallel()
		shared := fingerprinted("B1", "fpP")
		got, err := ReclaimTargets("proj1", "prod",
			[]string{recordKeyFor("web", shared)},
			[]string{recordKeyFor("web", shared)},
			nil)
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		want := []PruneTarget{{
			App:         "web",
			Identity:    shared,
			Stack:       appStack(t, "prod", "web", shared),
			CachePrefix: isrPrefixOf(storageCoordinate("prod", "proj1", "web", releaseOf(shared))),
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ReclaimTargets = %+v, want %+v", got, want)
		}
	})

	t.Run("the env leads every prefix", func(t *testing.T) {
		t.Parallel()
		got, err := ReclaimTargets("proj1", "pr-7", []string{recordKeyFor("web", deployedInto("pr-7", "B1", ""))}, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		if want := "pr-7/proj1/web/" + releaseOf(deployedInto("pr-7", "B1", "")).String() + "/isr"; got[0].CachePrefix != want {
			t.Errorf("CachePrefix = %q, want %q", got[0].CachePrefix, want)
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
		for _, key := range []string{"no-slash", "record:/build-1", "record:web/", "record:web/build-1"} {
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

func TestReclaimCoversEveryKeyTheDeployWrote(t *testing.T) {
	root := writeTree(t, map[string]string{
		"apps/web/routing-manifest.json":      `{"buildId":"WEB1"}`,
		"apps/web/image-config.json":          `{"formats":["image/webp"]}`,
		"apps/web/static/logo.png":            "PNG",
		"apps/web/cache/index.cache.json":     `{"lastModified":1,"value":{"kind":"APP_PAGE"}}`,
		"apps/web/fetch-cache/abc.cache.json": `{"lastModified":1,"value":{"kind":"FETCH"}}`,
		"apps/web/edge/bundle.json":           `{"version":1,"mainModule":"main.js"}`,
		"web.func/index.js":                   "export const handler = () => {}",
	})
	account := &fakeUploader{exists: map[string]bool{}}
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot:       root,
		Env:                "prod",
		ArtifactBucket:     "artifacts",
		AssetBucket:        "assets",
		Uploader:           account,
		CacheStoreBucket:   "isr",
		CacheStoreUploader: store,
		Edge:               testLoaderEdge(),
	}
	manifest := &deploymentsv1.Manifest{
		Slug: "proj",
		Apps: []*deploymentsv1.ManifestApp{{Name: "web", Framework: frameworkNext}},
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index", Framework: frameworkNext, App: "web", ArtifactPath: "web.func"},
		},
	}

	builds := releaseBuilds(t, cfg, manifest, "fp1")
	ctx := context.Background()
	if _, err := uploadFunctionArtifacts(ctx, cfg, manifest, nil, builds, nil); err != nil {
		t.Fatalf("uploadFunctionArtifacts: %v", err)
	}
	if err := uploadPrerenderAssets(ctx, cfg, builds); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}
	if err := uploadStaticAssets(ctx, cfg, manifest, builds); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}
	if err := uploadEdgeBundles(ctx, cfg, manifest, builds); err != nil {
		t.Fatalf("uploadEdgeBundles: %v", err)
	}
	record, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[0], builds.identities["web"], nil, builds)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if err := writeOriginRecords(ctx, cfg, []appDeployResult{{App: "web", Record: record}}); err != nil {
		t.Fatalf("writeOriginRecords: %v", err)
	}

	id := builds.identities["web"]
	targets, err := ReclaimTargets("proj", "prod", []string{recordKeyFor("web", id)}, nil, nil)
	if err != nil {
		t.Fatalf("ReclaimTargets: %v", err)
	}
	target := targets[0]
	reclaimed := map[string][]string{
		"artifacts": {target.FunctionPrefix},
		"assets":    {target.AssetPrefix, target.ImageConfigKey, target.CachePrefix},
		"isr":       {target.AssetPrefix, target.CachePrefix, target.EdgePrefix},
	}

	written := map[string][]string{}
	for _, up := range []*fakeUploader{account, store} {
		for i, key := range up.puts {
			written[up.buckets[i]] = append(written[up.buckets[i]], key)
		}
	}
	if len(written) != 3 {
		t.Fatalf("the deploy wrote into %v, want all three buckets", written)
	}
	for bucket, keys := range written {
		for _, key := range keys {
			covered := false
			for _, prefix := range reclaimed[bucket] {
				if prefix != "" && strings.HasPrefix(key, prefix) {
					covered = true
				}
			}
			if !covered {
				t.Errorf("the deploy wrote %s/%s and the prune of that release deletes no prefix covering it", bucket, key)
			}
		}
	}
}
