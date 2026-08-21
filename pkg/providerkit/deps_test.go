package providerkit_test

import (
	"os/exec"
	"strings"
	"testing"
)

var reachable = map[string]bool{
	"github.com/ocelhq/ocel/pkg/providerkit":        true,
	"github.com/ocelhq/ocel/pkg/channel":            true,
	"github.com/ocelhq/ocel/pkg/naming":             true,
	"github.com/ocelhq/ocel/pkg/proto":              true,
	"github.com/ocelhq/ocel/platform/edge/contract": true,
}

var engines = []string{
	"github.com/aws/aws-sdk-go",
	"github.com/pulumi/",
	"github.com/cloudflare/",
	"cloud.google.com/go",
}

func TestTheKitReachesNothingItMayNotImport(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if module, ok := ocelModuleOf(pkg); ok && !reachable[module] {
			t.Errorf("the kit reaches %s, which the module's dependency list does not allow", pkg)
		}
		for _, engine := range engines {
			if strings.HasPrefix(pkg, engine) {
				t.Errorf("the kit reaches %s: a port is named for what it achieves, never for the engine that achieves it", pkg)
			}
		}
	}
}

func ocelModuleOf(pkg string) (string, bool) {
	const repo = "github.com/ocelhq/ocel/"
	if !strings.HasPrefix(pkg, repo) {
		return "", false
	}
	for module := range reachable {
		if pkg == module || strings.HasPrefix(pkg, module+"/") {
			return module, true
		}
	}
	return pkg, true
}
