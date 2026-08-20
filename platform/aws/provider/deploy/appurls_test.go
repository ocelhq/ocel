package deploy

import (
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/progress/v1"
)

func TestAppURLs(t *testing.T) {
	t.Parallel()

	t.Run("prefers each app's worker URL", func(t *testing.T) {
		t.Parallel()

		manifest := &deploymentsv1.Manifest{
			Apps: []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
			Functions: []*deploymentsv1.ManifestFunction{
				{LogicalName: "index", Framework: "next", App: "web"},
			},
		}
		outputs := []*progressv1.FunctionOutput{
			fnOutput("index", "https://index.lambda-url.example"),
			fnOutput(workerOutputName("web"), "https://app.workers.dev"),
		}

		got := appURLs(manifest, outputs)
		if len(got) != 1 || got[0] != "https://app.workers.dev" {
			t.Fatalf("appURLs = %v, want just the worker URL", got)
		}
	})

	t.Run("falls back to the app's own function URLs", func(t *testing.T) {
		t.Parallel()

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
		outputs := []*progressv1.FunctionOutput{
			fnOutput("api_handler", "https://handler.lambda-url.example"),
			fnOutput("api_worker", "https://worker.lambda-url.example"),
			fnOutput("web_index", "https://index.lambda-url.example"),
			fnOutput(workerOutputName("web"), "https://web.workers.dev"),
		}

		want := []string{
			"https://handler.lambda-url.example",
			"https://worker.lambda-url.example",
			"https://web.workers.dev",
		}
		if got := appURLs(manifest, outputs); !slicesEqual(got, want) {
			t.Fatalf("appURLs = %v, want %v", got, want)
		}
	})

	t.Run("no functions returns empty", func(t *testing.T) {
		t.Parallel()

		manifest := &deploymentsv1.Manifest{Apps: []*deploymentsv1.ManifestApp{{Name: "web"}}}
		if got := appURLs(manifest, nil); len(got) != 0 {
			t.Fatalf("appURLs = %v, want empty", got)
		}
	})
}
