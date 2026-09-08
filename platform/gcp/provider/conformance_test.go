package gcp_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func withoutApplicationDefaultCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-such-key.json"))
}

func TestGCPProvider(t *testing.T) {
	withoutApplicationDefaultCredentials(t)
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	conformance.Run(t, conformance.Suite{
		Spec:    providerkit.Spec{Version: "test", New: gcp.New},
		Options: providerkit.Options{"project": "conformance", "region": "europe-west1"},
		Binary:  buildProvider(t),
	})
}

func TestTheCredentialsPortAnswersOrSaysWhyItCannot(t *testing.T) {
	withoutApplicationDefaultCredentials(t)

	conformance.RunCredentials(t, gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"}).Credentials())
}

func TestTheEdgeRegistryOpensTheCloudflareEdge(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	conformance.RunEdgeRegistry(t, gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"}).Edges())
}

func TestTheDNSRegistryOpensACloudflareWriter(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	conformance.RunDNSRegistry(t, gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"}).DNS())
}

func buildProvider(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "deploy")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/deploy")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the gcp provider: %v\n%s", err, out)
	}
	return binary
}
