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

func TestTheFilterForDatabasesOnANetworkMatchesTheNamespaceLabelAStackWrites(t *testing.T) {
	t.Parallel()
	namespace := provider.Namespace(strings.Repeat("ocel-", 20) + "end")
	written := stackLabels(Names{namespace: namespace}, provider.StackRef{Tier: environment.TierProduction})[namespaceLabel]

	got := databasesFilter(namespace, environment.TierProduction)

	if want := "settings.userLabels.ocel-namespace:" + written + " "; !strings.Contains(got, want) {
		t.Errorf("databasesFilter() = %q, want it to hold %q: the filter reads the label stackLabels writes", got, want)
	}
}

func TestThePrivateNetworkIsAFeatureNoEdgeOrFrameworkPullsIn(t *testing.T) {
	t.Parallel()

	catalogue := bootstrap{}.Catalogue()
	at := slices.IndexFunc(catalogue, func(f provider.Feature) bool { return f.Name == networkFeature })
	if at < 0 {
		t.Fatalf("Catalogue() = %v, want the %q feature kv stores and databases are reached over", catalogue, networkFeature)
	}
	if feature := catalogue[at]; len(feature.Edges) != 0 || len(feature.Frameworks) != 0 || !strings.Contains(feature.Summary, networkSubnetRange) {
		t.Errorf("the %q feature is %+v, want one no edge or framework pulls in that names its subnet's range", networkFeature, feature)
	}
	for _, kind := range []edge.Kind{edge.None, alb.Kind} {
		required, err := bootstrapplan.RequiredFeatures(catalogue, nil, kind)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(required, networkFeature) {
			t.Errorf("a bootstrap fronted by %q requires %v, and a network is raised only for a tier that asks for kv stores or databases", kind, required)
		}
	}
}

func TestThePrivateNetworkChecksTheServicesAndPermissionsItIsRaisedWith(t *testing.T) {
	t.Parallel()

	for _, api := range []string{"memorystore.googleapis.com", "sqladmin.googleapis.com", "networkconnectivity.googleapis.com", "serviceconsumermanagement.googleapis.com", "servicenetworking.googleapis.com", "compute.googleapis.com"} {
		if !slices.Contains(apisFor(environment.TierProduction, []string{networkFeature}), api) {
			t.Errorf("a %q bootstrap checks %v, want %s among them", networkFeature, apisFor(environment.TierProduction, []string{networkFeature}), api)
		}
	}
	both := []string{albFeature, networkFeature}
	if !slices.Contains(apisFor(environment.TierProduction, both), "certificatemanager.googleapis.com") || !slices.Contains(apisFor(environment.TierProduction, both), "memorystore.googleapis.com") {
		t.Errorf("a bootstrap with the load balancer and the private network checks %v, want the services of both", apisFor(environment.TierProduction, both))
	}
	for _, permission := range []string{"networkconnectivity.serviceConnectionPolicies.create", "compute.subnetworks.setIamPolicy", "compute.networks.create", "cloudsql.instances.list"} {
		if !slices.Contains(permissionsFor([]string{networkFeature}), permission) {
			t.Errorf("a %q bootstrap checks %v, want %s among them", networkFeature, permissionsFor([]string{networkFeature}), permission)
		}
	}
	for _, role := range []string{"roles/compute.networkAdmin", "roles/cloudsql.viewer"} {
		if !slices.Contains(rolesCovering([]string{networkFeature}), role) {
			t.Errorf("a %q bootstrap names %v as covering it, want %s among them", networkFeature, rolesCovering([]string{networkFeature}), role)
		}
	}
	if !slices.Contains(rolesFor(edge.PurposeDeploy), "roles/memorystore.admin") {
		t.Errorf("a deploy credential is granted %v, want roles/memorystore.admin: a deploy creates, reshapes and deletes stores", rolesFor(edge.PurposeDeploy))
	}
	if slices.Contains(rolesFor(edge.PurposeDeploy), "roles/cloudsql.admin") {
		t.Errorf("a deploy credential is granted %v, with roles/cloudsql.admin over every Cloud SQL instance in the project", rolesFor(edge.PurposeDeploy))
	}
}

