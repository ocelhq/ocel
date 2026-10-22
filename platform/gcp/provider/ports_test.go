package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/seal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func testProvider(t *testing.T) *gcp.Provider {
	t.Helper()
	return newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"})
}

func newProvider(t *testing.T, options gcp.Options) *gcp.Provider {
	t.Helper()
	p, err := gcp.NewProvider(options)
	if err != nil {
		t.Fatalf("NewProvider(%+v) = %v", options, err)
	}
	return p
}

func namesOf(t *testing.T, p *gcp.Provider) names.Names {
	t.Helper()
	derived, err := p.Names(context.Background())
	if err != nil {
		t.Fatalf("Names() = %v", err)
	}
	return derived
}

func TestTheAlbEdgeIsRegisteredAndOpensWithTheProvidersOwnPorts(t *testing.T) {
	t.Parallel()

	p := testProvider(t)
	registry := p.Edges()
	if got := p.Facts().Edges; !slices.Contains(got, alb.Kind) {
		t.Fatalf("Facts().Edges = %v, want the %q edge among them: a config that names it would be refused", got, alb.Kind)
	}
	front, err := registry.Open(alb.Kind, nil)
	if err != nil {
		t.Fatalf("Open(%q) = %v", alb.Kind, err)
	}
	if front.Kind() != alb.Kind {
		t.Errorf("Open(%q) opened the %q edge", alb.Kind, front.Kind())
	}
	if !front.Facts().InvalidatesByCacheTag {
		t.Error("Facts() says the alb edge does not invalidate by cache tag, and Cloud CDN invalidates by Cache-Tag")
	}
	if !front.Facts().CachesResponses {
		t.Error("Facts() says the alb edge does not cache responses, and Cloud CDN does")
	}
	routes, err := p.Routers().Open(router.Kind(alb.Kind))
	if err != nil {
		t.Fatalf("Routers().Open(%q) = %v", alb.Kind, err)
	}
	if routes.Facts().AddressesItself {
		t.Error("Facts() says the alb router addresses itself, and a deploy would then never be asked for the hostname it fronts")
	}
	if !routes.Facts().ServesPreviewDeployments {
		t.Error("Facts() says the alb edge serves no preview deployment on its own hostname, and each one is an exact host rule " +
			"onto the revision tag its deploy created")
	}
	for _, need := range []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming, edge.NeedRevalidation} {
		if !slices.Contains(front.Facts().Supported, need) {
			t.Errorf("Supported() does not name %q, and the load balancer with Cloud CDN in front of Cloud Run serves it", need)
		}
	}
}

func TestAnEdgeThisProviderCannotFrontWithIsRefusedWithThePriceOfTheOneThatCan(t *testing.T) {
	t.Parallel()

	var refused refusal.Refusal
	_, err := testProvider(t).Edges().Open(edge.Kind("firebase"), nil)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Open(firebase) = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	for _, said := range []string{string(alb.Kind), "$18"} {
		if !strings.Contains(refused.Message, said) {
			t.Errorf("the refusal reads %q, want it to name %q so the reader knows what to name instead and what it costs", refused.Message, said)
		}
	}
}

func TestTheCloudflareFrontOnGCPRunsCodeAndShieldsItsOrigin(t *testing.T) {
	t.Parallel()

	p := testProvider(t)
	if !slices.Contains(p.Facts().Edges, cloudflare.Kind) {
		t.Errorf("Facts().Edges = %v, want cloudflare among them", p.Facts().Edges)
	}
	if _, err := p.Bootstrap(cloudflare.Kind); err != nil {
		t.Fatalf("Bootstrap(%q) = %v, want the bootstrap that keeps its origin secret and asset reader", cloudflare.Kind, err)
	}
	front, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatalf("Open(%q) = %v", cloudflare.Kind, err)
	}
	facts := front.Facts()
	if !facts.RunsCode {
		t.Errorf("the cloudflare edge's facts = %+v, want a worker that runs code", facts)
	}
	if !facts.ShieldsOrigin {
		t.Errorf("the cloudflare edge's facts = %+v, want the origin shielded, so nothing reaches a release around the worker", facts)
	}
	hooks := front.Hooks()
	if hooks.PlanBootstrap == nil {
		t.Error("the cloudflare edge plans no bootstrap, so its workers never show in a bootstrap plan")
	}
	if hooks.CheckBootstrapInstalled != nil || hooks.ListBoundHostnames != nil || hooks.OriginCertificates != nil || hooks.PurgeHostnames != nil {
		t.Error("the cloudflare edge on GCP answers for a load balancer or proxied hostnames, and the worker reaches each release on its revision-tag URL with neither between")
	}
	permissions, err := hooks.DescribeCredentialPermissions(edge.PurposeBootstrap)
	if err != nil {
		t.Fatalf("DescribeCredentialPermissions(bootstrap) = %v", err)
	}
	if strings.Contains(permissions.Document, "Account · SSL and Certificates") {
		t.Errorf("a bootstrap token on GCP needs %q, and the worker presents the origin secret rather than a client certificate", permissions.Document)
	}
}

