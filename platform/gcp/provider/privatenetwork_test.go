package gcp

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

func TestTheFilterForDatabasesOnANetworkMatchesTheNamespaceLabelAStackWrites(t *testing.T) {
	t.Parallel()
	namespace := provider.Namespace(strings.Repeat("ocel-", 20) + "end")
	written := must(names.StackLabels(names.New(namespace, ""), provider.StackRef{Tier: environment.TierProduction}))[names.NamespaceLabel]

	got := must(network.DatabasesFilter(namespace, environment.TierProduction))

	if want := "settings.userLabels.ocel-namespace:" + written + " "; !strings.Contains(got, want) {
		t.Errorf("databasesFilter() = %q, want it to hold %q: the filter reads the label stackLabels writes", got, want)
	}
}

func TestTheFilterForStoresOnANetworkMatchesTheLabelsAStackWrites(t *testing.T) {
	t.Parallel()
	namespace := provider.Namespace(strings.Repeat("ocel-", 20) + "end")
	written := must(names.StackLabels(names.New(namespace, ""), provider.StackRef{Tier: environment.TierProduction}))

	got := must(network.StoresFilter(namespace, environment.TierProduction))

	want := "labels.ocel-namespace=" + strconv.Quote(written[names.NamespaceLabel]) + " AND labels.ocel-tier=" + strconv.Quote(written[names.TierLabel])
	if got != want {
		t.Errorf("storesFilter() = %q, want %q: the filter reads the labels stackLabels writes", got, want)
	}
}

func TestThePrivateNetworkIsAPartADeployAdds(t *testing.T) {
	t.Parallel()

	if slices.ContainsFunc(bootstrap.CredentialParts(), func(f provider.Part) bool { return f.Name == network.PartName }) {
		t.Errorf("credentialParts() = %v, want no %q part only the bootstrap credential adds: the first deploy declaring a database or a store adds the network", bootstrap.CredentialParts(), network.PartName)
	}
	b, _ := servingNetworks(t)
	net := network.NewPart(b, environment.TierProduction, network.Patches{})
	if net.Name != network.PartName || !strings.Contains(network.Summary, network.SubnetRange) || len(net.Inventory) != len(net.Resources) {
		t.Errorf("the network part is %+v, want it named %s, its summary naming its range and each resource priced", net, network.PartName)
	}
}

func TestThePrivateNetworkChecksItsServicesAndADeployCredentialMayAddIt(t *testing.T) {
	t.Parallel()
	b, _ := servingNetworks(t)
	network := network.NewPart(b, environment.TierProduction, network.Patches{})

	for _, api := range []string{"memorystore.googleapis.com", "sqladmin.googleapis.com", "networkconnectivity.googleapis.com", "serviceconsumermanagement.googleapis.com", "servicenetworking.googleapis.com", "compute.googleapis.com"} {
		if !slices.Contains(network.APIs, api) {
			t.Errorf("the network part checks %v, want %s among them", network.APIs, api)
		}
	}
	parts := bootstrap.NewPartsRole(b.Clients.Names).Permissions
	for _, permission := range []string{
		"networkconnectivity.serviceConnectionPolicies.create", "compute.networks.create", "compute.subnetworks.create", "compute.regionOperations.get", "serviceusage.services.get",
		"compute.subnetworks.getIamPolicy", "compute.subnetworks.setIamPolicy",
		"compute.subnetworks.update", "compute.subnetworks.setPrivateIpGoogleAccess", "networkconnectivity.serviceConnectionPolicies.update",
	} {
		if !slices.Contains(parts, permission) {
			t.Errorf("the parts role holds %v, want %s among them: a deploy adds the network and lets Cloud Run's agent attach services to its subnetwork", parts, permission)
		}
	}
	if slices.Contains(parts, "compute.networks.delete") {
		t.Error("the parts role holds compute.networks.delete, which no deploy needs: only a bootstrap removes the network")
	}
	for _, role := range []string{"roles/compute.networkAdmin", "roles/cloudsql.viewer"} {
		if !slices.Contains(bootstrap.BootstrapRoles(), role) {
			t.Errorf("a bootstrap credential is granted %v, want %s: removing a bootstrap takes its network down once nothing is on it", bootstrap.BootstrapRoles(), role)
		}
	}
	lines := credential.Lines(deployOf(environment.TierProduction).Grants())
	if !slices.Contains(lines, deployOf(environment.TierProduction).Stores().String()) {
		t.Errorf("a deploy credential is granted\n%s\nwant roles/memorystore.admin: a deploy creates, reshapes and deletes stores, and Memorystore takes no tag", strings.Join(lines, "\n"))
	}
	if slices.ContainsFunc(lines, func(line string) bool { return strings.HasPrefix(line, "roles/cloudsql.admin, unconditioned") }) {
		t.Errorf("a deploy credential is granted\n%s\nwith roles/cloudsql.admin over every Cloud SQL instance in the project", strings.Join(lines, "\n"))
	}
}

