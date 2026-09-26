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
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
)

func TestTheDNSRegistryOpensACloudflareWriter(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	registry := p.DNS()

	conformance.RunDNS(t, p.Facts(), registry)
	if got := p.Facts().DNSKinds; !slices.Equal(got, []provider.DNSKind{"cloudflare"}) {
		t.Errorf("Facts().DNSKinds = %v, want cloudflare alone", got)
	}
	writer, err := registry.Open("cloudflare", "app.com", "")
	if err != nil || writer == nil {
		t.Fatalf("Open(cloudflare) = %v, %v, want a writer", writer, err)
	}

	var rejection refusal.Refusal
	opened, err := registry.Open("route53", "app.com", "")
	if !errors.As(err, &rejection) || rejection.Code != refusal.CodeInvalid {
		t.Fatalf("Open(route53) = %v, %v, want an invalid refusal", opened, err)
	}
	if !strings.Contains(rejection.Message, "cloudflare") {
		t.Errorf("Open(route53) refusal = %q, want it to name the writers this provider has", rejection.Message)
	}
}

func TestTheEdgeRegistryOpensTheBoxEdge(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	registry := p.Edges()

	conformance.RunEdges(t, p.Facts(), registry)

	if got := p.Facts().Edges; !slices.Equal(got, []edge.Kind{boxedge.Kind}) {
		t.Errorf("Facts().Edges = %v, want %q alone: a machine is fronted by the proxy ocel puts on it", got, boxedge.Kind)
	}
	if got := p.Facts().DefaultEdge; got != boxedge.Kind {
		t.Errorf("Facts().DefaultEdge = %q, want %q: a deploy that names no edge reaches the box's own proxy", got, boxedge.Kind)
	}

	var rejection refusal.Refusal
	opened, err := registry.Open("cloudflare")
	if !errors.As(err, &rejection) || rejection.Code != refusal.CodeInvalid {
		t.Fatalf("Open(cloudflare) = %v, %v, want an invalid refusal", opened, err)
	}
	if !strings.Contains(rejection.Message, "cloudflare") || !strings.Contains(rejection.Message, string(boxedge.Kind)) {
		t.Errorf("Open(cloudflare) refused with %q, want it to name the edge asked for and the one this provider serves", rejection.Message)
	}
}

func TestVPSProvider(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	conformance.Run(t, conformance.Suite{
		Server:  providerserver.Config{Version: "test", New: vps.New},
		Options: provider.Options{"ssh": map[string]any{"host": "203.0.113.10"}},
		Binary:  buildProvider(t),
		Certificates: &conformance.CertificateChecks{
			Kind:      boxedge.Kind,
			Hostnames: []string{"shop.example.com", "www.shop.example.com"},
			Handle:    certs.ProxyHandle,
		},
	})
}

func TestTheProviderNamesTheVendorAndSetsTheHooksABoxImplements(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})

	if p.Facts().Vendor != vps.Vendor {
		t.Errorf("Facts().Vendor = %q, want %q", p.Facts().Vendor, vps.Vendor)
	}
	if got := p.Facts().Bindings; !slices.Equal(got, []provider.BindingType{provider.BindingPostgres, provider.BindingBucket}) {
		t.Errorf("Facts().Bindings = %v, want the binding types a box provisions for itself", got)
	}

	hooks := p.Hooks()
	for name, set := range map[string]bool{
		"WarmFunctions": hooks.WarmFunctions != nil,
		"EmbedCode":     hooks.EmbedCode != nil,
		"InspectStack":  hooks.InspectStack != nil,
		"VerifyGrants":  hooks.VerifyGrants != nil,
		"PackApp":       hooks.PackApp != nil,
	} {
		if set {
			t.Errorf("the box's hooks set %s, a step no box takes", name)
		}
	}
	if hooks.PreflightDeploy == nil {
		t.Error("the box's hooks leave PreflightDeploy nil, and a box then learns its engine, its disk, its proxy or its ports are not ready halfway through an image transfer")
	}
	if hooks.CheckHost == nil {
		t.Error("the box's hooks leave CheckHost nil, and `doctor` calls Preflight and DescribeBootstrap and nothing else: with no permanent port there is nowhere for a verdict about the life of the box to arrive")
	}
}

func TestTheReleasePortRefusesTheResourcesThisProviderServesNoneOf(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	release := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}}).Stacks()
	spec := provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Class:   edge.ClassProduction,
			Name:    naming.InfraStack("prod"),
		},
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "orders", Type: provider.BindingPostgres}},
	}

	var refusal refusal.Refusal
	if _, err := release.Plan(ctx, spec, nil); !errors.As(err, &refusal) {
		t.Errorf("Plan() of a resource this provider serves none of = %v, want a refusal", err)
	}
	if _, err := release.Provision(ctx, spec, nil); !errors.As(err, &refusal) {
		t.Errorf("Provision() of a resource this provider serves none of = %v, want a refusal rather than a release that reads as done", err)
	}
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
