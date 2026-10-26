package cloudflare

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func keptObjectsOn(t *testing.T, m *cfMock) KeptObjects {
	t.Helper()
	p := m.provider(t)
	p.KeepsObjects = true
	return KeptObjects{p: p}
}

func keptBucketName(t *testing.T, tier environment.Tier) string {
	t.Helper()
	bucket, err := testNames.objectsBucket(tier)
	if err != nil {
		t.Fatal(err)
	}
	return bucket
}

func installKeptBootstrap(t *testing.T, m *cfMock, tier environment.Tier) {
	t.Helper()
	planned, err := testNames.bootstrapWorkers(tier, keptBucketName(t, tier))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range planned {
		content, contentType, err := buildDurableObjectScriptMultipart(b.worker, b.do, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		m.putBodies[b.scriptName] = putBody{contentType: contentType, content: content}
		m.scriptSettings[b.scriptName] = putSettings(m.putBodies[b.scriptName])
		m.scriptSecrets[b.scriptName] = []string{bootstrapSecretBinding}
	}
}

func TestTheStoreAnEdgeKeepsObjectsInIsInstalledWithItsBucketAndTheCredentialItWasHanded(t *testing.T) {
	seedBootstrapBundle(t, "export default {}")
	m := bootstrapMock(t)
	m.keptBuckets = map[string]bool{}
	kept := keptObjectsOn(t, m)

	store, err := kept.EnsureStore(t.Context(), environment.TierProduction, "box-credential")
	if err != nil {
		t.Fatalf("EnsureStore: %v", err)
	}

	bucket := keptBucketName(t, environment.TierProduction)
	if !m.keptBuckets[bucket] {
		t.Errorf("buckets %v, want %s created to hold the origin's objects", m.keptBuckets, bucket)
	}
	put, found := m.putBodies[sharedStoreScriptName]
	if !found {
		t.Fatalf("uploaded %v, want the releases store", m.putScripts)
	}
	meta := putMetadata(put)
	bound := bindingsByType(meta, "r2_bucket")
	if len(bound) != 1 || bound[0]["name"] != storeObjectsBinding || bound[0]["bucket_name"] != bucket {
		t.Errorf("the releases store binds %v, want %s bound to %s", bound, storeObjectsBinding, bucket)
	}
	secrets := bindingsByType(meta, "secret_text")
	if len(secrets) != 1 || secrets[0]["text"] != "box-credential" {
		t.Errorf("the releases store is handed secrets %v, want the credential the origin keeps", secrets)
	}
	if store.BootstrapCredential != "box-credential" || store.Endpoint == "" {
		t.Errorf("EnsureStore = %+v, want the store's endpoint and the credential", store)
	}
}

func TestAStoreAlreadyHoldingACredentialIsHandedTheOneTheOriginKeeps(t *testing.T) {
	seedBootstrapBundle(t, "export default {}")
	m := bootstrapMock(t)
	m.keptBuckets = map[string]bool{}
	kept := keptObjectsOn(t, m)
	if _, err := kept.EnsureStore(t.Context(), environment.TierProduction, "lost"); err != nil {
		t.Fatalf("EnsureStore: %v", err)
	}

	if _, err := kept.EnsureStore(t.Context(), environment.TierProduction, "kept"); err != nil {
		t.Fatalf("the second EnsureStore: %v", err)
	}

	if last := m.putSecrets[len(m.putSecrets)-1]; last.text != "kept" {
		t.Errorf("the store was last handed %q, want kept: the origin's own record is the credential it initializes projects with", last.text)
	}
}

type keptRequest struct {
	path, bearer string
	keys         []string
	body         string
}

func keptStoreServer(t *testing.T) (*httptest.Server, *[]keptRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []keptRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := keptRequest{path: r.URL.Path, bearer: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")}
		if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && params["boundary"] != "" {
			form := multipart.NewReader(r.Body, params["boundary"])
			for part, err := form.NextPart(); err == nil; part, err = form.NextPart() {
				request.keys = append(request.keys, part.FormName())
			}
		} else {
			raw, _ := io.ReadAll(r.Body)
			request.body = string(raw)
		}
		mu.Lock()
		seen = append(seen, request)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestKeptObjectsArePutInBatchesUnderTheProjectWithTheStoresCredential(t *testing.T) {
	srv, seen := keptStoreServer(t)
	store := edge.ReleasesStore{Endpoint: srv.URL, BootstrapCredential: "box-credential"}
	large := make([]byte, objectBatchBytes)

	err := KeptObjects{p: &cloudflare{}}.Put(t.Context(), store, "shop", []KeptObject{
		{Key: "assets/shop/prod/web/r1/a.js", ContentType: "text/javascript", Body: large},
		{Key: "assets/shop/prod/web/r1/b.js", ContentType: "text/javascript", Body: []byte("b")},
		{Key: "assets/shop/prod/web/r1/c.js", ContentType: "text/javascript", Body: []byte("c")},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if len(*seen) != 2 {
		t.Fatalf("the store was sent %d requests, want two batches", len(*seen))
	}
	first, second := (*seen)[0], (*seen)[1]
	if first.path != "/shop/objects" || first.bearer != "box-credential" || !slices.Equal(first.keys, []string{"assets/shop/prod/web/r1/a.js"}) {
		t.Errorf("first batch = %+v, want a.js alone under /shop/objects", first)
	}
	if !slices.Equal(second.keys, []string{"assets/shop/prod/web/r1/b.js", "assets/shop/prod/web/r1/c.js"}) {
		t.Errorf("second batch = %+v, want b.js and c.js", second)
	}
}

func TestKeptObjectsAreRemovedByThePrefixesNamed(t *testing.T) {
	srv, seen := keptStoreServer(t)
	store := edge.ReleasesStore{Endpoint: srv.URL, BootstrapCredential: "box-credential"}

	if err := (KeptObjects{p: &cloudflare{}}).Remove(t.Context(), store, "shop", []string{"assets/shop/prod/web/r1/"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if len(*seen) != 1 || (*seen)[0].path != "/shop/objects/remove" || !strings.Contains((*seen)[0].body, "assets/shop/prod/web/r1/") {
		t.Errorf("the store was sent %+v, want one removal naming the prefix", *seen)
	}
}

func TestAnEdgeThatKeepsItsOriginsObjectsBindsItsEntryWorkerToTheirBucket(t *testing.T) {
	store := fakeStoreServer(t, "s3cr3t")
	m := zoneMock()
	t.Setenv(envAccountID, "acct")
	p := m.provider(t)
	p.KeepsObjects = true
	spec := testSpec(store.URL, "v2")
	installKeptBootstrap(t, m, spec.Tier)

	if _, err := reconcileState(t, p, spec, testState(store.URL, "s3cr3t")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	bound := bindingsByType(putMetadata(m.putBodies[entryOf(t, spec).name]), "r2_bucket")
	if len(bound) != 1 || bound[0]["name"] != entryObjectsBinding || bound[0]["bucket_name"] != keptBucketName(t, spec.Tier) {
		t.Errorf("the entry worker binds %v, want %s bound to the origin's bucket", bound, entryObjectsBinding)
	}
}
