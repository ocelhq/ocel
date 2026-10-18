package gcp

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestTheFilterForDatabasesOnANetworkMatchesTheNamespaceLabelAStackWrites(t *testing.T) {
	t.Parallel()
	namespace := provider.Namespace(strings.Repeat("ocel-", 20) + "end")
	written := names.StackLabels(names.New(namespace, ""), provider.StackRef{Tier: environment.TierProduction})[names.NamespaceLabel]

	got := databasesFilter(namespace, environment.TierProduction)

	if want := "settings.userLabels.ocel-namespace:" + written + " "; !strings.Contains(got, want) {
		t.Errorf("databasesFilter() = %q, want it to hold %q: the filter reads the label stackLabels writes", got, want)
	}
}

func TestTheFilterForStoresOnANetworkMatchesTheLabelsAStackWrites(t *testing.T) {
	t.Parallel()
	namespace := provider.Namespace(strings.Repeat("ocel-", 20) + "end")
	written := names.StackLabels(names.New(namespace, ""), provider.StackRef{Tier: environment.TierProduction})

	got := storesFilter(namespace, environment.TierProduction)

	want := "labels.ocel-namespace=" + strconv.Quote(written[names.NamespaceLabel]) + " AND labels.ocel-tier=" + strconv.Quote(written[names.TierLabel])
	if got != want {
		t.Errorf("storesFilter() = %q, want %q: the filter reads the labels stackLabels writes", got, want)
	}
}

func TestThePrivateNetworkIsAPartADeployAdds(t *testing.T) {
	t.Parallel()

	if slices.ContainsFunc(credentialParts(), func(f provider.Part) bool { return f.Name == networkPart }) {
		t.Errorf("credentialParts() = %v, want no %q part only the bootstrap credential adds: the first deploy declaring a database or a store adds the network", credentialParts(), networkPart)
	}
	b, _ := servingNetworks(t)
	network := b.networkPart(environment.TierProduction)
	if network.name != networkPart || !strings.Contains(network.summary, networkSubnetRange) || len(network.shape) != len(network.resources) {
		t.Errorf("the network part is %+v, want it named %s, its summary naming its range and each resource priced", network, networkPart)
	}
}

func TestThePrivateNetworkChecksItsServicesAndADeployCredentialMayAddIt(t *testing.T) {
	t.Parallel()
	b, _ := servingNetworks(t)
	network := b.networkPart(environment.TierProduction)

	for _, api := range []string{"memorystore.googleapis.com", "sqladmin.googleapis.com", "networkconnectivity.googleapis.com", "serviceconsumermanagement.googleapis.com", "servicenetworking.googleapis.com", "compute.googleapis.com"} {
		if !slices.Contains(network.apis, api) {
			t.Errorf("the network part checks %v, want %s among them", network.apis, api)
		}
	}
	parts := newPartsRole(b.clients.Names).permissions
	for _, permission := range []string{"networkconnectivity.serviceConnectionPolicies.create", "compute.networks.create", "compute.subnetworks.create", "compute.regionOperations.get", "serviceusage.services.get"} {
		if !slices.Contains(parts, permission) {
			t.Errorf("the parts role holds %v, want %s among them: a deploy adds the network", parts, permission)
		}
	}
	for _, permission := range []string{"compute.subnetworks.setIamPolicy", "compute.networks.delete"} {
		if slices.Contains(parts, permission) {
			t.Errorf("the parts role holds %s, which no deploy needs: the Cloud Run agent is granted the network in the base, and only a bootstrap removes it", permission)
		}
	}
	for _, role := range []string{"roles/compute.networkAdmin", "roles/cloudsql.viewer"} {
		if !slices.Contains(rolesFor(edge.PurposeBootstrap), role) {
			t.Errorf("a bootstrap credential is granted %v, want %s: removing a bootstrap takes its network down once nothing is on it", rolesFor(edge.PurposeBootstrap), role)
		}
	}
	if !slices.Contains(rolesFor(edge.PurposeDeploy), "roles/memorystore.admin") {
		t.Errorf("a deploy credential is granted %v, want roles/memorystore.admin: a deploy creates, reshapes and deletes stores", rolesFor(edge.PurposeDeploy))
	}
	if slices.Contains(rolesFor(edge.PurposeDeploy), "roles/cloudsql.admin") {
		t.Errorf("a deploy credential is granted %v, with roles/cloudsql.admin over every Cloud SQL instance in the project", rolesFor(edge.PurposeDeploy))
	}
}

