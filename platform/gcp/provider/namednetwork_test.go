package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

const (
	fleetNetwork    = "fleet"
	fleetSubnetwork = "fleet-europe-west1"
	fleetEgress     = "PRIVATE_RANGES_ONLY projects/acme-prod/global/networks/fleet projects/acme-prod/regions/europe-west1/subnetworks/fleet-europe-west1"
)

func joiningTheFleet(p *Provider) *Provider {
	p.options.Network = &NetworkOptions{Network: fleetNetwork, Subnetwork: fleetSubnetwork}
	p.resolved.Names = p.resolved.WithNetwork(fleetNetwork, fleetSubnetwork)
	return p
}

func (s *networkServer) holdingTheFleet(subnetworkOn string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.networks[fleetNetwork] = &compute.Network{Name: fleetNetwork, SelfLink: "https://www.googleapis.com/compute/v1/projects/acme-prod/global/networks/" + fleetNetwork}
	s.subnets[fleetSubnetwork] = &compute.Subnetwork{
		Name:        fleetSubnetwork,
		IpCidrRange: "10.8.0.0/20",
		Network:     "https://www.googleapis.com/compute/v1/projects/acme-prod/global/networks/" + subnetworkOn,
	}
}

func TestADeployDeclaringADatabaseOnANamedNetworkAddsNoPrivateNetworkAndWritesNothingToIt(t *testing.T) {
	p, server := deployingNetworks(t)
	joiningTheFleet(p)
	server.holdingTheFleet(fleetNetwork)
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)
	ctx := context.Background()
	pre := declaringADatabase("17")
	pre.Deploy.Slug = "shop"

	plan, err := p.PlanPreflight(ctx, pre)
	if err != nil {
		t.Fatalf("PlanPreflight() = %v", err)
	}
	if slices.ContainsFunc(plan.Groups, func(group provider.ChangeGroup) bool { return group.Part == network.PartName }) {
		t.Errorf("a dry deploy on a named network planned %+v, want no %s part: the project joins the network it names", plan.Groups, network.PartName)
	}
	if err := p.ApplyPreflight(ctx, pre); err != nil {
		t.Fatalf("ApplyPreflight() = %v", err)
	}
	if got := server.wrote(); len(got) != 0 {
		t.Errorf("a deploy on a named network wrote %v, want nothing: its owner makes everything on it", got)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), environment.TierProduction, network.PartName); err != nil || present {
		t.Errorf("the %s part is recorded %t, %v, want it never added", network.PartName, present, err)
	}
}

