package ports_test

import (
	"os/exec"
	"strings"
	"testing"
)

var controlPlane = []string{
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap",
	"github.com/ocelhq/ocel/platform/aws/provider/control",
	"github.com/ocelhq/ocel/platform/aws/provider/deploy",
	"github.com/ocelhq/ocel/platform/aws/provider/payloads",
	"github.com/ocelhq/ocel/platform/aws/provider/server",
}

func TestTheRuntimePortsReachNoControlPlane(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		for _, linked := range controlPlane {
			if pkg == linked {
				t.Errorf("the runtime ports reach %s: the membrane links these ports, and a lambda that reads records must not carry the control plane", pkg)
			}
		}
	}
}
