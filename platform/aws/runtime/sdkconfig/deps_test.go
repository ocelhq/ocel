package sdkconfig

import (
	"os/exec"
	"strings"
	"testing"
)

func TestTheWorkloadBinariesLinkNoTracingSDKOrExporter(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", "github.com/ocelhq/ocel/platform/aws/runtime/cmd/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		for _, sdk := range []string{"go.opentelemetry.io/otel/sdk", "go.opentelemetry.io/otel/exporters", "go.opentelemetry.io/proto/otlp"} {
			if strings.HasPrefix(pkg, sdk) {
				t.Errorf("an aws workload binary reaches %s: a workload has no collector to send spans to, and every function cold-starts with what it links", pkg)
			}
		}
	}
}