func TestADeployOnANamedNetworkTheProjectLacksIsRefusedBeforeAnyChange(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hold  func(*networkServer)
		names []string
	}{
		{name: "no network", hold: func(*networkServer) {}, names: []string{`"fleet"`, "acme-prod"}},
		{name: "no subnetwork", hold: func(s *networkServer) {
			s.holdingTheFleet(fleetNetwork)
			delete(s.subnets, fleetSubnetwork)
		}, names: []string{`"fleet-europe-west1"`, "europe-west1"}},
		{name: "a subnetwork on another network", hold: func(s *networkServer) { s.holdingTheFleet("ocel-prod-network") }, names: []string{`"fleet-europe-west1"`, "ocel-prod-network", `"fleet"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, server := deployingNetworks(t)
			joiningTheFleet(p)
			tc.hold(server)
			recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)
			pre := declaringADatabase("17")
			pre.Deploy.Slug = "shop"

			_, planned := p.PlanPreflight(context.Background(), pre)
			applied := p.ApplyPreflight(context.Background(), pre)
			for _, err := range []error{planned, applied} {
				var refused refusal.Refusal
				if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
					t.Fatalf("preflight on %s = %v, want an %s refusal", tc.name, err, refusal.CodeInvalid)
				}
				for _, name := range tc.names {
					if !strings.Contains(refused.Message, name) {
						t.Errorf("preflight on %s refused with %q, want it to name %s", tc.name, refused.Message, name)
					}
				}
			}
			if got := server.wrote(); len(got) != 0 {
				t.Errorf("a refused deploy wrote %v, want nothing", got)
			}
		})
	}
}

func TestADeployWhoseAppReachesTheNamedNetworkChecksItWithNoStoreDeclared(t *testing.T) {
	p, _ := deployingNetworks(t)
	joiningTheFleet(p)
	p.options.Apps = map[string]AppOptions{"web": {Network: true}}
	pre := provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction, Apps: []provider.AppEntry{{App: "web", Manifest: containerApp}}}}

	err := p.ApplyPreflight(context.Background(), pre)
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || !strings.Contains(err.Error(), `"fleet"`) {
		t.Errorf("ApplyPreflight() of an app reaching a network the project lacks = %v, want a refusal naming the network", err)
	}
}

func TestADeployOnANamedNetworkReadsNoNetworkWhenNoAppJoinsIt(t *testing.T) {
	p, _ := deployingNetworks(t)
	joiningTheFleet(p)
	pre := provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction, Apps: []provider.AppEntry{{App: "web", Manifest: containerApp}}}}

	if err := p.ApplyPreflight(context.Background(), pre); err != nil {
		t.Errorf("ApplyPreflight() of an app that neither binds a store nor reaches the network = %v, want the missing network unread", err)
	}
}

func TestAnAppBoundToADatabaseReachesPrivateRangesOverTheNamedSubnetwork(t *testing.T) {
	server := &googletest.Run{}
	p := joiningTheFleet(providerServing(server, t))
	spec := previewSpec()
	spec.App.Values.Bindings = []provider.Binding{{Type: provider.BindingPostgres, Name: "orders"}}

	if _, err := p.ProvisionContainers(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if got := egressOf(server.Created[len(server.Created)-1]); got != fleetEgress {
		t.Errorf("a service bound to a database on a named network has egress %q, want %q", got, fleetEgress)
	}
}

func TestAnAppSetToReachTheNamedNetworkReachesItBoundToNothing(t *testing.T) {
	server := &googletest.Run{}
	p := joiningTheFleet(providerServing(server, t))
	p.options.Apps = map[string]AppOptions{"web": {Network: true}}

	if _, err := p.ProvisionContainers(context.Background(), previewSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if _, err := p.ProvisionFunctions(context.Background(), previewSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	released := slices.Concat(server.Created, server.Patched)
	if len(released) != 2 {
		t.Fatalf("released %d services, want the container and the function", len(released))
	}
	for _, service := range released {
		if got := egressOf(service); got != fleetEgress {
			t.Errorf("a service of an app set to reach the network has egress %q, want %q", got, fleetEgress)
		}
	}
}

func TestAnAppOnANamedNetworkNotSetToReachItAndBoundToNothingHasNoNetworkInterface(t *testing.T) {
	server := &googletest.Run{}
	p := joiningTheFleet(providerServing(server, t))

	if _, err := p.ProvisionContainers(context.Background(), previewSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if got := egressOf(server.Created[0]); got != "" {
		t.Errorf("a service bound to nothing has egress %q, want none", got)
	}
}

func TestForwardingPortsOnANamedNetworkIsRefusedBeforeAnyBastionIsMade(t *testing.T) {
	server := &googletest.Run{}
	p := joiningTheFleet(providerServing(server, t))

	forwards, err := p.ForwardPorts(context.Background(), provider.PortForwardRequest{
		Tier:     environment.TierProduction,
		Bindings: []provider.Binding{{Type: provider.BindingPostgres, Name: "orders", Properties: map[string]string{provider.PropertyHost: "10.8.0.9", provider.PropertyPort: "5432"}}},
	}, nil)

	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || len(forwards) != 0 || !strings.Contains(err.Error(), fleetNetwork) {
		t.Fatalf("ForwardPorts() on a named network = %v, %v, want an invalid refusal naming the network", forwards, err)
	}
	if server.Serving() != nil {
		t.Error("a bastion was made on the tier's own network for a database on a named one")
	}
}
