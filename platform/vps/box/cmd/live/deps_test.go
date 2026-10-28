package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestTheBoxDaemonsLinkNoOTelSDKOrExporter(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", "../switchboard", "../connector", "../container").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		for _, sdk := range []string{"go.opentelemetry.io/otel/sdk", "go.opentelemetry.io/otel/exporters"} {
			if strings.HasPrefix(pkg, sdk) {
				t.Errorf("a box daemon reaches %s: only ocel-live's commands a deploy runs export spans, and a daemon outlives the run it would send them to", pkg)
			}
		}
	}
}