func TestInstallingThePrivateNetworkRaisesTheTiersNetworkSubnetworkPoliciesAndGrant(t *testing.T) {
	b, server := servingNetworks(t)

	if err := b.raiseNetwork(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("raiseNetwork() = %v", err)
	}

	want := []string{"create network ocel-production", "create subnetwork ocel-production", "grant on ocel-production", "create policy ocel-production-memorystore", "create policy ocel-production-cloudsql"}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("installing the network wrote %v, want %v", got, want)
	}
	network := server.networks["ocel-production"]
	if network.AutoCreateSubnetworks || !slices.Contains(network.ForceSendFields, "AutoCreateSubnetworks") {
		t.Errorf("the network is %+v, want it created in custom mode, with no subnetwork in every region", network)
	}
	subnet := server.subnets["ocel-production"]
	if subnet.IpCidrRange != networkSubnetRange || subnet.Region != "europe-west1" || !strings.HasSuffix(subnet.Network, "/global/networks/ocel-production") {
		t.Errorf("the subnetwork is %+v, want %s in europe-west1 on the tier's network", subnet, networkSubnetRange)
	}
	policy := server.policies["ocel-production-memorystore"]
	if policy.ServiceClass != memorystoreServiceClass || policy.Network != "projects/acme-prod/global/networks/ocel-production" ||
		policy.PscConfig == nil || !slices.Equal(policy.PscConfig.Subnetworks, []string{"projects/acme-prod/regions/europe-west1/subnetworks/ocel-production"}) {
		t.Errorf("the connection policy is %+v, want Memorystore connected through the tier's subnetwork", policy)
	}
	databases := server.policies["ocel-production-cloudsql"]
	if databases.ServiceClass != "google-cloud-sql" || databases.Network != "projects/acme-prod/global/networks/ocel-production" ||
		databases.PscConfig == nil || !slices.Equal(databases.PscConfig.Subnetworks, []string{"projects/acme-prod/regions/europe-west1/subnetworks/ocel-production"}) {
		t.Errorf("the connection policy is %+v, want Cloud SQL connected through the tier's subnetwork", databases)
	}
	granted := server.iam["ocel-production"]
	if granted == nil || !slices.ContainsFunc(granted.Bindings, func(binding *compute.Binding) bool {
		return binding.Role == networkUserRole && slices.Contains(binding.Members, serviceAgent)
	}) {
		t.Errorf("the subnetwork grants %+v, want the Cloud Run service agent allowed to attach services to it", granted)
	}
}

func TestInstallingThePrivateNetworkAgainWritesNothing(t *testing.T) {
	b, server := servingNetworks(t)
	server.installed()

	if err := b.raiseNetwork(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("raiseNetwork() = %v", err)
	}
	if got := server.wrote(); len(got) != 0 {
		t.Errorf("installing a network already in place wrote %v, want nothing", got)
	}
}

func TestRemovingThePrivateNetworkTakesDownThePoliciesThenTheSubnetworkThenTheNetwork(t *testing.T) {
	b, server := servingNetworks(t)
	server.installed()

	if err := b.tearNetwork(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("tearNetwork() = %v", err)
	}
	want := []string{"delete policy ocel-production-memorystore", "delete policy ocel-production-cloudsql", "delete subnetwork ocel-production", "delete network ocel-production"}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("removing the network wrote %v, want %v", got, want)
	}
	if err := b.tearNetwork(context.Background(), environment.TierProduction); err != nil {
		t.Errorf("tearNetwork() of a network already gone = %v, want nil", err)
	}
}

func TestThePrivateNetworkIsReportedInstalledOnlyOnceBothItsPoliciesExist(t *testing.T) {
	b, server := servingNetworks(t)

	described, err := b.described(context.Background(), surveyed(networkFeature))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if stack, _ := featureStack(described, networkFeature); stack.Present {
		t.Error("a private network with no connection policy is reported installed, so the gate never raises it again")
	}

	server.installedWithout("ocel-production-cloudsql")
	described, err = b.described(context.Background(), surveyed(networkFeature))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if stack, _ := featureStack(described, networkFeature); stack.Present {
		t.Error("a private network with no Cloud SQL connection policy is reported installed, so a bootstrap made before databases never raises it")
	}

	server.installed()
	described, err = b.described(context.Background(), surveyed(networkFeature))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if stack, _ := featureStack(described, networkFeature); !stack.Present {
		t.Error("an installed private network is reported absent, so every bootstrap raises it again")
	}
}

