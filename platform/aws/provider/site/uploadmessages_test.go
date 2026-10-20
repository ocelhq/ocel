package site

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestAnUploadSaysWhichAppAndHowManyFilesOrWhere(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		tree func(*testing.T) string
		set  func(publication, appBuilds) (*AssetSet, error)
		want string
	}{
		{
			name: "static assets",
			tree: staticAppTree,
			set: func(cfg publication, builds appBuilds) (*AssetSet, error) {
				return staticAssetSet(cfg.buckets, cfg.root, "web", nextStatic, builds.coords["web"])
			},
			want: "INFO Uploading web's 2 static assets",
		},
		{
			name: "prerender cache entries",
			tree: twoAppTree,
			set: func(cfg publication, builds appBuilds) (*AssetSet, error) {
				return prerenderAssetSet(cfg.buckets, cfg.root, "web", builds.caches["web"])
			},
			want: "INFO Uploading web's 1 prerender cache entry",
		},
		{
			name: "an edge bundle",
			tree: edgeAppTree,
			set: func(cfg publication, builds appBuilds) (*AssetSet, error) {
				set, _, err := edgeBundleSet(cfg.buckets, cfg.root, "web", builds.coords["web"], builds.baked["web"])
				return set, err
			},
			want: "INFO Uploading web's edge bundle to bucket edge",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := publication{root: tc.tree(t), buckets: Buckets{Edge: "edge", Cache: "cache", Objects: &fakeArtifactStore{exists: map[string]bool{}}, EdgeHoldsKey: true}}
			set, err := tc.set(cfg, appBuildsFor(t, cfg, twoAppManifest()))
			if err != nil {
				t.Fatalf("the %s set: %v", tc.name, err)
			}
			var progress fake.Log
			if err := set.push(context.Background(), &progress); err != nil {
				t.Fatalf("push: %v", err)
			}
			if !slices.Contains(progress.Lines(), tc.want) {
				t.Errorf("the upload said %q, want %q", progress.Lines(), tc.want)
			}
		})
	}
}
