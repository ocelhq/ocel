package gcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

type routedHosts struct {
	routed   []alb.OriginHost
	unrouted []string
	err      error
}

func (r *routedHosts) RouteOriginHost(_ context.Context, host alb.OriginHost) error {
	r.routed = append(r.routed, host)
	return r.err
}

func (r *routedHosts) UnrouteOriginHost(_ context.Context, _ environment.Tier, hostname string) error {
	r.unrouted = append(r.unrouted, hostname)
	return nil
}

func recordOriginWildcard(t *testing.T, store keyvalue.Store, recorded originWildcard) {
	t.Helper()
	entry, err := keyvalue.ReadOrEmpty(context.Background(), store, originWildcardKey(environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Value, err = json.Marshal(recorded); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
}

type shieldingFront struct{ edge.Edge }

func (f shieldingFront) Facts() edge.Facts {
	facts := f.Edge.Facts()
	facts.ShieldsOrigin = true
	return facts
}

func withOriginBase(t *testing.T, p *Provider) *routedHosts {
	t.Helper()
	p.records = fake.NewKeyValues()
	routing := &routedHosts{}
	p.originRoutes = routing
	recordOriginWildcard(t, p.records, originWildcard{BaseDomain: "o.example.com", Address: "203.0.113.7"})
	return routing
}

func behindTheWorker(spec provider.StackSpec) provider.StackSpec {
	spec.Edge = codeRunningFront{kind: cloudflareKind, runsCode: true, shields: true}
	return spec
}

func TestAnOriginHostnameIsTheReleaseAndServiceUnderTheTiersOriginBase(t *testing.T) {
	t.Parallel()
	if got, want := originHostname("r0001abcd", "ocel-shop-prod-web-abc123", "o.example.com"), "r0001abcd-ocel-shop-prod-web-abc123.o.example.com"; got != want {
		t.Errorf("originHostname = %q, want %q", got, want)
	}
}

func TestAnOriginHostnameFitsOneDNSLabel(t *testing.T) {
	t.Parallel()
	for _, service := range []string{strings.Repeat("s", 37), strings.Repeat("s", 200)} {
		host := originHostname("r0001abcd", service, "o.example.com")
		label, _, _ := strings.Cut(host, ".")
		if len(label) > 63 || !strings.HasPrefix(label, "r0001abcd-") {
			t.Errorf("label %q (%d characters) for a %d-character service, want at most 63 and led by the release", label, len(label), len(service))
		}
	}
}

func TestAFunctionBehindTheWorkerAnswersOnItsReleasesOriginHostname(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	p.records = newOffersHarness(t).records
	routing := &routedHosts{}
	p.originRoutes = routing
	recordOriginWildcard(t, p.records, originWildcard{BaseDomain: "o.example.com", Address: "203.0.113.7"})
	spec := behindTheWorker(functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout:sha256-one"))

	functions, err := p.ProvisionFunctions(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("ProvisionFunctions = %v", err)
	}

	release := spec.Ref.Name.Release.String()
	if len(routing.routed) != 1 || routing.routed[0].Tag != release || routing.routed[0].Slug != "shop" ||
		routing.routed[0].Hostname != originHostname(release, functions[0].Physical, "o.example.com") {
		t.Fatalf("routed %+v, want the release-tagged revision at the origin hostname", routing.routed)
	}
	if want := "https://" + routing.routed[0].Hostname; functions[0].URL != want {
		t.Errorf("Function.URL = %q, want %q", functions[0].URL, want)
	}
	if strings.Contains(functions[0].DeploymentURL, "o.example.com") {
		t.Errorf("DeploymentURL = %q, want the tagged run.app address", functions[0].DeploymentURL)
	}
}

func TestAFunctionBehindAnEdgeThatRunsNoCodeKeepsItsCloudRunURL(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	p.records = newOffersHarness(t).records
	routing := &routedHosts{}
	p.originRoutes = routing
	recordOriginWildcard(t, p.records, originWildcard{BaseDomain: "o.example.com", Address: "203.0.113.7"})
	spec := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout:sha256-one")
	spec.Edge = codeRunningFront{kind: "alb", shields: true}

	functions, err := p.ProvisionFunctions(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(routing.routed) != 0 || strings.Contains(functions[0].URL, "o.example.com") {
		t.Errorf("routed %v and answered on %q, want the Cloud Run URL and no routing", routing.routed, functions[0].URL)
	}
}

func TestAFunctionBehindTheWorkerOnATierWithNoOriginDomainIsRefused(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	p.records = newOffersHarness(t).records
	p.originRoutes = &routedHosts{}

	_, err := p.ProvisionFunctions(context.Background(), behindTheWorker(functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout:sha256-one")), nil)

	if refusalCode(err) != refusal.CodeInvalid || !strings.Contains(err.Error(), "originDomain") {
		t.Errorf("ProvisionFunctions = %v, want an invalid refusal naming originDomain", err)
	}
}

func TestAFunctionBehindTheWorkerWaitsForTheOriginWildcardsCertificate(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	p.records = newOffersHarness(t).records
	p.originRoutes = &routedHosts{}
	recordOriginWildcard(t, p.records, originWildcard{BaseDomain: "o.example.com"})

	_, err := p.ProvisionFunctions(context.Background(), behindTheWorker(functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout:sha256-one")), nil)

	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "deploy again") {
		t.Errorf("ProvisionFunctions = %v, want a not-ready refusal asking to deploy again", err)
	}
}

func prunable(t *testing.T) (*Provider, *routedHosts, []provider.Function) {
	t.Helper()
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	routing := &routedHosts{}
	p.originRoutes = routing
	recordOriginWildcard(t, p.records, originWildcard{BaseDomain: "o.example.com", Address: "203.0.113.7"})
	var deployed []provider.Function
	for _, id := range []string{"d1", "d2", "d3"} {
		image := "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-" + id
		deployed = append(deployed, released.provision(t, behindTheWorker(functionRelease(id, image))))
	}
	active := deployed[2]
	if _, err := p.Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}
	return p, routing, deployed
}

func TestPruningARevisionUnroutesItsOriginHostname(t *testing.T) {
	p, routing, deployed := prunable(t)

	left, err := p.RemoveFunctionRevisions(context.Background(), provider.StackRef{Tier: environment.TierProduction}, deployed[:1], nil)
	if err != nil {
		t.Fatalf("RemoveFunctionRevisions = %v", err)
	}
	host := strings.TrimPrefix(deployed[0].URL, "https://")
	if len(left) != 0 || !slices.Equal(routing.unrouted, []string{host}) {
		t.Errorf("kept %v and unrouted %v, want the removed revision's host %s unrouted", left, routing.unrouted, host)
	}
}

func TestARevisionThatStaysKeepsItsOriginHostname(t *testing.T) {
	p, routing, deployed := prunable(t)

	left, err := p.RemoveFunctionRevisions(context.Background(), provider.StackRef{Tier: environment.TierProduction}, deployed[2:], nil)
	if err != nil {
		t.Fatalf("RemoveFunctionRevisions = %v", err)
	}
	if len(left) != 1 || len(routing.unrouted) != 0 {
		t.Errorf("kept %v and unrouted %v, want the active revision kept with its host", left, routing.unrouted)
	}
}

func TestRemovingAFunctionUnroutesItsOriginHostnameFirst(t *testing.T) {
	routing := &routedHosts{}
	p := &Provider{originRoutes: routing}
	functions := []provider.Function{
		{Name: "a", URL: "https://r1-a.o.example.com"},
		{Name: "b", URL: "https://svc-abc.europe-west1.run.app"},
		{Name: "c", URL: ""},
	}

	if err := p.unrouteOriginHosts(context.Background(), environment.TierProduction, functions); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(routing.unrouted, []string{"r1-a.o.example.com"}) {
		t.Errorf("unrouted %v, want only the origin host: a run.app URL and an empty one name no origin host", routing.unrouted)
	}
}