func TestTheCloudflareFrontOnGCPRefusesOriginDomainAsAnOptionItDoesNotTake(t *testing.T) {
	t.Parallel()

	_, err := testProvider(t).Edges().Open(cloudflare.Kind, provider.Options{"originDomain": "o.example.com"})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnknownOption || !strings.Contains(refused.Message, "edge.cloudflare.originDomain") {
		t.Errorf("Open(cloudflare, originDomain) = %v, want an unknown-option refusal naming edge.cloudflare.originDomain: the worker reaches each release on its revision-tag URL, under no domain of yours", err)
	}
}

func TestTheCloudflareFrontOnGCPDescribesTheEdgesBootstrap(t *testing.T) {
	t.Parallel()

	front, err := testProvider(t).Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	if front.Hooks().DescribeBootstrap == nil {
		t.Error("the cloudflare front describes no bootstrap, so its workers, certificate and queue never show in the status or get repaired on deploy")
	}
}

func TestGCPPairsServerlessAppsAndContainersWithTheCloudflareRouterAndForwardsNothing(t *testing.T) {
	t.Parallel()

	facts := testProvider(t).Facts()

	for _, compute := range provider.Computes() {
		if got, found := facts.PairedRouter(cloudflare.Kind, compute); !found || got != router.Kind(cloudflare.Kind) {
			t.Errorf("PairedRouter(cloudflare, %s) = %q, %v, want the cloudflare router: the entry worker reaches every release on its revision-tag URL", compute, got, found)
		}
	}
	for _, pairing := range facts.Pairings {
		if pairing.Edge == cloudflare.Kind && (pairing.Forwarded || pairing.Router != router.Kind(cloudflare.Kind)) {
			t.Errorf("pairing %+v: nothing behind the cloudflare edge is forwarded through a load balancer", pairing)
		}
	}
}

func TestTheCloudflareRouterOnGCPSignsAndDispatchesAndHasNoOriginHooks(t *testing.T) {
	t.Parallel()

	opened, err := testProvider(t).Routers().Open(router.Kind(cloudflare.Kind))
	if err != nil {
		t.Fatalf("Open(cloudflare router) = %v", err)
	}
	facts := opened.Facts()
	if !facts.SignsOriginForwards || !facts.Dispatches || !facts.ReachesFunctions || !facts.ReachesContainers {
		t.Errorf("facts = %+v, want a router that signs and dispatches to functions and containers", facts)
	}
	if hooks := opened.Hooks(); hooks.Origin != nil {
		t.Error("the cloudflare router has origin hooks, and the deploy would treat it as one that forwards to an origin")
	}
}

func TestTheALBRoutersFactsAreUnchangedByTheCloudflareEdge(t *testing.T) {
	t.Parallel()

	opened, err := testProvider(t).Routers().Open(router.Kind(alb.Kind))
	if err != nil {
		t.Fatal(err)
	}
	if facts := opened.Facts(); facts.SignsOriginForwards || facts.Dispatches {
		t.Errorf("alb router facts = %+v, want neither: the origin guard behind the plain load balancer depends on it", facts)
	}
}

