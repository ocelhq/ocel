package providerserver_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func forwardingRelay(t *testing.T) (*fake.Provider, *fake.Edge) {
	t.Helper()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	return vendor, relay
}

func productionPreflight(domains []string, apps map[string]string) *contractv1.PreflightRequest {
	return &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      domains,
		DomainApps:   apps,

		CheckForwarding: true,
	}
}

func TestPreflightRefusesAHostnameForwardedToItsOriginThroughAZoneTheEdgeCannotForwardItThrough(t *testing.T) {
	t.Parallel()
	for code, message := range map[refusal.Code]string{
		refusal.CodeInvalid: `zone acme.com reaches origins with SSL mode "full", which does not check the certificate the origin answers with`,
		refusal.CodeDenied:  "CLOUDFLARE_API_TOKEN may not read whether zone acme.com presents a client certificate to origins",
	} {
		vendor, relay := forwardingRelay(t)
		relay.RefusesUnreadyHostnames(refusal.Refuse(code, "%s", message))
		client := servedBy(t, vendor)

		_, err := preflight(context.Background(), client, productionPreflight([]string{"app.acme.com"}, map[string]string{"app.acme.com": "web"}))

		if err == nil || !strings.Contains(err.Error(), message) {
			t.Errorf("Preflight() error = %v, want the edge's refusal %q: a zone that cannot forward the hostname fails the deploy at its attach, after every stack was provisioned", err, message)
		}
		if asked := relay.HostnamesAskedReady(); !slices.Equal(asked, []string{"app.acme.com"}) {
			t.Errorf("the edge was asked about %v, want app.acme.com, the hostname it forwards to an origin", asked)
		}
	}
}

func TestPreflightPassesEveryForwardedHostnameTheEdgeCanForward(t *testing.T) {
	t.Parallel()
	vendor, relay := forwardingRelay(t)
	client := servedBy(t, vendor)

	_, err := preflight(context.Background(), client, productionPreflight([]string{"acme.com", "app.acme.com"}, map[string]string{"app.acme.com": "web"}))

	if err != nil {
		t.Fatalf("Preflight() error = %v, want it to pass: the edge can forward every hostname", err)
	}
	if asked := relay.HostnamesAskedReady(); !slices.Equal(asked, []string{"acme.com", "app.acme.com"}) {
		t.Errorf("the edge was asked about %v, want acme.com and app.acme.com, each forwarded to an origin", asked)
	}
}

type workerServedSites struct{ *fake.Provider }

func (p workerServedSites) Facts() provider.Facts {
	facts := p.Provider.Facts()
	facts.Pairings = []provider.Pairing{
		{Edge: fake.KindRelay, Router: fake.RouterRelayStore, Computes: []provider.Compute{provider.ComputeServerless}},
		{Edge: fake.KindRelay, Router: fake.RouterRelay, Computes: []provider.Compute{provider.ComputeContainer}, Forwarded: true},
	}
	return facts
}

func TestPreflightAsksTheEdgeNothingAboutAHostnameItServesItself(t *testing.T) {
	t.Parallel()
	vendor, relay := forwardingRelay(t)
	vendor.Edges().(*fake.Edges).PairAppsWithStoreRouter(fake.KindRelay)
	relay.RefusesUnreadyHostnames(errors.New("the zone forwards nothing"))
	client := servedBy(t, workerServedSites{vendor})
	req := productionPreflight([]string{"acme.com", "site.acme.com", "api.acme.com"}, map[string]string{"site.acme.com": "site", "api.acme.com": "api"})
	req.Containers = []*contractv1.ContainerApp{{App: "api"}}

	_, err := preflight(context.Background(), client, req)

	if err == nil {
		t.Fatal("Preflight() error = nil, want api.acme.com refused: its container app is forwarded to its origin")
	}
	if asked := relay.HostnamesAskedReady(); !slices.Equal(asked, []string{"api.acme.com"}) {
		t.Errorf("the edge was asked about %v, want only api.acme.com: acme.com and site.acme.com are served by the edge's own code, which forwards neither to an origin", asked)
	}
}

