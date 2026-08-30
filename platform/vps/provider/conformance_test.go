package vps_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
)

func TestTheDNSRegistryOpensACloudflareWriter(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	registry := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}}).DNS()

	if got := registry.Supported(); !slices.Equal(got, []providerkit.DNSKind{"cloudflare"}) {
		t.Errorf("Supported() = %v, want cloudflare alone", got)
	}
	if got := registry.Default(); got != "" {
		t.Errorf("Default() = %q, want no default: instructions-only DNS is the absence of a writer", got)
	}
	writer, err := registry.Open("cloudflare", "app.com")
	if err != nil || writer == nil {
		t.Fatalf("Open(cloudflare) = %v, %v, want a writer", writer, err)
	}

	var refusal providerkit.Refusal
	opened, err := registry.Open("route53", "app.com")
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Open(route53) = %v, %v, want an invalid refusal", opened, err)
	}
	if !strings.Contains(refusal.Message, "cloudflare") {
		t.Errorf("Open(route53) refusal = %q, want it to name the writers this provider has", refusal.Message)
	}
}

func TestTheEdgeRegistryOpensTheBoxEdge(t *testing.T) {
	t.Parallel()

	registry := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}}).Edges()

	conformance.RunEdgeRegistry(t, registry)

	if got := registry.Supported(); !slices.Equal(got, []edge.Kind{boxedge.Kind}) {
		t.Errorf("Supported() = %v, want %q alone: a machine is fronted by the proxy ocel puts on it", got, boxedge.Kind)
	}
	if got := registry.Default(); got != boxedge.Kind {
		t.Errorf("Default() = %q, want %q: a deploy that names no edge reaches the box's own proxy", got, boxedge.Kind)
	}

	var refusal providerkit.Refusal
	opened, err := registry.Open("cloudflare")
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Open(cloudflare) = %v, %v, want an invalid refusal", opened, err)
	}
	if !strings.Contains(refusal.Message, "cloudflare") || !strings.Contains(refusal.Message, string(boxedge.Kind)) {
		t.Errorf("Open(cloudflare) refused with %q, want it to name the edge asked for and the one this provider serves", refusal.Message)
	}
}

func TestVPSProvider(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	conformance.Run(t, conformance.Suite{
		Spec:    providerkit.Spec{Version: "test", New: vps.New},
		Options: providerkit.Options{"ssh": map[string]any{"host": "203.0.113.10"}},
		Binary:  buildProvider(t),
		Certifier: &conformance.CertifierChecks{
			Kind:      boxedge.Kind,
			Hostnames: []string{"shop.example.com", "www.shop.example.com"},
			Handle:    certs.ProxyHandle,
		},
	})
}

func TestTheRootCarriesTheVendorAndNoOptionalSetYet(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})

	if p.Vendor() != vps.Vendor {
		t.Errorf("Vendor() = %q, want %q", p.Vendor(), vps.Vendor)
	}
	if got := p.Serves(); len(got) != 0 {
		t.Errorf("Serves() = %v, want nothing until the provider provisions links of its own", got)
	}

	var root providerkit.Provider = p
	for name, held := range map[string]bool{
		"Warmer":            held[providerkit.Warmer](root),
		"CodeEmbedder":      held[providerkit.CodeEmbedder](root),
		"StackInspector":    held[providerkit.StackInspector](root),
		"GrantVerifier":     held[providerkit.GrantVerifier](root),
		"MembraneCrosser":   held[providerkit.MembraneCrosser](root),
		"DeployPreflighter": held[providerkit.DeployPreflighter](root),
		"MembraneSource":    held[providerkit.MembraneSource](root),
		"ArtifactPacker":    held[providerkit.ArtifactPacker](root),
	} {
		if held {
			t.Errorf("the root carries %s, which the optional-set tier must be told to expect on it", name)
		}
	}
	if !held[providerkit.Certifier](root) {
		t.Error("the root carries no Certifier, and a provider without one blanks the certificate and renewal lines in `ocel domain status` entirely, which reads as no tls rather than tls you do not manage")
	}
	if !held[providerkit.Prober](root) {
		t.Error("the root carries no Prober, and the settle then answers from what it just bound rather than from what the hostname serves")
	}
}

func TestTheReleasePortRefusesTheResourcesThisProviderServesNoneOf(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	release := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}}).Releases()
	plan := providerkit.StackPlan{
		Ref: providerkit.StackRef{
			Project: "shop",
			Class:   providerkit.ClassProduction,
			Name:    naming.InfraStack("prod"),
		},
		Kind:      providerkit.StackInfra,
		Resources: []providerkit.Resource{{Name: "orders", Type: providerkit.LinkPostgres}},
	}

	var refusal providerkit.Refusal
	if _, err := release.Plan(ctx, plan, nil); !errors.As(err, &refusal) {
		t.Errorf("Plan() of a resource this provider serves none of = %v, want a refusal", err)
	}
	if _, err := release.Provision(ctx, plan, nil); !errors.As(err, &refusal) {
		t.Errorf("Provision() of a resource this provider serves none of = %v, want a refusal rather than a release that reads as done", err)
	}
}

func held[T any](root providerkit.Provider) bool {
	_, ok := root.(T)
	return ok
}

func buildProvider(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "deploy")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/deploy")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the vps provider: %v\n%s", err, out)
	}
	return binary
}
