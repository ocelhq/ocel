package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestTheConnectorLinksNoProvisioningEngine(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if strings.Contains(pkg, "pulumi") {
			t.Errorf("the gcp connector reaches %s: a connector reads and writes variables, and linking a provisioning engine into it puts a hundred megabytes of deploy machinery on the target", pkg)
		}
	}
}