func TestMakingThePrivateNetworkRaisesTheTiersNetworkSubnetworkAndPolicies(t *testing.T) {
	b, server := servingNetworks(t)

	if err := network.Make(context.Background(), b, environment.TierProduction, network.Patches{}); err != nil {
		t.Fatalf("makeNetwork() = %v", err)
	}

	want := []string{"create network ocel-prod-network", "create subnetwork ocel-prod-network", "grant on ocel-prod-network", "create policy ocel-prod-network-memorystore", "create policy ocel-prod-network-cloudsql"}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("installing the network wrote %v, want %v", got, want)
	}
	granted := server.iam["ocel-prod-network"]
	if granted == nil || len(granted.Bindings) != 1 || granted.Bindings[0].Role != network.NetworkUserRole || !slices.Equal(granted.Bindings[0].Members, []string{serviceAgent}) {
		t.Errorf("the subnetwork's policy is %+v, want Cloud Run's agent alone granted %s on it: Direct VPC egress attaches services through it", granted, network.NetworkUserRole)
	}
	net := server.networks["ocel-prod-network"]
	if net.AutoCreateSubnetworks || !slices.Contains(net.ForceSendFields, "AutoCreateSubnetworks") {
		t.Errorf("the network is %+v, want it created in custom mode, with no subnetwork in every region", net)
	}
	subnet := server.subnets["ocel-prod-network"]
	if subnet.IpCidrRange != network.SubnetRange || subnet.Region != "europe-west1" || !strings.HasSuffix(subnet.Network, "/global/networks/ocel-prod-network") {
		t.Errorf("the subnetwork is %+v, want %s in europe-west1 on the tier's network", subnet, network.SubnetRange)
	}
	policy := server.policies["ocel-prod-network-memorystore"]
	if policy.ServiceClass != network.MemorystoreServiceClass || policy.Network != "projects/acme-prod/global/networks/ocel-prod-network" ||
		policy.PscConfig == nil || !slices.Equal(policy.PscConfig.Subnetworks, []string{"projects/acme-prod/regions/europe-west1/subnetworks/ocel-prod-network"}) {
		t.Errorf("the connection policy is %+v, want Memorystore connected through the tier's subnetwork", policy)
	}
	databases := server.policies["ocel-prod-network-cloudsql"]
	if databases.ServiceClass != "google-cloud-sql" || databases.Network != "projects/acme-prod/global/networks/ocel-prod-network" ||
		databases.PscConfig == nil || !slices.Equal(databases.PscConfig.Subnetworks, []string{"projects/acme-prod/regions/europe-west1/subnetworks/ocel-prod-network"}) {
		t.Errorf("the connection policy is %+v, want Cloud SQL connected through the tier's subnetwork", databases)
	}
}

func TestMakingThePrivateNetworkAgainWritesNothing(t *testing.T) {
	b, server := servingNetworks(t)
	server.installed()

	if err := network.Make(context.Background(), b, environment.TierProduction, network.Patches{}); err != nil {
		t.Fatalf("makeNetwork() = %v", err)
	}
	if got := server.wrote(); len(got) != 0 {
		t.Errorf("installing a network already in place wrote %v, want nothing", got)
	}
}

func TestRemovingThePrivateNetworkTakesDownThePoliciesThenTheSubnetworkThenTheNetwork(t *testing.T) {
	b, server := servingNetworks(t)
	server.installed()

	if err := network.Remove(context.Background(), b, environment.TierProduction); err != nil {
		t.Fatalf("removeNetwork() = %v", err)
	}
	want := []string{"delete policy ocel-prod-network-memorystore", "delete policy ocel-prod-network-cloudsql", "delete subnetwork ocel-prod-network", "delete network ocel-prod-network"}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("removing the network wrote %v, want %v", got, want)
	}
	if err := network.Remove(context.Background(), b, environment.TierProduction); err != nil {
		t.Errorf("removeNetwork() of a network already gone = %v, want nil", err)
	}
}

