package control_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestTheControlAssemblyLinksNoProvisioningEngine(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if strings.Contains(pkg, "pulumi") {
			t.Errorf("the gcp control assembly reaches %s: an endpoint built from it serves control calls, and a provisioning engine linked into it runs only on a runner", pkg)
		}
	}
}
