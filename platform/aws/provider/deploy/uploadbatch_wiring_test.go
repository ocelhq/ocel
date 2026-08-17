package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadFunctionArtifactsEmitsOneBatchSpan(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	cfg := Config{
		ArtifactRoot: twoAppTree(t), ArtifactBucket: "artifacts", AssetBucket: "assets", Env: "prod",
		Uploader: &fakeUploader{exists: map[string]bool{}},
		Stages:   Stages{Uploading: NewRootStage("Uploading")},
		Tracer:   ft,
	}
	manifest := twoAppManifest()
	builds := appBuildsFor(t, cfg, manifest)

	if _, err := uploadFunctionArtifacts(context.Background(), cfg, manifest, builds, nil); err != nil {
		t.Fatalf("uploadFunctionArtifacts: %v", err)
	}

	batches := 0
	for _, sp := range ft.spans {
		if sp.name == uploadBatchSpanName(uploadKindFunctionArtifact) {
			batches++
			if got := attrValue(sp.attrs, AttrResourceCount(0).Key); got != "2" {
				t.Errorf("resource count = %q, want %q (two functions)", got, "2")
			}
			if sp.parentID != cfg.Stages.Uploading.ID {
				t.Errorf("parentID = %v, want the Uploading stage id", sp.parentID)
			}
		}
	}
	if batches != 1 {
		t.Fatalf("batch spans = %d, want 1", batches)
	}
}

func TestUploadFunctionArtifactsFailurePathEmitsAStandoutPerFailure(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	denied := errors.New("AccessDenied: bucket policy forbids this principal")
	cfg := Config{
		ArtifactRoot: twoAppTree(t), ArtifactBucket: "artifacts", AssetBucket: "assets", Env: "prod",
		Uploader: &fakeUploader{headErr: denied},
		Stages:   Stages{Uploading: NewRootStage("Uploading")},
		Tracer:   ft,
	}
	manifest := twoAppManifest()
	builds := appBuildsFor(t, cfg, manifest)

	if _, err := uploadFunctionArtifacts(context.Background(), cfg, manifest, builds, nil); err == nil {
		t.Fatal("uploadFunctionArtifacts = nil error, want the HeadObject failure surfaced")
	}

	failureSpans := 0
	for _, sp := range ft.spans {
		if sp.name == uploadStandoutName(uploadKindFunctionArtifact, true) {
			failureSpans++
			if sp.err == nil {
				t.Error("failure span has no error")
			}
			for _, a := range sp.attrs {
				if strings.Contains(a.Value, "bucket policy") {
					t.Errorf("failure span attribute leaked the raw error text: %v", a)
				}
			}
		}
	}
	if failureSpans == 0 {
		t.Fatal("got 0 failure spans, want at least 1")
	}
}

func TestUploadStaticAssetsEmitsOneBatchSpan(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: imageConfigTree(t), AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
		Stages: Stages{Uploading: NewRootStage("Uploading")},
		Tracer: ft,
	}
	manifest := nextManifest()
	builds := appBuildsFor(t, cfg, manifest)

	if err := uploadStaticAssets(context.Background(), cfg, manifest, builds); err != nil {
		t.Fatalf("uploadStaticAssets: %v", err)
	}

	batches := 0
	for _, sp := range ft.spans {
		if sp.name == uploadBatchSpanName(uploadKindStaticAsset) {
			batches++
			if got := attrValue(sp.attrs, AttrResourceCount(0).Key); got != "3" {
				t.Errorf("resource count = %q, want %q (logo.png mirrored to both targets, plus the image config)", got, "3")
			}
		}
	}
	if batches != 1 {
		t.Fatalf("batch spans = %d, want 1", batches)
	}
}

func TestUploadStaticAssetsRecordsAReadFailureAsAStandout(t *testing.T) {
	t.Parallel()

	root := imageConfigTree(t)
	path := filepath.Join(root, "apps/web/image-config.json")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod image config: %v", err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this environment can read a chmod 0000 file (likely running as root)")
	}

	ft := &fakeTracer{}
	store := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot: root, AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: store,
		Stages: Stages{Uploading: NewRootStage("Uploading")},
		Tracer: ft,
	}
	manifest := nextManifest()
	builds := appBuildsFor(t, cfg, manifest)

	if err := uploadStaticAssets(context.Background(), cfg, manifest, builds); err == nil {
		t.Fatal("uploadStaticAssets = nil, want the missing image config source surfaced")
	}

	failureSpans := 0
	for _, sp := range ft.spans {
		if sp.name == uploadStandoutName(uploadKindStaticAsset, true) {
			failureSpans++
			if sp.err == nil {
				t.Error("failure standout span has no error")
			}
		}
	}
	if failureSpans == 0 {
		t.Fatal("got 0 failure spans, want at least 1 for the local read failure")
	}
}