func TestMakingThePrivateNetworkRaisesTheTiersNetworkSubnetworkAndPolicies(t *testing.T) {
	b, server := servingNetworks(t)

	if err := b.makeNetwork(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("makeNetwork() = %v", err)
	}

	want := []string{"create network ocel-production", "create subnetwork ocel-production", "create policy ocel-production-memorystore", "create policy ocel-production-cloudsql"}
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
}

func TestMakingThePrivateNetworkAgainWritesNothing(t *testing.T) {
	b, server := servingNetworks(t)
	server.installed()

	if err := b.makeNetwork(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("makeNetwork() = %v", err)
	}
	if got := server.wrote(); len(got) != 0 {
		t.Errorf("installing a network already in place wrote %v, want nothing", got)
	}
}

func TestRemovingThePrivateNetworkTakesDownThePoliciesThenTheSubnetworkThenTheNetwork(t *testing.T) {
	b, server := servingNetworks(t)
	server.installed()

	if err := b.removeNetwork(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("removeNetwork() = %v", err)
	}
	want := []string{"delete policy ocel-production-memorystore", "delete policy ocel-production-cloudsql", "delete subnetwork ocel-production", "delete network ocel-production"}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("removing the network wrote %v, want %v", got, want)
	}
	if err := b.removeNetwork(context.Background(), environment.TierProduction); err != nil {
		t.Errorf("removeNetwork() of a network already gone = %v, want nil", err)
	}
}

func recordingNetwork(t *testing.T, b *bootstrap) {
	t.Helper()
	b.records = fake.NewKeyValues()
	if err := stackrecords.MarkBootstrapPartReady(context.Background(), b.records, environment.TierProduction, networkPart, stackrecords.BootstrapPart{AddedBy: "shop"}); err != nil {
		t.Fatal(err)
	}
}

func TestRemovingATierTakesItsPrivateNetworkDownAndAPlanToRemoveItListsIt(t *testing.T) {
	b, server, _ := servingNetworksAndStores(t)
	server.installed()
	recordingNetwork(t, &b)
	ctx := context.Background()

	groups, err := b.listPartRemovals(ctx, environment.TierProduction)
	if err != nil || len(groups) != 1 || groups[0].Part != networkPart || groups[0].Action != provider.ActionDelete || len(groups[0].Changes) != 4 {
		t.Fatalf("listPartRemovals() = %+v, %v, want the network part deleted with its four resources", groups, err)
	}
	if err := b.removeParts(ctx, environment.TierProduction, nil); err != nil {
		t.Fatalf("removeParts() = %v", err)
	}
	if len(server.networks) != 0 || len(server.policies) != 0 {
		t.Errorf("removing the tier left %v and %v", server.networks, server.policies)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.records, environment.TierProduction, networkPart); err != nil || present {
		t.Errorf("the part record outlived the network: %t, %v", present, err)
	}
}

func TestAConnectionPolicyCreateAnsweredWithAnUnnamedUnfinishedOperationIsAnError(t *testing.T) {
	b, server := servingNetworks(t)
	server.unnamed = true

	err := b.makeNetwork(context.Background(), environment.TierProduction)
	if err == nil || !strings.Contains(err.Error(), "no name") {
		t.Errorf("makeNetwork() = %v, want an operation nothing can poll named as an error rather than taken as done", err)
	}
}

const connectedStore = "projects/acme-prod/locations/europe-west1/instances/ocel-shop-prod-cache-abc123"

func storeLabels(tier environment.Tier) map[string]string {
	return map[string]string{"ocel-namespace": "ocel", "ocel-tier": string(tier), "ocel-project": "shop", "ocel-environment": "prod", "ocel-kv": "cache"}
}

func TestRemovingThePrivateNetworkWhileAStoreIsOnItIsRefusedNamingTheStore(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(connectedStore, storeLabels(environment.TierProduction))

	recordingNetwork(t, &b)
	err := b.refusePartsInUse(context.Background(), environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "kv cache") || !strings.Contains(refused.Message, "shop") ||
		!strings.Contains(refused.Message, "ocel-shop-prod-cache-abc123") {
		t.Errorf("refusePartsInUse() with a store on the network = %v, want a refusal naming the store", err)
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("a refused removal wrote %v, want the network left in place", got)
	}
	if filters := stores.filters(); len(filters) == 0 || !strings.Contains(filters[0], `labels.ocel-namespace="ocel"`) ||
		!strings.Contains(filters[0], `labels.ocel-tier="production"`) {
		t.Errorf("the stores were listed with filters %q, want one bounded to this namespace's tier", filters)
	}
}

