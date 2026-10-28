package tracing_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestThePackageLinksTheOTelAPIAndNeverItsSDKOrExporter(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		for _, sdk := range []string{"go.opentelemetry.io/otel/sdk", "go.opentelemetry.io/otel/exporters", "go.opentelemetry.io/proto/otlp"} {
			if strings.HasPrefix(pkg, sdk) {
				t.Errorf("pkg/tracing reaches %s: the AWS and GCP workloads and the box link pkg/tracing, and none of them exports spans", pkg)
			}
		}
	}
}

func TestThePackageLinksNeitherConnectNorProtobuf(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		for _, wire := range []string{"github.com/ocelhq/ocel/pkg/proto", "connectrpc.com/connect", "google.golang.org/protobuf", "buf.build/"} {
			if strings.HasPrefix(pkg, wire) {
				t.Errorf("pkg/tracing reaches %s: an env source sync spans its requests and commands through pkg/tracing, and must not link the wire", pkg)
			}
		}
	}
}