func recordingNetwork(t *testing.T, b *bootstrap.Engine) {
	t.Helper()
	b.Records = fake.NewKeyValues()
	if err := stackrecords.MarkBootstrapPartReady(context.Background(), b.Records, environment.TierProduction, network.PartName, stackrecords.BootstrapPart{AddedBy: "shop"}); err != nil {
		t.Fatal(err)
	}
}

func TestRemovingATierTakesItsPrivateNetworkDownAndAPlanToRemoveItListsIt(t *testing.T) {
	b, server, _ := servingNetworksAndStores(t)
	server.installed()
	recordingNetwork(t, &b)
	ctx := context.Background()

	groups, err := b.ListPartRemovals(ctx, environment.TierProduction)
	if err != nil || len(groups) != 1 || groups[0].Part != network.PartName || groups[0].Action != provider.ActionDelete || len(groups[0].Changes) != 4 {
		t.Fatalf("listPartRemovals() = %+v, %v, want the network part deleted with its four resources", groups, err)
	}
	if err := b.RemoveParts(ctx, environment.TierProduction, nil); err != nil {
		t.Fatalf("removeParts() = %v", err)
	}
	if len(server.networks) != 0 || len(server.policies) != 0 {
		t.Errorf("removing the tier left %v and %v", server.networks, server.policies)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, environment.TierProduction, network.PartName); err != nil || present {
		t.Errorf("the part record outlived the network: %t, %v", present, err)
	}
}

func TestAConnectionPolicyCreateAnsweredWithAnUnnamedUnfinishedOperationIsAnError(t *testing.T) {
	b, server := servingNetworks(t)
	server.unnamed = true

	err := network.Make(context.Background(), b, environment.TierProduction, network.Patches{})
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
	stores.Holding(connectedStore, storeLabels(environment.TierProduction))

	recordingNetwork(t, &b)
	err := b.RefusePartsInUse(context.Background(), environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "kv cache") || !strings.Contains(refused.Message, "shop") ||
		!strings.Contains(refused.Message, "ocel-shop-prod-cache-abc123") {
		t.Errorf("refusePartsInUse() with a store on the network = %v, want a refusal naming the store", err)
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("a refused removal wrote %v, want the network left in place", got)
	}
	if filters := stores.Filters(); len(filters) == 0 || !strings.Contains(filters[0], `labels.ocel-namespace="ocel"`) ||
		!strings.Contains(filters[0], `labels.ocel-tier="production"`) {
		t.Errorf("the stores were listed with filters %q, want one bounded to this namespace's tier", filters)
	}
}

func TestRemovingThePrivateNetworkIgnoresAStoreOnAnotherTiersNetwork(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.Holding(connectedStore, storeLabels(environment.TierPreview))

	recordingNetwork(t, &b)
	if err := b.RefusePartsInUse(context.Background(), environment.TierProduction); err != nil {
		t.Errorf("refusePartsInUse() with a store on another tier = %v, want the network free to remove", err)
	}
}

func TestRemovingThePrivateNetworkReadsPastEmptyPagesToTheStoreOnIt(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.Holding(connectedStore, storeLabels(environment.TierProduction))
	stores.EmptyPages = 2

	recordingNetwork(t, &b)
	err := b.RefusePartsInUse(context.Background(), environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "ocel-shop-prod-cache-abc123") {
		t.Errorf("refusePartsInUse() with the store behind two empty pages = %v, want a refusal naming the store", err)
	}
	if filters := stores.Filters(); len(filters) != 3 || filters[2] != filters[0] {
		t.Errorf("the stores were listed with filters %q, want three pages read under one filter", filters)
	}
}

func TestRemovingThePrivateNetworkIsAnErrorWhenEveryPageReadIsEmptyAndNamesAnother(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.EmptyPages = network.StorePagesRead + 1

	recordingNetwork(t, &b)
	if err := b.RefusePartsInUse(context.Background(), environment.TierProduction); err == nil {
		t.Error("refusePartsInUse() with only empty pages that name another = nil, want an error that leaves the network up")
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("an unread listing let the removal write %v, want the network left in place", got)
	}
	if got := len(stores.Filters()); got != network.StorePagesRead {
		t.Errorf("the stores were listed %d times, want %d", got, network.StorePagesRead)
	}
}

