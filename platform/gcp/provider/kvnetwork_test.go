package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func TestTheKVNetworkIsAFeatureNoEdgeOrFrameworkPullsIn(t *testing.T) {
	t.Parallel()

	catalogue := bootstrap{}.Catalogue()
	at := slices.IndexFunc(catalogue, func(f provider.Feature) bool { return f.Name == kvFeature })
	if at < 0 {
		t.Fatalf("Catalogue() = %v, want the %q feature kv stores are reached over", catalogue, kvFeature)
	}
	if feature := catalogue[at]; len(feature.Edges) != 0 || len(feature.Frameworks) != 0 || !strings.Contains(feature.Summary, kvSubnetRange) {
		t.Errorf("the %q feature is %+v, want one no edge or framework pulls in that names its subnet's range", kvFeature, feature)
	}
	for _, kind := range []edge.Kind{edge.None, alb.Kind} {
		required, err := bootstrapplan.RequiredFeatures(catalogue, nil, kind)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(required, kvFeature) {
			t.Errorf("a bootstrap fronted by %q requires %v, and a network is raised only for a tier that asks for kv stores", kind, required)
		}
	}
}

func TestTheKVNetworkChecksTheServicesAndPermissionsItIsRaisedWith(t *testing.T) {
	t.Parallel()

	for _, api := range []string{"memorystore.googleapis.com", "networkconnectivity.googleapis.com", "serviceconsumermanagement.googleapis.com", "compute.googleapis.com"} {
		if !slices.Contains(apisFor([]string{kvFeature}), api) {
			t.Errorf("a %q bootstrap checks %v, want %s among them", kvFeature, apisFor([]string{kvFeature}), api)
		}
	}
	both := []string{albFeature, kvFeature}
	if !slices.Contains(apisFor(both), "certificatemanager.googleapis.com") || !slices.Contains(apisFor(both), "memorystore.googleapis.com") {
		t.Errorf("a bootstrap with the load balancer and the kv network checks %v, want the services of both", apisFor(both))
	}
	for _, permission := range []string{"networkconnectivity.serviceConnectionPolicies.create", "compute.subnetworks.setIamPolicy", "compute.networks.create"} {
		if !slices.Contains(permissionsFor([]string{kvFeature}), permission) {
			t.Errorf("a %q bootstrap checks %v, want %s among them", kvFeature, permissionsFor([]string{kvFeature}), permission)
		}
	}
	for _, role := range []string{"roles/compute.networkAdmin"} {
		if !slices.Contains(rolesCovering([]string{kvFeature}), role) {
			t.Errorf("a %q bootstrap names %v as covering it, want %s among them", kvFeature, rolesCovering([]string{kvFeature}), role)
		}
	}
	if !slices.Contains(rolesFor(edge.PurposeDeploy), "roles/memorystore.admin") {
		t.Errorf("a deploy credential is granted %v, want roles/memorystore.admin: a deploy creates, reshapes and deletes stores", rolesFor(edge.PurposeDeploy))
	}
}

func TestInstallingTheKVNetworkRaisesTheTiersNetworkSubnetworkPolicyAndGrant(t *testing.T) {
	b, server := servingNetworks(t)

	if err := b.raiseNetwork(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("raiseNetwork() = %v", err)
	}

	want := []string{"create network ocel-production", "create subnetwork ocel-production", "grant on ocel-production", "create policy ocel-production-memorystore"}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("installing the network wrote %v, want %v", got, want)
	}
	network := server.networks["ocel-production"]
	if network.AutoCreateSubnetworks || !slices.Contains(network.ForceSendFields, "AutoCreateSubnetworks") {
		t.Errorf("the network is %+v, want it created in custom mode, with no subnetwork in every region", network)
	}
	subnet := server.subnets["ocel-production"]
	if subnet.IpCidrRange != kvSubnetRange || subnet.Region != "europe-west1" || !strings.HasSuffix(subnet.Network, "/global/networks/ocel-production") {
		t.Errorf("the subnetwork is %+v, want %s in europe-west1 on the tier's network", subnet, kvSubnetRange)
	}
	policy := server.policies["ocel-production-memorystore"]
	if policy.ServiceClass != memorystoreServiceClass || policy.Network != "projects/acme-prod/global/networks/ocel-production" ||
		policy.PscConfig == nil || !slices.Equal(policy.PscConfig.Subnetworks, []string{"projects/acme-prod/regions/europe-west1/subnetworks/ocel-production"}) {
		t.Errorf("the connection policy is %+v, want Memorystore connected through the tier's subnetwork", policy)
	}
	granted := server.iam["ocel-production"]
	if granted == nil || !slices.ContainsFunc(granted.Bindings, func(binding *compute.Binding) bool {
		return binding.Role == networkUserRole && slices.Contains(binding.Members, serviceAgent)
	}) {
		t.Errorf("the subnetwork grants %+v, want the Cloud Run service agent allowed to attach services to it", granted)
	}
}

func TestInstallingTheKVNetworkAgainWritesNothing(t *testing.T) {
	b, server := servingNetworks(t)
	server.installed()

	if err := b.raiseNetwork(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("raiseNetwork() = %v", err)
	}
	if got := server.wrote(); len(got) != 0 {
		t.Errorf("installing a network already in place wrote %v, want nothing", got)
	}
}

func TestRemovingTheKVNetworkTakesDownThePolicyThenTheSubnetworkThenTheNetwork(t *testing.T) {
	b, server := servingNetworks(t)
	server.installed()

	if err := b.tearNetwork(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("tearNetwork() = %v", err)
	}
	want := []string{"delete policy ocel-production-memorystore", "delete subnetwork ocel-production", "delete network ocel-production"}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("removing the network wrote %v, want %v", got, want)
	}
	if err := b.tearNetwork(context.Background(), environment.TierProduction); err != nil {
		t.Errorf("tearNetwork() of a network already gone = %v, want nil", err)
	}
}

