package cloudflare

import (
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

const absentScriptSettings = `{"success":false,"errors":[{"code":10007,"message":"workers.api.error.script_not_found"}],"result":null}`

const foreignClassSettings = `{"result":{"bindings":[
	{"class_name":"IsrDeploy","name":"ISR_WRITER_DO","script_name":"some-other-worker",
	 "type":"durable_object_namespace"}
]},"success":true,"errors":[],"messages":[]}`

func settingsProvider(t *testing.T, status int, body string) *provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &provider{client: cf.NewClient(
		option.WithBaseURL(srv.URL+"/"),
		option.WithAPIToken("test"),
	)}
}

func readDeployedClasses(t *testing.T, p *provider, script string) ([]string, error) {
	t.Helper()
	settings, err := p.scriptSettings(t.Context(), "acct", script)
	if err != nil {
		return nil, err
	}
	return deployedClasses(settings), nil
}

func TestDeployedClasses(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	for _, tc := range []struct {
		name    string
		status  int
		body    string
		script  string
		want    []string
		wantLen int
	}{
		{
			name:   "reads the class off the live settings response",
			status: http.StatusOK,
			body:   liveDeploymentsStoreSettings,
			script: "ocel-deployments-store-preview",
			want:   []string{"DeploymentsStore"},
		},
		{
			name:   "a script that does not exist has no classes",
			status: http.StatusNotFound,
			body:   absentScriptSettings,
			script: "ocel-isr-writer-preview",
			want:   nil,
		},
		{
			name:   "a class owned by another script is not ours",
			status: http.StatusOK,
			body:   foreignClassSettings,
			script: "ocel-isr-writer-preview",
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			classes, err := readDeployedClasses(t, settingsProvider(t, tc.status, tc.body), tc.script)
			if err != nil {
				t.Fatalf("read settings: %v", err)
			}
			if !reflect.DeepEqual(classes, tc.want) {
				t.Errorf("deployedClasses = %v, want %v", classes, tc.want)
			}
		})
	}
}

func TestDurableObjectMigrationAgainstLiveClasses(t *testing.T) {
	t.Setenv(envAccountID, "acct")

	t.Run("a class the script already carries is not redeclared", func(t *testing.T) {
		classes, err := readDeployedClasses(t, settingsProvider(t, http.StatusOK, liveDeploymentsStoreSettings), "ocel-deployments-store-preview")
		if err != nil {
			t.Fatalf("read settings: %v", err)
		}

		meta := doMetadataFromMultipart(t, testStoreWorker(), deploymentsStoreWorker, classes)
		if migrations, present := meta["migrations"]; present {
			t.Fatalf("upload declares migrations %v against a script that already has DeploymentsStore; "+
				"Cloudflare rejects this with 10074", migrations)
		}
	})

	t.Run("a class another script owns is still created locally", func(t *testing.T) {
		classes, err := readDeployedClasses(t, settingsProvider(t, http.StatusOK, foreignClassSettings), "ocel-isr-writer-preview")
		if err != nil {
			t.Fatalf("read settings: %v", err)
		}

		meta := doMetadataFromMultipart(t, testStoreWorker(), isrWriterWorker, classes)
		migrations, ok := meta["migrations"].(map[string]any)
		if !ok {
			t.Fatalf("expected a migrations object, got %v", meta["migrations"])
		}
		if steps := migrationSteps(t, migrations); !reflect.DeepEqual(steps, [][]string{{"IsrDeploy"}, {"IsrSnapshot"}}) {
			t.Errorf("migrations.steps = %v, want both classes still created locally", steps)
		}
	})
}