func TestUploadEdgeBundlesEmitsOneBatchSpan(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	cfg := Config{
		ArtifactRoot: edgeAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: &fakeUploader{exists: map[string]bool{}},
		Stages: Stages{Uploading: NewRootStage("Uploading")},
		Tracer: ft,
	}
	manifest := twoAppManifest()
	builds := appBuildsFor(t, cfg, manifest)

	if err := uploadEdgeBundles(context.Background(), cfg, manifest, builds); err != nil {
		t.Fatalf("uploadEdgeBundles: %v", err)
	}

	batches := 0
	for _, sp := range ft.spans {
		if sp.name == uploadBatchSpanName(uploadKindEdgeBundle) {
			batches++
			if got := attrValue(sp.attrs, AttrResourceCount(0).Key); got != "1" {
				t.Errorf("resource count = %q, want %q (only web has an edge bundle)", got, "1")
			}
			if sp.parentID != cfg.Stages.Uploading.ID {
				t.Errorf("parentID = %v, want the Uploading stage id", sp.parentID)
			}
		}
	}
	if batches != 1 {
		t.Fatalf("batch spans = %d, want 1", batches)
	}
}

func TestUploadEdgeBundlesFailurePathEmitsAStandout(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	denied := errors.New("AccessDenied: bucket policy forbids this principal")
	cfg := Config{
		ArtifactRoot: edgeAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader:         &fakeUploader{exists: map[string]bool{}},
		CacheStoreBucket: "isr", CacheStoreUploader: &fakeUploader{putErr: denied},
		Stages: Stages{Uploading: NewRootStage("Uploading")},
		Tracer: ft,
	}
	manifest := twoAppManifest()
	builds := appBuildsFor(t, cfg, manifest)

	if err := uploadEdgeBundles(context.Background(), cfg, manifest, builds); err == nil {
		t.Fatal("uploadEdgeBundles = nil error, want the PutObject failure surfaced")
	}

	failureSpans := 0
	for _, sp := range ft.spans {
		if sp.name == uploadStandoutName(uploadKindEdgeBundle, true) {
			failureSpans++
			if sp.err == nil {
				t.Error("failure span has no error")
			}
			for _, a := range sp.attrs {
				if strings.Contains(a.Value, "bucket policy") {
					t.Errorf("failure span attribute leaked the raw error text: %v", a)
				}
			}
		}
	}
	if failureSpans == 0 {
		t.Fatal("got 0 failure spans, want at least 1")
	}
}

func TestUploadEdgeBundlesWithNoAdoptedStoreEmitsNoSpan(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	cfg := Config{
		ArtifactRoot: edgeAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader: &fakeUploader{exists: map[string]bool{}},
		Stages:   Stages{Uploading: NewRootStage("Uploading")},
		Tracer:   ft,
	}
	manifest := twoAppManifest()
	builds := appBuildsFor(t, cfg, manifest)

	if err := uploadEdgeBundles(context.Background(), cfg, manifest, builds); err != nil {
		t.Fatalf("uploadEdgeBundles: %v", err)
	}

	for _, sp := range ft.spans {
		if strings.Contains(sp.name, "edge bundle") {
			t.Errorf("span %q emitted with nothing uploaded", sp.name)
		}
	}
}

func TestUploadPrerenderAssetsEmitsOneBatchSpan(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	cfg := Config{
		ArtifactRoot: twoAppTree(t), AssetBucket: "assets", Env: "prod",
		Uploader: &fakeUploader{exists: map[string]bool{}},
		Stages:   Stages{Uploading: NewRootStage("Uploading")},
		Tracer:   ft,
	}
	manifest := twoAppManifest()
	builds := appBuildsFor(t, cfg, manifest)

	if err := uploadPrerenderAssets(context.Background(), cfg, builds); err != nil {
		t.Fatalf("uploadPrerenderAssets: %v", err)
	}

	batches := 0
	for _, sp := range ft.spans {
		if sp.name == uploadBatchSpanName(uploadKindPrerenderAsset) {
			batches++
			if got := attrValue(sp.attrs, AttrResourceCount(0).Key); got != "3" {
				t.Errorf("resource count = %q, want %q (three prerendered files)", got, "3")
			}
		}
	}
	if batches != 1 {
		t.Fatalf("batch spans = %d, want 1", batches)
	}
}