func TestRemovingThePrivateNetworkIgnoresAStoreOnAnotherTiersNetwork(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(connectedStore, storeLabels(environment.TierPreview))

	recordingNetwork(t, &b)
	if err := b.refusePartsInUse(context.Background(), environment.TierProduction); err != nil {
		t.Errorf("refusePartsInUse() with a store on another tier = %v, want the network free to remove", err)
	}
}

func TestRemovingThePrivateNetworkReadsPastEmptyPagesToTheStoreOnIt(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(connectedStore, storeLabels(environment.TierProduction))
	stores.emptyPages = 2

	recordingNetwork(t, &b)
	err := b.refusePartsInUse(context.Background(), environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "ocel-shop-prod-cache-abc123") {
		t.Errorf("refusePartsInUse() with the store behind two empty pages = %v, want a refusal naming the store", err)
	}
	if filters := stores.filters(); len(filters) != 3 || filters[2] != filters[0] {
		t.Errorf("the stores were listed with filters %q, want three pages read under one filter", filters)
	}
}

func TestRemovingThePrivateNetworkIsAnErrorWhenEveryPageReadIsEmptyAndNamesAnother(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.emptyPages = storePagesRead + 1

	recordingNetwork(t, &b)
	if err := b.refusePartsInUse(context.Background(), environment.TierProduction); err == nil {
		t.Error("refusePartsInUse() with only empty pages that name another = nil, want an error that leaves the network up")
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("an unread listing let the removal write %v, want the network left in place", got)
	}
	if got := len(stores.filters()); got != storePagesRead {
		t.Errorf("the stores were listed %d times, want %d", got, storePagesRead)
	}
}

const connectedDatabase = "ocel--shop-prod-orders-5e6f7a"

func TestRemovingThePrivateNetworkWhileADatabaseIsOnItIsRefusedNamingTheDatabase(t *testing.T) {
	b, networks, _ := servingNetworksAndStores(t)
	networks.installed()
	networks.databases.holding(connectedDatabase, map[string]string{
		"ocel-namespace": "ocel", "ocel-tier": "production", "ocel-project": "shop", "ocel-environment": "prod", "ocel-postgres": "orders",
	})

	recordingNetwork(t, &b)
	err := b.refusePartsInUse(context.Background(), environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "postgres orders") || !strings.Contains(refused.Message, connectedDatabase) {
		t.Errorf("refusePartsInUse() with a database on the network = %v, want a refusal naming the database", err)
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("a refused removal wrote %v, want the network left in place", got)
	}
	if filters := networks.databases.filters(); len(filters) == 0 || !strings.Contains(filters[0], "settings.userLabels.ocel-namespace:ocel") ||
		!strings.Contains(filters[0], "settings.userLabels.ocel-tier:production") {
		t.Errorf("the databases were listed with filters %q, want one bounded to this namespace's tier", filters)
	}
}

func TestARecordedPrivateNetworkIsDescribedAsAPartUsedByTheProjectsConnectedToIt(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(connectedStore, storeLabels(environment.TierProduction))
	recordingNetwork(t, &b)

	parts, err := b.listDescribedParts(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Name != networkPart || !slices.Equal(parts[0].UsedBy, []string{"shop"}) || len(parts[0].Resources) != 4 || parts[0].Shape == nil {
		t.Errorf("listDescribedParts() = %+v, want the network used by shop, holding its four resources, priced", parts)
	}
}

func TestABootstrapMakesARecordedPrivateNetworkAgainWhenItWasTakenDownByHand(t *testing.T) {
	b, server, _ := servingNetworksAndStores(t)
	recordingNetwork(t, &b)
	ctx := context.Background()

	repaired, err := b.repairParts(ctx, environment.TierProduction)
	if err != nil {
		t.Fatalf("repairParts() = %v", err)
	}
	if !slices.Contains(repaired, networkPart) || server.networks["ocel-production"] == nil || len(server.policies) != 2 {
		t.Errorf("repairParts() = %v leaving %v and %v, want the recorded network made again", repaired, server.networks, server.policies)
	}
}