func TestABootstrapRequestingThePrivateNetworkRaisesItAndDroppingItTakesItDown(t *testing.T) {
	b, server, _ := servingNetworksAndStores(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Features: []string{networkFeature}}

	if err := b.raiseFeatures(context.Background(), req, nil); err != nil {
		t.Fatalf("raiseFeatures() = %v", err)
	}
	if server.policies["ocel-production-memorystore"] == nil {
		t.Error("a bootstrap requesting the private network left no connection policy")
	}

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{networkFeature}}
	if err := b.dropFeatures(context.Background(), surveyed(networkFeature), drop, nil); err != nil {
		t.Fatalf("dropFeatures() = %v", err)
	}
	if len(server.networks) != 0 {
		t.Errorf("dropping the private network left %v", server.networks)
	}

	server.installed()
	if err := b.tearFeatures(context.Background(), environment.TierProduction, []string{networkFeature}); err != nil {
		t.Fatalf("tearFeatures() = %v", err)
	}
	if len(server.networks) != 0 {
		t.Errorf("removing a bootstrap with the private network left %v", server.networks)
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

const connectedStore = "projects/acme-prod/locations/europe-west1/instances/ocel-shop-prod-cache-abc123"

func storeLabels(tier environment.Tier) map[string]string {
	return map[string]string{"ocel-namespace": "ocel", "ocel-tier": string(tier), "ocel-project": "shop", "ocel-environment": "prod", "ocel-kv": "cache"}
}

func TestDroppingThePrivateNetworkWhileAStoreIsOnItIsRefusedNamingTheStore(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(connectedStore, storeLabels(environment.TierProduction))

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{networkFeature}}
	err := b.dropFeatures(context.Background(), surveyed(networkFeature), drop, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "kv cache") || !strings.Contains(refused.Message, "shop") ||
		!strings.Contains(refused.Message, "ocel-shop-prod-cache-abc123") {
		t.Errorf("dropFeatures() with a store on the network = %v, want a refusal naming the store", err)
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("a refused drop wrote %v, want the network left in place", got)
	}
	if err := b.featuresFree(context.Background(), environment.TierProduction, []string{networkFeature}); err == nil {
		t.Error("featuresFree() with a store on the network = nil, want removing the bootstrap refused at plan time too")
	}
	if filters := stores.filters(); len(filters) == 0 || !strings.Contains(filters[0], `labels.ocel-namespace="ocel"`) ||
		!strings.Contains(filters[0], `labels.ocel-tier="production"`) {
		t.Errorf("the stores were listed with filters %q, want one bounded to this namespace's tier", filters)
	}
}

func TestDroppingThePrivateNetworkIgnoresAStoreOnAnotherTiersNetwork(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(connectedStore, storeLabels(environment.TierPreview))

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{networkFeature}}
	if err := b.dropFeatures(context.Background(), surveyed(networkFeature), drop, nil); err != nil {
		t.Fatalf("dropFeatures() with a store on another tier = %v, want the network taken down", err)
	}
	if len(networks.networks) != 0 {
		t.Errorf("dropping the private network left %v", networks.networks)
	}
}

func TestDroppingThePrivateNetworkReadsPastEmptyPagesToTheStoreOnIt(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(connectedStore, storeLabels(environment.TierProduction))
	stores.emptyPages = 2

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{networkFeature}}
	err := b.dropFeatures(context.Background(), surveyed(networkFeature), drop, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "ocel-shop-prod-cache-abc123") {
		t.Errorf("dropFeatures() with the store behind two empty pages = %v, want a refusal naming the store", err)
	}
	if filters := stores.filters(); len(filters) != 3 || filters[2] != filters[0] {
		t.Errorf("the stores were listed with filters %q, want three pages read under one filter", filters)
	}
}

func TestDroppingThePrivateNetworkIsAnErrorWhenEveryPageReadIsEmptyAndNamesAnother(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.emptyPages = storePagesRead + 1

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{networkFeature}}
	if err := b.dropFeatures(context.Background(), surveyed(networkFeature), drop, nil); err == nil {
		t.Error("dropFeatures() with only empty pages that name another = nil, want an error that leaves the network up")
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("an unread listing let the drop write %v, want the network left in place", got)
	}
	if got := len(stores.filters()); got != storePagesRead {
		t.Errorf("the stores were listed %d times, want %d", got, storePagesRead)
	}
}

const connectedDatabase = "ocel--shop-prod-orders-5e6f7a"

func TestDroppingThePrivateNetworkWhileADatabaseIsOnItIsRefusedNamingTheDatabase(t *testing.T) {
	b, networks, _ := servingNetworksAndStores(t)
	networks.installed()
	networks.databases.holding(connectedDatabase, map[string]string{
		"ocel-namespace": "ocel", "ocel-tier": "production", "ocel-project": "shop", "ocel-environment": "prod", "ocel-postgres": "orders",
	})

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{networkFeature}}
	err := b.dropFeatures(context.Background(), surveyed(networkFeature), drop, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "postgres orders") || !strings.Contains(refused.Message, connectedDatabase) {
		t.Errorf("dropFeatures() with a database on the network = %v, want a refusal naming the database", err)
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("a refused drop wrote %v, want the network left in place", got)
	}
	if filters := networks.databases.filters(); len(filters) == 0 || !strings.Contains(filters[0], "settings.userLabels.ocel-namespace:ocel") ||
		!strings.Contains(filters[0], "settings.userLabels.ocel-tier:production") {
		t.Errorf("the databases were listed with filters %q, want one bounded to this namespace's tier", filters)
	}
}
