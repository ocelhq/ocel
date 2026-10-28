package live

import (
	"os/exec"
	"strings"
	"testing"
)

func TestTheWorkloadBinariesLinkNoTracingSDKOrExporter(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", "github.com/ocelhq/ocel/platform/gcp/runtime/cmd/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		for _, sdk := range []string{"go.opentelemetry.io/otel/sdk", "go.opentelemetry.io/otel/exporters", "go.opentelemetry.io/proto/otlp"} {
			if strings.HasPrefix(pkg, sdk) {
				t.Errorf("a gcp workload binary reaches %s: a workload has no collector to send spans to, and every instance cold-starts with what it links", pkg)
			}
		}
	}
}
