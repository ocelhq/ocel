package topics_test

import (
	"os/exec"
	"strings"
	"testing"
)

var controlPlane = []string{
	"github.com/ocelhq/ocel/platform/gcp/provider",
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb",
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads",
}

func TestTheTopicsEngineReachesNoControlPlane(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if strings.Contains(pkg, "pulumi") {
			t.Errorf("the topics engine reaches %s: the runtime serves topics from this package and must not link a provisioning engine", pkg)
		}
		for _, linked := range controlPlane {
			if pkg == linked {
				t.Errorf("the topics engine reaches %s: the runtime serves topics from this package and must not link the control plane", pkg)
			}
		}
	}
}