func TestAFrontedServiceStopsAnsweringOnItsOwnCloudRunUrl(t *testing.T) {
	t.Parallel()

	front, err := testProvider(t).Edges().Open(alb.Kind, nil)
	if err != nil {
		t.Fatalf("Open(%q) = %v", alb.Kind, err)
	}
	unfronted, err := testProvider(t).Edges().Open(edge.None, nil)
	if err != nil {
		t.Fatalf("Open(no edge) = %v", err)
	}
	if !front.Facts().ShieldsOrigin {
		t.Errorf("the %q edge does not declare that it shields the origin, and the ingress a release takes is read from that fact "+
			"rather than from which edge it is", alb.Kind)
	}
	if unfronted.Facts().ShieldsOrigin {
		t.Error("a project with no edge in front declares that it shields the origin, and the url Cloud Run gives each service is the whole of what serves it")
	}
	if got := gcp.IngressFor(edge.Facts{ShieldsOrigin: true}); got != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" {
		t.Errorf("a service behind an edge that shields the origin takes ingress %q, want the load balancer only: the run.app url would "+
			"otherwise answer past the front's certificate, cache and host rules", got)
	}
	if got := gcp.IngressFor(edge.Facts{}); got != "INGRESS_TRAFFIC_ALL" {
		t.Errorf("a service under an edge that shields nothing takes ingress %q, want every caller", got)
	}
}

func TestNoPortIsNilForProviderserverToCallThrough(t *testing.T) {
	t.Parallel()

	p := testProvider(t)
	for name, port := range map[string]any{
		"Stacks":      p.Stacks(),
		"Artifacts":   p.Artifacts(),
		"Records":     p.KeyValues(),
		"Cipher":      p.Cipher(),
		"Credentials": p.Credentials(),
		"Edges":       p.Edges(),
		"DNS":         p.DNS(),
	} {
		if port == nil {
			t.Errorf("%s() is nil, and providerserver calls methods on it", name)
		}
	}
}

func TestAnArtifactThatNamesNoTierOrNoKindIsTheCallersMistake(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := testProvider(t)
	tierless := provider.ArtifactRef{Key: "fn/shop/prod/web/r1a2b3c4d/api/bundle.zip"}
	storeless := provider.ArtifactRef{Tier: environment.TierProduction, Key: "bundle.zip"}

	for name, refused := range map[string]error{
		"Put with no tier":  p.Artifacts().Put(ctx, tierless, bytes.NewReader(nil)),
		"Has with no tier":  errorOf(p.Artifacts().Has(ctx, tierless)),
		"Open with no tier": errorOf(p.Artifacts().Open(ctx, tierless)),
		"Put with no kind":  p.Artifacts().Put(ctx, storeless, bytes.NewReader(nil)),
		"Has with no kind":  errorOf(p.Artifacts().Has(ctx, storeless)),
		"Open with no kind": errorOf(p.Artifacts().Open(ctx, storeless)),
	} {
		var rejection refusal.Refusal
		if !errors.As(refused, &rejection) || rejection.Code != refusal.CodeInvalid {
			t.Errorf("%s = %v, want an %s refusal", name, refused, refusal.CodeInvalid)
		}
	}
}

func TestSealingAValueThatNamesNoTierIsTheCallersMistake(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := testProvider(t)

	for name, refused := range map[string]error{
		"Seal": errorOf(p.Cipher().Seal(ctx, "", seal.Bind("variable", "K"), nil)),
		"Open": errorOf(p.Cipher().Open(ctx, "", seal.Bind("variable", "K"), nil)),
	} {
		var rejection refusal.Refusal
		if !errors.As(refused, &rejection) || rejection.Code != refusal.CodeInvalid {
			t.Errorf("%s() under no tier = %v, want an %s refusal: each tier is sealed under a key of its own, so a value naming no tier names no key",
				name, refused, refusal.CodeInvalid)
		}
	}
}

func TestServesEveryResourcePrimitive(t *testing.T) {
	t.Parallel()

	p := testProvider(t)
	if got, want := p.Facts().Bindings, []provider.BindingType{provider.BindingPostgres, provider.BindingBucket, provider.BindingKV, provider.BindingTopic, provider.BindingTask, provider.BindingRealtime}; !slices.Equal(got, want) {
		t.Errorf("Facts().Bindings = %v, want %v: the resource primitives this provider provisions", got, want)
	}
	want := []provider.Compute{provider.ComputeServerless, provider.ComputeContainer}
	got := p.Facts().Computes
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Computes() = %v, want %v with serverless first, which makes it the default", got, want)
	}
}

func errorOf[T any](_ T, err error) error { return err }
