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

func TestAppURLs_PrefersWorkerURL(t *testing.T) {
	outputs := []*deploymentsv1.ResourceOutput{
		fnOutput("api", "https://api.lambda-url.example"),
		fnOutput(nextWorkerOutputName, "https://app.workers.dev"),
		pgOutput("main"),
	}
	got := appURLs(outputs)
	if len(got) != 1 || got[0] != "https://app.workers.dev" {
		t.Fatalf("appURLs = %v, want just the worker URL", got)
	}
}

func TestAppURLs_FallsBackToFunctionURLs(t *testing.T) {
	outputs := []*deploymentsv1.ResourceOutput{
		fnOutput("api", "https://api.lambda-url.example"),
		fnOutput("worker", "https://worker.lambda-url.example"),
		pgOutput("main"),
	}
	got := appURLs(outputs)
	want := []string{"https://api.lambda-url.example", "https://worker.lambda-url.example"}
	if len(got) != len(want) {
		t.Fatalf("appURLs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("appURLs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAppURLs_NoFunctions_ReturnsEmpty(t *testing.T) {
	if got := appURLs([]*deploymentsv1.ResourceOutput{pgOutput("main")}); len(got) != 0 {
		t.Fatalf("appURLs = %v, want empty", got)
	}
}
