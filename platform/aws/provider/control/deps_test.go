package control_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
			t.Errorf("the aws control assembly reaches %s: an endpoint serves control calls only, and linking a provisioning engine into it puts a hundred megabytes of deploy machinery on the target", pkg)
		}
	}
}

var payloadsOnlyAProvisionUploads = []string{
	"runtime-layer-amd64.zip",
	"runtime-layer-arm64.zip",
	"container-runtime-amd64",
	"container-runtime-arm64",
	"upload-completer.zip",
	"realtime-authorizer.zip",
	"image-optimizer.zip",
	"tag-invalidator.zip",
}

func TestAnEndpointLinksNoPayloadOnlyAProvisionUploads(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(t.TempDir(), "endpoint")
	if out, err := exec.Command("go", "build", "-o", binary, "./testdata/endpoint").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	linked, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range payloadsOnlyAProvisionUploads {
		payload, err := os.ReadFile(filepath.Join("..", "payloads", "dist", name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(linked, payload) {
			t.Errorf("an endpoint built on the control assembly carries %s, which only a provision uploads", name)
		}
	}
}
