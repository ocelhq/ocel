package aws

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/ocelhq/ocel/platform/aws/provider/"

var layerFences = []struct {
	pattern   string
	forbidden []string
	why       string
}{
	{"./iam/...", []string{"component", "resources", "compute", "tier", "site", "deploy", "bootstrap", "control", "ports"}, "iam renders policies for every layer above it and may reach none of them"},
	{"./component/...", []string{"resources", "compute", "tier", "site", "deploy", "bootstrap", "control"}, "component is the seam the components implement"},
	{"./resources/...", []string{"compute", "tier", "site", "deploy", "bootstrap", "control"}, "a resource component is declared by the infra stack alone"},
	{"./site/...", []string{"compute", "tier", "deploy", "bootstrap", "control"}, "site uploads what a release serves"},
	{"./compute/...", []string{"deploy", "bootstrap", "control"}, "compute declares an app's code; deploy orchestrates it"},
	{"./tier/...", []string{"compute", "deploy", "control"}, "a tier part is shared by every deploy and owned by none"},
	{"./deploy/...", []string{"bootstrap", "control"}, "deploy runs stacks and reads the bootstrap through its config"},
}

func TestEachLayerReachesOnlyTheLayersBelowIt(t *testing.T) {
	t.Parallel()

	for _, fence := range layerFences {
		out, err := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, fence.pattern).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", fence.pattern, err, out)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			pkg, imports := fields[0], fields[1:]
			for _, imported := range imports {
				local, inModule := strings.CutPrefix(imported, modulePath)
				if !inModule {
					continue
				}
				for _, layer := range fence.forbidden {
					if local == layer || strings.HasPrefix(local, layer+"/") {
						t.Errorf("%s imports %s: %s", pkg, imported, fence.why)
					}
				}
			}
		}
	}
}