func TestPreflightAsksTheEdgeNothingAboutAProjectPreviewWildcardWhenItRunsTheCodeThatServesIt(t *testing.T) {
	t.Parallel()
	vendor, relay := forwardingRelay(t)
	client := servedBy(t, vendor)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW})

	_, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PREVIEW,
		Slug:         "shop",
		Domains:      []string{edge.PreviewWildcard("preview.acme.com")},

		CheckForwarding: true,
	})

	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if asked := relay.HostnamesAskedReady(); len(asked) != 0 {
		t.Errorf("the edge was asked about %v, want nothing: an edge that runs code serves every preview itself", asked)
	}
}

func TestPreflightAsksTheEdgeAboutAProjectPreviewWildcardItForwardsToTheOrigin(t *testing.T) {
	t.Parallel()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	direct := vendor.Edges().(*fake.Edges).Edge(fake.KindDirect)
	direct.ProxiesRecords()
	client := servedBy(t, vendor)
	sel := &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW, Edge: sel})

	_, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PREVIEW,
		Slug:         "shop",
		Edge:         sel,
		Domains:      []string{edge.PreviewWildcard("preview.acme.com")},

		CheckForwarding: true,
	})

	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if asked := direct.HostnamesAskedReady(); !slices.Equal(asked, []string{edge.PreviewWildcard("preview.acme.com")}) {
		t.Errorf("the edge was asked about %v, want the preview wildcard it forwards to the origin", asked)
	}
}

func TestAPreflightNotAskedToCheckForwardingAsksTheEdgeNothing(t *testing.T) {
	t.Parallel()
	vendor, relay := forwardingRelay(t)
	relay.RefusesUnreadyHostnames(errors.New("the zone forwards nothing"))
	client := servedBy(t, vendor)
	req := productionPreflight([]string{"app.acme.com"}, nil)
	req.CheckForwarding = false

	if _, err := preflight(context.Background(), client, req); err != nil {
		t.Fatalf("Preflight() error = %v, want the edge never asked", err)
	}
	if asked := relay.HostnamesAskedReady(); len(asked) != 0 {
		t.Errorf("the edge was asked about %v, want nothing: only a deploy forwards its hostnames", asked)
	}
}

func TestPreflightAsksTheEdgeNothingAboutForwardedHostnamesWhenTheChecksAreSkipped(t *testing.T) {
	t.Setenv(processenv.SkipChecksEnvVar, "1")
	vendor, relay := forwardingRelay(t)
	relay.RefusesUnreadyHostnames(errors.New("the zone forwards nothing"))
	client := servedBy(t, vendor)

	if _, err := preflight(context.Background(), client, productionPreflight([]string{"app.acme.com"}, nil)); err != nil {
		t.Fatalf("Preflight() with the checks skipped error = %v, want the edge never asked", err)
	}
	if asked := relay.HostnamesAskedReady(); len(asked) != 0 {
		t.Errorf("the edge was asked about %v with the checks skipped, want nothing", asked)
	}
}

func TestPreflightAsksNothingOfAnEdgeThatTunnelsToItsOrigin(t *testing.T) {
	t.Parallel()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	vendor.WithFacts(runsTunnels)
	relay := tunnelRelay(t, vendor)
	relay.RefusesUnreadyHostnames(errors.New("the zone forwards nothing"))
	client := servedBy(t, vendor)
	req := productionPreflight([]string{"app.acme.com"}, nil)
	req.Edge = tunneled(t)

	if _, err := preflight(context.Background(), client, req); err != nil {
		t.Fatalf("Preflight() error = %v, want the tunneled hostname never asked about", err)
	}
	if asked := relay.HostnamesAskedReady(); len(asked) != 0 {
		t.Errorf("the edge was asked about %v, want nothing: a hostname reached through a tunnel is presented no client certificate", asked)
	}
}
