package assets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

type bucketObjects struct {
	mu      sync.Mutex
	objects map[string][]string
	asked   []string
	deleted []string
}

func (b *bucketObjects) store(t *testing.T) Store {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		bucket, object, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/storage/v1/b/"), "/o")
		object, _ = url.PathUnescape(strings.TrimPrefix(object, "/"))
		b.asked = append(b.asked, bucket)
		switch {
		case r.Method == http.MethodGet && object == "":
			var items []map[string]string
			for _, name := range b.objects[bucket] {
				if strings.HasPrefix(name, r.URL.Query().Get("prefix")) {
					items = append(items, map[string]string{"name": name, "bucket": bucket})
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == http.MethodGet:
			if !slices.Contains(b.objects[bucket], object) {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"code":404,"message":"No such object"}}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"name": object, "bucket": bucket})
		case r.Method == http.MethodDelete:
			b.deleted = append(b.deleted, bucket+"/"+object)
			b.objects[bucket] = slices.DeleteFunc(b.objects[bucket], func(name string) bool { return name == object })
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("the store called %s %s, which nothing here serves", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(server.Close)
	return Store{
		OpenClients: googletest.Opener(googletest.Clients(server.URL)),
		RemoveTagRecords: func(context.Context, environment.Tier, naming.StorageScope, progress.Log) error {
			return nil
		},
	}
}

func storedRelease() naming.Coordinate {
	return naming.Coordinate{Project: "shop", Env: "prod", App: "web", Release: naming.NewReleaseToken("r1", "")}
}

func TestEachKindIsKeptInItsDataClassesBucket(t *testing.T) {
	release := storedRelease()
	for key, bucket := range map[string]string{
		release.AssetKey("_next/static/a.js"):                      "ocel-prod-edge-k3x9q2",
		release.ObjectKey(naming.ObjectPages, "index.cache.json"):  "ocel-prod-edge-k3x9q2",
		release.ImageConfigKey():                                   "ocel-prod-edge-k3x9q2",
		release.ObjectKey(naming.ObjectFetchCache, "h.cache.json"): "ocel-prod-cache-k3x9q2",
		release.ObjectKey(naming.ObjectUseCache, "u.json"):         "ocel-prod-cache-k3x9q2",
		release.ObjectKey(naming.ObjectTagClock, "tag-clock.json"): "ocel-prod-cache-k3x9q2",
	} {
		objects := &bucketObjects{objects: map[string][]string{bucket: {key}}}
		present, err := objects.store(t).Has(context.Background(), provider.ArtifactRef{Tier: environment.TierProduction, Key: key})
		if err != nil || !present {
			t.Errorf("Has(%s) = %v, %v, want it found in %s", key, present, err, bucket)
		}
		if !slices.Equal(objects.asked, []string{bucket}) {
			t.Errorf("Has(%s) asked buckets %q, want only %s", key, objects.asked, bucket)
		}
	}
}

func TestAFunctionArtifactIsRefusedBecauseCloudRunRunsFunctionsFromImages(t *testing.T) {
	objects := &bucketObjects{objects: map[string][]string{}}
	key := storedRelease().FunctionArtifactKey("abc")
	_, err := objects.store(t).Has(context.Background(), provider.ArtifactRef{Tier: environment.TierProduction, Key: key})
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("Has(%s) = %v, want an invalid-input refusal: GCP keeps no artifacts bucket", key, err)
	}
	if len(objects.asked) != 0 {
		t.Errorf("Has(%s) asked buckets %q, want none", key, objects.asked)
	}
}

func TestRemovingObjectsTakesEachKindsPrefixFromItsOwnBucketAndNothingBeside(t *testing.T) {
	release := storedRelease()
	sibling := release
	sibling.Env = "pr-7"
	objects := &bucketObjects{objects: map[string][]string{
		"ocel-preview-edge-k3x9q2": {
			release.AssetKey("a.js"), release.ObjectKey(naming.ObjectPages, "i.cache.json"), sibling.AssetKey("a.js"),
		},
		"ocel-preview-cache-k3x9q2": {
			release.ObjectKey(naming.ObjectFetchCache, "h.cache.json"), sibling.ObjectKey(naming.ObjectUseCache, "u.json"),
		},
	}}
	scope, err := naming.EnvScope(release.Project, release.Env)
	if err != nil {
		t.Fatal(err)
	}

	if err := objects.store(t).RemoveObjects(context.Background(), environment.TierPreview, scope, naming.CoordinateObjectKinds, nil); err != nil {
		t.Fatalf("RemoveObjects() = %v", err)
	}
	slices.Sort(objects.deleted)
	want := []string{
		"ocel-preview-cache-k3x9q2/" + release.ObjectKey(naming.ObjectFetchCache, "h.cache.json"),
		"ocel-preview-edge-k3x9q2/" + release.AssetKey("a.js"),
		"ocel-preview-edge-k3x9q2/" + release.ObjectKey(naming.ObjectPages, "i.cache.json"),
	}
	if !slices.Equal(objects.deleted, want) {
		t.Errorf("RemoveObjects() deleted %q, want exactly %q", objects.deleted, want)
	}
}

func TestRemovingObjectsWhereTheTierRecordsNoBucketSaysNothingWasRemoved(t *testing.T) {
	scope, err := naming.ProjectScope("shop")
	if err != nil {
		t.Fatal(err)
	}
	unrecorded := &gcloud.Clients{Names: googletest.Names, Region: googletest.Region, Endpoint: "http://127.0.0.1:1", Records: fake.NewKeyValues()}
	store := Store{
		OpenClients:      googletest.Opener(unrecorded),
		RemoveTagRecords: func(context.Context, environment.Tier, naming.StorageScope, progress.Log) error { return nil },
	}
	log := &fake.Log{}

	if err := store.RemoveObjects(context.Background(), environment.TierPreview, scope, naming.CoordinateObjectKinds, log); err != nil {
		t.Fatalf("RemoveObjects() = %v", err)
	}
	if lines := log.Lines(); len(lines) != 0 {
		t.Errorf("RemoveObjects() with no bucket recorded said %q, want nothing: it removed no objects", lines)
	}
}
