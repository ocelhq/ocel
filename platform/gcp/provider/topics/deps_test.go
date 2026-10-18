package topics_test

import (
	"os/exec"
	"strings"
	"testing"
)

var controlPlane = []string{
	"github.com/ocelhq/ocel/platform/gcp/provider",
	"github.com/ocelhq/ocel/platform/gcp/provider/assets",
	"github.com/ocelhq/ocel/platform/gcp/provider/certs",
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/containers",
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/functions",
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next",
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release",
	"github.com/ocelhq/ocel/platform/gcp/provider/edges",
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb",
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart",
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin",
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun",
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
