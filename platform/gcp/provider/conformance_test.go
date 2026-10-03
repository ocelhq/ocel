package gcp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/router"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/cloudrun"
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
		Server:       providerserver.Config{Version: "test", New: gcp.New},
		Options:      provider.Options{"project": "conformance", "region": "europe-west1"},
		Binary:       buildProvider(t),
		Certificates: &conformance.CertificateChecks{Kind: alb.Kind},
	})
}

func TestTheCredentialsPortAnswersOrSaysWhyItCannot(t *testing.T) {
	withoutApplicationDefaultCredentials(t)

	conformance.RunCredentials(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}).Credentials())
}

func TestTheEdgeRegistryOpensTheEdgesThisProviderFronts(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	facts := p.Facts()

	conformance.RunEdges(t, facts, p.Edges())

	if got := facts.Edges; !slices.Equal(got, []edge.Kind{alb.Kind, "cloudflare"}) {
		t.Errorf("Facts().Edges = %v, want %q and cloudflare", got, alb.Kind)
	}
	if got := facts.DefaultEdge; got != edge.None {
		t.Errorf("Facts().DefaultEdge = %q, want no edge: a deploy that names none is answered on the url Cloud Run gave it", got)
	}
	if got := facts.ListPairedRouters(edge.None); !slices.Equal(got, []router.Kind{cloudrun.RouterKind}) {
		t.Errorf("ListPairedRouters(no edge) = %v, want Cloud Run alone", got)
	}
	if got := facts.ListPairedRouters(alb.Kind); !slices.Equal(got, []router.Kind{router.Kind(alb.Kind)}) {
		t.Errorf("ListPairedRouters(alb) = %v, want the load balancer, which is edge and router in one", got)
	}
	for _, gone := range []edge.Kind{"direct", "cloud-run"} {
		if _, err := p.Edges().Open(gone, nil); err == nil {
			t.Errorf("Open(%q) opened an edge, want it refused: leave `edge` out for no edge", gone)
		}
	}
}

func TestTheRouterRegistryOpensTheRouterEveryEdgePairsWith(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	conformance.RunRouters(t, p.Facts(), p.Edges(), p.Routers())
}

func TestAProjectWithNoEdgeBindsNoHostnameAndSaysSo(t *testing.T) {
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	registry := p.Edges()

	front, err := registry.Open(edge.None, nil)
	if err != nil {
		t.Fatalf("Open(no edge) = %v", err)
	}
	stack, err := front.Open(edge.StackState{Slug: "shop", Tier: environment.TierProduction})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := p.Routers().Open(cloudrun.RouterKind)
	if err != nil {
		t.Fatalf("Routers().Open(%q) = %v", cloudrun.RouterKind, err)
	}
	if !opened.Facts().AddressesItself {
		t.Error("Facts() says the origin does not address itself, and a deploy would then demand a hostname " +
			"nothing in front has a way to bind")
	}
	err = stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: "shop.example.com", App: "web"})
	if err == nil {
		t.Fatal("BindDomain() bound a hostname with no edge in front to claim it")
	}
	if !strings.Contains(err.Error(), string(alb.Kind)) {
		t.Errorf("BindDomain() = %v, want it to name the edge that would serve the hostname", err)
	}
}

func TestTheDNSRegistryOpensACloudflareWriter(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	conformance.RunDNS(t, p.Facts(), p.DNS())
}

func buildProvider(t *testing.T) string {
	t.Helper()
	if prebuilt := os.Getenv("OCEL_GCP_PROVIDER_BINARY"); prebuilt != "" {
		return prebuilt
	}
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

func TestTopicsTasksAndWorkersAreRefusedAtPreflightAsUnsupported(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	conformance.RunWorkers(t, p.Facts())
}

func TestKVStoresPassTheConformanceSuite(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	if served := p.Facts().Bindings; !slices.Contains(served, provider.BindingKV) {
		t.Errorf("Facts().Bindings = %v, and a project declaring a kv store is refused at deploy on gcp", served)
	}
	conformance.RunKVStores(t, p.Facts())
}

func TestRealtimePassesTheConformanceSuite(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")
	p := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	if served := p.Facts().Bindings; !slices.Contains(served, provider.BindingRealtime) {
		t.Errorf("Facts().Bindings = %v, and a project declaring realtime is refused at deploy on gcp", served)
	}
	conformance.RunRealtime(t, p.Facts())
}
