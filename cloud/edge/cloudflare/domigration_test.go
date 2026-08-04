package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
)

// liveDeploymentsStoreSettings is the verbatim body the Cloudflare API returned
// for ocel-deployments-store-preview, a script that demonstrably carries the
// DeploymentsStore class — it is bound, with a namespace_id — and yet reports no
// migrations object at all. Every other endpoint that could carry a tag (the
// script list, the version list, a version's detail) omits it too.
//
// This is the response that broke bootstrap: read for a migration tag it yields
// "", which is the same answer as "never migrated", so the upload redeclared
// new_sqlite_classes for an existing class and Cloudflare rejected it with
// 400/10074. It is pinned here as a fixture because no unit test could have
// reached the API's real shape, and the shape is the whole defect.
const liveDeploymentsStoreSettings = `{
  "result": {
    "compatibility_date": "2026-07-13",
    "compatibility_flags": ["nodejs_compat"],
    "usage_model": "standard",
    "bindings": [
      {"name": "BOOTSTRAP_SECRET", "type": "secret_text"},
      {"class_name": "DeploymentsStore", "name": "DEPLOYMENTS_DO",
       "namespace_id": "96859c78dede4eaaba17a47a51fc3ff4",
       "type": "durable_object_namespace"}
    ]
  },
  "success": true, "errors": [], "messages": []
}`

func liveSettingsProvider(t *testing.T) *provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(liveDeploymentsStoreSettings))
	}))
	t.Cleanup(srv.Close)
	return &provider{client: cf.NewClient(
		option.WithBaseURL(srv.URL+"/"),
		option.WithAPIToken("test"),
	)}
}

// The class is what the response actually reports, so it is what the decision
// reads. Secret bindings are not classes and must not be counted.
func TestDeployedClasses_ReadsTheClassOffTheLiveResponse(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	classes, err := liveSettingsProvider(t).deployedClasses(context.Background(), "ocel-deployments-store-preview")
	if err != nil {
		t.Fatalf("deployedClasses: %v", err)
	}
	if !reflect.DeepEqual(classes, []string{"DeploymentsStore"}) {
		t.Errorf("deployedClasses = %v, want [DeploymentsStore]", classes)
	}
}

// The bootstrap failure itself, stated as the payload it turned on: an upload of
// the already-migrated store worker must declare no migration at all. Declaring
// one is 400/10074, which is what the operator saw.
func TestDeploymentsStoreUpload_DoesNotRedeclareAnExistingClass(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	classes, err := liveSettingsProvider(t).deployedClasses(context.Background(), "ocel-deployments-store-preview")
	if err != nil {
		t.Fatalf("deployedClasses: %v", err)
	}
	meta := doMetadataFromMultipart(t, testStoreWorker(), deploymentsStoreWorker, classes)
	if migrations, present := meta["migrations"]; present {
		t.Fatalf("upload declares migrations %v against a script that already has DeploymentsStore; "+
			"Cloudflare rejects this with 10074", migrations)
	}
}

// A script Cloudflare has never heard of 404s, and that is the fresh-bootstrap
// answer rather than a failure — it must still get the whole log.
func TestDeployedClasses_AbsentScriptIsNoClasses(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10007,"message":"workers.api.error.script_not_found"}],"result":null}`))
	}))
	t.Cleanup(srv.Close)
	p := &provider{client: cf.NewClient(option.WithBaseURL(srv.URL+"/"), option.WithAPIToken("test"))}

	classes, err := p.deployedClasses(context.Background(), "ocel-isr-writer-preview")
	if err != nil {
		t.Fatalf("deployedClasses on an absent script: %v", err)
	}
	if len(classes) != 0 {
		t.Fatalf("deployedClasses = %v, want none for a script that does not exist", classes)
	}
	meta := doMetadataFromMultipart(t, testStoreWorker(), isrWriterWorker, classes)
	migrations, ok := meta["migrations"].(map[string]any)
	if !ok {
		t.Fatalf("expected a migrations object for an absent script, got %v", meta["migrations"])
	}
	if steps := migrationSteps(t, migrations); !reflect.DeepEqual(steps, [][]string{{"IsrDeploy"}, {"IsrSnapshot"}}) {
		t.Errorf("migrations.steps = %v, want the whole log", steps)
	}
}
