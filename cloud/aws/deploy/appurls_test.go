package deploy

import (
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func pgOutput(logicalName string) *deploymentsv1.ResourceOutput {
	return &deploymentsv1.ResourceOutput{
		LogicalName: logicalName,
		Output:      &deploymentsv1.ResourceOutput_Postgres{Postgres: &deploymentsv1.PostgresOutput{Host: "db"}},
	}
}

func TestAppURLs_PrefersEachAppsWorkerURL(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		Apps: []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "index", Framework: "next", App: "web"},
		},
	}
	outputs := []*deploymentsv1.ResourceOutput{
		fnOutput("index", "https://index.lambda-url.example"),
		fnOutput(workerOutputName("web"), "https://app.workers.dev"),
		pgOutput("main"),
	}

	got := appURLs(manifest, outputs)
	if len(got) != 1 || got[0] != "https://app.workers.dev" {
		t.Fatalf("appURLs = %v, want just the worker URL", got)
	}
}

// An app with no worker is served straight from its functions — and only its
// own, so one app's URLs never surface under another.
func TestAppURLs_FallsBackToTheAppsOwnFunctionURLs(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		Apps: []*deploymentsv1.ManifestApp{
			{Name: "api", Framework: "express"},
			{Name: "web", Framework: "next"},
		},
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "api_handler", Framework: "express", App: "api"},
			{LogicalName: "api_worker", Framework: "express", App: "api"},
			{LogicalName: "web_index", Framework: "next", App: "web"},
		},
	}
	outputs := []*deploymentsv1.ResourceOutput{
		fnOutput("api_handler", "https://handler.lambda-url.example"),
		fnOutput("api_worker", "https://worker.lambda-url.example"),
		fnOutput("web_index", "https://index.lambda-url.example"),
		fnOutput(workerOutputName("web"), "https://web.workers.dev"),
		pgOutput("main"),
	}

	want := []string{
		"https://handler.lambda-url.example",
		"https://worker.lambda-url.example",
		"https://web.workers.dev",
	}
	if got := appURLs(manifest, outputs); !slicesEqual(got, want) {
		t.Fatalf("appURLs = %v, want %v", got, want)
	}
}

func TestAppURLs_NoFunctions_ReturnsEmpty(t *testing.T) {
	manifest := &deploymentsv1.Manifest{Apps: []*deploymentsv1.ManifestApp{{Name: "web"}}}
	if got := appURLs(manifest, []*deploymentsv1.ResourceOutput{pgOutput("main")}); len(got) != 0 {
		t.Fatalf("appURLs = %v, want empty", got)
	}
}