func TestTheKVNetworkIsReportedInstalledOnlyOnceItsPolicyExists(t *testing.T) {
	b, server := servingNetworks(t)

	described, err := b.described(context.Background(), surveyed(kvFeature))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if stack, _ := featureStack(described, kvFeature); stack.Present {
		t.Error("a kv network with no connection policy is reported installed, so the gate never raises it again")
	}

	server.installed()
	described, err = b.described(context.Background(), surveyed(kvFeature))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if stack, _ := featureStack(described, kvFeature); !stack.Present {
		t.Error("an installed kv network is reported absent, so every bootstrap raises it again")
	}
}

func TestABootstrapRequestingTheKVNetworkRaisesItAndDroppingItTakesItDown(t *testing.T) {
	b, server, _ := servingNetworksAndStores(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Features: []string{kvFeature}}

	if err := b.raiseFeatures(context.Background(), req, nil); err != nil {
		t.Fatalf("raiseFeatures() = %v", err)
	}
	if server.policies["ocel-production-memorystore"] == nil {
		t.Error("a bootstrap requesting the kv network left no connection policy")
	}

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{kvFeature}}
	if err := b.dropFeatures(context.Background(), surveyed(kvFeature), drop, nil); err != nil {
		t.Fatalf("dropFeatures() = %v", err)
	}
	if len(server.networks) != 0 {
		t.Errorf("dropping the kv network left %v", server.networks)
	}

	server.installed()
	if err := b.tearFeatures(context.Background(), environment.TierProduction, []string{kvFeature}); err != nil {
		t.Fatalf("tearFeatures() = %v", err)
	}
	if len(server.networks) != 0 {
		t.Errorf("removing a bootstrap with the kv network left %v", server.networks)
	}
}

func TestAConnectionPolicyCreateAnsweredWithAnUnnamedUnfinishedOperationIsAnError(t *testing.T) {
	b, server := servingNetworks(t)
	server.unnamed = true

	err := b.raiseNetwork(context.Background(), environment.TierProduction, nil)
	if err == nil || !strings.Contains(err.Error(), "no name") {
		t.Errorf("raiseNetwork() = %v, want an operation nothing can poll named as an error rather than taken as done", err)
	}
}

const heldStore = "projects/acme-prod/locations/europe-west1/instances/ocel-shop-prod-cache-abc123"

func storeLabels(tier environment.Tier) map[string]string {
	return map[string]string{"ocel-namespace": "ocel", "ocel-tier": string(tier), "ocel-project": "shop", "ocel-environment": "prod", "ocel-kv": "cache"}
}

func TestDroppingTheKVNetworkWhileAStoreIsOnItIsRefusedNamingTheStore(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(heldStore, storeLabels(environment.TierProduction))

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{kvFeature}}
	err := b.dropFeatures(context.Background(), surveyed(kvFeature), drop, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "kv cache") || !strings.Contains(refused.Message, "shop") ||
		!strings.Contains(refused.Message, "ocel-shop-prod-cache-abc123") {
		t.Errorf("dropFeatures() with a store on the network = %v, want a refusal naming the store", err)
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("a refused drop wrote %v, want the network left in place", got)
	}
	if err := b.featuresFree(context.Background(), environment.TierProduction, []string{kvFeature}); err == nil {
		t.Error("featuresFree() with a store on the network = nil, want removing the bootstrap refused at plan time too")
	}
	if filters := stores.filters(); len(filters) == 0 || !strings.Contains(filters[0], `labels.ocel-namespace="ocel"`) ||
		!strings.Contains(filters[0], `labels.ocel-tier="production"`) {
		t.Errorf("the stores were listed with filters %q, want one bounded to this namespace's tier", filters)
	}
}

func TestDroppingTheKVNetworkIgnoresAStoreOnAnotherTiersNetwork(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(heldStore, storeLabels(environment.TierPreview))

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{kvFeature}}
	if err := b.dropFeatures(context.Background(), surveyed(kvFeature), drop, nil); err != nil {
		t.Fatalf("dropFeatures() with a store on another tier = %v, want the network taken down", err)
	}
	if len(networks.networks) != 0 {
		t.Errorf("dropping the kv network left %v", networks.networks)
	}
}

func TestDroppingTheKVNetworkReadsPastEmptyPagesToTheStoreOnIt(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(heldStore, storeLabels(environment.TierProduction))
	stores.emptyPages = 2

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{kvFeature}}
	err := b.dropFeatures(context.Background(), surveyed(kvFeature), drop, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "ocel-shop-prod-cache-abc123") {
		t.Errorf("dropFeatures() with the store behind two empty pages = %v, want a refusal naming the store", err)
	}
	if filters := stores.filters(); len(filters) != 3 || filters[2] != filters[0] {
		t.Errorf("the stores were listed with filters %q, want three pages read under one filter", filters)
	}
}

func TestDroppingTheKVNetworkIsAnErrorWhenEveryPageReadIsEmptyAndNamesAnother(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.emptyPages = storePagesRead + 1

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{kvFeature}}
	if err := b.dropFeatures(context.Background(), surveyed(kvFeature), drop, nil); err == nil {
		t.Error("dropFeatures() with only empty pages that name another = nil, want an error that leaves the network up")
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("an unread listing let the drop write %v, want the network left in place", got)
	}
	if got := len(stores.filters()); got != storePagesRead {
		t.Errorf("the stores were listed %d times, want %d", got, storePagesRead)
	}
}
