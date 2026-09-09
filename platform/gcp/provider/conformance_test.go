package gcp_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func withoutApplicationDefaultCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-such-key.json"))
}

func TestGCPProvider(t *testing.T) {
	withoutApplicationDefaultCredentials(t)
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	conformance.Run(t, conformance.Suite{
		Spec:      providerkit.Spec{Version: "test", New: gcp.New},
		Options:   providerkit.Options{"project": "conformance", "region": "europe-west1"},
		Binary:    buildProvider(t),
		Certifier: &conformance.CertifierChecks{Kind: alb.Kind},
	})
}

func TestTheCredentialsPortAnswersOrSaysWhyItCannot(t *testing.T) {
	withoutApplicationDefaultCredentials(t)

	conformance.RunCredentials(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}).Credentials())
}

func TestTheEdgeRegistryOpensTheEdgesThisProviderFronts(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	registry := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}).Edges()

	conformance.RunEdgeRegistry(t, registry)

	if got := registry.Supported(); !slices.Equal(got, []edge.Kind{direct.Kind, alb.Kind, cloudflare.Kind}) {
		t.Errorf("Supported() = %v, want %q, %q and %q", got, direct.Kind, alb.Kind, cloudflare.Kind)
	}
	if got := registry.Default(); got != direct.Kind {
		t.Errorf("Default() = %q, want %q: a deploy that names no edge is answered on the url Cloud Run gave it", got, direct.Kind)
	}
}

func TestTheDirectEdgeBindsNoHostnameAndSaysSo(t *testing.T) {
	registry := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}).Edges()

	front, err := registry.Open(direct.Kind)
	if err != nil {
		t.Fatalf("Open(%q) = %v", direct.Kind, err)
	}
	stack, err := front.Open(edge.StackState{Slug: "shop", Class: edge.ClassProduction})
	if err != nil {
		t.Fatal(err)
	}
	if !front.Facts().AddressesItself {
		t.Error("Facts() says the origin does not address itself, and a deploy would then demand a hostname " +
			"the direct edge has no way to bind")
	}
	err = stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web"})
	if err == nil {
		t.Fatal("BindDomain() bound a hostname to an edge that claims none")
	}
	if !strings.Contains(err.Error(), string(cloudflare.Kind)) {
		t.Errorf("BindDomain() = %v, want it to name the edge that would serve the hostname", err)
	}
}

func TestTheDNSRegistryOpensACloudflareWriter(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	conformance.RunDNSRegistry(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}).DNS())
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
