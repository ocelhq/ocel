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
		t.Errorf("deployedClasses = %v, want none for a script that does not exist", classes)
	}
}

func TestDeployedClasses_IgnoresAClassOwnedByAnotherScript(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"bindings":[
			{"class_name":"IsrDeploy","name":"ISR_WRITER_DO","script_name":"some-other-worker",
			 "type":"durable_object_namespace"}
		]},"success":true,"errors":[],"messages":[]}`))
	}))
	t.Cleanup(srv.Close)
	p := &provider{client: cf.NewClient(option.WithBaseURL(srv.URL+"/"), option.WithAPIToken("test"))}

	classes, err := p.deployedClasses(context.Background(), "ocel-isr-writer-preview")
	if err != nil {
		t.Fatalf("deployedClasses: %v", err)
	}
	if len(classes) != 0 {
		t.Fatalf("deployedClasses = %v, want none: IsrDeploy belongs to another script", classes)
	}
	meta := doMetadataFromMultipart(t, testStoreWorker(), isrWriterWorker, classes)
	migrations, ok := meta["migrations"].(map[string]any)
	if !ok {
		t.Fatalf("expected a migrations object, got %v", meta["migrations"])
	}
	if steps := migrationSteps(t, migrations); !reflect.DeepEqual(steps, [][]string{{"IsrDeploy"}, {"IsrSnapshot"}}) {
		t.Errorf("migrations.steps = %v, want both classes still created locally", steps)
	}
}
