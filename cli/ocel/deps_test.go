package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestTheCLINeverLinksTheProviderServer(t *testing.T) {
	t.Parallel()

	const server = "github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if pkg == server || strings.HasPrefix(pkg, server+"/") {
			t.Errorf("the ocel binary links %s: the server runs inside a provider binary, and the CLI only speaks to it over the wire", pkg)
		}
	}
}