const connectedDatabase = "ocel-app-shop-prod-orders-k3x9q2"

func TestRemovingThePrivateNetworkWhileADatabaseIsOnItIsRefusedNamingTheDatabase(t *testing.T) {
	b, networks, _ := servingNetworksAndStores(t)
	networks.installed()
	networks.databases.Holding(connectedDatabase, map[string]string{
		"ocel-namespace": "ocel", "ocel-tier": "production", "ocel-project": "shop", "ocel-environment": "prod", "ocel-postgres": "orders",
	})

	recordingNetwork(t, &b)
	err := b.RefusePartsInUse(context.Background(), environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "postgres orders") || !strings.Contains(refused.Message, connectedDatabase) {
		t.Errorf("refusePartsInUse() with a database on the network = %v, want a refusal naming the database", err)
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("a refused removal wrote %v, want the network left in place", got)
	}
	if filters := networks.databases.Filters(); len(filters) == 0 || !strings.Contains(filters[0], "settings.userLabels.ocel-namespace:ocel") ||
		!strings.Contains(filters[0], "settings.userLabels.ocel-tier:production") {
		t.Errorf("the databases were listed with filters %q, want one bounded to this namespace's tier", filters)
	}
}

func TestARecordedPrivateNetworkIsDescribedAsAPartUsedByTheProjectsConnectedToIt(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.Holding(connectedStore, storeLabels(environment.TierProduction))
	recordingNetwork(t, &b)

	parts, err := b.ListDescribedParts(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Name != network.PartName || !slices.Equal(parts[0].UsedBy, []string{"shop"}) || len(parts[0].Resources) != 4 || parts[0].Inventory == nil {
		t.Errorf("listDescribedParts() = %+v, want the network used by shop, holding its four resources, priced", parts)
	}
}

func TestABootstrapMakesARecordedPrivateNetworkAgainWhenItWasTakenDownByHand(t *testing.T) {
	b, server, _ := servingNetworksAndStores(t)
	recordingNetwork(t, &b)
	ctx := context.Background()

	repaired, err := b.RepairParts(ctx, environment.TierProduction, false)
	if err != nil {
		t.Fatalf("repairParts() = %v", err)
	}
	if !slices.Contains(repaired, network.PartName) || server.networks["ocel-prod-network"] == nil || len(server.policies) != 2 {
		t.Errorf("repairParts() = %v leaving %v and %v, want the recorded network made again", repaired, server.networks, server.policies)
	}
}

func TestTheTiersPrivateNetworkIsNotHeldInUseByStoresAndDatabasesATransformPutOnAnotherNetwork(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	shared := "projects/acme-prod/global/networks/shared"
	stores.HoldingOn(connectedStore, storeLabels(environment.TierProduction), shared)
	networks.databases.HoldingOn(connectedDatabase, map[string]string{
		"ocel-namespace": "ocel", "ocel-tier": "production", "ocel-project": "shop", "ocel-environment": "prod", "ocel-postgres": "orders",
	}, shared)
	recordingNetwork(t, &b)
	ctx := context.Background()

	if err := b.RefusePartsInUse(ctx, environment.TierProduction); err != nil {
		t.Errorf("refusePartsInUse() with a store and a database on the network shared = %v, want the tier's own network free to remove", err)
	}
	parts, err := b.ListDescribedParts(ctx, environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || len(parts[0].UsedBy) != 0 {
		t.Errorf("listDescribedParts() = %+v, want the network used by no project: shop's store and database are on shared", parts)
	}
}

func TestTheTiersPrivateNetworkIsHeldInUseByAStoreAndADatabaseConnectedToIt(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	own := "projects/acme-prod/global/networks/ocel-prod-network"
	stores.HoldingOn(connectedStore, storeLabels(environment.TierProduction), own)
	networks.databases.HoldingOn(connectedDatabase, map[string]string{
		"ocel-namespace": "ocel", "ocel-tier": "production", "ocel-project": "shop", "ocel-environment": "prod", "ocel-postgres": "orders",
	}, own)
	recordingNetwork(t, &b)

	err := b.RefusePartsInUse(context.Background(), environment.TierProduction)
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || !strings.Contains(err.Error(), "kv cache") || !strings.Contains(err.Error(), "postgres orders") {
		t.Errorf("refusePartsInUse() with a store and a database connected to the tier's network = %v, want a refusal naming both", err)
	}
}
